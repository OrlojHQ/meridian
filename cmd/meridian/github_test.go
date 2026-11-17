package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestGitHubConnectReportsEachCLIState(t *testing.T) {
	const secret = "ghp_cli_fallback_token"
	dir := t.TempDir()
	bin := filepath.Join(dir, "gh")
	writeGH(t, bin, "#!/bin/sh\ncase \"$1 $2\" in\n  'auth status')\n    printf '%s\\n' 'Logged in to github.com account octocat (keyring)'\n    exit 0\n    ;;\n  'auth token')\n    printf '%s\\n' '"+secret+"'\n    exit 0\n    ;;\nesac\nexit 1\n")

	var posted string
	server := githubConnectServer(t, false, map[string]any{
		"available": false,
		"state":     "not_found",
		"detail":    "The GitHub CLI was not found. Searched PATH.",
	}, func(body string) { posted = body })
	defer server.Close()

	output, progress, err := runGitHubConnect(t, server.URL, "connect", "--yes", "--gh-path", bin)
	if err != nil {
		t.Fatalf("connect = %v\n%s\n%s", err, output, progress)
	}
	if !strings.Contains(posted, secret) {
		t.Fatalf("stored body = %s", posted)
	}
	if strings.Contains(output+progress, secret) {
		t.Fatal("printed the token")
	}
	if !strings.Contains(output, "@octocat") {
		t.Fatalf("output = %s", output)
	}
}

func TestGitHubConnectExplainsNotSignedInNotFoundAndUnreadable(t *testing.T) {
	const secret = "ghp_unreadable_token_value"
	dir := t.TempDir()
	bin := filepath.Join(dir, "gh")

	t.Run("not signed in", func(t *testing.T) {
		writeGH(t, bin, "#!/bin/sh\nprintf '%s\\n' 'You are not logged into any GitHub hosts. To log in, run: gh auth login'\nexit 1\n")
		server := githubConnectServer(t, false, map[string]any{"available": false, "state": "unavailable"}, nil)
		defer server.Close()
		_, progress, err := runGitHubConnect(t, server.URL, "connect", "--gh-path", bin)
		if err == nil || !strings.Contains(err.Error(), "no GitHub OAuth client id") {
			t.Fatalf("error = %v", err)
		}
		if !strings.Contains(progress, "not signed in") || !strings.Contains(progress, bin) || strings.Contains(progress, secret) {
			t.Fatalf("progress = %s", progress)
		}
	})

	t.Run("not found", func(t *testing.T) {
		missing := filepath.Join(dir, "missing")
		server := githubConnectServer(t, false, map[string]any{"available": false, "state": "not_found", "detail": "station missed gh"}, nil)
		defer server.Close()
		_, progress, err := runGitHubConnect(t, server.URL, "connect", "--gh-path", missing)
		if err == nil || !strings.Contains(err.Error(), "no GitHub OAuth client id") {
			t.Fatalf("error = %v", err)
		}
		if !strings.Contains(progress, missing) || !strings.Contains(progress, "not an executable") {
			t.Fatalf("progress = %s", progress)
		}
	})

	t.Run("token unreadable", func(t *testing.T) {
		writeGH(t, bin, "#!/bin/sh\nprintf '%s\\n' '  X Failed to log in to github.com account octocat (keyring)'\nprintf '%s\\n' '  - The token in keyring is invalid.'\nprintf '%s\\n' '"+secret+"' >&2\nexit 1\n")
		server := githubConnectServer(t, false, map[string]any{"available": false, "state": "unavailable"}, nil)
		defer server.Close()
		_, progress, err := runGitHubConnect(t, server.URL, "connect", "--gh-path", bin)
		if err == nil || !strings.Contains(err.Error(), "no GitHub OAuth client id") {
			t.Fatalf("error = %v", err)
		}
		if !strings.Contains(progress, "could not be read") || !strings.Contains(progress, bin) || strings.Contains(progress, secret) {
			t.Fatalf("progress = %s", progress)
		}
	})
}

func githubConnectServer(t *testing.T, deviceFlow bool, host map[string]any, onToken func(string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("missing installation bearer")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/github/connection":
			_ = json.NewEncoder(w).Encode(map[string]any{"connected": false, "deviceFlow": deviceFlow})
		case "/github/connection/host":
			if r.Method == http.MethodPost {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"connected": true, "deviceFlow": deviceFlow, "login": "octocat", "status": "active", "resourceVersion": 1,
				})
				return
			}
			_ = json.NewEncoder(w).Encode(host)
		case "/github/connection/token":
			body, _ := io.ReadAll(r.Body)
			if onToken != nil {
				onToken(string(body))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"connected": true, "deviceFlow": deviceFlow, "login": "octocat", "status": "active", "resourceVersion": 1,
			})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func runGitHubConnect(t *testing.T, server string, args ...string) (string, string, error) {
	t.Helper()
	var output, progress bytes.Buffer
	config := &cliConfig{server: server, tokenFile: testAPITokenFile(t), stdout: &output, stdin: strings.NewReader("")}
	root := &cobra.Command{Use: "meridian", SilenceUsage: true, SilenceErrors: true}
	root.SetErr(&progress)
	root.SetOut(&output)
	root.AddCommand(newGitHubCommand(config))
	root.SetArgs(append([]string{"github"}, args...))
	err := root.Execute()
	return output.String(), progress.String(), err
}

func writeGH(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
