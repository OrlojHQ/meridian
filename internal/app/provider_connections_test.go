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
	if err := s.ConfigureProviderGateway("http://insecure.example"); err == nil {
		t.Fatal("accepted insecure gateway")
	}
	if err := s.ConfigureProviderGateway("https://meridian.example"); err != nil {
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
	value, err := s.authorizeProviderLease(ctx, lease)
	if err != nil || value != "upstream-private-key" {
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
