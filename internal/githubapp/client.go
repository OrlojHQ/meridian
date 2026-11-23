package githubapp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultAPI = "https://api.github.com"

// Client calls the GitHub App API. Base is the API origin, overridden in tests.
type Client struct {
	http *http.Client
	base string
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	copy := *httpClient
	copy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{http: &copy, base: defaultAPI}
}

// WithBase returns a client that talks to base instead of api.github.com.
// Tests use it. Production keeps the default.
func (c *Client) WithBase(base string) *Client {
	next := *c
	next.base = strings.TrimRight(base, "/")
	return &next
}

// Conversion is the one-time answer from GitHub's manifest exchange. The
// private key, webhook secret, and client secret are shown only here.
type Conversion struct {
	AppID         int64
	Slug          string
	Name          string
	ClientID      string
	ClientSecret  string
	WebhookSecret string
	PrivateKey    string
	HTMLURL       string
	OwnerLogin    string
	OwnerType     string
}

// Installation is one install of the App.
type Installation struct {
	ID                  int64
	AccountLogin        string
	AccountType         string
	RepositorySelection string
	Suspended           bool
}

// Token is a short-lived installation access token.
type Token struct {
	Value   string
	Expires time.Time
}

// ExchangeManifest trades the manifest code for the App credentials.
// The code is single-use. The request is unauthenticated.
func (c *Client) ExchangeManifest(ctx context.Context, code string) (Conversion, error) {
	if code == "" || strings.ContainsAny(code, "/?#") {
		return Conversion{}, errInvalid("manifest code")
	}
	var response struct {
		ID            int64  `json:"id"`
		Slug          string `json:"slug"`
		Name          string `json:"name"`
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		WebhookSecret string `json:"webhook_secret"`
		PEM           string `json:"pem"`
		HTMLURL       string `json:"html_url"`
		Owner         struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"owner"`
	}
	endpoint := c.base + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	if err := c.call(ctx, http.MethodPost, endpoint, "", nil, &response); err != nil {
		return Conversion{}, err
	}
	return Conversion{
		AppID: response.ID, Slug: response.Slug, Name: response.Name,
		ClientID: response.ClientID, ClientSecret: response.ClientSecret,
		WebhookSecret: response.WebhookSecret, PrivateKey: response.PEM,
		HTMLURL: response.HTMLURL, OwnerLogin: response.Owner.Login, OwnerType: response.Owner.Type,
	}, nil
}

// RepositoryInstallation finds the installation that covers owner/repo.
// A 404 is ErrNotInstalled.
func (c *Client) RepositoryInstallation(ctx context.Context, appID int64, key *rsa.PrivateKey, owner, repo string) (Installation, error) {
	jwt, err := SignJWT(appID, key, time.Now())
	if err != nil {
		return Installation{}, err
	}
	endpoint := c.base + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/installation"
	var response installationJSON
	if err := c.call(ctx, http.MethodGet, endpoint, jwt, nil, &response); err != nil {
		var api *APIError
		if errors.As(err, &api) && api.Status == http.StatusNotFound {
			return Installation{}, ErrNotInstalled
		}
		return Installation{}, err
	}
	return response.installation(), nil
}

// ListInstallations lists the App's installations.
func (c *Client) ListInstallations(ctx context.Context, appID int64, key *rsa.PrivateKey) ([]Installation, error) {
	jwt, err := SignJWT(appID, key, time.Now())
	if err != nil {
		return nil, err
	}
	var response []installationJSON
	if err := c.call(ctx, http.MethodGet, c.base+"/app/installations?per_page=100", jwt, nil, &response); err != nil {
		return nil, err
	}
	items := make([]Installation, 0, len(response))
	for _, item := range response {
		items = append(items, item.installation())
	}
	return items, nil
}

// CreateInstallationToken mints a token for one repository. permissions is
// the subset the token should have. GitHub expires the token in one hour.
func (c *Client) CreateInstallationToken(
	ctx context.Context, appID int64, key *rsa.PrivateKey, installationID int64,
	repository string, permissions map[string]string,
) (Token, error) {
	if installationID <= 0 || repository == "" || strings.Contains(repository, "/") {
		return Token{}, errInvalid("installation token request")
	}
	jwt, err := SignJWT(appID, key, time.Now())
	if err != nil {
		return Token{}, err
	}
	body := map[string]any{
		"repositories": []string{repository},
		"permissions":  permissions,
	}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	endpoint := c.base + "/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	if err := c.call(ctx, http.MethodPost, endpoint, jwt, body, &response); err != nil {
		return Token{}, err
	}
	if response.Token == "" || response.ExpiresAt.IsZero() {
		return Token{}, errors.New("github app token response was incomplete")
	}
	return Token{Value: response.Token, Expires: response.ExpiresAt.UTC()}, nil
}

type installationJSON struct {
	ID                  int64   `json:"id"`
	RepositorySelection string  `json:"repository_selection"`
	SuspendedAt         *string `json:"suspended_at"`
	Account             struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
}

func (item installationJSON) installation() Installation {
	selection := item.RepositorySelection
	if selection != "all" {
		selection = "selected"
	}
	accountType := item.Account.Type
	if accountType != "Organization" {
		accountType = "User"
	}
	return Installation{
		ID: item.ID, AccountLogin: item.Account.Login, AccountType: accountType,
		RepositorySelection: selection, Suspended: item.SuspendedAt != nil && *item.SuspendedAt != "",
	}
}

func (c *Client) call(ctx context.Context, method, endpoint, bearer string, body any, dest any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &APIError{Status: response.StatusCode}
	}
	if dest == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, dest); err != nil {
		return err
	}
	return nil
}
