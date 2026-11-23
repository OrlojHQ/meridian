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

const githubAppEnvelopeVersion uint16 = 1

type githubAppPayload struct {
	PrivateKey    string `json:"privateKey"`
	WebhookSecret string `json:"webhookSecret"`
	ClientSecret  string `json:"clientSecret"`
}

// GitHubAppSecrets are the App credentials that stay on the Station.
type GitHubAppSecrets struct {
	PrivateKey    string
	WebhookSecret string
	ClientSecret  string
}

// SealGitHubApp seals the App private key, webhook secret, and client
// secret. The authenticated context binds the row id, App id, and slug, so
// a ciphertext moved to another row fails to open. This envelope is not a
// named secret and not a user's GitHub connection.
func (k *InstallationKey) SealGitHubApp(app domain.GitHubApp, secrets GitHubAppSecrets) ([]byte, []byte, error) {
	if k == nil || app.ID != domain.GitHubAppRowID || app.AppID <= 0 || app.Slug == "" ||
		app.ResourceVersion <= 0 || secrets.PrivateKey == "" {
		return nil, nil, domain.ErrInvalid
	}
	plain, err := json.Marshal(githubAppPayload{
		PrivateKey: secrets.PrivateKey, WebhookSecret: secrets.WebhookSecret, ClientSecret: secrets.ClientSecret,
	})
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
	return nonce, aead.Seal(nil, nonce, plain, githubAppAAD(app)), nil
}

// OpenGitHubApp opens an App envelope. A ciphertext sealed for a different
// App id, slug, or version does not open.
func (k *InstallationKey) OpenGitHubApp(app domain.GitHubApp) (GitHubAppSecrets, error) {
	if k == nil || app.KEKID != k.ID || app.KEKVersion != k.Version ||
		app.EnvelopeVersion != githubAppEnvelopeVersion || app.ID != domain.GitHubAppRowID {
		return GitHubAppSecrets{}, domain.ErrKeyMismatch
	}
	block, err := aes.NewCipher(k.key[:])
	if err != nil {
		return GitHubAppSecrets{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return GitHubAppSecrets{}, err
	}
	plain, err := aead.Open(nil, app.Nonce, app.Ciphertext, githubAppAAD(app))
	if err != nil {
		return GitHubAppSecrets{}, domain.ErrKeyMismatch
	}
	defer zero(plain)
	var payload githubAppPayload
	if json.Unmarshal(plain, &payload) != nil || payload.PrivateKey == "" {
		return GitHubAppSecrets{}, domain.ErrCorrupt
	}
	return GitHubAppSecrets{
		PrivateKey: payload.PrivateKey, WebhookSecret: payload.WebhookSecret, ClientSecret: payload.ClientSecret,
	}, nil
}

// GitHubAppEnvelopeVersion is the envelope version stored with the App.
func GitHubAppEnvelopeVersion() uint16 { return githubAppEnvelopeVersion }

func githubAppAAD(app domain.GitHubApp) []byte {
	return []byte(fmt.Sprintf("meridian.github-app.v1\x00%s\x00%d\x00%s\x00%d",
		app.ID, app.AppID, app.Slug, githubAppEnvelopeVersion))
}
