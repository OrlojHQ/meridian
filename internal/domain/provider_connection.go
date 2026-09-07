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
}
