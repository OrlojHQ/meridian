package secrets

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func TestKeyPermissionsEnvelopeBindingAndMismatch(t *testing.T) {
	path := DefaultKeyPath(t.TempDir())
	key, err := OpenOrCreateKey(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o", info.Mode().Perm())
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if parent.Mode().Perm() != 0o700 {
		t.Fatalf("key directory mode = %o", parent.Mode().Perm())
	}
	now := time.Now().UTC()
	secret := domain.Secret{
		ID: "secret-id", Name: "TOKEN", Purpose: domain.SecretHarnessEnv,
		EnvelopeVersion: EnvelopeVersion, KEKID: key.ID, KEKVersion: key.Version,
		CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	nonce, ciphertext, err := key.Seal(secret, Payload{Value: "plaintext-canary"})
	if err != nil {
		t.Fatal(err)
	}
	secret.Nonce, secret.Ciphertext = nonce, ciphertext
	if bytes.Contains(ciphertext, []byte("plaintext-canary")) {
		t.Fatal("ciphertext contains plaintext")
	}
	payload, err := key.Open(secret)
	if err != nil || payload.Value != "plaintext-canary" {
		t.Fatalf("open = %#v, %v", payload, err)
	}
	renamed := secret
	renamed.Name = "OTHER"
	if _, err := key.Open(renamed); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("renamed envelope error = %v", err)
	}
	reversioned := secret
	reversioned.ResourceVersion++
	if _, err := key.Open(reversioned); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("reversioned envelope error = %v", err)
	}
	otherPath := DefaultKeyPath(t.TempDir())
	other, err := OpenOrCreateKey(otherPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Zero()
	if _, err := other.Open(secret); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("wrong key error = %v", err)
	}
}

func TestMissingCredentialKeyCannotRegenerate(t *testing.T) {
	path := DefaultKeyPath(t.TempDir())
	if _, err := OpenOrCreateKey(path, false); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("missing locked key error = %v", err)
	}
}
