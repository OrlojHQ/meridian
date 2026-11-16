package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"

	"github.com/OrlojHQ/meridian/internal/domain"
)

const githubEnvelopeVersion uint16 = 1

type githubPayload struct {
	Token string `json:"token"`
}

// SealGitHub seals a GitHub connection token. The authenticated context binds
// the connection id, owner, and login, so a ciphertext moved to another row
// or another user fails to open. Status lives outside the envelope, so a
// reconnect flag can change without unsealing the token. The context is
// different from a named secret, so the two envelopes are not interchangeable.
func (k *InstallationKey) SealGitHub(connection domain.GitHubConnection, token string) ([]byte, []byte, error) {
	if k == nil || connection.ID == "" || connection.OwnerUserID == "" || connection.Login == "" ||
		connection.ResourceVersion <= 0 || token == "" {
		return nil, nil, domain.ErrInvalid
	}
	plain, err := json.Marshal(githubPayload{Token: token})
	if err != nil {
		return nil, nil, err
	}
	defer zero(plain)
	if len(plain) > MaxValueBytes {
		return nil, nil, fmt.Errorf("%w: secret value exceeds limit", domain.ErrInvalid)
	}
	block, err := aes.NewCipher(k.key[:])
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plain, githubAAD(connection)), nil
}

// OpenGitHub opens a connection envelope. A ciphertext sealed for a different
// owner, id, login, or version does not open.
func (k *InstallationKey) OpenGitHub(connection domain.GitHubConnection) (string, error) {
	if k == nil || connection.KEKID != k.ID || connection.KEKVersion != k.Version ||
		connection.EnvelopeVersion != githubEnvelopeVersion || connection.OwnerUserID == "" {
		return "", domain.ErrKeyMismatch
	}
	block, err := aes.NewCipher(k.key[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := aead.Open(nil, connection.Nonce, connection.Ciphertext, githubAAD(connection))
	if err != nil {
		return "", domain.ErrKeyMismatch
	}
	defer zero(plain)
	var payload githubPayload
	if json.Unmarshal(plain, &payload) != nil || payload.Token == "" {
		return "", domain.ErrCorrupt
	}
	return payload.Token, nil
}

// GitHubEnvelopeVersion is the envelope version stored with a connection.
func GitHubEnvelopeVersion() uint16 { return githubEnvelopeVersion }

func githubAAD(connection domain.GitHubConnection) []byte {
	return []byte(fmt.Sprintf("meridian.github-connection.v1\x00%s\x00%s\x00%s\x00%d",
		connection.ID, connection.OwnerUserID, connection.Login, githubEnvelopeVersion))
}
