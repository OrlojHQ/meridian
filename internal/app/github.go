package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

type PullRequest struct {
	Number  int64
	URL     string
	HeadSHA string
}

type CreatePullRequestInput struct {
	Owner, Repository string
	Head, Base        string
	Title, Body       string
}

// GitHubClient is intentionally narrow and injectable. Implementations must
// never put the token in a URL or persist response bodies.
type GitHubClient interface {
	FindOpenPullRequest(context.Context, string, string, string, string, string) (*PullRequest, error)
	CreatePullRequest(context.Context, string, CreatePullRequestInput) (PullRequest, error)
}

type githubHTTPClient struct {
	http *http.Client
}

func NewGitHubHTTPClient(client *http.Client) GitHubClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &githubHTTPClient{http: &copy}
}

func (c *githubHTTPClient) FindOpenPullRequest(
	ctx context.Context,
	token, owner, repository, head, base string,
) (*PullRequest, error) {
	query := url.Values{
		"state": {"open"}, "head": {owner + ":" + head}, "base": {base},
	}
	endpoint := "https://api.github.com/repos/" + url.PathEscape(owner) + "/" +
		url.PathEscape(repository) + "/pulls?" + query.Encode()
	var response []struct {
		Number  int64  `json:"number"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := c.call(ctx, http.MethodGet, endpoint, token, nil, &response); err != nil {
		return nil, err
	}
	if len(response) == 0 {
		return nil, nil
	}
	if len(response) != 1 || response[0].Head.Ref != head || response[0].Base.Ref != base {
		return nil, fmt.Errorf("%w: divergent GitHub pull request state", domain.ErrConflict)
	}
	item := response[0]
	if err := validatePullRequestResult(item.Number, item.HTMLURL, owner, repository); err != nil {
		return nil, err
	}
	return &PullRequest{Number: item.Number, URL: item.HTMLURL, HeadSHA: item.Head.SHA}, nil
}

func (c *githubHTTPClient) CreatePullRequest(
	ctx context.Context,
	token string,
	input CreatePullRequestInput,
) (PullRequest, error) {
	endpoint := "https://api.github.com/repos/" + url.PathEscape(input.Owner) + "/" +
		url.PathEscape(input.Repository) + "/pulls"
	request := map[string]string{
		"head": input.Head, "base": input.Base, "title": input.Title, "body": input.Body,
	}
	var response struct {
		Number  int64  `json:"number"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := c.call(ctx, http.MethodPost, endpoint, token, request, &response); err != nil {
		return PullRequest{}, err
	}
	if err := validatePullRequestResult(
		response.Number, response.HTMLURL, input.Owner, input.Repository,
	); err != nil {
		return PullRequest{}, err
	}
	return PullRequest{Number: response.Number, URL: response.HTMLURL, HeadSHA: response.Head.SHA}, nil
}

func (c *githubHTTPClient) call(
	ctx context.Context, method, endpoint, token string, input, output any,
) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "api.github.com" ||
		parsed.User != nil || parsed.Fragment != "" {
		return errors.New("invalid GitHub API URL")
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	request.Header.Del("Authorization")
	if err != nil {
		return errors.New("GitHub API request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("GitHub API request failed with status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(output); err != nil {
		return errors.New("invalid GitHub API response")
	}
	return nil
}

func validatePullRequestResult(number int64, value, owner, repository string) error {
	parsed, err := url.Parse(value)
	expectedPath := fmt.Sprintf("/%s/%s/pull/%d", owner, repository, number)
	if number <= 0 || err != nil || parsed.Scheme != "https" ||
		!strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" ||
		len(value) > 4096 || !strings.EqualFold(parsed.Path, expectedPath) {
		return errors.New("invalid GitHub pull request response")
	}
	return nil
}

func parseGitHubRepository(value string) (string, string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", "", err
	}
	if parsed.Scheme != "https" && parsed.Scheme != "ssh" {
		return "", "", errors.New("repository is not a supported GitHub URL")
	}
	if !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil && parsed.Scheme == "https" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", errors.New("repository is not a credential-free GitHub URL")
	}
	trimmed := strings.TrimSuffix(strings.Trim(path.Clean(parsed.Path), "/"), ".git")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || !validGitHubSlug(parts[0]) || !validGitHubSlug(parts[1]) {
		return "", "", errors.New("GitHub repository path is invalid")
	}
	return parts[0], parts[1], nil
}

func validGitHubSlug(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}
