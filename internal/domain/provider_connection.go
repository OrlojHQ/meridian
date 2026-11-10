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

	// ChatGPT plan connections only (ADR 0038). Account is the signed-in
	// email from the validated ID token, for display. NeedsSignIn is set when
	// the refresh token stops working. RevocationUnconfirmed is set when a
	// revoked connection's token could not be revoked at OpenAI.
	Account               string `json:"account,omitempty"`
	NeedsSignIn           bool   `json:"needsSignIn,omitempty"`
	RevocationUnconfirmed bool   `json:"revocationUnconfirmed,omitempty"`
	// CredentialGeneration is bound into the token envelope and bumped on
	// every rotation, so a stale ciphertext cannot be swapped back in. It is
	// persisted metadata, not part of the public API.
	CredentialGeneration int64 `json:"credentialGeneration,omitempty"`
	// CredentialKind selects how the gateway attaches an official Anthropic
	// login. Empty means an API key (X-Api-Key). "oauth" is a Claude Code
	// setup token or in-UI sign-in (Authorization: Bearer). It is not the secret.
	CredentialKind string `json:"credentialKind,omitempty"`
}
