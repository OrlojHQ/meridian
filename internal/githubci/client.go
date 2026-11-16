// Package githubci reads check runs and commit statuses for one commit.
// The poller calls Reader. A later webhook or GitHub App can submit the same
// Snapshot without this package. Tokens are request headers only: they are
// removed before the body is read and they are never put in an error or a URL.
package githubci

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
	"unicode"
	"unicode/utf8"
)

const defaultAPI = "https://api.github.com"

// StatusError is a GitHub HTTP status. Reason is set only when the login
// itself is unusable. A 403 or 404 for one repository leaves Reason empty so
// the login is not retired.
type StatusError struct {
	Status int
	Reason string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("GitHub API request failed with status %d", e.Status)
}

func (e *StatusError) GitHubAuthStatus() (int, string) { return e.Status, e.Reason }

// Check is one check run or commit status. Detail is a short tail, already
// bounded. It is not a log archive.
type Check struct {
	Name       string
	Source     string
	Conclusion string
	URL        string
	Detail     string
}

// Snapshot is one reading of a commit. ApplyRemoteCI stores it. Nothing here
// is a credential.
type Snapshot struct {
	Checks []Check
}

// Reader reads CI for one commit. Implementations must not log the token.
type Reader interface {
	ReadCommit(ctx context.Context, token, owner, repo, sha string) (Snapshot, error)
}

// Editor replaces the server-owned checks section of a pull request body.
// A reader may also implement it. The section contains no log output.
type Editor interface {
	WriteChecksSection(ctx context.Context, token, owner, repo string, number int64, section string) error
}

// Client talks to the GitHub API. Tests point APIBase at a stand-in.
type Client struct {
	HTTP    *http.Client
	APIBase string
}

func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	copy := *httpClient
	copy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{HTTP: &copy, APIBase: defaultAPI}
}

func (c *Client) ReadCommit(ctx context.Context, token, owner, repo, sha string) (Snapshot, error) {
	if !slug(owner) || !slug(repo) || !objectID(sha) {
		return Snapshot{}, errors.New("invalid GitHub CI request")
	}
	var runs struct {
		CheckRuns []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
			Output     struct {
				Title   string `json:"title"`
				Summary string `json:"summary"`
			} `json:"output"`
		} `json:"check_runs"`
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) +
		"/commits/" + url.PathEscape(sha) + "/check-runs?per_page=100"
	if err := c.get(ctx, path, token, &runs); err != nil {
		return Snapshot{}, err
	}
	var combined struct {
		Statuses []struct {
			Context     string `json:"context"`
			State       string `json:"state"`
			Description string `json:"description"`
			TargetURL   string `json:"target_url"`
		} `json:"statuses"`
	}
	statusPath := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) +
		"/commits/" + url.PathEscape(sha) + "/status"
	if err := c.get(ctx, statusPath, token, &combined); err != nil {
		var status *StatusError
		if !errors.As(err, &status) || status.Status != http.StatusNotFound {
			return Snapshot{}, err
		}
	}
	snapshot := Snapshot{}
	for _, run := range runs.CheckRuns {
		if len(snapshot.Checks) >= 100 {
			break
		}
		conclusion := classifyRun(run.Status, run.Conclusion)
		detail := strings.TrimSpace(run.Output.Summary)
		if detail == "" {
			detail = strings.TrimSpace(run.Output.Title)
		}
		if conclusion == "failure" || conclusion == "timed_out" || conclusion == "cancelled" || conclusion == "action_required" {
			if notes := c.annotations(ctx, token, owner, repo, run.ID); notes != "" {
				detail = notes
			}
		}
		snapshot.Checks = append(snapshot.Checks, Check{
			Name: boundName(run.Name), Source: "check_run", Conclusion: conclusion,
			URL: safeURL(run.HTMLURL), Detail: tail(detail),
		})
	}
	for _, item := range combined.Statuses {
		if len(snapshot.Checks) >= 100 {
			break
		}
		snapshot.Checks = append(snapshot.Checks, Check{
			Name: boundName(item.Context), Source: "status", Conclusion: classifyStatus(item.State),
			URL: safeURL(item.TargetURL), Detail: tail(item.Description),
		})
	}
	return snapshot, nil
}

func (c *Client) WriteChecksSection(ctx context.Context, token, owner, repo string, number int64, section string) error {
	if !slug(owner) || !slug(repo) || number <= 0 || section == "" {
		return errors.New("invalid GitHub CI request")
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	var current struct {
		Body string `json:"body"`
	}
	if err := c.get(ctx, path, token, &current); err != nil {
		return err
	}
	next := replaceSection(current.Body, section)
	if next == current.Body {
		return nil
	}
	if len(next) > 64<<10 {
		return errors.New("pull request body is too long")
	}
	return c.patch(ctx, path, token, map[string]string{"body": next})
}

func (c *Client) annotations(ctx context.Context, token, owner, repo string, id int64) string {
	if id <= 0 {
		return ""
	}
	path := fmt.Sprintf("/repos/%s/%s/check-runs/%d/annotations?per_page=20",
		url.PathEscape(owner), url.PathEscape(repo), id)
	var notes []struct {
		Message string `json:"message"`
	}
	if err := c.get(ctx, path, token, &notes); err != nil {
		return ""
	}
	lines := make([]string, 0, len(notes))
	for _, note := range notes {
		message := strings.TrimSpace(note.Message)
		if message != "" {
			lines = append(lines, message)
		}
	}
	return strings.Join(lines, "\n")
}

func (c *Client) get(ctx context.Context, path, token string, output any) error {
	endpoint, err := c.endpoint(path)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("GitHub request failed")
	}
	return c.do(request, token, output)
}

func (c *Client) patch(ctx context.Context, path, token string, body any) error {
	endpoint, err := c.endpoint(path)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return errors.New("GitHub request failed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return errors.New("GitHub request failed")
	}
	request.Header.Set("Content-Type", "application/json")
	return c.do(request, token, nil)
}

func (c *Client) do(request *http.Request, token string, output any) error {
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.HTTP.Do(request)
	request.Header.Del("Authorization")
	if err != nil {
		return errors.New("GitHub request failed")
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return errors.New("GitHub request failed")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return classify(response.StatusCode, payload)
	}
	if output == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, output); err != nil {
		return errors.New("invalid GitHub response")
	}
	return nil
}

func (c *Client) endpoint(path string) (string, error) {
	base := c.APIBase
	if base == "" {
		base = defaultAPI
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("invalid GitHub URL")
	}
	if strings.Contains(path, "://") || strings.Contains(path, "..") {
		return "", errors.New("invalid GitHub URL")
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", errors.New("invalid GitHub URL")
	}
	return parsed.ResolveReference(ref).String(), nil
}

func classify(status int, payload []byte) error {
	parsed := &StatusError{Status: status}
	if status == http.StatusUnauthorized {
		parsed.Reason = "invalid"
		if bytes.Contains(bytes.ToLower(payload), []byte("expired")) {
			parsed.Reason = "expired"
		}
	}
	return parsed
}

func classifyRun(status, conclusion string) string {
	status = strings.ToLower(status)
	conclusion = strings.ToLower(conclusion)
	if status != "completed" {
		return "pending"
	}
	switch conclusion {
	case "success", "skipped", "neutral", "timed_out", "cancelled", "action_required":
		return conclusion
	default:
		return "failure"
	}
}

func classifyStatus(state string) string {
	switch strings.ToLower(state) {
	case "success":
		return "success"
	case "pending":
		return "pending"
	case "error":
		return "failure"
	case "failure":
		return "failure"
	default:
		return "pending"
	}
}

func slug(value string) bool {
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

func objectID(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if !unicode.Is(unicode.ASCII_Hex_Digit, character) {
			return false
		}
	}
	return true
}

func boundName(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\n", " "))
	if name == "" {
		return "ci"
	}
	if len(name) > 128 {
		name = name[:128]
	}
	return name
}

func safeURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Host == "" || len(value) > 2048 {
		return ""
	}
	return parsed.String()
}

func tail(value string) string {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "")
	}
	const limit = 4 << 10
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

const (
	markerStart = "<!-- meridian-checks -->"
	markerEnd   = "<!-- /meridian-checks -->"
)

func replaceSection(body, section string) string {
	if start := strings.Index(body, markerStart); start >= 0 {
		end := strings.Index(body[start:], markerEnd)
		if end >= 0 {
			end += start + len(markerEnd)
			body = strings.TrimSpace(body[:start] + body[end:])
		}
	}
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return section
	}
	return body + "\n\n" + section
}
