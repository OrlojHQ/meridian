package githubapp

import (
	"encoding/json"
	"net/url"
	"strings"
)

// Manifest is the GitHub App manifest the browser posts to GitHub. It
// contains no secret. GitHub shows it for confirmation and then redirects
// to RedirectURL with a one-time code.
type Manifest struct {
	Name                  string            `json:"name"`
	URL                   string            `json:"url"`
	Description           string            `json:"description"`
	RedirectURL           string            `json:"redirect_url"`
	CallbackURLs          []string          `json:"callback_urls"`
	SetupURL              string            `json:"setup_url"`
	SetupOnUpdate         bool              `json:"setup_on_update"`
	Public                bool              `json:"public"`
	RequestOAuthOnInstall bool              `json:"request_oauth_on_install"`
	DefaultPermissions    map[string]string `json:"default_permissions"`
	DefaultEvents         []string          `json:"default_events,omitempty"`
	HookAttributes        *HookAttributes   `json:"hook_attributes,omitempty"`
}

// HookAttributes is the App webhook. It is set only when the Station has a
// public HTTPS address GitHub can reach.
type HookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// ActionURL is the GitHub page that creates the App. An empty organization
// creates it on the signed-in user. Otherwise it creates it on that org.
func ActionURL(organization string) (string, error) {
	if organization == "" {
		return "https://github.com/settings/apps/new", nil
	}
	if !validLogin(organization) {
		return "", errInvalid("organization login")
	}
	return "https://github.com/organizations/" + url.PathEscape(organization) + "/settings/apps/new", nil
}

// NewManifest builds the manifest for a browser origin. webhookURL is the
// public HTTPS webhook, or empty when the Station has no public address.
// Polling still learns CI either way.
func NewManifest(redirectURL, setupURL, webhookURL string) (Manifest, error) {
	if err := httpsOrLocal(redirectURL); err != nil {
		return Manifest{}, err
	}
	if err := httpsOrLocal(setupURL); err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		Name:                  "Meridian",
		URL:                   "https://github.com/OrlojHQ/meridian",
		Description:           "Meridian Station. Capsules receive a short-lived token for one repository.",
		RedirectURL:           redirectURL,
		CallbackURLs:          []string{stripQuery(redirectURL)},
		SetupURL:              setupURL,
		SetupOnUpdate:         true,
		Public:                false,
		RequestOAuthOnInstall: false,
		DefaultPermissions:    ManifestPermissions(),
	}
	if webhookURL != "" {
		parsed, err := url.Parse(webhookURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return Manifest{}, errInvalid("webhook url")
		}
		manifest.HookAttributes = &HookAttributes{URL: webhookURL, Active: true}
		manifest.DefaultEvents = []string{
			"check_run", "check_suite", "status",
			"pull_request", "pull_request_review", "issue_comment",
			"installation", "installation_repositories",
		}
	}
	return manifest, nil
}

// JSON returns the manifest as the form field GitHub expects.
func (m Manifest) JSON() (string, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func stripQuery(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func httpsOrLocal(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return errInvalid("redirect url")
	}
	return nil
}

func validLogin(login string) bool {
	if len(login) < 1 || len(login) > 39 {
		return false
	}
	for i := 0; i < len(login); i++ {
		r := login[i]
		ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok || (r == '-' && (i == 0 || i == len(login)-1)) {
			return false
		}
	}
	return true
}

type invalidError string

func (e invalidError) Error() string { return string(e) }

func errInvalid(what string) error { return invalidError("invalid github app " + what) }

// ValidLogin reports whether login can be a GitHub user or organization.
func ValidLogin(login string) bool { return login == "" || validLogin(login) }

// IsInvalid reports a rejected manifest field.
func IsInvalid(err error) bool {
	_, ok := err.(invalidError)
	return ok || (err != nil && strings.Contains(err.Error(), "invalid github app"))
}
