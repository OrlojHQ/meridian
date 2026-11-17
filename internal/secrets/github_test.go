package secrets

import (
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func TestGitHubEnvelopeBindsTheOwner(t *testing.T) {
	key, err := OpenOrCreateKey(DefaultKeyPath(t.TempDir()), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	connection := domain.GitHubConnection{
		ID: "ghc_1", OwnerUserID: "usr_bootstrap_owner", Login: "octocat",
		ResourceVersion: 1,
	}
	nonce, ciphertext, err := key.SealGitHub(connection, "gho_canarytokenvalue")
	if err != nil {
		t.Fatal(err)
	}
	connection.Nonce, connection.Ciphertext = nonce, ciphertext
	connection.EnvelopeVersion = GitHubEnvelopeVersion()
	connection.KEKID, connection.KEKVersion = key.ID, key.Version
	token, err := key.OpenGitHub(connection)
	if err != nil || token != "gho_canarytokenvalue" {
		t.Fatalf("open = %q, %v", token, err)
	}
	moved := connection
	moved.OwnerUserID = "usr_other"
	if _, err := key.OpenGitHub(moved); err == nil {
		t.Fatal("ciphertext opened for a different owner")
	}
	renamed := connection
	renamed.Login = "someoneelse"
	if _, err := key.OpenGitHub(renamed); err == nil {
		t.Fatal("ciphertext opened under a different login")
	}
	secret := domain.Secret{
		ID: "secret-1", OwnerUserID: connection.OwnerUserID, Name: "github_api",
		Purpose: domain.SecretGitHubAPI, ResourceVersion: 1,
		EnvelopeVersion: EnvelopeVersion, KEKID: key.ID, KEKVersion: key.Version,
		Nonce: nonce, Ciphertext: ciphertext,
	}
	if _, err := key.Open(secret); err == nil {
		t.Fatal("a connection envelope opened as a named secret")
	}
}
