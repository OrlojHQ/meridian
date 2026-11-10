package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/providergateway"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

type movingClock struct{ now time.Time }

func (c *movingClock) Now() time.Time { return c.now }

func TestClaudeSignInSealsTokensAndRefreshesWithoutLeavingTheCapsule(t *testing.T) {
	ctx := context.Background()
	const access = "sk-ant-oat01-access-canary"
	const refresh = "sk-ant-ort01-refresh-canary"
	const renewed = "sk-ant-oat01-renewed-canary"
	const code = "auth-code-canary"
	const codexKey = "codex-upstream-key-canary"
	var exchanges, refreshes int
	var verifier string
	token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		text := string(body)
		switch {
		case strings.Contains(text, `"grant_type":"authorization_code"`):
			exchanges++
			verifier = between(text, `"code_verifier":"`, `"`)
			if !strings.Contains(text, `"redirect_uri":"http://localhost:8080/callback"`) || !strings.Contains(text, `"code":"`+code+`"`) {
				t.Errorf("exchange body = %s", text)
			}
			fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":120,"token_type":"Bearer","scope":"user:inference"}`, access, refresh)
		case strings.Contains(text, `"grant_type":"refresh_token"`):
			refreshes++
			if !strings.Contains(text, refresh) {
				t.Errorf("refresh body missing the stored refresh token")
			}
			fmt.Fprintf(w, `{"access_token":%q,"expires_in":120,"token_type":"Bearer"}`, renewed)
		default:
			t.Errorf("unexpected token body")
			http.Error(w, "no", http.StatusBadRequest)
		}
	}))
	defer token.Close()

	dataDir := t.TempDir()
	db, err := sqlite.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "keys", "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	clock := &movingClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	s := NewService(db, clock, &connectionIDs{}, connectionQueue{})
	s.ConfigureSecrets(key)
	if err := s.ConfigureProviderGateway("https://meridian.example", false); err != nil {
		t.Fatal(err)
	}
	defer s.CloseProviderGateway()
	if err := s.SetClaudeTokenURL("https://evil.example/v1/oauth/token"); err == nil {
		t.Fatal("accepted a non-official token URL")
	}
	if err := s.SetClaudeTokenURL(token.URL); err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(ctx, "project", "project")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := s.CreateCapsule(ctx, project.ID, "capsule", "capsule")
	if err != nil {
		t.Fatal(err)
	}
	err = db.Transact(ctx, func(tx ports.Transaction) error {
		capsule.State = domain.CapsuleReady
		capsule.ResourceVersion++
		return tx.UpdateCapsule(ctx, capsule, 1)
	})
	if err != nil {
		t.Fatal(err)
	}

	manual, err := s.BeginClaudeLogin(ctx, "meridian.example:443", "")
	if err != nil || !manual.Manual || !strings.Contains(manual.AuthorizeURL, "code=true") || strings.Contains(manual.AuthorizeURL, "code_verifier") {
		t.Fatalf("manual start = %+v %v", manual, err)
	}
	if _, err := s.CompleteClaudeLogin(ctx, manual.LoginID, code+"#not-the-ceremony-state"); !errors.Is(err, domain.ErrInvalid) || exchanges != 0 {
		t.Fatalf("mismatched state err=%v exchanges=%d", err, exchanges)
	}

	started, err := s.BeginClaudeLogin(ctx, "127.0.0.1:8080", "")
	if err != nil || started.Manual || !strings.Contains(started.AuthorizeURL, "redirect_uri=http%3A%2F%2Flocalhost%3A8080%2Fcallback") {
		t.Fatalf("start = %+v %v", started, err)
	}
	startRaw, _ := json.Marshal(started)
	status, err := s.CompleteClaudeLogin(ctx, started.LoginID, code)
	if err != nil || status.Status != "complete" || status.ConnectionID == "" || exchanges != 1 {
		t.Fatalf("complete = %+v %v exchanges=%d", status, err, exchanges)
	}
	if verifier == "" || bytes.Contains(startRaw, []byte(verifier)) || bytes.Contains(startRaw, []byte(access)) || bytes.Contains(startRaw, []byte(refresh)) {
		t.Fatal("sign-in response contained a verifier or token")
	}
	items, err := s.ListProviderConnections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed, _ := json.Marshal(items)
	if bytes.Contains(listed, []byte(access)) || bytes.Contains(listed, []byte(refresh)) || bytes.Contains(listed, []byte(verifier)) {
		t.Fatal("connection list contains a secret")
	}
	err = db.View(ctx, func(r ports.Reader) error {
		stored, err := r.GetProviderConnection(ctx, status.ConnectionID)
		if err != nil {
			return err
		}
		if stored.CredentialKind != providergateway.CredentialOAuth {
			t.Fatalf("kind = %q", stored.CredentialKind)
		}
		if bytes.Contains(stored.Ciphertext, []byte(access)) || bytes.Contains(stored.Ciphertext, []byte(refresh)) {
			t.Fatal("plaintext token in the ciphertext column")
		}
		opened, err := key.OpenSetup(connectionEnvelopeID(stored), stored.KeyID, stored.Nonce, stored.Ciphertext)
		if err != nil {
			return err
		}
		defer wipe(opened)
		if !bytes.Contains(opened, []byte(access)) || !bytes.Contains(opened, []byte(refresh)) {
			t.Fatal("sealed login did not open to the exchanged tokens")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	absent := func(parts ...string) {
		t.Helper()
		if err := filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, part := range parts {
				if bytes.Contains(raw, []byte(part)) {
					t.Errorf("plaintext secret in %s", path)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	absent(access, refresh, renewed, verifier)

	codex, err := s.PutProviderConnection(ctx, "", ConnectionInput{Name: "OpenAI", Provider: "openai", APIKey: codexKey})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GrantProviderConnection(ctx, project.ID, "codex", codex.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantProviderConnection(ctx, project.ID, "claude", status.ConnectionID); err != nil {
		t.Fatal(err)
	}
	claudeRuntime, err := s.runtimeProviderConnection(ctx, capsule, "claude")
	if err != nil {
		t.Fatal(err)
	}
	runtimeRaw, _ := json.Marshal(claudeRuntime)
	if bytes.Contains(runtimeRaw, []byte(access)) || bytes.Contains(runtimeRaw, []byte(refresh)) || claudeRuntime.Token == access || len(claudeRuntime.Token) != 64 || claudeRuntime.URL != "https://meridian.example/provider-gateway/anthropic" {
		t.Fatal("claude login crossed into the capsule connection")
	}
	lease := providergateway.Lease{Connection: status.ConnectionID, Project: string(project.ID), Capsule: string(capsule.ID), Harness: "claude", Provider: "anthropic"}
	upstream, err := s.authorizeProviderLease(ctx, lease)
	if err != nil || upstream.Key != access || strings.Contains(upstream.Key, refresh) || upstream.CredentialKind != providergateway.CredentialOAuth || refreshes != 0 {
		t.Fatalf("gateway key = %q err=%v refreshes=%d", upstream.Key, err, refreshes)
	}
	clock.now = clock.now.Add(3 * time.Minute)
	upstream, err = s.authorizeProviderLease(ctx, lease)
	if err != nil || upstream.Key != renewed || refreshes != 1 {
		t.Fatalf("refreshed key err=%v refreshes=%d", err, refreshes)
	}
	absent(access, refresh, renewed, verifier)
	codexUpstream, err := s.authorizeProviderLease(ctx, providergateway.Lease{Connection: codex.ID, Project: string(project.ID), Capsule: string(capsule.ID), Harness: "codex", Provider: "openai"})
	if err != nil || codexUpstream.Key != codexKey || codexUpstream.CredentialKind != "" {
		t.Fatalf("codex upstream changed: kind=%q err=%v", codexUpstream.CredentialKind, err)
	}
}

func between(value, start, end string) string {
	from := strings.Index(value, start)
	if from < 0 {
		return ""
	}
	value = value[from+len(start):]
	to := strings.Index(value, end)
	if to < 0 {
		return ""
	}
	return value[:to]
}
