package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
)

type githubRoundTrip func(*http.Request) (*http.Response, error)

func (f githubRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestGitHubClientUsesHeaderAndExactHeadBaseLookup(t *testing.T) {
	const token = "github-secret-token"
	httpClient := &http.Client{Transport: githubRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" ||
			strings.Contains(request.URL.String(), token) ||
			request.Header.Get("Authorization") != "Bearer "+token ||
			request.URL.Query().Get("head") != "owner:feature/reviewed" ||
			request.URL.Query().Get("base") != "main" {
			t.Fatalf("unsafe GitHub request: %s headers=%v", request.URL, request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(
				`[{"number":7,"html_url":"https://github.com/owner/repository/pull/7",` +
					`"head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ref":"feature/reviewed"},` +
					`"base":{"ref":"main"}}]`,
			)),
			Header: make(http.Header),
		}, nil
	})}
	result, err := NewGitHubHTTPClient(httpClient).FindOpenPullRequest(
		context.Background(), token, "owner", "repository", "feature/reviewed", "main",
	)
	if err != nil || result == nil || result.Number != 7 {
		t.Fatalf("pull request = %#v, %v", result, err)
	}
}

func TestGitHubClientRejectsDivergentAndUnsafeResponses(t *testing.T) {
	tests := map[string]string{
		"multiple": `[{"number":1,"html_url":"https://github.com/o/r/pull/1","head":{"ref":"f"},"base":{"ref":"main"}},` +
			`{"number":2,"html_url":"https://github.com/o/r/pull/2","head":{"ref":"f"},"base":{"ref":"main"}}]`,
		"wrong ref": `[{"number":1,"html_url":"https://github.com/o/r/pull/1","head":{"ref":"other"},"base":{"ref":"main"}}]`,
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			httpClient := &http.Client{Transport: githubRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response)),
					Header: make(http.Header),
				}, nil
			})}
			_, err := NewGitHubHTTPClient(httpClient).FindOpenPullRequest(
				context.Background(), "token", "o", "r", "f", "main",
			)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if err := validatePullRequestResult(1, "http://github.com/o/r/pull/1", "o", "r"); err == nil {
		t.Fatal("non-TLS pull request URL accepted")
	}
	if _, _, err := parseGitHubRepository("https://user:secret@github.com/o/r"); err == nil {
		t.Fatal("credentialed repository URL accepted")
	}
}
