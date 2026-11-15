package domain

import (
	"time"

	"github.com/OrlojHQ/meridian/internal/harnesssetup"
)

// UserProfile is one user's workstation profile (ADR 0049). It groups the
// owner's default harness setups, which stay on HarnessSetup.Default, with a
// git commit identity and a default provider connection per harness. A
// default connection is a suggestion, never a grant.
type UserProfile struct {
	OwnerUserID UserID
	GitName     string
	GitEmail    string
	// DefaultConnections maps a harness to one of the owner's provider
	// connection ids.
	DefaultConnections map[string]string
	// ResourceVersion is zero for a user who has never saved a profile.
	ResourceVersion ResourceVersion
	UpdatedAt       time.Time
}

// HarnessSetupReport is the sealed import report of one setup revision.
// Ciphertext opens only under the envelope id that binds the revision's
// owner.
type HarnessSetupReport struct {
	RevisionID string
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

// CapsuleProfilePin is the profile identity a Capsule was created with or
// last updated to. A clean Capsule has none.
type CapsuleProfilePin struct {
	CapsuleID CapsuleID
	GitName   string
	GitEmail  string
	PinnedAt  time.Time
}

// ValidGitIdentity reports whether a commit identity is safe to store. The
// rule lives with harnesssetup so capsuled, which writes the identity into a
// Capsule, checks exactly the same thing.
func ValidGitIdentity(name, email string) bool {
	return harnesssetup.ValidGitIdentity(name, email)
}
