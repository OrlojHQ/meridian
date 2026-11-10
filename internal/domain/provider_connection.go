package domain

import "time"

type ProviderConnection struct {
	ID              string          `json:"id"`
	Provider        string          `json:"provider"`
	Name            string          `json:"name"`
	CreatedAt       time.Time       `json:"createdAt"`
	ResourceVersion ResourceVersion `json:"resourceVersion"`
	Revoked         bool            `json:"revoked"`
	KeyID           string          `json:"-"`
	Nonce           []byte          `json:"-"`
	Ciphertext      []byte          `json:"-"`

	// Custom endpoint types only. These stay on the control plane and are
	// never sent to a Capsule, except the model name and its effort levels.
	BaseURL          string `json:"baseUrl,omitempty"`
	DefaultModel     string `json:"defaultModel,omitempty"`
	AllowPrivateHTTP bool   `json:"allowPrivateHttp,omitempty"`
	// ReasoningEfforts are the effort levels the default model accepts, in
	// harnesssetup.EffortLevels order. Harnesses offer exactly these (plus
	// off) for that model and send each level's name upstream.
	ReasoningEfforts []string `json:"reasoningEfforts,omitempty"`
	// CredentialKind selects how the gateway attaches an official Anthropic
	// login. Empty means an API key (X-Api-Key). "oauth" is a Claude Code
	// setup token (Authorization: Bearer). It is not the secret.
	CredentialKind string `json:"credentialKind,omitempty"`
}
