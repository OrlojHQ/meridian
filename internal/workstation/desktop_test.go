package workstation_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/workstation"
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) }

type sequentialIDs struct{ next int }

func (ids *sequentialIDs) NewID() string { ids.next++; return fmt.Sprintf("id%06d", ids.next) }

type noQueue struct{}

func (noQueue) Enqueue(context.Context, domain.CapsuleID) error { return nil }

// desktopGateway is a real API server and the browser session cookie the
// desktop window would hold for it.
func desktopGateway(t *testing.T) (*app.Service, string, string) {
	t.Helper()
	ctx := principal.With(context.Background(), principal.Owner())
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service := app.NewService(store, fixedClock{}, &sequentialIDs{}, noQueue{})
	service.ConfigureSecrets(key)
	const bearer = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	token, err := apiauth.ParseToken(bearer)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(service, token)
	identity, err := app.NewIdentity(ctx, store, fixedClock{}, &sequentialIDs{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	api.SetIdentity(identity)
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/auth/browser-session", nil)
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Origin", server.URL)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("browser session cookies = %v (%d)", cookies, response.StatusCode)
	}
	return service, server.URL, cookies[0].Name + "=" + cookies[0].Value
}

func laptop(t *testing.T) func() (workstation.Scan, error) {
	t.Helper()
	home := t.TempDir()
	for path, content := range map[string]string{
		".codex/AGENTS.md":  "review carefully",
		".codex/auth.json":  `{"tokens":"auth-canary"}`,
		".gitconfig":        "[user]\n\tname = Ada Lovelace\n\temail = ada@example.com\n",
		".ssh/id_ed25519":   "PRIVATE KEY canary",
		".claude/CLAUDE.md": "be brief",
	} {
		full := filepath.Join(home, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return func() (workstation.Scan, error) {
		return workstation.ScanComputer(home, func(string) string { return "" })
	}
}

func run(t *testing.T, lines []string, scan func() (workstation.Scan, error)) []workstation.DesktopMessage {
	t.Helper()
	var out strings.Builder
	err := workstation.RunDesktopImport(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, scan,
		func(hello workstation.DesktopHello) (workstation.DesktopAPI, error) {
			return workstation.NewDesktopClient(hello)
		})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "canary") {
		t.Fatalf("output carried a secret:\n%s", out.String())
	}
	var messages []workstation.DesktopMessage
	scanner := bufio.NewScanner(strings.NewReader(out.String()))
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		var message workstation.DesktopMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			t.Fatalf("not one JSON message per line: %q", scanner.Text())
		}
		messages = append(messages, message)
	}
	return messages
}

func hello(t *testing.T, origin, cookie string) string {
	t.Helper()
	raw, err := json.Marshal(workstation.DesktopHello{Origin: origin, Cookie: cookie})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDesktopImportReviewsThenSavesAsTheWindowsSession(t *testing.T) {
	service, origin, cookie := desktopGateway(t)
	messages := run(t, []string{hello(t, origin, cookie), `{"action":"save"}`}, laptop(t))
	if len(messages) != 2 || messages[0].Type != "review" || messages[1].Type != "saved" {
		t.Fatalf("messages = %+v", messages)
	}
	review := messages[0]
	if review.Origin != origin || review.Account == nil || review.Account.Mode != "single_user" ||
		review.Exclusions != 1 || !review.Changes || review.Digest == "" || len(review.Review.Harnesses) != 2 {
		t.Fatalf("review = %+v", review)
	}
	if strings.Join(messages[1].Saved, ",") != "claude,codex" || messages[1].Digest != review.Digest {
		t.Fatalf("saved = %+v", messages[1])
	}
	profile, err := service.GetProfile(principal.With(context.Background(), principal.Owner()))
	if err != nil || len(profile.Setups) != 2 || profile.GitName != "Ada Lovelace" {
		t.Fatalf("saved profile = %+v, %v", profile, err)
	}

	// The same computer again: a review with nothing to change.
	again := run(t, []string{hello(t, origin, cookie), `{"action":"save"}`}, laptop(t))
	if again[0].Type != "review" || again[0].Changes || again[1].Type != "saved" {
		t.Fatalf("second run = %+v", again)
	}
}

func TestDesktopImportSavesNothingWithoutSave(t *testing.T) {
	service, origin, cookie := desktopGateway(t)
	for name, lines := range map[string][]string{
		"cancel":        {hello(t, origin, cookie), `{"action":"cancel"}`},
		"window closed": {hello(t, origin, cookie)},
		"other text":    {hello(t, origin, cookie), `{"action":"upload"}`, `not json`},
	} {
		messages := run(t, lines, laptop(t))
		if messages[0].Type != "review" || messages[len(messages)-1].Type != "cancelled" {
			t.Fatalf("%s: messages = %+v", name, messages)
		}
	}
	profile, err := service.GetProfile(principal.With(context.Background(), principal.Owner()))
	if err != nil || profile.ResourceVersion != 0 {
		t.Fatalf("profile without Save = %+v, %v", profile, err)
	}
}

func TestDesktopImportNeedsASignedInWindowOnTheBoundOrigin(t *testing.T) {
	_, origin, _ := desktopGateway(t)
	for name, test := range map[string]struct {
		hello string
		want  string
	}{
		"no session":       {hello(t, origin, ""), "signedOut"},
		"stale session":    {hello(t, origin, "meridian_session=stale"), "signedOut"},
		"remote over HTTP": {hello(t, "http://gateway.example.com", "a=b"), "error"},
		"path, not origin": {hello(t, origin+"/ui/", "a=b"), "error"},
		"header injection": {hello(t, origin, "a=b\r\nX-Evil: 1"), "error"},
	} {
		messages := run(t, []string{test.hello, `{"action":"save"}`}, laptop(t))
		if len(messages) != 1 || messages[0].Type != test.want {
			t.Fatalf("%s: messages = %+v", name, messages)
		}
	}
}
