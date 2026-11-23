package secrets_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/secrets"
)

func TestOpenGitHubAppRejectsAShortNonce(t *testing.T) {
	key, err := secrets.OpenOrCreateKey(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	_, err = key.OpenGitHubApp(domain.GitHubApp{
		ID: domain.GitHubAppRowID, AppID: 1, Slug: "meridian", ResourceVersion: 1,
		KEKID: key.ID, KEKVersion: key.Version, EnvelopeVersion: secrets.GitHubAppEnvelopeVersion(),
		Nonce: []byte{1}, Ciphertext: []byte{1, 2, 3},
	})
	if !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("err = %v", err)
	}
}
