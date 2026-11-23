package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/githubauth"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/secrets"
)

const gitHubHTTPSUser = "x-access-token"

// GitHubHost reads a local gh login. Probe must not return the token.
// Token is called after the person confirms, and again during a connection
// check when the stored login already came from gh on this computer.
type GitHubHost interface {
	Probe(context.Context) githubauth.Status
	Token(context.Context) (string, error)
}

// HostGitHubView is the host gh login this station is willing to show.
// Detail is safe to display. It never contains a token.
type HostGitHubView struct {
	Available bool
	Login     string
	State     string
	Path      string
	Searched  []string
	Detail    string
}

// HostGitHubUnavailable is what a team-mode or remote request is told.
// Those requests do not look for gh.
const HostGitHubUnavailable = "This Station does not use a GitHub CLI login for this request. On your computer, run `meridian github connect`."

// RepositoryProbe asks GitHub whether a repository can be read. An empty
// token is anonymous. A transport error is not an access denial.
type RepositoryProbe interface {
	ProbeRepository(context.Context, string, string, string) (int, bool, error)
}

// GitHubOAuth is the device flow and the repository listing. The device
// code stays inside the service.
type GitHubOAuth interface {
	RequestDeviceCode(context.Context, string) (githubauth.DeviceCode, error)
	PollDeviceToken(context.Context, string, string) (githubauth.DevicePoll, error)
	CurrentUser(context.Context, string) (githubauth.Account, error)
	ListRepos(context.Context, string, string) ([]githubauth.Repo, error)
}

type githubDeviceCeremony struct {
	deviceCode      string
	userCode        string
	verificationURI string
	interval        time.Duration
	expires         time.Time
	lastPoll        time.Time
	expected        domain.ResourceVersion
}

// GitHubConnectionState is the public view of a login. It never carries a token.
type GitHubConnectionState struct {
	Connected       bool
	DeviceFlow      bool
	Login           string
	Status          domain.GitHubConnectionStatus
	Reason          domain.GitHubConnectionReason
	Scopes          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ResourceVersion domain.ResourceVersion
}

// GitHubDeviceStart is what the person types on GitHub. The device code is
// not included.
type GitHubDeviceStart struct {
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	IntervalSeconds int
}

// GitHubDevicePoll is the device flow's current step.
type GitHubDevicePoll struct {
	Status     string
	Connection GitHubConnectionState
}

// GitHubRepo is one repository the login can see.
type GitHubRepo struct {
	FullName      string
	Private       bool
	Description   string
	DefaultBranch string
	URL           string
}

// HostGitHubAllowed reports whether this station may read the host's gh
// login. Team mode must not: that login belongs to the machine, not the
// member. A non-loopback request must not: a remote caller must not be able
// to pull the host token into the station.
func HostGitHubAllowed(mode domain.InstallationMode, loopback bool) bool {
	return loopback && mode != domain.TeamMode
}

func (s *Service) GitHubConnection(ctx context.Context) (GitHubConnectionState, error) {
	if err := requirePrincipal(ctx); err != nil {
		return GitHubConnectionState{}, err
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return GitHubConnectionState{}, err
	}
	return s.githubState(ctx, owner)
}

func (s *Service) CheckGitHubConnection(ctx context.Context, loopback bool) (GitHubConnectionState, error) {
	if err := requirePrincipal(ctx); err != nil {
		return GitHubConnectionState{}, err
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return GitHubConnectionState{}, err
	}
	connection, found, err := s.loadGitHubConnection(ctx, owner)
	if err != nil || !found {
		return s.githubState(ctx, owner)
	}
	if s.githubOAuth == nil {
		return s.githubState(ctx, owner)
	}
	token, err := s.openGitHubToken(connection)
	if err != nil {
		return GitHubConnectionState{}, err
	}
	replaced, ok, err := s.useRefreshedHostToken(ctx, connection, loopback, token)
	if err != nil {
		wipeString(&token)
		return GitHubConnectionState{}, err
	}
	if ok {
		wipeString(&token)
		return replaced, nil
	}
	account, probeErr := s.githubOAuth.CurrentUser(ctx, token)
	wipeString(&token)
	if probeErr != nil {
		if reason, ok := reconnectReason(probeErr); ok {
			if err := s.markGitHubStatus(ctx, owner, domain.GitHubConnectionNeedsReconnect, reason); err != nil {
				return GitHubConnectionState{}, err
			}
			return s.githubState(ctx, owner)
		}
		return GitHubConnectionState{}, probeErr
	}
	return s.refreshGitHubAccount(ctx, connection, account)
}

// useRefreshedHostToken replaces a host-sourced login when gh now holds a
// different token. The person already confirmed this computer's gh login.
// A remote request, team mode, or a login that came from the device flow
// does not read gh. A replacement GitHub rejects is ignored so a still-valid
// stored token is kept.
func (s *Service) useRefreshedHostToken(
	ctx context.Context, connection domain.GitHubConnection, loopback bool, stored string,
) (GitHubConnectionState, bool, error) {
	if connection.Source != domain.GitHubSourceHost || s.githubHost == nil || !s.hostGitHubAllowed(ctx, loopback) {
		return GitHubConnectionState{}, false, nil
	}
	next, err := s.githubHost.Token(ctx)
	if err != nil {
		return GitHubConnectionState{}, false, nil
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(next)) == 1 {
		wipeString(&next)
		return GitHubConnectionState{}, false, nil
	}
	state, err := s.finishGitHubToken(
		ctx, next, domain.GitHubSourceHost, connection.ResourceVersion, "github-host-"+s.ids.NewID(),
	)
	wipeString(&next)
	if err != nil {
		return GitHubConnectionState{}, false, nil
	}
	return state, true, nil
}

func (s *Service) DisconnectGitHub(
	ctx context.Context, expected domain.ResourceVersion, idempotencyKey string,
) (GitHubConnectionState, error) {
	if err := requirePrincipal(ctx); err != nil {
		return GitHubConnectionState{}, err
	}
	if s.secretKey == nil {
		return GitHubConnectionState{}, domain.ErrUnsupported
	}
	if err := requireIdempotency(idempotencyKey); err != nil {
		return GitHubConnectionState{}, err
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return GitHubConnectionState{}, err
	}
	scope := "github:disconnect"
	var result GitHubConnectionState
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[GitHubConnectionState](ctx, tx, scope, idempotencyKey); err != nil {
			return err
		} else if ok {
			result = replay
			return nil
		}
		current, err := tx.GetGitHubConnection(ctx, owner)
		if err != nil {
			return err
		}
		if current.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if err := tx.DeleteGitHubConnection(ctx, owner); err != nil {
			return err
		}
		result = GitHubConnectionState{DeviceFlow: s.deviceFlowEnabled()}
		if err := s.appendEvent(ctx, tx, "github_connection", string(current.ID),
			"github_connection.updated", expected, nil); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditGitHubDisconnect, "github_connection", string(current.ID)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, idempotencyKey, result, s.clock.Now().UTC())
	})
	s.auditFailure(ctx, domain.AuditGitHubDisconnect, "github_connection", string(owner), err)
	return result, err
}

func (s *Service) HostGitHubLogin(ctx context.Context, loopback bool) (HostGitHubView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return HostGitHubView{}, err
	}
	if s.githubHost == nil || !s.hostGitHubAllowed(ctx, loopback) {
		return HostGitHubView{State: githubauth.StateUnavailable, Detail: HostGitHubUnavailable}, nil
	}
	status := s.githubHost.Probe(ctx)
	if status.State == "" {
		status.State = githubauth.StateNotFound
	}
	view := HostGitHubView{
		Available: status.State == githubauth.StateSignedIn && validGitHubLogin(status.Login),
		Login:     status.Login,
		State:     status.State,
		Path:      status.Path,
		Searched:  status.Searched,
		Detail:    status.Detail,
	}
	if !view.Available {
		view.Login = ""
		if view.State == githubauth.StateSignedIn {
			view.State = githubauth.StateNotSignedIn
		}
	}
	return view, nil
}

func (s *Service) UseHostGitHub(
	ctx context.Context, loopback bool, expected domain.ResourceVersion, idempotencyKey string,
) (GitHubConnectionState, error) {
	if err := requirePrincipal(ctx); err != nil {
		return GitHubConnectionState{}, err
	}
	if s.githubHost == nil || !s.hostGitHubAllowed(ctx, loopback) {
		return GitHubConnectionState{}, fmt.Errorf("%w: host GitHub login is available only on a local single-user Station", domain.ErrUnsupported)
	}
	token, err := s.githubHost.Token(ctx)
	if err != nil {
		return GitHubConnectionState{}, fmt.Errorf("%w: the GitHub CLI token could not be read", domain.ErrInvalid)
	}
	return s.finishGitHubToken(ctx, token, domain.GitHubSourceHost, expected, idempotencyKey)
}

func (s *Service) ConnectGitHubToken(
	ctx context.Context, token string, expected domain.ResourceVersion, idempotencyKey string,
) (GitHubConnectionState, error) {
	if err := requirePrincipal(ctx); err != nil {
		return GitHubConnectionState{}, err
	}
	return s.finishGitHubToken(ctx, token, domain.GitHubSourceToken, expected, idempotencyKey)
}

func (s *Service) BeginGitHubDevice(
	ctx context.Context, expected domain.ResourceVersion,
) (GitHubDeviceStart, error) {
	if err := requirePrincipal(ctx); err != nil {
		return GitHubDeviceStart{}, err
	}
	if !s.deviceFlowEnabled() {
		return GitHubDeviceStart{}, fmt.Errorf("%w: GitHub device flow needs a client id", domain.ErrUnsupported)
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return GitHubDeviceStart{}, err
	}
	code, err := s.githubOAuth.RequestDeviceCode(ctx, s.githubOAuthClientID)
	if err != nil {
		return GitHubDeviceStart{}, err
	}
	if code.UserCode == "" || code.VerificationURI == "" || code.DeviceCode == "" {
		return GitHubDeviceStart{}, domain.ErrInvalid
	}
	interval := code.Interval
	if interval < 5 {
		interval = 5
	}
	expires := s.clock.Now().UTC().Add(time.Duration(code.ExpiresIn) * time.Second)
	s.githubMu.Lock()
	if s.githubDevices == nil {
		s.githubDevices = map[domain.UserID]*githubDeviceCeremony{}
	}
	s.githubDevices[owner] = &githubDeviceCeremony{
		deviceCode: code.DeviceCode, userCode: code.UserCode, verificationURI: code.VerificationURI,
		interval: time.Duration(interval) * time.Second, expires: expires, expected: expected,
	}
	s.githubMu.Unlock()
	return GitHubDeviceStart{
		UserCode: code.UserCode, VerificationURI: code.VerificationURI,
		ExpiresAt: expires, IntervalSeconds: interval,
	}, nil
}

func (s *Service) PollGitHubDevice(ctx context.Context) (GitHubDevicePoll, error) {
	if err := requirePrincipal(ctx); err != nil {
		return GitHubDevicePoll{}, err
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return GitHubDevicePoll{}, err
	}
	s.githubMu.Lock()
	ceremony := s.githubDevices[owner]
	if ceremony == nil {
		s.githubMu.Unlock()
		state, err := s.githubState(ctx, owner)
		if err != nil {
			return GitHubDevicePoll{}, err
		}
		if state.Connected {
			return GitHubDevicePoll{Status: "connected", Connection: state}, nil
		}
		return GitHubDevicePoll{}, domain.ErrNotFound
	}
	now := s.clock.Now().UTC()
	if !now.Before(ceremony.expires) {
		delete(s.githubDevices, owner)
		s.githubMu.Unlock()
		return GitHubDevicePoll{Status: "expired"}, nil
	}
	if !ceremony.lastPoll.IsZero() && now.Before(ceremony.lastPoll.Add(ceremony.interval)) {
		s.githubMu.Unlock()
		return GitHubDevicePoll{Status: "pending"}, nil
	}
	deviceCode := ceremony.deviceCode
	expected := ceremony.expected
	s.githubMu.Unlock()

	poll, err := s.githubOAuth.PollDeviceToken(ctx, s.githubOAuthClientID, deviceCode)
	if err != nil {
		return GitHubDevicePoll{}, err
	}
	s.githubMu.Lock()
	current := s.githubDevices[owner]
	if current == nil || current.deviceCode != deviceCode {
		s.githubMu.Unlock()
		state, stateErr := s.githubState(ctx, owner)
		if stateErr != nil {
			return GitHubDevicePoll{}, stateErr
		}
		if state.Connected {
			return GitHubDevicePoll{Status: "connected", Connection: state}, nil
		}
		return GitHubDevicePoll{}, domain.ErrNotFound
	}
	current.lastPoll = s.clock.Now().UTC()
	switch {
	case poll.SlowDown:
		current.interval += 5 * time.Second
		s.githubMu.Unlock()
		return GitHubDevicePoll{Status: "pending"}, nil
	case poll.Pending:
		s.githubMu.Unlock()
		return GitHubDevicePoll{Status: "pending"}, nil
	case poll.Denied:
		delete(s.githubDevices, owner)
		s.githubMu.Unlock()
		return GitHubDevicePoll{Status: "denied"}, nil
	case poll.Expired:
		delete(s.githubDevices, owner)
		s.githubMu.Unlock()
		return GitHubDevicePoll{Status: "expired"}, nil
	case poll.Token != "":
		s.githubMu.Unlock()
		state, err := s.finishGitHubToken(ctx, poll.Token, domain.GitHubSourceDevice, expected, "github-device-"+s.ids.NewID())
		wipeString(&poll.Token)
		s.githubMu.Lock()
		if err == nil || errors.Is(err, domain.ErrConflict) {
			if current := s.githubDevices[owner]; current != nil && current.deviceCode == deviceCode {
				delete(s.githubDevices, owner)
			}
		}
		s.githubMu.Unlock()
		if errors.Is(err, domain.ErrConflict) {
			state, err = s.githubState(ctx, owner)
		}
		if err != nil {
			return GitHubDevicePoll{}, err
		}
		return GitHubDevicePoll{Status: "connected", Connection: state}, nil
	default:
		s.githubMu.Unlock()
		return GitHubDevicePoll{}, domain.ErrInvalid
	}
}

func (s *Service) CancelGitHubDevice(ctx context.Context) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return err
	}
	s.githubMu.Lock()
	delete(s.githubDevices, owner)
	s.githubMu.Unlock()
	return nil
}

func (s *Service) SearchGitHubRepos(ctx context.Context, query string) ([]GitHubRepo, error) {
	if err := requirePrincipal(ctx); err != nil {
		return nil, err
	}
	if len(query) > 256 {
		return nil, fmt.Errorf("%w: repository search is too long", domain.ErrInvalid)
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return nil, err
	}
	connection, found, err := s.loadGitHubConnection(ctx, owner)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: connect GitHub to search repositories", domain.ErrNotFound)
	}
	if !connection.Active() {
		return nil, fmt.Errorf("%w: reconnect GitHub to search repositories", domain.ErrIllegalTransition)
	}
	token, err := s.openGitHubToken(connection)
	if err != nil {
		return nil, err
	}
	listed, listErr := s.githubOAuth.ListRepos(ctx, token, query)
	wipeString(&token)
	if listErr != nil {
		if reason, ok := reconnectReason(listErr); ok {
			_ = s.markGitHubStatus(ctx, owner, domain.GitHubConnectionNeedsReconnect, reason)
			return nil, fmt.Errorf("%w: reconnect GitHub to search repositories", domain.ErrIllegalTransition)
		}
		return nil, listErr
	}
	repos := make([]GitHubRepo, 0, len(listed))
	for _, item := range listed {
		if item.FullName == "" || item.URL == "" {
			continue
		}
		repos = append(repos, GitHubRepo{
			FullName: item.FullName, Private: item.Private, Description: item.Description,
			DefaultBranch: item.DefaultBranch, URL: item.URL,
		})
	}
	return repos, nil
}

func (s *Service) finishGitHubToken(
	ctx context.Context, token string, source domain.GitHubConnectionSource,
	expected domain.ResourceVersion, idempotencyKey string,
) (GitHubConnectionState, error) {
	if !githubauth.ValidToken(token) {
		wipeString(&token)
		return GitHubConnectionState{}, fmt.Errorf("%w: GitHub token is invalid", domain.ErrInvalid)
	}
	if s.githubOAuth == nil || s.secretKey == nil {
		wipeString(&token)
		return GitHubConnectionState{}, domain.ErrUnsupported
	}
	account, err := s.githubOAuth.CurrentUser(ctx, token)
	if err != nil {
		wipeString(&token)
		if reason, ok := reconnectReason(err); ok {
			return GitHubConnectionState{}, fmt.Errorf("%w: GitHub rejected the login (%s)", domain.ErrInvalid, reason)
		}
		return GitHubConnectionState{}, err
	}
	if !validGitHubLogin(account.Login) {
		wipeString(&token)
		return GitHubConnectionState{}, fmt.Errorf("%w: GitHub login is invalid", domain.ErrInvalid)
	}
	state, err := s.storeGitHubToken(ctx, token, account, source, expected, idempotencyKey)
	wipeString(&token)
	return state, err
}

func (s *Service) storeGitHubToken(
	ctx context.Context, token string, account githubauth.Account,
	source domain.GitHubConnectionSource, expected domain.ResourceVersion, idempotencyKey string,
) (GitHubConnectionState, error) {
	if err := requireIdempotency(idempotencyKey); err != nil {
		return GitHubConnectionState{}, err
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return GitHubConnectionState{}, err
	}
	status := domain.GitHubConnectionActive
	reason := domain.GitHubReasonNone
	if repoScopeMissing(account.Scopes) {
		status = domain.GitHubConnectionNeedsReconnect
		reason = domain.GitHubReasonMissingAccess
	}
	scope := "github:connect"
	var result GitHubConnectionState
	var changed bool
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[GitHubConnectionState](ctx, tx, scope, idempotencyKey); err != nil {
			return err
		} else if ok {
			result = replay
			return nil
		}
		now := s.clock.Now().UTC()
		current, err := tx.GetGitHubConnection(ctx, owner)
		var item domain.GitHubConnection
		switch {
		case errors.Is(err, domain.ErrNotFound):
			if expected != 0 {
				return domain.ErrConflict
			}
			item = domain.GitHubConnection{
				ID: domain.GitHubConnectionID(s.ids.NewID()), OwnerUserID: owner,
				CreatedAt: now, ResourceVersion: 1,
			}
		case err != nil:
			return err
		default:
			if expected == 0 || current.ResourceVersion != expected {
				return domain.ErrConflict
			}
			item = current
			item.ResourceVersion++
		}
		item.Login = account.Login
		item.Status = status
		item.Reason = reason
		item.Scopes = account.Scopes
		item.Source = source
		item.EnvelopeVersion = secrets.GitHubEnvelopeVersion()
		item.KEKID = s.secretKey.ID
		item.KEKVersion = s.secretKey.Version
		item.UpdatedAt = now
		item.CheckedAt = now
		nonce, ciphertext, err := s.secretKey.SealGitHub(item, token)
		if err != nil {
			return err
		}
		item.Nonce, item.Ciphertext = nonce, ciphertext
		if item.ResourceVersion == 1 {
			if err := tx.InsertGitHubConnection(ctx, item); err != nil {
				return err
			}
		} else if err := tx.UpdateGitHubConnection(ctx, item, expected); err != nil {
			return err
		}
		result = githubStateFrom(item, s.deviceFlowEnabled())
		if err := s.appendEvent(ctx, tx, "github_connection", string(item.ID),
			"github_connection.updated", item.ResourceVersion, nil); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditGitHubConnect, "github_connection", string(item.ID)); err != nil {
			return err
		}
		changed = true
		return putReplay(ctx, tx, scope, idempotencyKey, result, now)
	})
	s.auditFailure(ctx, domain.AuditGitHubConnect, "github_connection", string(owner), err)
	if err == nil && changed && result.Status == domain.GitHubConnectionActive {
		s.retryRepositoryAccessCapsules(ctx, owner, result.ResourceVersion)
	}
	return result, err
}

// retryRepositoryAccessCapsules starts one retry of each Failed Capsule whose
// clone could not read the repository. It runs once per connection change.
// A later access failure stays failed until the connection changes again or
// the owner retries. A retry that cannot start does not fail the connection.
func (s *Service) retryRepositoryAccessCapsules(
	ctx context.Context, owner domain.UserID, version domain.ResourceVersion,
) {
	var capsules []domain.Capsule
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		capsules, err = reader.ListRepositoryAccessFailures(ctx, owner)
		return err
	})
	if err != nil {
		return
	}
	for _, capsule := range capsules {
		_, _ = s.RetryCapsule(ctx, capsule.ID, capsule.ResourceVersion, fmt.Sprintf("github-access-v%d", version))
	}
}

func (s *Service) refreshGitHubAccount(
	ctx context.Context, connection domain.GitHubConnection, account githubauth.Account,
) (GitHubConnectionState, error) {
	if !validGitHubLogin(account.Login) {
		return GitHubConnectionState{}, domain.ErrInvalid
	}
	status := domain.GitHubConnectionActive
	reason := domain.GitHubReasonNone
	if repoScopeMissing(account.Scopes) {
		status = domain.GitHubConnectionNeedsReconnect
		reason = domain.GitHubReasonMissingAccess
	}
	if connection.Login == account.Login && connection.Status == status && connection.Reason == reason &&
		connection.Scopes == account.Scopes {
		if err := s.touchGitHubChecked(ctx, connection.OwnerUserID); err != nil {
			return GitHubConnectionState{}, err
		}
		return s.githubState(ctx, connection.OwnerUserID)
	}
	owner := connection.OwnerUserID
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetGitHubConnection(ctx, owner)
		if err != nil {
			return err
		}
		token, err := s.secretKey.OpenGitHub(current)
		if err != nil {
			return err
		}
		previous := current.ResourceVersion
		current.Login = account.Login
		current.Status = status
		current.Reason = reason
		current.Scopes = account.Scopes
		current.ResourceVersion++
		current.UpdatedAt = s.clock.Now().UTC()
		current.CheckedAt = current.UpdatedAt
		nonce, ciphertext, err := s.secretKey.SealGitHub(current, token)
		wipeString(&token)
		if err != nil {
			return err
		}
		current.Nonce, current.Ciphertext = nonce, ciphertext
		if err := tx.UpdateGitHubConnection(ctx, current, previous); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "github_connection", string(current.ID),
			"github_connection.updated", current.ResourceVersion, nil)
	})
	if err != nil {
		return GitHubConnectionState{}, err
	}
	return s.githubState(ctx, owner)
}

func (s *Service) markGitHubStatus(
	ctx context.Context, owner domain.UserID,
	status domain.GitHubConnectionStatus, reason domain.GitHubConnectionReason,
) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetGitHubConnection(ctx, owner)
		if err != nil {
			return err
		}
		if current.Status == status && current.Reason == reason {
			return nil
		}
		previous := current.ResourceVersion
		current.Status = status
		current.Reason = reason
		current.ResourceVersion++
		current.UpdatedAt = s.clock.Now().UTC()
		current.CheckedAt = current.UpdatedAt
		if err := tx.UpdateGitHubConnection(ctx, current, previous); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "github_connection", string(current.ID),
			"github_connection.updated", current.ResourceVersion, nil)
	})
}

// touchGitHubChecked records a check that changed nothing. It keeps the
// resource version, so a client holding the connection's version can still
// disconnect or reconnect without a conflict it has no event to explain.
func (s *Service) touchGitHubChecked(ctx context.Context, owner domain.UserID) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetGitHubConnection(ctx, owner)
		if err != nil {
			return err
		}
		current.CheckedAt = s.clock.Now().UTC()
		return tx.UpdateGitHubConnection(ctx, current, current.ResourceVersion)
	})
}

func (s *Service) githubState(ctx context.Context, owner domain.UserID) (GitHubConnectionState, error) {
	connection, found, err := s.loadGitHubConnection(ctx, owner)
	if err != nil || !found {
		return GitHubConnectionState{DeviceFlow: s.deviceFlowEnabled()}, err
	}
	return githubStateFrom(connection, s.deviceFlowEnabled()), nil
}

func (s *Service) loadGitHubConnection(
	ctx context.Context, owner domain.UserID,
) (domain.GitHubConnection, bool, error) {
	var connection domain.GitHubConnection
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		connection, err = reader.GetGitHubConnection(ctx, owner)
		return err
	})
	if errors.Is(err, domain.ErrNotFound) {
		return domain.GitHubConnection{}, false, nil
	}
	if err != nil {
		return domain.GitHubConnection{}, false, err
	}
	return connection, true, nil
}

// runtimeGitHubAccess is the owner's connection for one Capsule session.
// It is nil when the owner has turned Capsule GitHub access off, or when
// there is no active connection. A failure to open the token does not fail
// the session: the session starts without GitHub access. The caller wipes
// the token after the runtime has copied it.
func (s *Service) runtimeGitHubAccess(ctx context.Context, owner domain.UserID) (*harnesssetup.GitHubAccess, error) {
	var profile domain.UserProfile
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		profile, err = reader.GetUserProfile(ctx, owner)
		return err
	})
	if err != nil || !profile.CapsuleGitHubAccess {
		return nil, err
	}
	connection, found, err := s.loadGitHubConnection(ctx, owner)
	if err != nil || !found || !connection.Active() {
		return nil, err
	}
	token, err := s.openGitHubToken(connection)
	if err != nil {
		return nil, nil
	}
	access := &harnesssetup.GitHubAccess{Login: connection.Login, Token: token}
	wipeString(&token)
	if !access.Usable() {
		access.Wipe()
		return nil, nil
	}
	return access, nil
}

func (s *Service) openGitHubToken(connection domain.GitHubConnection) (string, error) {
	if s.secretKey == nil {
		return "", domain.ErrUnsupported
	}
	return s.secretKey.OpenGitHub(connection)
}

func (s *Service) deviceFlowEnabled() bool {
	return s.githubOAuth != nil && s.githubOAuthClientID != ""
}

func (s *Service) hostGitHubAllowed(ctx context.Context, loopback bool) bool {
	if !loopback {
		return false
	}
	var installation domain.Installation
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		installation, err = reader.GetInstallation(ctx)
		return err
	})
	if err != nil {
		return false
	}
	return HostGitHubAllowed(installation.Mode, true)
}

func githubStateFrom(connection domain.GitHubConnection, deviceFlow bool) GitHubConnectionState {
	return GitHubConnectionState{
		Connected: true, DeviceFlow: deviceFlow, Login: connection.Login,
		Status: connection.Status, Reason: connection.Reason, Scopes: connection.Scopes,
		CreatedAt: connection.CreatedAt, UpdatedAt: connection.UpdatedAt,
		ResourceVersion: connection.ResourceVersion,
	}
}

func repoScopeMissing(scopes string) bool {
	if strings.TrimSpace(scopes) == "" {
		return false
	}
	for _, scope := range strings.FieldsFunc(scopes, func(r rune) bool {
		return r == ',' || r == ' '
	}) {
		if strings.TrimSpace(scope) == "repo" {
			return false
		}
	}
	return true
}

func validGitHubLogin(login string) bool {
	if login == "" || len(login) > 39 {
		return false
	}
	for index, character := range login {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9':
		case character == '-' && index > 0 && index < len(login)-1:
		default:
			return false
		}
	}
	return true
}

func reconnectReason(err error) (domain.GitHubConnectionReason, bool) {
	_, reason, ok := gitHubAuthFailure(err)
	if !ok || reason == "" {
		return "", false
	}
	parsed := domain.GitHubConnectionReason(reason)
	if !parsed.Valid() || parsed == domain.GitHubReasonNone {
		return "", false
	}
	return parsed, true
}

func (s *Service) ensureGitHubRepositoryAccessible(ctx context.Context, repositoryURL, gitSecret string) error {
	if gitSecret != "" {
		return nil
	}
	probe, ok := s.githubOAuth.(RepositoryProbe)
	if !ok || probe == nil {
		return nil
	}
	ownerName, repoName, err := parseGitHubRepository(repositoryURL)
	if err != nil {
		return nil
	}
	token := ""
	user, err := ownerOf(ctx)
	if err != nil {
		return err
	}
	connection, found, err := s.loadGitHubConnection(ctx, user)
	if err != nil {
		return err
	}
	if found && connection.Active() {
		token, err = s.openGitHubToken(connection)
		if err != nil {
			return err
		}
		defer wipeString(&token)
	}
	status, _, err := probe.ProbeRepository(ctx, token, ownerName, repoName)
	if err != nil || status == 0 {
		return nil
	}
	if status == 401 && token != "" {
		_ = s.markGitHubStatus(ctx, user, domain.GitHubConnectionNeedsReconnect, domain.GitHubReasonInvalid)
	}
	if status == 401 || status == 403 || status == 404 {
		return &domain.RepositoryAccessError{Owner: ownerName, Name: repoName}
	}
	return nil
}

func (r *Reconciler) denyInaccessibleGitHub(ctx context.Context, repositoryURL string) error {
	if r.repoProbe == nil {
		return nil
	}
	ownerName, repoName, err := parseGitHubRepository(repositoryURL)
	if err != nil {
		return nil
	}
	status, _, err := r.repoProbe.ProbeRepository(ctx, "", ownerName, repoName)
	if err != nil || status == 0 {
		return nil
	}
	if status == 401 || status == 403 || status == 404 {
		return &domain.RepositoryAccessError{Owner: ownerName, Name: repoName}
	}
	return nil
}

func (r *Reconciler) repositoryAccessFailure(ctx context.Context, capsule domain.Capsule) string {
	var project domain.Project
	err := r.store.View(ctx, func(reader ports.Reader) error {
		var err error
		project, err = reader.GetProject(ctx, capsule.ProjectID)
		return err
	})
	if err != nil {
		return domain.RepositoryAccess.Message()
	}
	ownerName, repoName, err := parseGitHubRepository(project.RepositoryURL)
	if err != nil {
		return domain.RepositoryAccess.Message()
	}
	return domain.RepositoryAccessMessage(ownerName, repoName)
}

// wipeString drops a reference to a token so it is not kept or logged by
// accident. Go strings are immutable: this does not overwrite the bytes, which
// stay in memory until the garbage collector reclaims them.
func wipeString(value *string) {
	if value == nil || *value == "" {
		return
	}
	*value = ""
}

// gitHubHTTPSRepository reports whether a repository URL is an HTTPS
// github.com repository. Only such a URL may receive the GitHub connection's
// token: any other host would be handed a GitHub credential it can replay.
func gitHubHTTPSRepository(repositoryURL string) bool {
	parsed, err := url.Parse(repositoryURL)
	if err != nil || parsed.Scheme != "https" || parsed.Port() != "" && parsed.Port() != "443" {
		return false
	}
	_, _, err = parseGitHubRepository(repositoryURL)
	return err == nil
}

// openCloneCredential resolves the one-shot clone grant. A named git_https
// secret wins. Otherwise an active GitHub connection is used, and only for an
// HTTPS github.com repository. A missing or unusable connection, or another
// host, clones anonymously, which is how a public repository works today.
func openCloneCredential(
	ctx context.Context, store ports.Store, key *secrets.InstallationKey,
	owner domain.UserID, repositoryURL, explicitName string,
) (*ports.GitHTTPSCredential, error) {
	if explicitName != "" {
		if key == nil {
			return nil, domain.ErrInvalid
		}
		var secret domain.Secret
		if err := store.View(ctx, func(reader ports.Reader) error {
			var err error
			secret, err = reader.GetSecret(ctx, owner, explicitName)
			return err
		}); err != nil {
			return nil, err
		}
		if secret.Purpose != domain.SecretGitHTTPS {
			return nil, domain.ErrInvalid
		}
		payload, err := key.Open(secret)
		if err != nil {
			return nil, err
		}
		credential := &ports.GitHTTPSCredential{Username: payload.Username, Password: payload.Password}
		payload.Username, payload.Password, payload.Value = "", "", ""
		return credential, nil
	}
	if key == nil || !gitHubHTTPSRepository(repositoryURL) {
		return nil, nil
	}
	var connection domain.GitHubConnection
	err := store.View(ctx, func(reader ports.Reader) error {
		var err error
		connection, err = reader.GetGitHubConnection(ctx, owner)
		return err
	})
	if errors.Is(err, domain.ErrNotFound) || err == nil && !connection.Active() {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	token, err := key.OpenGitHub(connection)
	if err != nil {
		return nil, err
	}
	credential := &ports.GitHTTPSCredential{Username: gitHubHTTPSUser, Password: token}
	wipeString(&token)
	return credential, nil
}

// resolveDeliveryCredential resolves a push or pull-request credential.
// A named secret for that purpose wins and is never replaced by the
// connection. An empty name uses the owner's active GitHub connection, and
// only for an HTTPS github.com repository.
func (s *Service) resolveDeliveryCredential(
	ctx context.Context, owner domain.UserID, repositoryURL, name string, purpose domain.SecretPurpose,
) (secrets.Payload, bool, error) {
	if name != "" {
		payload, err := s.resolveSecret(ctx, owner, name, purpose)
		return payload, false, err
	}
	if !gitHubHTTPSRepository(repositoryURL) {
		return secrets.Payload{}, false, fmt.Errorf("%w: a %s secret is required for a repository outside github.com", domain.ErrIllegalTransition, purpose)
	}
	connection, found, err := s.loadGitHubConnection(ctx, owner)
	if err != nil {
		return secrets.Payload{}, false, err
	}
	if !found {
		return secrets.Payload{}, false, fmt.Errorf("%w: GitHub connection or a %s secret is required", domain.ErrIllegalTransition, purpose)
	}
	if !connection.Active() {
		return secrets.Payload{}, false, fmt.Errorf("%w: reconnect GitHub before delivery", domain.ErrIllegalTransition)
	}
	token, err := s.openGitHubToken(connection)
	if err != nil {
		return secrets.Payload{}, false, err
	}
	switch purpose {
	case domain.SecretGitPush:
		return secrets.Payload{Username: gitHubHTTPSUser, Password: token}, true, nil
	case domain.SecretGitHubAPI:
		return secrets.Payload{Value: token}, true, nil
	default:
		wipeString(&token)
		return secrets.Payload{}, false, domain.ErrInvalid
	}
}

func (s *Service) noteGitHubFailure(ctx context.Context, owner domain.UserID, err error) {
	reason, ok := reconnectReason(err)
	if !ok {
		return
	}
	_ = s.markGitHubStatus(ctx, owner, domain.GitHubConnectionNeedsReconnect, reason)
}
