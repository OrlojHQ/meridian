package capsuleproto

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/antigravityauth"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

const signInLink = "https://accounts.google.com/o/oauth2/auth?client_id=" + antigravityauth.ClientID +
	"&redirect_uri=https%3A%2F%2Fantigravity.google%2Foauth-callback&response_type=code&state=s"

// fakeAgyLogin mimics agy acp --login: it prints a link, reads one line, and
// writes the profile when the line is "4/0good". It records its HOME and
// environment so the test can check what the login saw.
func fakeAgyLogin(t *testing.T, link string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "agy")
	body := `#!/bin/sh
printf '%s\n' "$HOME" > "$RECORD/home"
env > "$RECORD/env"
printf 'Open this link in a browser to log in with Google:\n\n  %s\n\nThen paste the authorization code:\n' '` + link + `'
read code
if [ "$code" != "4/0good" ]; then echo "login failed"; exit 1; fi
mkdir -p "$HOME/.gemini/antigravity-cli/cache"
printf '%s' '{"token":{"access_token":"a","token_type":"Bearer","refresh_token":"r"},"auth_method":"consumer","id_token_email":"dev@example.com"}' > "$HOME/.gemini/antigravity-cli/antigravity-oauth-token"
printf '%s' '{"methodId":"oauth-personal"}' > "$HOME/.gemini/antigravity-cli/cache/acp-auth-state.json"
echo "Logged in as dev@example.com."
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := signInCommands["agy"]
	signInCommands["agy"] = []string{script}
	t.Cleanup(func() { signInCommands["agy"] = previous })
	return script
}

func newSignInServer(t *testing.T) (*Client, string, string) {
	t.Helper()
	home, record := t.TempDir(), t.TempDir()
	t.Setenv("RECORD", record)
	t.Setenv("MERIDIAN_CAPSULE_TOKEN", "capsule-token-value")
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: t.TempDir(), HarnessHome: home})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	client, err := NewClient(httpServer.URL, testToken, httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, home, record
}

func protocolCode(err error) string {
	var protocolErr *Error
	if errors.As(err, &protocolErr) {
		return protocolErr.Code
	}
	return ""
}

func TestHarnessSignInRelaysLinkAndReturnsProfile(t *testing.T) {
	fakeAgyLogin(t, signInLink)
	client, home, record := newSignInServer(t)
	ctx := context.Background()
	start, err := client.StartHarnessSignIn(ctx, "agy")
	if err != nil {
		t.Fatal(err)
	}
	if start.AuthorizationURL != signInLink {
		t.Fatalf("link = %q", start.AuthorizationURL)
	}
	if _, err := client.StartHarnessSignIn(ctx, "agy"); protocolCode(err) != "sign_in_in_progress" {
		t.Fatalf("second start error = %v", err)
	}
	result, err := client.FinishHarnessSignIn(ctx, " 4/0good ")
	if err != nil {
		t.Fatal(err)
	}
	if account, err := antigravityauth.Check(result.Files); err != nil || account != "dev@example.com" {
		t.Fatalf("profile = %q, %v", account, err)
	}
	seen, _ := os.ReadFile(filepath.Join(record, "home"))
	if strings.TrimSpace(string(seen)) != filepath.Join(home, signInHomeName) {
		t.Fatalf("login HOME = %q", seen)
	}
	env, _ := os.ReadFile(filepath.Join(record, "env"))
	if strings.Contains(string(env), "capsule-token-value") {
		t.Fatal("login saw the Capsule token")
	}
	if _, err := os.Stat(filepath.Join(home, signInHomeName)); !os.IsNotExist(err) {
		t.Fatalf("sign-in home kept after the attempt: %v", err)
	}
	if _, err := client.FinishHarnessSignIn(ctx, "4/0good"); protocolCode(err) != "sign_in_not_found" {
		t.Fatalf("second code error = %v", err)
	}
}

func TestHarnessSignInRejectedCodeEndsAttempt(t *testing.T) {
	fakeAgyLogin(t, signInLink)
	client, home, _ := newSignInServer(t)
	ctx := context.Background()
	if _, err := client.StartHarnessSignIn(ctx, "agy"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FinishHarnessSignIn(ctx, "4/0nope"); protocolCode(err) != "sign_in_rejected" {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, signInHomeName)); !os.IsNotExist(err) {
		t.Fatalf("sign-in home kept: %v", err)
	}
	// A new attempt can start once the failed one ended.
	if _, err := client.StartHarnessSignIn(ctx, "agy"); err != nil {
		t.Fatal(err)
	}
	if err := client.CancelHarnessSignIn(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHarnessSignInRefusesOtherLinksAndInput(t *testing.T) {
	fakeAgyLogin(t, "https://evil.test/o/oauth2/auth?client_id="+antigravityauth.ClientID)
	client, home, _ := newSignInServer(t)
	ctx := context.Background()
	if _, err := client.StartHarnessSignIn(ctx, "agy"); protocolCode(err) != "sign_in_failed" {
		t.Fatalf("foreign link error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, signInHomeName)); !os.IsNotExist(err) {
		t.Fatalf("sign-in home kept: %v", err)
	}
	if _, err := client.StartHarnessSignIn(ctx, "claude"); protocolCode(err) != "invalid_request" {
		t.Fatalf("unknown harness error = %v", err)
	}
	if _, err := client.FinishHarnessSignIn(ctx, "4/0 two words"); protocolCode(err) != "invalid_request" {
		t.Fatalf("multi-word code error = %v", err)
	}
	var protocolErr *Error
	if _, err := client.FinishHarnessSignIn(ctx, "4/0good"); !errors.As(err, &protocolErr) || protocolErr.Status != http.StatusNotFound {
		t.Fatalf("code without attempt error = %v", err)
	}
}

func TestHarnessSignInCancelStopsLogin(t *testing.T) {
	previousWait := signInURLWait
	signInURLWait = 5 * time.Second
	t.Cleanup(func() { signInURLWait = previousWait })
	fakeAgyLogin(t, signInLink)
	client, home, _ := newSignInServer(t)
	ctx := context.Background()
	if _, err := client.StartHarnessSignIn(ctx, "agy"); err != nil {
		t.Fatal(err)
	}
	if err := client.CancelHarnessSignIn(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, signInHomeName)); !os.IsNotExist(err) {
		t.Fatalf("sign-in home kept: %v", err)
	}
	if _, err := client.FinishHarnessSignIn(ctx, "4/0good"); protocolCode(err) != "sign_in_not_found" {
		t.Fatalf("code after cancel error = %v", err)
	}
}

func TestAntigravityProfileReachesAgyHome(t *testing.T) {
	home := t.TempDir()
	s := &Server{config: ServerConfig{HarnessHome: home}}
	profile := []antigravityauth.File{
		{Path: antigravityauth.TokenFile, Content: []byte(`{"token":{"access_token":"a","token_type":"Bearer","refresh_token":"r"},"auth_method":"consumer","id_token_email":"dev@example.com"}`)},
		{Path: antigravityauth.StateFile, Content: []byte(`{"methodId":"oauth-personal"}`)},
	}
	env, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: "antigravity", Profile: profile}, nil, "agy-structured", nil)
	if err != nil {
		t.Fatal(err)
	}
	setupHome := filepath.Join(home, ".meridian-setup-connection-agy")
	if env["HOME"] != setupHome {
		t.Fatalf("HOME = %q", env["HOME"])
	}
	for _, file := range profile {
		p := filepath.Join(setupHome, ".gemini", "antigravity-cli", filepath.FromSlash(file.Path))
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", file.Path, info, err)
		}
		if raw, _ := os.ReadFile(p); string(raw) != string(file.Content) {
			t.Fatalf("%s content differs", file.Path)
		}
	}
	// A new sign-in replaces the profile on the next start.
	profile[0].Content = []byte(strings.Replace(string(profile[0].Content), `"r"`, `"r2"`, 1))
	if _, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: "antigravity", Profile: profile}, nil, "agy", nil); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(setupHome, ".gemini/antigravity-cli", antigravityauth.TokenFile)); !strings.Contains(string(raw), `"r2"`) {
		t.Fatal("profile was not replaced")
	}
	bad := []harnesssetup.Connection{
		{Provider: "antigravity", Profile: profile[:1]},
		{Provider: "antigravity", Profile: profile, URL: "https://meridian.example/provider-gateway/antigravity"},
		{Provider: "antigravity", Profile: profile, Token: strings.Repeat("a", 64)},
	}
	for _, connection := range bad {
		if _, err := s.prepareProviderConnection(context.Background(), &connection, nil, "agy", nil); err == nil {
			t.Errorf("accepted %+v", connection.URL+connection.Token)
		}
	}
	if _, err := s.prepareProviderConnection(context.Background(), &harnesssetup.Connection{Provider: "antigravity", Profile: profile}, nil, "codex", nil); err == nil {
		t.Fatal("codex accepted an antigravity connection")
	}
}
