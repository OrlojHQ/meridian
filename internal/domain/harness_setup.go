package domain

import (
	"errors"
	"time"
)

type HarnessSetup struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Harness         string          `json:"harness"`
	Revision        string          `json:"revision"`
	Default         bool            `json:"default"`
	Deleted         bool            `json:"deleted"`
	CreatedAt       time.Time       `json:"createdAt"`
	ResourceVersion ResourceVersion `json:"resourceVersion"`
}
type HarnessSetupRevision struct {
	ID         string    `json:"id"`
	SetupID    string    `json:"setupId"`
	Digest     string    `json:"digest"`
	Files      []string  `json:"files"`
	CreatedAt  time.Time `json:"createdAt"`
	KeyID      string    `json:"-"`
	Nonce      []byte    `json:"-"`
	Ciphertext []byte    `json:"-"`
}

// PinnedHarnessSetup is the revision a Capsule was created with.
// Name follows a later rename. RevisedAt is when that revision was saved.
type PinnedHarnessSetup struct {
	Harness   string
	SetupID   string
	Name      string
	Revision  string
	RevisedAt time.Time
}

var ErrHarnessDependencies = errors.New("harness dependency preparation failed")
