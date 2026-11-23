package app

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/githubapp"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/secrets"
)

const githubTokenRefreshLead = 10 * time.Minute

// coverage is whether the App can mint a token for a repository.
type appCoverage int

const (
	appMissing appCoverage = iota
	appNotInstalled
	appSuspended
	appCovered
	appUnknown
)

type openedGitHubApp struct {
	app     domain.GitHubApp
	key     *rsa.PrivateKey
	secrets secrets.GitHubAppSecrets
}

func (o *openedGitHubApp) close() {
	if o == nil {
		return
	}
	wipeString(&o.secrets.PrivateKey)
	wipeString(&o.secrets.WebhookSecret)
	wipeString(&o.secrets.ClientSecret)
	o.key = nil
}

func (s *Service) openGitHubApp(ctx context.Context) (*openedGitHubApp, error) {
	if s.secretKey == nil {
		return nil, domain.ErrInvalid
	}
	var appRow domain.GitHubApp
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		appRow, err = reader.GetGitHubApp(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	sealed, err := s.secretKey.OpenGitHubApp(appRow)
	if err != nil {
		return nil, err
	}
	key, err := githubapp.ParsePrivateKey(sealed.PrivateKey)
	if err != nil {
		wipeString(&sealed.PrivateKey)
		wipeString(&sealed.WebhookSecret)
		wipeString(&sealed.ClientSecret)
		return nil, err
	}
	return &openedGitHubApp{app: appRow, key: key, secrets: sealed}, nil
}

// mintInstallationToken returns a repository-scoped installation token when
// the App covers repositoryURL. covered is false when the App is not
// configured or not installed. The private key stays in this process.
func (s *Service) mintInstallationToken(ctx context.Context, repositoryURL string) (token string, expires time.Time, slug string, covered bool, err error) {
	owner, repo, parseErr := parseGitHubRepository(repositoryURL)
	if parseErr != nil || !gitHubHTTPSRepository(repositoryURL) {
		return "", time.Time{}, "", false, nil
	}
	opened, err := s.openGitHubApp(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return "", time.Time{}, "", false, nil
	}
	if err != nil {
		return "", time.Time{}, "", false, err
	}
	defer opened.close()
	slug = opened.app.Slug
	coverage, installationID, err := s.coverageFor(ctx, opened, owner, repo)
	if err != nil {
		return "", time.Time{}, slug, false, err
	}
	if coverage != appCovered {
		return "", time.Time{}, slug, false, nil
	}
	minted, err := s.githubAPI().CreateInstallationToken(ctx, opened.app.AppID, opened.key, installationID, repo, githubapp.TokenPermissions())
	if err != nil {
		return "", time.Time{}, slug, true, err
	}
	return minted.Value, minted.Expires, slug, true, nil
}

func (s *Service) coverageFor(ctx context.Context, opened *openedGitHubApp, owner, repo string) (appCoverage, int64, error) {
	key := strings.ToLower(owner + "/" + repo)
	now := s.clock.Now()
	s.githubAppMu.Lock()
	if s.githubAppCache != nil {
		if hit, ok := s.githubAppCache[key]; ok && now.Before(hit.until) {
			s.githubAppMu.Unlock()
			return hit.coverage, hit.installationID, nil
		}
	}
	s.githubAppMu.Unlock()
	installation, err := s.githubAPI().RepositoryInstallation(ctx, opened.app.AppID, opened.key, owner, repo)
	if errors.Is(err, githubapp.ErrNotInstalled) {
		s.rememberCoverage(key, appNotInstalled, 0, now.Add(time.Minute))
		return appNotInstalled, 0, nil
	}
	if err != nil {
		return appUnknown, 0, err
	}
	if installation.Suspended {
		s.rememberCoverage(key, appSuspended, installation.ID, now.Add(time.Minute))
		return appSuspended, installation.ID, nil
	}
	s.rememberCoverage(key, appCovered, installation.ID, now.Add(10*time.Minute))
	return appCovered, installation.ID, nil
}

type githubCoverageHit struct {
	coverage       appCoverage
	installationID int64
	until          time.Time
}

func (s *Service) rememberCoverage(key string, coverage appCoverage, installationID int64, until time.Time) {
	s.githubAppMu.Lock()
	defer s.githubAppMu.Unlock()
	if s.githubAppCache == nil {
		s.githubAppCache = map[string]githubCoverageHit{}
	}
	s.githubAppCache[key] = githubCoverageHit{coverage: coverage, installationID: installationID, until: until}
}

// CloneInstallationToken is the one-shot clone grant from the App. covered
// is false when the App does not apply, and the caller keeps the existing
// credential. When covered is true the token is the only clone credential:
// a mint failure is returned instead of the user's login.
func (s *Service) CloneInstallationToken(ctx context.Context, repositoryURL string) (token string, covered bool, err error) {
	if err := requirePrincipal(ctx); err != nil {
		return "", false, err
	}
	token, _, _, covered, err = s.mintInstallationToken(ctx, repositoryURL)
	if err != nil && covered {
		wipeString(&token)
		return "", true, fmt.Errorf("%w: could not mint a GitHub App token for clone", domain.ErrInvalid)
	}
	if err != nil {
		wipeString(&token)
		return "", false, nil
	}
	return token, covered, nil
}

// runtimeGitHubAccess is the session token for a Capsule, or nil when the
// switch is off or the App does not cover the repository. It never returns
// the user's personal GitHub token.
func (s *Service) runtimeGitHubAccess(ctx context.Context, owner domain.UserID, repositoryURL string) (*harnesssetup.GitHubAccess, error) {
	var profile domain.UserProfile
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		profile, err = reader.GetUserProfile(ctx, owner)
		return err
	})
	if err != nil || !profile.CapsuleGitHubAccess {
		return nil, err
	}
	token, expires, slug, covered, err := s.mintInstallationToken(ctx, repositoryURL)
	if err != nil || !covered || token == "" {
		wipeString(&token)
		return nil, nil
	}
	return &harnesssetup.GitHubAccess{
		Login: slug, Token: token, Expires: expires, Repository: repositoryURL,
	}, nil
}

func (s *Service) githubAppAccess(ctx context.Context, owner domain.UserID, repositoryURL string) (GitHubAppAccess, error) {
	if !gitHubHTTPSRepository(repositoryURL) {
		return GitHubAppAccess{State: "not_github"}, nil
	}
	repoOwner, repoName, _ := parseGitHubRepository(repositoryURL)
	var profile domain.UserProfile
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		profile, err = reader.GetUserProfile(ctx, owner)
		return err
	})
	if err != nil {
		return GitHubAppAccess{}, err
	}
	view, err := s.loadGitHubAppView(ctx)
	if err != nil {
		return GitHubAppAccess{}, err
	}
	if !profile.CapsuleGitHubAccess {
		return GitHubAppAccess{
			State: "off", AppSlug: view.Slug,
			Message: "GitHub access for Capsules is turned off. Turn it on under What Capsules inherit. Clone, push, and pull requests on the Station still use your GitHub login.",
		}, nil
	}
	if !view.Configured {
		return GitHubAppAccess{
			State: "not_configured",
			Message: "This Station has no GitHub App, so Capsules cannot use gh. Create one in GitHub settings. Your GitHub login still works for clone, push, and pull requests on the Station.",
		}, nil
	}
	opened, err := s.openGitHubApp(ctx)
	if err != nil {
		return GitHubAppAccess{State: "unavailable", AppSlug: view.Slug, InstallURL: view.InstallURL,
			Message: "Meridian could not read the GitHub App. Capsules will not receive a token until that succeeds."}, nil
	}
	defer opened.close()
	coverage, _, err := s.coverageFor(ctx, opened, repoOwner, repoName)
	if err != nil || coverage == appUnknown {
		return GitHubAppAccess{State: "unavailable", AppSlug: view.Slug, InstallURL: view.InstallURL,
			Message: fmt.Sprintf("Meridian could not ask GitHub whether the App is installed on %s/%s. Capsules will not receive a token until that check succeeds.", repoOwner, repoName)}, nil
	}
	switch coverage {
	case appSuspended:
		return GitHubAppAccess{State: "suspended", AppSlug: view.Slug, InstallURL: view.InstallURL,
			Message: fmt.Sprintf("The GitHub App installation that covers %s/%s is suspended, so Capsules cannot use gh.", repoOwner, repoName)}, nil
	case appNotInstalled:
		return GitHubAppAccess{State: "not_installed", AppSlug: view.Slug, InstallURL: view.InstallURL,
			Message: fmt.Sprintf("The GitHub App is not installed on %s/%s. Install it and include this repository, or choose All repositories. Until then, Capsules cannot use gh. Clone, push, and pull requests on the Station still use your GitHub login.", repoOwner, repoName)}, nil
	default:
		return GitHubAppAccess{State: "ready", AppSlug: view.Slug, InstallURL: view.InstallURL}, nil
	}
}

type githubTokenPusher interface {
	RefreshGitHubToken(context.Context, string, string) error
}

func (s *Service) pushGitHubToken(ctx context.Context, resourceID, token string) error {
	if s.githubPush != nil {
		return s.githubPush.RefreshGitHubToken(ctx, resourceID, token)
	}
	if p, ok := s.runtime.(githubTokenPusher); ok {
		return p.RefreshGitHubToken(ctx, resourceID, token)
	}
	if p, ok := s.structured.(githubTokenPusher); ok {
		return p.RefreshGitHubToken(ctx, resourceID, token)
	}
	return errors.New("capsule cannot refresh a github token")
}

func githubRefreshWait(now, expires time.Time) time.Duration {
	wait := expires.Add(-githubTokenRefreshLead).Sub(now)
	if wait < time.Minute {
		wait = time.Minute
	}
	if wait > 50*time.Minute {
		wait = 50 * time.Minute
	}
	return wait
}

// scheduleGitHubRefresh mints a new installation token before the current
// one expires and pushes it into the Capsule. The private key stays here.
func (s *Service) scheduleGitHubRefresh(resourceID, repositoryURL string, expires time.Time) {
	if resourceID == "" || repositoryURL == "" || expires.IsZero() {
		return
	}
	wait := githubRefreshWait(s.clock.Now(), expires)
	s.githubAppMu.Lock()
	if s.githubRefresh == nil {
		s.githubRefresh = map[string]context.CancelFunc{}
	}
	if cancel := s.githubRefresh[resourceID]; cancel != nil {
		cancel()
	}
	parent := s.githubAppCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.githubRefresh[resourceID] = cancel
	s.githubAppMu.Unlock()
	go func() {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		token, exp, _, covered, err := s.mintInstallationToken(ctx, repositoryURL)
		if err != nil || !covered || token == "" {
			wipeString(&token)
			return
		}
		pushErr := s.pushGitHubToken(ctx, resourceID, token)
		wipeString(&token)
		if pushErr != nil {
			return
		}
		s.scheduleGitHubRefresh(resourceID, repositoryURL, exp)
	}()
}
