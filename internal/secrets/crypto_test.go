package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
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
		OwnerUserID: domain.BootstrapOwnerID,
		ID:          "secret-id", Name: "TOKEN", Purpose: domain.SecretHarnessEnv,
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
	// The owner is bound: the same ciphertext on another user's row does
	// not open (ADR 0046).
	moved := secret
	moved.OwnerUserID = "usr_other"
	if _, err := key.Open(moved); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("moved envelope error = %v", err)
	}
	unowned := secret
	unowned.OwnerUserID = ""
	if _, _, err := key.Seal(unowned, Payload{Value: "x"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("seal without an owner = %v", err)
	}
	if _, err := key.OpenLegacy(secret); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("current envelope opened as legacy: %v", err)
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

func TestLegacyEnvelopesOpenOnlyThroughOpenLegacy(t *testing.T) {
	key, err := OpenOrCreateKey(DefaultKeyPath(t.TempDir()), true)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	now := time.Now().UTC()
	secret := domain.Secret{
		ID: "secret-id", Name: "TOKEN", Purpose: domain.SecretHarnessEnv,
		EnvelopeVersion: LegacyEnvelopeVersion, KEKID: key.ID, KEKVersion: key.Version,
		CreatedAt: now, UpdatedAt: now, ResourceVersion: 3,
	}
	block, err := aes.NewCipher(key.key[:])
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	secret.Nonce = make([]byte, aead.NonceSize())
	secret.Ciphertext = aead.Seal(nil, secret.Nonce, []byte(`{"value":"legacy-canary"}`), aad(secret, LegacyEnvelopeVersion))
	secret.OwnerUserID = domain.BootstrapOwnerID
	if _, err := key.Open(secret); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("legacy envelope opened as current: %v", err)
	}
	payload, err := key.OpenLegacy(secret)
	if err != nil || payload.Value != "legacy-canary" {
		t.Fatalf("OpenLegacy = %+v, %v", payload, err)
	}

	current, err := key.RewrapLegacy(secret)
	if err != nil {
		t.Fatal(err)
	}
	if current.EnvelopeVersion != EnvelopeVersion || current.ResourceVersion != secret.ResourceVersion ||
		bytes.Equal(current.Ciphertext, secret.Ciphertext) {
		t.Fatalf("re-wrapped = %+v", current)
	}
	if payload, err := key.Open(current); err != nil || payload.Value != "legacy-canary" {
		t.Fatalf("Open after re-wrap = %+v, %v", payload, err)
	}
	moved := current
	moved.OwnerUserID = "usr_other"
	if _, err := key.Open(moved); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("re-wrapped envelope opened for another owner: %v", err)
	}
	if _, err := key.RewrapLegacy(current); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("re-wrapping a current envelope = %v", err)
	}
}
