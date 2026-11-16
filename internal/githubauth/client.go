// Package githubauth talks to GitHub for the device flow, login check, and
// repository search. Tokens stay in memory and are never written to a URL,
// an error string, or a log.
package githubauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultDeviceURL = "https://github.com/login/device/code"
	defaultTokenURL  = "https://github.com/login/oauth/access_token"
	defaultAPIBase   = "https://api.github.com"
	// RepoScope is the classic scope that covers private clone, push, and
	// pull requests. read:user lets the station show the login.
	RepoScope = "repo read:user"
)

// StatusError is a GitHub HTTP status. Reason classifies a credential
// problem: invalid, expired, or missing_access. Other failures leave Reason
// empty so a single repository's permission error does not drop the login.
type StatusError struct {
	Status int
	Reason string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("GitHub API request failed with status %d", e.Status)
}

func (e *StatusError) GitHubAuthStatus() (int, string) { return e.Status, e.Reason }

// DeviceCode is the start of a device authorization. DeviceCode itself is
// the polling secret and must not be shown.
type DeviceCode struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresIn       int
	Interval        int
}

// DevicePoll is one poll of the device flow.
type DevicePoll struct {
	Token    string
	Scope    string
	Pending  bool
	SlowDown bool
	Denied   bool
	Expired  bool
}

// Account is the authenticated GitHub user. Scopes come from the response
// header and may be empty for some token kinds.
type Account struct {
	Login  string
	Scopes string
}

// Repo is a repository the token can see.
type Repo struct {
	FullName      string
	Private       bool
	Description   string
	DefaultBranch string
	URL           string
}

// Client is a GitHub HTTP client. Empty endpoint fields use the public
// GitHub URLs. Tests point them at a local server.
type Client struct {
	HTTP      *http.Client
	DeviceURL string
	TokenURL  string
	APIBase   string
}

func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	copy := *httpClient
	copy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		HTTP: &copy, DeviceURL: defaultDeviceURL, TokenURL: defaultTokenURL, APIBase: defaultAPIBase,
	}
}

func (c *Client) RequestDeviceCode(ctx context.Context, clientID string) (DeviceCode, error) {
	var response struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
	}
	err := c.postForm(ctx, c.DeviceURL, url.Values{
		"client_id": {clientID}, "scope": {RepoScope},
	}, &response)
	if err != nil {
		return DeviceCode{}, err
	}
	if response.Error != "" || response.DeviceCode == "" || response.UserCode == "" ||
		response.VerificationURI == "" {
		return DeviceCode{}, errors.New("GitHub device authorization was refused")
	}
	if response.Interval < 5 {
		response.Interval = 5
	}
	if response.ExpiresIn <= 0 {
		response.ExpiresIn = 900
	}
	return DeviceCode{
		DeviceCode: response.DeviceCode, UserCode: response.UserCode,
		VerificationURI: response.VerificationURI, ExpiresIn: response.ExpiresIn,
		Interval: response.Interval,
	}, nil
}

func (c *Client) PollDeviceToken(ctx context.Context, clientID, deviceCode string) (DevicePoll, error) {
	var response struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
	}
	err := c.postForm(ctx, c.TokenURL, url.Values{
		"client_id": {clientID}, "device_code": {deviceCode},
		"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
	}, &response)
	if err != nil {
		return DevicePoll{}, err
	}
	switch response.Error {
	case "":
		if response.AccessToken == "" {
			return DevicePoll{}, errors.New("GitHub device authorization returned no token")
		}
		return DevicePoll{Token: response.AccessToken, Scope: response.Scope}, nil
	case "authorization_pending":
		return DevicePoll{Pending: true}, nil
	case "slow_down":
		return DevicePoll{Pending: true, SlowDown: true}, nil
	case "access_denied":
		return DevicePoll{Denied: true}, nil
	case "expired_token":
		return DevicePoll{Expired: true}, nil
	default:
		return DevicePoll{}, errors.New("GitHub device authorization failed")
	}
}

func (c *Client) CurrentUser(ctx context.Context, token string) (Account, error) {
	var response struct {
		Login string `json:"login"`
	}
	header, err := c.getJSON(ctx, c.APIBase+"/user", token, &response)
	if err != nil {
		return Account{}, err
	}
	if response.Login == "" {
		return Account{}, errors.New("GitHub user response had no login")
	}
	return Account{Login: response.Login, Scopes: header.Get("X-OAuth-Scopes")}, nil
}

func (c *Client) ListRepos(ctx context.Context, token, query string) ([]Repo, error) {
	query = strings.TrimSpace(query)
	var listed []repoItem
	if err := c.getDecode(ctx, c.APIBase+"/user/repos?per_page=100&sort=updated&affiliation=owner,collaborator,organization_member", token, &listed); err != nil {
		return nil, err
	}
	items := make([]Repo, 0, 20)
	seen := map[string]bool{}
	add := func(item repoItem) {
		repo, ok := item.repo()
		if !ok || seen[repo.FullName] {
			return
		}
		seen[repo.FullName] = true
		items = append(items, repo)
	}
	if owner, name, ok := splitRepoQuery(query); ok {
		var one repoItem
		err := c.getDecode(ctx, c.APIBase+"/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), token, &one)
		if err == nil {
			add(one)
		} else if !isMissingRepo(err) {
			return nil, err
		}
	}
	folded := strings.ToLower(query)
	for _, item := range listed {
		repo, ok := item.repo()
		if !ok {
			continue
		}
		if folded != "" && !strings.Contains(strings.ToLower(repo.FullName), folded) &&
			!strings.Contains(strings.ToLower(repo.Description), folded) {
			continue
		}
		add(item)
		if len(items) >= 20 {
			break
		}
	}
	return items, nil
}

type repoItem struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

func (item repoItem) repo() (Repo, bool) {
	owner, name, ok := splitRepoQuery(item.FullName)
	if !ok || item.HTMLURL == "" {
		return Repo{}, false
	}
	parsed, err := url.Parse(item.HTMLURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Repo{}, false
	}
	return Repo{
		FullName: owner + "/" + name, Private: item.Private, Description: item.Description,
		DefaultBranch: item.DefaultBranch, URL: "https://github.com/" + owner + "/" + name,
	}, true
}

func splitRepoQuery(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "https://github.com/")
	value = strings.TrimPrefix(value, "http://github.com/")
	value = strings.TrimSuffix(value, ".git")
	value = strings.Trim(value, "/")
	owner, name, ok := strings.Cut(value, "/")
	if !ok || strings.Contains(name, "/") || !validSlug(owner) || !validSlug(name) {
		return "", "", false
	}
	return owner, name, true
}

func validSlug(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func isMissingRepo(err error) bool {
	var status *StatusError
	return errors.As(err, &status) && (status.Status == http.StatusNotFound || status.Status == http.StatusForbidden)
}

func (c *Client) getDecode(ctx context.Context, endpoint, token string, output any) error {
	_, err := c.getJSON(ctx, endpoint, token, output)
	return err
}

func (c *Client) getJSON(ctx context.Context, endpoint, token string, output any) (http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	return c.do(request, token, output)
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values, output any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = c.do(request, "", output)
	return err
}

func (c *Client) do(request *http.Request, token string, output any) (http.Header, error) {
	if err := allowedEndpoint(request.URL, c); err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.HTTP.Do(request)
	request.Header.Del("Authorization")
	if err != nil {
		return nil, errors.New("GitHub request failed")
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, errors.New("GitHub request failed")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, classify(response, payload)
	}
	if output == nil {
		return response.Header, nil
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return nil, errors.New("invalid GitHub response")
	}
	return response.Header, nil
}

func classify(response *http.Response, payload []byte) error {
	status := &StatusError{Status: response.StatusCode}
	folded := bytes.ToLower(payload)
	switch response.StatusCode {
	case http.StatusUnauthorized:
		status.Reason = "invalid"
		if bytes.Contains(folded, []byte("expired")) {
			status.Reason = "expired"
		}
	case http.StatusForbidden:
		if response.Header.Get("X-GitHub-SSO") != "" || bytes.Contains(folded, []byte("saml")) {
			status.Reason = "missing_access"
		}
	}
	return status
}

func allowedEndpoint(parsed *url.URL, client *Client) error {
	if parsed == nil || parsed.User != nil || parsed.Host == "" {
		return errors.New("invalid GitHub URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("invalid GitHub URL")
	}
	hosts := map[string]string{}
	for _, raw := range []string{client.DeviceURL, client.TokenURL, client.APIBase} {
		endpoint, err := url.Parse(raw)
		if err != nil || endpoint.Host == "" {
			return errors.New("invalid GitHub URL")
		}
		hosts[endpoint.Host] = endpoint.Scheme
	}
	scheme, ok := hosts[parsed.Host]
	if !ok || scheme != parsed.Scheme {
		return errors.New("invalid GitHub URL")
	}
	if strings.Contains(strings.ToLower(parsed.RawQuery), "token=") {
		return errors.New("invalid GitHub URL")
	}
	return nil
}
