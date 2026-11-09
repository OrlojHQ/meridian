package app

import (
	"context"
	"errors"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/providergateway"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type connectionClock struct{}

func (connectionClock) Now() time.Time { return time.Now() }

type connectionIDs struct{ next int }

func (i *connectionIDs) NewID() string { i.next++; return time.Now().Format("150405.000000000") }

type connectionQueue struct{}

func (connectionQueue) Enqueue(context.Context, domain.CapsuleID) error { return nil }
func TestProviderGrantsAndEncryptedRevocation(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "keys", "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	s := NewService(db, connectionClock{}, &connectionIDs{}, connectionQueue{})
	s.ConfigureSecrets(key)
	if err := s.ConfigureProviderGateway("http://insecure.example", false); err == nil {
		t.Fatal("accepted insecure gateway")
	}
	if err := s.ConfigureProviderGateway("https://meridian.example", false); err != nil {
		t.Fatal(err)
	}
	defer s.CloseProviderGateway()
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
	connection, err := s.PutProviderConnection(ctx, "", ConnectionInput{Name: "OpenAI", Provider: "openai", APIKey: "upstream-private-key"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GrantProviderConnection(ctx, project.ID, "claude", connection.ID); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("cross-provider grant accepted")
	}
	lease := providergateway.Lease{Connection: connection.ID, Project: string(project.ID), Capsule: string(capsule.ID), Harness: "codex", Provider: "openai"}
	if _, err := s.authorizeProviderLease(ctx, lease); err == nil {
		t.Fatal("ungranted connection accepted")
	}
	if err := s.GrantProviderConnection(ctx, project.ID, "codex", connection.ID); err != nil {
		t.Fatal(err)
	}
	upstream, err := s.authorizeProviderLease(ctx, lease)
	value := upstream.Key
	if err != nil || value != "upstream-private-key" || upstream.BaseURL != "" {
		t.Fatal("authorized request failed", err)
	}
	runtime, err := s.runtimeProviderConnection(ctx, capsule, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Token == value || len(runtime.Token) != 64 {
		t.Fatal("upstream key crossed Capsule boundary")
	}
	_, err = s.PutProviderConnection(ctx, connection.ID, ConnectionInput{Name: "OpenAI", Provider: "openai", Revoked: true, ExpectedResourceVersion: connection.ResourceVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.authorizeProviderLease(ctx, lease); err == nil {
		t.Fatal("revoked connection accepted")
	}
	err = db.View(ctx, func(r ports.Reader) error {
		stored, err := r.GetProviderConnection(ctx, connection.ID)
		if err != nil {
			return err
		}
		raw, err := key.OpenSetup(connectionEnvelopeID(stored), stored.KeyID, stored.Nonce, stored.Ciphertext)
		if err != nil {
			return err
		}
		if len(raw) != 0 {
			t.Fatal("revoked key retained")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newConnectionService(t *testing.T, allowPrivateHTTP bool) (*Service, *sqlite.Store, domain.Project, domain.Capsule) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "keys", "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	s := NewService(db, connectionClock{}, &connectionIDs{}, connectionQueue{})
	s.ConfigureSecrets(key)
	if err := s.ConfigureProviderGateway("https://meridian.example", allowPrivateHTTP); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.CloseProviderGateway)
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
	return s, db, project, capsule
}

func TestCustomEndpointConnectionValidation(t *testing.T) {
	ctx := context.Background()
	if err := NewService(nil, connectionClock{}, &connectionIDs{}, connectionQueue{}).ConfigureProviderGateway("", true); err == nil {
		t.Fatal("private HTTP accepted without a gateway")
	}
	locked, _, _, _ := newConnectionService(t, false)
	if _, err := locked.PutProviderConnection(ctx, "", ConnectionInput{Name: "DGX", Provider: "openai-compatible", BaseURL: "http://192.168.1.20:8000", AllowPrivateHTTP: true}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("private HTTP accepted without the installation opt-in", err)
	}
	s, _, _, _ := newConnectionService(t, true)
	for _, input := range []ConnectionInput{
		{Name: "x", Provider: "ollama", BaseURL: "https://models.example"},
		{Name: "x", Provider: "openai", APIKey: "upstream-private-key", BaseURL: "https://models.example"},
		{Name: "x", Provider: "openai", APIKey: "upstream-private-key", DefaultModel: "gpt"},
		{Name: "x", Provider: "openai-compatible"},
		{Name: "x", Provider: "openai-compatible", BaseURL: "http://192.168.1.20:8000"},
		{Name: "x", Provider: "openai-compatible", BaseURL: "http://8.8.8.8:8000", AllowPrivateHTTP: true},
		{Name: "x", Provider: "openai-compatible", BaseURL: "https://models.example", AllowPrivateHTTP: true},
		{Name: "x", Provider: "openai-compatible", BaseURL: "https://169.254.169.254"},
		{Name: "x", Provider: "openai-compatible", BaseURL: "https://key@models.example"},
		{Name: "x", Provider: "openai-compatible", BaseURL: "https://models.example", DefaultModel: "bad model"},
		{Name: "x", Provider: "openai-compatible", BaseURL: "https://models.example", APIKey: "line\nbreak"},
		{Name: "x", Provider: "openai", APIKey: "upstream-private-key", ReasoningEfforts: []string{"high"}},
		{Name: "x", Provider: "openai-compatible", BaseURL: "https://models.example", ReasoningEfforts: []string{"high"}},
		{Name: "x", Provider: "openai-compatible", BaseURL: "https://models.example", DefaultModel: "m", ReasoningEfforts: []string{"ultra"}},
		{Name: "x", Provider: "openai-compatible", BaseURL: "https://models.example", DefaultModel: "m", ReasoningEfforts: []string{"high", "high"}},
		{Name: "x", Provider: "openai-compatible", BaseURL: "https://models.example", DefaultModel: "m", ReasoningEfforts: []string{"off"}},
	} {
		if _, err := s.PutProviderConnection(ctx, "", input); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("accepted %+v: %v", input, err)
		}
	}
	saved, err := s.PutProviderConnection(ctx, "", ConnectionInput{Name: "DGX", Provider: "openai-compatible", BaseURL: "http://dgx.lan:8000/v1/", AllowPrivateHTTP: true, DefaultModel: "Qwen/Qwen3-Coder-30B-A3B-Instruct"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.BaseURL != "http://dgx.lan:8000" || !saved.AllowPrivateHTTP || saved.DefaultModel != "Qwen/Qwen3-Coder-30B-A3B-Instruct" {
		t.Fatalf("unexpected saved connection %+v", saved)
	}
	reasoning, err := s.PutProviderConnection(ctx, "", ConnectionInput{
		Name: "GLM", Provider: "openai-compatible", BaseURL: "https://models.example",
		DefaultModel: "glm-5", ReasoningEfforts: []string{"max", "low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(reasoning.ReasoningEfforts, ",") != "low,high,max" {
		t.Fatalf("levels = %v, want canonical order", reasoning.ReasoningEfforts)
	}
}

func TestCustomEndpointGrantsAndRuntimeLease(t *testing.T) {
	ctx := context.Background()
	s, db, project, capsule := newConnectionService(t, true)
	openai, err := s.PutProviderConnection(ctx, "", ConnectionInput{Name: "vLLM", Provider: "openai-compatible", BaseURL: "https://vllm.example", APIKey: "local-secret-key"})
	if err != nil {
		t.Fatal(err)
	}
	anthropic, err := s.PutProviderConnection(ctx, "", ConnectionInput{Name: "LiteLLM", Provider: "anthropic-compatible", BaseURL: "https://litellm.example", DefaultModel: "qwen3-coder"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		harness, connection string
		ok                  bool
	}{
		{"claude", openai.ID, false},
		{"claude", anthropic.ID, true},
		{"codex", anthropic.ID, false},
		{"codex", openai.ID, true},
		{"opencode", anthropic.ID, false},
		{"opencode", openai.ID, false},
		{"pi", openai.ID, false},
		// DeepSeek Harness needs the connection's model to declare its route.
		{"dsh", openai.ID, false},
		{"dsh", anthropic.ID, true},
		{"mock", anthropic.ID, false},
	} {
		if err := s.GrantProviderConnection(ctx, project.ID, tc.harness, tc.connection); (err == nil) != tc.ok {
			t.Errorf("%s/%s: %v", tc.harness, tc.connection, err)
		}
	}
	dsh, err := s.runtimeProviderConnection(ctx, capsule, "dsh")
	if err != nil {
		t.Fatal(err)
	}
	if dsh.URL != "https://meridian.example/provider-gateway/anthropic-compatible" || dsh.Model != "qwen3-coder" || strings.Contains(dsh.URL, "litellm") {
		t.Fatalf("unexpected dsh runtime connection %+v", dsh)
	}
	lease := providergateway.Lease{Connection: openai.ID, Project: string(project.ID), Capsule: string(capsule.ID), Harness: "codex", Provider: "openai-compatible"}
	upstream, err := s.authorizeProviderLease(ctx, lease)
	if err != nil || upstream.Key != "local-secret-key" || upstream.BaseURL != "https://vllm.example" || upstream.AllowPrivateHTTP {
		t.Fatalf("unexpected upstream %+v %v", upstream, err)
	}
	runtime, err := s.runtimeProviderConnection(ctx, capsule, "codex-structured")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.URL != "https://meridian.example/provider-gateway/openai-compatible" || runtime.Provider != "openai-compatible" || runtime.Token == upstream.Key || strings.Contains(runtime.URL, "vllm") {
		t.Fatalf("custom upstream crossed the Capsule boundary: %+v", runtime)
	}
	claude, err := s.runtimeProviderConnection(ctx, capsule, "claude")
	if err != nil || claude.Model != "qwen3-coder" {
		t.Fatalf("model not delivered: %+v %v", claude, err)
	}

	// Editing stored metadata must not redirect the sealed key elsewhere.
	err = db.Transact(ctx, func(tx ports.Transaction) error {
		stored, err := tx.GetProviderConnection(ctx, openai.ID)
		if err != nil {
			return err
		}
		stored.BaseURL = "https://attacker.example"
		return tx.PutProviderConnection(ctx, stored)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.authorizeProviderLease(ctx, lease); err == nil {
		t.Fatal("key released for a tampered upstream")
	}

	// Without a key, a no-auth server is accepted and nothing is forwarded.
	local, err := s.PutProviderConnection(ctx, "", ConnectionInput{Name: "Ollama", Provider: "openai-compatible", BaseURL: "http://127.0.0.1:11434", AllowPrivateHTTP: true, DefaultModel: "qwen2.5-coder:7b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GrantProviderConnection(ctx, project.ID, "opencode", local.ID); err != nil {
		t.Fatal(err)
	}
	upstream, err = s.authorizeProviderLease(ctx, providergateway.Lease{Connection: local.ID, Project: string(project.ID), Capsule: string(capsule.ID), Harness: "opencode", Provider: "openai-compatible"})
	if err != nil || upstream.Key != "" || !upstream.AllowPrivateHTTP {
		t.Fatalf("unexpected upstream %+v %v", upstream, err)
	}
	revoked, err := s.PutProviderConnection(ctx, local.ID, ConnectionInput{Name: "Ollama", Provider: "openai-compatible", Revoked: true, ExpectedResourceVersion: local.ResourceVersion})
	if err != nil || revoked.BaseURL != local.BaseURL {
		t.Fatalf("revocation failed: %+v %v", revoked, err)
	}
}
