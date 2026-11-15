package domain

import (
	"strings"
	"time"
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

// ValidGitIdentity reports whether a commit identity is safe to store and
// to write as a quoted value in a generated [user] section. Both parts are
// set or both are empty. Control characters, quotes, backslashes, and the
// angle brackets that delimit a commit author are refused rather than
// escaped, so a quoted value can never end early.
func ValidGitIdentity(name, email string) bool {
	if (name == "") != (email == "") {
		return false
	}
	if len(name) > 320 || len(email) > 320 {
		return false
	}
	for _, r := range name + email {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	if strings.ContainsAny(name+email, "\"\\<>") {
		return false
	}
	return strings.TrimSpace(name) == name && strings.TrimSpace(email) == email
}
