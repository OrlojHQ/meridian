package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/githubapp"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
)

const (
	githubAppSetupTTL   = 15 * time.Minute
	githubAppSetupLimit = 8
)

// ErrGitHubAppSignature is a webhook whose signature does not match the
// secret stored for the App. It is not an administrator check.
var ErrGitHubAppSignature = errors.New("github app webhook signature rejected")

// GitHubAppInstallationView is one install, with no credential.
type GitHubAppInstallationView struct {
	ID                  int64  `json:"id"`
	AccountLogin        string `json:"accountLogin"`
	AccountType         string `json:"accountType"`
	RepositorySelection string `json:"repositorySelection"`
	Suspended           bool   `json:"suspended"`
}

// GitHubAppView is the Station's App as the API shows it. It has no private
// key, webhook secret, or client secret.
type GitHubAppView struct {
	Configured      bool                        `json:"configured"`
	CanManage       bool                        `json:"canManage"`
	ResourceVersion int64                       `json:"resourceVersion"`
	AppID           int64                       `json:"appId,omitempty"`
	Slug            string                      `json:"slug,omitempty"`
	Name            string                      `json:"name,omitempty"`
	HTMLURL         string                      `json:"htmlUrl,omitempty"`
	OwnerLogin      string                      `json:"ownerLogin,omitempty"`
	OwnerType       string                      `json:"ownerType,omitempty"`
	Webhook         bool                        `json:"webhook"`
	InstallURL      string                      `json:"installUrl,omitempty"`
	Installations   []GitHubAppInstallationView `json:"installations"`
}

// GitHubAppSetup is a same-origin page that posts the manifest to GitHub.
// The manifest is not in this response. The page's own content security
// policy allows that one form. The Station UI does not.
type GitHubAppSetup struct {
	HandoffURL string `json:"handoffUrl"`
}

// GitHubAppAccess is whether Capsules for one Project can use the App.
type GitHubAppAccess struct {
	State      string `json:"state"`
	Message    string `json:"message,omitempty"`
	InstallURL string `json:"installUrl,omitempty"`
	AppSlug    string `json:"appSlug,omitempty"`
}

type githubAppPending struct {
	user      domain.UserID
	actionURL string
	manifest  string
	expires   time.Time
}

func (s *Service) ConfigureGitHubApp(publicURL string, api *githubapp.Client) {
	s.githubAppMu.Lock()
	defer s.githubAppMu.Unlock()
	s.githubAppPublicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if api == nil {
		api = githubapp.NewClient(nil)
	}
	s.githubAppAPI = api
}

// StartGitHubApp keeps refresh work inside the daemon lifetime.
func (s *Service) StartGitHubApp(ctx context.Context) {
	s.githubAppMu.Lock()
	s.githubAppCtx = ctx
	s.githubAppMu.Unlock()
}

func (s *Service) githubAPI() *githubapp.Client {
	s.githubAppMu.Lock()
	defer s.githubAppMu.Unlock()
	if s.githubAppAPI == nil {
		s.githubAppAPI = githubapp.NewClient(nil)
	}
	return s.githubAppAPI
}

// GitHubApp returns the Station App without its credentials.
func (s *Service) GitHubApp(ctx context.Context) (GitHubAppView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return GitHubAppView{}, err
	}
	can, err := s.canManageGitHubApp(ctx)
	if err != nil {
		return GitHubAppView{}, err
	}
	view, err := s.loadGitHubAppView(ctx)
	if err != nil {
		return GitHubAppView{}, err
	}
	view.CanManage = can
	return view, nil
}

// BeginGitHubAppSetup starts the manifest flow. organization empty creates
// the App on the signed-in user's account. The caller must be allowed to
// manage the App. origin is the browser origin that will receive the redirect.
func (s *Service) BeginGitHubAppSetup(ctx context.Context, organization, origin, host string) (GitHubAppSetup, error) {
	if err := requirePrincipal(ctx); err != nil {
		return GitHubAppSetup{}, err
	}
	if err := s.requireGitHubAppManager(ctx); err != nil {
		return GitHubAppSetup{}, err
	}
	caller, ok := principal.From(ctx)
	if !ok {
		return GitHubAppSetup{}, principal.ErrMissing
	}
	base, err := githubAppOriginAllowed(origin, host, s.publicGitHubAppURL())
	if err != nil {
		return GitHubAppSetup{}, err
	}
	organization = strings.TrimSpace(organization)
	state, err := githubAppState()
	if err != nil {
		return GitHubAppSetup{}, err
	}
	action, err := githubapp.ActionURL(organization, state)
	if err != nil {
		return GitHubAppSetup{}, fmt.Errorf("%w: %s", domain.ErrInvalid, "organization login")
	}
	nameSuffix, err := githubAppNameSuffix()
	if err != nil {
		return GitHubAppSetup{}, err
	}
	// GitHub appends code and state to this URL. It must have no query.
	redirect := base + "/github/app/callback"
	setupURL := base + "/github/app/setup-complete"
	webhook := ""
	if strings.HasPrefix(s.publicGitHubAppURL(), "https://") {
		webhook = s.publicGitHubAppURL() + "/github/app/webhook"
	}
	manifest, err := githubapp.NewManifest("Meridian-"+nameSuffix, redirect, setupURL, webhook)
	if err != nil {
		return GitHubAppSetup{}, fmt.Errorf("%w: manifest", domain.ErrInvalid)
	}
	encoded, err := manifest.JSON()
	if err != nil {
		return GitHubAppSetup{}, err
	}
	if !githubapp.AllowedActionURL(action) {
		return GitHubAppSetup{}, domain.ErrInvalid
	}
	s.rememberGitHubAppSetup(state, caller.UserID(), action, encoded)
	return GitHubAppSetup{HandoffURL: "/github/app/handoff?ticket=" + url.QueryEscape(state)}, nil
}

// GitHubAppHandoff returns the manifest form for a one-time ticket. The
// ticket is the setup state. It is not consumed here; GitHub's redirect
// consumes it. There is no principal: the ticket is the authorization, and
// the page is what a system browser loads.
func (s *Service) GitHubAppHandoff(ticket string) (actionURL, manifest string, err error) {
	pending, ok := s.peekGitHubAppSetup(ticket)
	if !ok || !githubapp.AllowedActionURL(pending.actionURL) || pending.manifest == "" {
		return "", "", fmt.Errorf("%w: this GitHub App setup has expired; start it again from Meridian", domain.ErrInvalid)
	}
	return pending.actionURL, pending.manifest, nil
}

// CompleteGitHubAppSetup exchanges the manifest code and stores the App.
// state was issued by BeginGitHubAppSetup. The private key is sealed and
// is not returned.
func (s *Service) CompleteGitHubAppSetup(ctx context.Context, state, code string) error {
	pending, ok := s.takeGitHubAppSetup(state)
	if !ok {
		return fmt.Errorf("%w: this GitHub App setup has expired; start it again from Meridian", domain.ErrInvalid)
	}
	user, ok := principal.User(pending.user)
	if !ok {
		return domain.ErrInvalid
	}
	ctx = principal.With(ctx, user)
	if err := s.requireGitHubAppManager(ctx); err != nil {
		return err
	}
	if s.secretKey == nil {
		return domain.ErrInvalid
	}
	conversion, err := s.githubAPI().ExchangeManifest(ctx, code)
	if err != nil {
		s.auditFailure(ctx, domain.AuditGitHubAppCreate, "github_app", domain.GitHubAppRowID, err)
		return fmt.Errorf("%w: GitHub did not finish creating the App", domain.ErrInvalid)
	}
	defer wipeString(&conversion.PrivateKey)
	defer wipeString(&conversion.ClientSecret)
	defer wipeString(&conversion.WebhookSecret)
	conversion.Slug = strings.ToLower(conversion.Slug)
	if _, err := githubapp.ParsePrivateKey(conversion.PrivateKey); err != nil {
		return fmt.Errorf("%w: GitHub returned an unusable App key", domain.ErrInvalid)
	}
	if err := validateGitHubAppConversion(conversion); err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetGitHubApp(ctx)
		version := domain.ResourceVersion(1)
		created := now
		if err == nil {
			version = current.ResourceVersion + 1
			created = current.CreatedAt
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		webhook := strings.HasPrefix(s.publicGitHubAppURL(), "https://")
		item := domain.GitHubApp{
			ID: domain.GitHubAppRowID, AppID: conversion.AppID, Slug: conversion.Slug,
			Name: conversion.Name, ClientID: conversion.ClientID, HTMLURL: conversion.HTMLURL,
			OwnerLogin: conversion.OwnerLogin, OwnerType: conversion.OwnerType,
			WebhookActive: webhook, CreatedBy: pending.user,
			CreatedAt: created, UpdatedAt: now, ResourceVersion: version,
		}
		nonce, ciphertext, err := s.secretKey.SealGitHubApp(item, secrets.GitHubAppSecrets{
			PrivateKey: conversion.PrivateKey, WebhookSecret: conversion.WebhookSecret,
			ClientSecret: conversion.ClientSecret,
		})
		if err != nil {
			return err
		}
		item.EnvelopeVersion = secrets.GitHubAppEnvelopeVersion()
		item.KEKID = s.secretKey.ID
		item.KEKVersion = s.secretKey.Version
		item.Nonce = nonce
		item.Ciphertext = ciphertext
		if err := tx.PutGitHubApp(ctx, item); err != nil {
			return err
		}
		if err := tx.ReplaceGitHubAppInstallations(ctx, nil); err != nil {
			return err
		}
		return s.audit(ctx, tx, domain.AuditGitHubAppCreate, "github_app", domain.GitHubAppRowID)
	})
	s.clearGitHubAppCache()
	if err != nil {
		s.auditFailure(ctx, domain.AuditGitHubAppCreate, "github_app", domain.GitHubAppRowID, err)
		return err
	}
	return nil
}

// DeleteGitHubApp removes the stored App. It does not uninstall it on GitHub.
func (s *Service) DeleteGitHubApp(ctx context.Context, expected domain.ResourceVersion) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	if err := s.requireGitHubAppManager(ctx); err != nil {
		return err
	}
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.DeleteGitHubApp(ctx, expected); err != nil {
			return err
		}
		return s.audit(ctx, tx, domain.AuditGitHubAppDelete, "github_app", domain.GitHubAppRowID)
	})
	s.clearGitHubAppCache()
	if err != nil {
		s.auditFailure(ctx, domain.AuditGitHubAppDelete, "github_app", domain.GitHubAppRowID, err)
	}
	return err
}

// SyncGitHubAppInstallations replaces the stored installations with what
// GitHub reports for this App.
func (s *Service) SyncGitHubAppInstallations(ctx context.Context) (GitHubAppView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return GitHubAppView{}, err
	}
	if err := s.requireGitHubAppManager(ctx); err != nil {
		return GitHubAppView{}, err
	}
	opened, err := s.openGitHubApp(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return s.GitHubApp(ctx)
	}
	if err != nil {
		return GitHubAppView{}, err
	}
	defer opened.close()
	items, err := s.githubAPI().ListInstallations(ctx, opened.app.AppID, opened.key)
	if err != nil {
		return GitHubAppView{}, fmt.Errorf("%w: could not list GitHub App installations", domain.ErrInvalid)
	}
	now := s.clock.Now().UTC()
	stored := make([]domain.GitHubAppInstallation, 0, len(items))
	for _, item := range items {
		if item.ID <= 0 || item.AccountLogin == "" {
			continue
		}
		stored = append(stored, domain.GitHubAppInstallation{
			ID: item.ID, AccountLogin: item.AccountLogin, AccountType: item.AccountType,
			RepositorySelection: item.RepositorySelection, Suspended: item.Suspended, UpdatedAt: now,
		})
	}
	if err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.ReplaceGitHubAppInstallations(ctx, stored)
	}); err != nil {
		return GitHubAppView{}, err
	}
	s.clearGitHubAppCache()
	return s.GitHubApp(ctx)
}

// GitHubAppAccess reports whether Capsules of projectID can use the App.
func (s *Service) GitHubAppAccess(ctx context.Context, projectID domain.ProjectID) (GitHubAppAccess, error) {
	if err := requirePrincipal(ctx); err != nil {
		return GitHubAppAccess{}, err
	}
	project, err := s.GetProject(ctx, projectID)
	if err != nil {
		return GitHubAppAccess{}, err
	}
	return s.githubAppAccess(ctx, project.OwnerUserID, project.RepositoryURL)
}

// ReceiveGitHubAppWebhook verifies the signature and applies an installation
// or CI event. A bad signature is refused. CI events wake the existing poll.
func (s *Service) ReceiveGitHubAppWebhook(ctx context.Context, event, signature string, body []byte) error {
	ctx = principal.WithSystem(ctx)
	opened, err := s.openGitHubApp(ctx)
	if err != nil {
		return err
	}
	defer opened.close()
	if !githubapp.ValidSignature(opened.secrets.WebhookSecret, body, signature) {
		return ErrGitHubAppSignature
	}
	switch event {
	case "installation":
		return s.applyInstallationWebhook(ctx, body)
	case "installation_repositories":
		s.clearGitHubAppCache()
		return nil
	case "ping":
		return nil
	case "check_run", "check_suite", "status", "pull_request", "pull_request_review", "issue_comment":
		s.wakeCI()
		return nil
	default:
		return nil
	}
}

func (s *Service) applyInstallationWebhook(ctx context.Context, body []byte) error {
	var payload struct {
		Action       string `json:"action"`
		Installation struct {
			ID                  int64   `json:"id"`
			RepositorySelection string  `json:"repository_selection"`
			SuspendedAt         *string `json:"suspended_at"`
			Account             struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"account"`
		} `json:"installation"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Installation.ID <= 0 {
		return nil
	}
	s.clearGitHubAppCache()
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.ListGitHubAppInstallations(ctx)
		if err != nil {
			return err
		}
		next := make([]domain.GitHubAppInstallation, 0, len(current)+1)
		if payload.Action == "deleted" {
			for _, item := range current {
				if item.ID != payload.Installation.ID {
					next = append(next, item)
				}
			}
			return tx.ReplaceGitHubAppInstallations(ctx, next)
		}
		selection := payload.Installation.RepositorySelection
		if selection != "all" {
			selection = "selected"
		}
		accountType := payload.Installation.Account.Type
		if accountType != "Organization" {
			accountType = "User"
		}
		updated := domain.GitHubAppInstallation{
			ID: payload.Installation.ID, AccountLogin: payload.Installation.Account.Login,
			AccountType: accountType, RepositorySelection: selection,
			Suspended: payload.Installation.SuspendedAt != nil && *payload.Installation.SuspendedAt != "",
			UpdatedAt: s.clock.Now().UTC(),
		}
		replaced := false
		for _, item := range current {
			if item.ID == updated.ID {
				next = append(next, updated)
				replaced = true
				continue
			}
			next = append(next, item)
		}
		if !replaced && updated.AccountLogin != "" {
			next = append(next, updated)
		}
		return tx.ReplaceGitHubAppInstallations(ctx, next)
	})
}

func (s *Service) loadGitHubAppView(ctx context.Context) (GitHubAppView, error) {
	var view GitHubAppView
	view.Installations = []GitHubAppInstallationView{}
	err := s.store.View(ctx, func(reader ports.Reader) error {
		appRow, err := reader.GetGitHubApp(ctx)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		items, err := reader.ListGitHubAppInstallations(ctx)
		if err != nil {
			return err
		}
		view.Configured = true
		view.ResourceVersion = int64(appRow.ResourceVersion)
		view.AppID = appRow.AppID
		view.Slug = appRow.Slug
		view.Name = appRow.Name
		view.HTMLURL = appRow.HTMLURL
		view.OwnerLogin = appRow.OwnerLogin
		view.OwnerType = appRow.OwnerType
		view.Webhook = appRow.WebhookActive
		view.InstallURL = "https://github.com/apps/" + url.PathEscape(appRow.Slug) + "/installations/new"
		for _, item := range items {
			view.Installations = append(view.Installations, GitHubAppInstallationView{
				ID: item.ID, AccountLogin: item.AccountLogin, AccountType: item.AccountType,
				RepositorySelection: item.RepositorySelection, Suspended: item.Suspended,
			})
		}
		return nil
	})
	return view, err
}

func (s *Service) requireGitHubAppManager(ctx context.Context) error {
	ok, err := s.canManageGitHubApp(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrForbidden
	}
	return nil
}

func (s *Service) canManageGitHubApp(ctx context.Context) (bool, error) {
	caller, ok := principal.From(ctx)
	if !ok {
		return false, principal.ErrMissing
	}
	if caller.Kind() == principal.KindSystem {
		return true, nil
	}
	if caller.Kind() != principal.KindUser {
		return false, nil
	}
	var mode domain.InstallationMode
	var user domain.User
	err := s.store.View(ctx, func(reader ports.Reader) error {
		installation, err := reader.GetInstallation(ctx)
		if err != nil {
			return err
		}
		mode = installation.Mode
		if mode != domain.TeamMode {
			return nil
		}
		user, err = reader.GetUser(ctx, caller.UserID())
		return err
	})
	if err != nil {
		return false, err
	}
	if mode != domain.TeamMode {
		return true, nil
	}
	return user.Role == domain.UserAdmin && user.DisabledAt.IsZero(), nil
}

func (s *Service) rememberGitHubAppSetup(state string, user domain.UserID, actionURL, manifest string) {
	s.githubAppMu.Lock()
	defer s.githubAppMu.Unlock()
	if s.githubAppSetups == nil {
		s.githubAppSetups = map[string]githubAppPending{}
	}
	now := s.clock.Now()
	for key, pending := range s.githubAppSetups {
		if now.After(pending.expires) {
			delete(s.githubAppSetups, key)
		}
	}
	kept := 0
	for _, pending := range s.githubAppSetups {
		if pending.user == user {
			kept++
		}
	}
	if kept >= githubAppSetupLimit {
		var oldest string
		var oldestAt time.Time
		for key, pending := range s.githubAppSetups {
			if pending.user != user {
				continue
			}
			if oldest == "" || pending.expires.Before(oldestAt) {
				oldest, oldestAt = key, pending.expires
			}
		}
		delete(s.githubAppSetups, oldest)
	}
	s.githubAppSetups[state] = githubAppPending{
		user: user, actionURL: actionURL, manifest: manifest, expires: now.Add(githubAppSetupTTL),
	}
}

func (s *Service) peekGitHubAppSetup(state string) (githubAppPending, bool) {
	s.githubAppMu.Lock()
	defer s.githubAppMu.Unlock()
	pending, ok := s.githubAppSetups[state]
	if !ok || s.clock.Now().After(pending.expires) {
		return githubAppPending{}, false
	}
	return pending, true
}

func (s *Service) takeGitHubAppSetup(state string) (githubAppPending, bool) {
	s.githubAppMu.Lock()
	defer s.githubAppMu.Unlock()
	pending, ok := s.githubAppSetups[state]
	if !ok {
		return githubAppPending{}, false
	}
	delete(s.githubAppSetups, state)
	if s.clock.Now().After(pending.expires) {
		return githubAppPending{}, false
	}
	return pending, true
}

func (s *Service) publicGitHubAppURL() string {
	s.githubAppMu.Lock()
	defer s.githubAppMu.Unlock()
	return s.githubAppPublicURL
}

func (s *Service) clearGitHubAppCache() {
	s.githubAppMu.Lock()
	s.githubAppCache = nil
	s.githubAppMu.Unlock()
}

func githubAppState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func githubAppNameSuffix() (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func githubAppOriginAllowed(origin, host, publicURL string) (string, error) {
	base, err := githubAppOrigin(origin)
	if err != nil {
		return "", err
	}
	parsed, _ := url.Parse(base)
	if host != "" && strings.EqualFold(parsed.Host, host) {
		return base, nil
	}
	if publicURL != "" {
		pub, err := url.Parse(publicURL)
		if err == nil && pub.Host != "" && strings.EqualFold(parsed.Host, pub.Host) && parsed.Scheme == pub.Scheme {
			return base, nil
		}
	}
	return "", fmt.Errorf("%w: browser origin", domain.ErrInvalid)
}

func githubAppOrigin(origin string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: browser origin", domain.ErrInvalid)
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func validateGitHubAppConversion(item githubapp.Conversion) error {
	if item.AppID <= 0 || item.ClientID == "" || item.PrivateKey == "" || item.OwnerLogin == "" {
		return fmt.Errorf("%w: GitHub App response was incomplete", domain.ErrInvalid)
	}
	if !validGitHubSlug(strings.ToLower(item.Slug)) {
		return fmt.Errorf("%w: GitHub App slug", domain.ErrInvalid)
	}
	if item.OwnerType != "User" && item.OwnerType != "Organization" {
		return fmt.Errorf("%w: GitHub App owner", domain.ErrInvalid)
	}
	parsed, err := url.Parse(item.HTMLURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") {
		return fmt.Errorf("%w: GitHub App url", domain.ErrInvalid)
	}
	return nil
}
