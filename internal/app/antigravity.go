package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/antigravityauth"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

// Antigravity connections (ADR 0061). The connection holds agy's own sign-in
// profile, sealed. Meridian never uses the token in it: the daemon only
// checks the profile's shape, seals it, and opens it to send it into an agy
// runtime. Sign-in runs agy's own login in a sign-in Capsule that has no
// repository, secrets, setup, or connection.

const (
	antigravityHarness     = "agy"
	antigravitySignInTTL   = 10 * time.Minute
	antigravityDefaultName = "Google"
	// antigravitySlots bounds concurrent sign-ins. Each attempt uses one
	// fixed sign-in Capsule ID, so a restarted daemon can delete every
	// sign-in Capsule an earlier process left behind.
	antigravitySlots = 4
)

type AntigravitySignInState string

const (
	AntigravityStarting     AntigravitySignInState = "starting"
	AntigravityAwaitingCode AntigravitySignInState = "awaiting_code"
	AntigravityCompleting   AntigravitySignInState = "completing"
	AntigravityCompleted    AntigravitySignInState = "completed"
	AntigravityFailed       AntigravitySignInState = "failed"
)

// AntigravitySignIn is the public view of an attempt. It never includes the
// code or the profile.
type AntigravitySignIn struct {
	ID               string                 `json:"id"`
	State            AntigravitySignInState `json:"state"`
	AuthorizationURL string                 `json:"authorizationUrl,omitempty"`
	ExpiresAt        time.Time              `json:"expiresAt"`
	ConnectionID     string                 `json:"connectionId,omitempty"`
	Account          string                 `json:"account,omitempty"`
	Failure          string                 `json:"failure,omitempty"`
}

type antigravityAttempt struct {
	view       AntigravitySignIn
	starter    principal.Principal
	connection string
	slot       int
	resource   string
	// stop cancels a sign-in Capsule that is still starting.
	stop    context.CancelFunc
	ended   bool
	release sync.Once
}

type antigravityState struct {
	provider ports.CapsuleProvider
	runtime  ports.HarnessSignInRuntime

	mu       sync.Mutex
	attempts map[string]*antigravityAttempt
	// busy marks slots in use, or not yet swept after a start.
	busy [antigravitySlots]bool
	// delivered is the credential generation each Capsule last received,
	// so a stale profile's failure does not mark a newer sign-in.
	delivered map[domain.CapsuleID]int64
}

// ConfigureAntigravity enables Sign in with Google for agy. Every slot stays
// busy until SweepAntigravitySignIns has deleted what a previous process may
// have left.
func (s *Service) ConfigureAntigravity(provider ports.CapsuleProvider, runtime ports.HarnessSignInRuntime) {
	state := &antigravityState{provider: provider, runtime: runtime, attempts: map[string]*antigravityAttempt{}, delivered: map[domain.CapsuleID]int64{}}
	for slot := range state.busy {
		state.busy[slot] = true
	}
	s.antigravity = state
}

// SweepAntigravitySignIns deletes every sign-in Capsule, then frees the
// slots. The daemon runs it once at start.
func (s *Service) SweepAntigravitySignIns(ctx context.Context) error {
	if err := requireSystem(ctx); err != nil {
		return err
	}
	state := s.antigravity
	if state == nil {
		return nil
	}
	var failures []error
	for slot := range antigravitySlots {
		if err := state.provider.Delete(ctx, state.provider.ResourceID(antigravityCapsuleID(slot))); err != nil && !errors.Is(err, domain.ErrNotFound) {
			failures = append(failures, err)
			continue
		}
		state.mu.Lock()
		state.busy[slot] = false
		state.mu.Unlock()
	}
	return errors.Join(failures...)
}

func antigravityCapsuleID(slot int) domain.CapsuleID {
	return domain.CapsuleID(fmt.Sprintf("antigravity-sign-in-%d", slot))
}

// AntigravityAvailable reports whether Sign in with Google can be offered.
func (s *Service) AntigravityAvailable() bool {
	return s.antigravity != nil && s.secretKey != nil && s.antigravityImage() != ""
}

// antigravityImage is the installation's agy pack image.
func (s *Service) antigravityImage() string {
	for _, pack := range s.harnessPacks {
		if pack.Name == antigravityHarness {
			return pack.ImageReference
		}
	}
	return ""
}

// StartAntigravitySignIn starts a sign-in Capsule for a new connection, or to
// replace connectionID's profile.
func (s *Service) StartAntigravitySignIn(ctx context.Context, connectionID string) (AntigravitySignIn, error) {
	starter, err := principal.Require(ctx)
	if err != nil {
		return AntigravitySignIn{}, err
	}
	// Only a user owns a connection, so only a user may start one.
	if _, err := ownerOf(ctx); err != nil {
		return AntigravitySignIn{}, err
	}
	if !s.AntigravityAvailable() {
		return AntigravitySignIn{}, domain.ErrUnsupported
	}
	if connectionID != "" {
		if _, err := s.antigravityConnection(ctx, connectionID); err != nil {
			return AntigravitySignIn{}, err
		}
	}
	state := s.antigravity
	// A user has one sign-in at a time. A dashboard that lost its attempt,
	// after a reload or a closed tab, cannot finish it, so a new start ends
	// the earlier one instead of leaving it to hold a slot until it expires.
	state.mu.Lock()
	var replaced []*antigravityAttempt
	for _, attempt := range state.attempts {
		if !attempt.ended && attempt.starter.UserID() == starter.UserID() {
			replaced = append(replaced, attempt)
		}
	}
	state.mu.Unlock()
	for _, attempt := range replaced {
		s.endAntigravity(attempt, "A newer Google sign-in replaced this one.")
	}
	now := s.clock.Now().UTC()
	state.mu.Lock()
	state.pruneLocked(now)
	slot := -1
	for index, busy := range state.busy {
		if !busy {
			slot = index
			break
		}
	}
	if slot < 0 {
		state.mu.Unlock()
		return AntigravitySignIn{}, fmt.Errorf("%w: too many Google sign-ins are in progress; try again in a few minutes", domain.ErrConflict)
	}
	state.busy[slot] = true
	launch, stop := context.WithTimeout(context.Background(), antigravitySignInTTL)
	attempt := &antigravityAttempt{
		starter: starter, connection: connectionID, slot: slot, stop: stop,
		view: AntigravitySignIn{ID: s.ids.NewID(), State: AntigravityStarting, ExpiresAt: now.Add(antigravitySignInTTL)},
	}
	state.attempts[attempt.view.ID] = attempt
	view := attempt.view
	state.mu.Unlock()
	image := s.antigravityImage()
	go s.launchAntigravity(launch, attempt, image)
	time.AfterFunc(antigravitySignInTTL, func() { s.endAntigravity(attempt, "This sign-in expired; start again.") })
	return view, nil
}

// launchAntigravity creates the sign-in Capsule and asks agy for its link.
func (s *Service) launchAntigravity(ctx context.Context, attempt *antigravityAttempt, image string) {
	state := s.antigravity
	resource, err := state.provider.Create(ctx, ports.CreateCapsuleRequest{
		CapsuleID: antigravityCapsuleID(attempt.slot), ImageReference: image,
	})
	link := ""
	if err == nil {
		link, err = state.runtime.StartHarnessSignIn(ctx, resource.ID, antigravityHarness)
	}
	if err == nil && antigravityauth.CheckAuthorizationURL(link) != nil {
		err = errors.New("the sign-in Capsule returned an unexpected sign-in page")
	}
	state.mu.Lock()
	attempt.resource = resource.ID
	if attempt.ended {
		state.mu.Unlock()
		s.releaseAntigravity(attempt)
		return
	}
	if err != nil {
		attempt.ended = true
		attempt.view.State, attempt.view.Failure = AntigravityFailed, antigravityStartFailure(err)
		state.mu.Unlock()
		s.releaseAntigravity(attempt)
		return
	}
	attempt.view.State, attempt.view.AuthorizationURL = AntigravityAwaitingCode, link
	state.mu.Unlock()
}

func antigravityStartFailure(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "The sign-in Capsule did not start in time; start again."
	case errors.Is(err, domain.ErrUnsupported):
		return "This installation cannot start a sign-in Capsule."
	}
	return "The sign-in Capsule could not start Antigravity CLI's sign-in; start again."
}

// AntigravitySignInStatus returns an attempt's current view.
func (s *Service) AntigravitySignInStatus(ctx context.Context, id string) (AntigravitySignIn, error) {
	if err := requirePrincipal(ctx); err != nil {
		return AntigravitySignIn{}, err
	}
	if s.antigravity == nil {
		return AntigravitySignIn{}, domain.ErrUnsupported
	}
	state := s.antigravity
	state.mu.Lock()
	defer state.mu.Unlock()
	state.pruneLocked(s.clock.Now().UTC())
	attempt := state.attempts[id]
	if attempt == nil || !startedBy(ctx, attempt.starter) {
		return AntigravitySignIn{}, domain.ErrNotFound
	}
	return attempt.view, nil
}

// SendAntigravitySignInCode passes the pasted code to agy and, when agy
// accepts it, seals the profile it wrote. A rejected code ends the attempt
// and is reported in the view, not as an error.
func (s *Service) SendAntigravitySignInCode(ctx context.Context, id, code string) (AntigravitySignIn, error) {
	if err := requirePrincipal(ctx); err != nil {
		return AntigravitySignIn{}, err
	}
	if s.antigravity == nil {
		return AntigravitySignIn{}, domain.ErrUnsupported
	}
	code, err := antigravityauth.CleanCode(code)
	if err != nil {
		return AntigravitySignIn{}, fmt.Errorf("%w: paste the code from Google's page, or that page's full address", domain.ErrInvalid)
	}
	state := s.antigravity
	state.mu.Lock()
	state.pruneLocked(s.clock.Now().UTC())
	attempt := state.attempts[id]
	switch {
	case attempt == nil || !startedBy(ctx, attempt.starter):
		state.mu.Unlock()
		return AntigravitySignIn{}, domain.ErrNotFound
	case attempt.ended || attempt.view.State != AntigravityAwaitingCode:
		view := attempt.view
		state.mu.Unlock()
		return view, fmt.Errorf("%w: this sign-in is not waiting for a code", domain.ErrConflict)
	}
	attempt.view.State = AntigravityCompleting
	resource, starter, connectionID := attempt.resource, attempt.starter, attempt.connection
	state.mu.Unlock()

	// The exchange outlives a closed browser tab: agy may already have
	// redeemed the code, and the profile would be lost.
	exchange, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	files, err := state.runtime.FinishHarnessSignIn(exchange, resource, code)
	var connection domain.ProviderConnection
	account := ""
	if err == nil {
		account, err = antigravityauth.Check(files)
	}
	if err == nil {
		connection, err = s.saveAntigravity(principal.With(exchange, starter), connectionID, files, account)
	}
	for index := range files {
		wipe(files[index].Content)
	}
	state.mu.Lock()
	if err != nil {
		attempt.view.State, attempt.view.Failure = AntigravityFailed, antigravityFinishFailure(err)
	} else {
		attempt.view.State, attempt.view.ConnectionID, attempt.view.Account = AntigravityCompleted, connection.ID, account
	}
	attempt.ended = true
	view := attempt.view
	state.mu.Unlock()
	go s.releaseAntigravity(attempt)
	return view, nil
}

func antigravityFinishFailure(err error) string {
	switch {
	case errors.Is(err, ports.ErrHarnessSignInRejected):
		return "Google did not accept that code. Start again, and paste the code from the page Google shows after you sign in."
	case errors.Is(err, antigravityauth.ErrInvalid):
		return "Antigravity CLI signed in, but not with a personal Google AI plan sign-in Meridian can use."
	case errors.Is(err, domain.ErrConflict):
		return "The connection changed during sign-in; start again."
	}
	return "Antigravity CLI's sign-in did not complete; start again."
}

// CancelAntigravitySignIn ends an unfinished attempt and deletes its
// sign-in Capsule.
func (s *Service) CancelAntigravitySignIn(ctx context.Context, id string) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	if s.antigravity == nil {
		return domain.ErrUnsupported
	}
	state := s.antigravity
	state.mu.Lock()
	attempt := state.attempts[id]
	if attempt == nil || !startedBy(ctx, attempt.starter) {
		state.mu.Unlock()
		return domain.ErrNotFound
	}
	delete(state.attempts, id)
	state.mu.Unlock()
	s.endAntigravity(attempt, "This sign-in was cancelled.")
	return nil
}

// endAntigravity ends an attempt that has not finished. One still starting
// is cancelled, and its launch releases the Capsule.
func (s *Service) endAntigravity(attempt *antigravityAttempt, failure string) {
	state := s.antigravity
	state.mu.Lock()
	if attempt.ended {
		state.mu.Unlock()
		return
	}
	starting := attempt.view.State == AntigravityStarting
	attempt.ended = true
	attempt.view.State, attempt.view.Failure = AntigravityFailed, failure
	state.mu.Unlock()
	attempt.stop()
	if !starting {
		go s.releaseAntigravity(attempt)
	}
}

// releaseAntigravity deletes the attempt's sign-in Capsule once, then frees
// its slot. A slot whose Capsule could not be deleted stays busy until the
// next start sweeps it, so a later attempt never reuses its Capsule.
func (s *Service) releaseAntigravity(attempt *antigravityAttempt) {
	attempt.release.Do(func() {
		attempt.stop()
		state := s.antigravity
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if attempt.resource != "" {
			_ = state.runtime.CancelHarnessSignIn(ctx, attempt.resource)
		}
		if err := state.provider.Delete(ctx, state.provider.ResourceID(antigravityCapsuleID(attempt.slot))); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return
		}
		state.mu.Lock()
		state.busy[attempt.slot] = false
		state.mu.Unlock()
	})
}

func (state *antigravityState) pruneLocked(now time.Time) {
	for id, attempt := range state.attempts {
		if attempt.ended && now.After(attempt.view.ExpiresAt) {
			delete(state.attempts, id)
		}
	}
}

// antigravityConnection returns a live Antigravity connection the caller
// can see.
func (s *Service) antigravityConnection(ctx context.Context, id string) (domain.ProviderConnection, error) {
	var connection domain.ProviderConnection
	err := s.store.View(ctx, func(r ports.Reader) error {
		var err error
		connection, err = r.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		if connection.Provider != antigravityauth.Provider || connection.Revoked {
			return fmt.Errorf("%w: sign in again only for a connected Google account", domain.ErrConflict)
		}
		return nil
	})
	return connection, err
}

// saveAntigravity seals a checked profile as a new connection, or in place
// of connectionID's profile.
func (s *Service) saveAntigravity(ctx context.Context, connectionID string, files []antigravityauth.File, account string) (domain.ProviderConnection, error) {
	plain, err := json.Marshal(files)
	if err != nil {
		return domain.ProviderConnection{}, err
	}
	defer wipe(plain)
	var result domain.ProviderConnection
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if connectionID == "" {
			items, err := tx.ListProviderConnections(ctx)
			if err != nil {
				return err
			}
			if len(items) >= 128 {
				return domain.ErrConflict
			}
			owner, err := ownerOf(ctx)
			if err != nil {
				return err
			}
			result = domain.ProviderConnection{
				OwnerUserID: owner, ID: s.ids.NewID(), Provider: antigravityauth.Provider,
				Name: antigravityDefaultName, CreatedAt: s.clock.Now().UTC(),
			}
		} else {
			var err error
			result, err = tx.GetProviderConnection(ctx, connectionID)
			if err != nil {
				return err
			}
			if result.Provider != antigravityauth.Provider || result.Revoked {
				return domain.ErrConflict
			}
		}
		result.ResourceVersion++
		result.CredentialGeneration++
		result.Account, result.NeedsSignIn = account, false
		result.KeyID = s.secretKey.ID
		var err error
		result.Nonce, result.Ciphertext, err = s.secretKey.SealSetup(connectionEnvelopeID(result), plain)
		if err != nil {
			return err
		}
		if err := tx.PutProviderConnection(ctx, result); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditConnectionSignIn, "provider_connection", result.ID); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "provider_connection", result.ID, "provider_connection.updated", result.ResourceVersion, nil)
	})
	s.auditFailure(ctx, domain.AuditConnectionSignIn, "provider_connection", connectionID, err)
	return result, err
}

// openAntigravity opens a connection's sealed profile.
func (s *Service) openAntigravity(connection domain.ProviderConnection) ([]antigravityauth.File, error) {
	raw, err := s.secretKey.OpenSetup(connectionEnvelopeID(connection), connection.KeyID, connection.Nonce, connection.Ciphertext)
	if err != nil {
		return nil, err
	}
	defer wipe(raw)
	var files []antigravityauth.File
	if err := json.Unmarshal(raw, &files); err != nil {
		return nil, errors.New("Antigravity connection credentials are unreadable")
	}
	if _, err := antigravityauth.Check(files); err != nil {
		return nil, errors.New("Antigravity connection credentials are unreadable")
	}
	return files, nil
}

// putAntigravityConnection handles the only edits an Antigravity connection
// accepts through the connection API: rename and sign out. Its profile comes
// only from a completed sign-in. Sign-out keeps an empty envelope and never
// reaches Google: the user removes Antigravity's access in their Google
// Account to end the sign-in everywhere.
func (s *Service) putAntigravityConnection(ctx context.Context, id, name string, input ConnectionInput) (domain.ProviderConnection, error) {
	if id == "" {
		return domain.ProviderConnection{}, fmt.Errorf("%w: sign in with Google to create this connection", domain.ErrInvalid)
	}
	if input.APIKey != "" || input.BaseURL != "" || input.DefaultModel != "" || input.AllowPrivateHTTP || len(input.ReasoningEfforts) != 0 || input.CredentialKind != "" {
		return domain.ProviderConnection{}, fmt.Errorf("%w: a Google account connection takes no key or endpoint; sign in again to change its account", domain.ErrInvalid)
	}
	var result domain.ProviderConnection
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		result, err = tx.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		if result.ResourceVersion != input.ExpectedResourceVersion || result.Provider != input.Provider {
			return domain.ErrConflict
		}
		if result.Revoked {
			if input.Revoked && name == result.Name {
				return nil
			}
			return fmt.Errorf("%w: this Google account connection is signed out; sign in again to create a new one", domain.ErrConflict)
		}
		result.Name = name
		result.ResourceVersion++
		if input.Revoked {
			result.Revoked, result.NeedsSignIn = true, false
			result.CredentialGeneration++
			result.KeyID = s.secretKey.ID
			result.Nonce, result.Ciphertext, err = s.secretKey.SealSetup(connectionEnvelopeID(result), nil)
			if err != nil {
				return err
			}
		}
		if err := tx.PutProviderConnection(ctx, result); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, input.auditAction(), "provider_connection", result.ID); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "provider_connection", result.ID, "provider_connection.updated", result.ResourceVersion, nil)
	})
	s.auditFailure(ctx, input.auditAction(), "provider_connection", id, err)
	return result, err
}

// antigravityRuntimeConnection opens the profile for one agy runtime start.
// The Capsule's owner must own the connection.
func (s *Service) antigravityRuntimeConnection(capsule domain.Capsule, harness string, connection domain.ProviderConnection) (*harnesssetup.Connection, error) {
	if s.secretKey == nil {
		return nil, domain.ErrUnsupported
	}
	if connection.OwnerUserID != capsule.OwnerUserID {
		return nil, domain.ErrNotFound
	}
	if connection.NeedsSignIn {
		return nil, fmt.Errorf("%w: Google rejected this Antigravity sign-in; sign in again in Harness settings", domain.ErrConflict)
	}
	files, err := s.openAntigravity(connection)
	if err != nil {
		return nil, err
	}
	if s.antigravity != nil {
		s.antigravity.mu.Lock()
		s.antigravity.delivered[capsule.ID] = connection.CredentialGeneration
		s.antigravity.mu.Unlock()
	}
	return &harnesssetup.Connection{Provider: connection.Provider, Profile: files, Name: connection.Name}, nil
}

// noteAntigravityAuthRequired marks a Capsule's Antigravity connection as
// needing sign-in after agy reported that Google rejected its login. Only
// the generation this Capsule received is marked, so a Capsule holding an
// older profile never flags a newer sign-in; after a daemon restart nothing
// is marked.
func (s *Service) noteAntigravityAuthRequired(ctx context.Context, capsule domain.Capsule, harness string) {
	if s.antigravity == nil || strings.TrimSuffix(harness, "-structured") != antigravityHarness {
		return
	}
	s.antigravity.mu.Lock()
	generation, ok := s.antigravity.delivered[capsule.ID]
	s.antigravity.mu.Unlock()
	if !ok {
		return
	}
	caller, ok := capsulePrincipal(capsule)
	if !ok {
		return
	}
	ctx = principal.With(ctx, caller)
	_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
		id, err := tx.GetProjectProviderConnection(ctx, capsule.ProjectID, antigravityHarness)
		if err != nil || id == "" {
			return err
		}
		current, err := tx.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		if current.Provider != antigravityauth.Provider || current.Revoked || current.NeedsSignIn ||
			current.CredentialGeneration != generation || current.OwnerUserID != capsule.OwnerUserID {
			return nil
		}
		current.NeedsSignIn = true
		current.ResourceVersion++
		if err := tx.PutProviderConnection(ctx, current); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "provider_connection", current.ID, "provider_connection.updated", current.ResourceVersion, nil)
	})
}
