package domain

import "time"

// GitHubConnectionID identifies one owner's GitHub connection (ADR 0051).
type GitHubConnectionID string

// GitHubConnectionStatus is whether the stored login can still be used.
type GitHubConnectionStatus string

const (
	GitHubConnectionActive         GitHubConnectionStatus = "active"
	GitHubConnectionNeedsReconnect GitHubConnectionStatus = "needs_reconnect"
)

func (s GitHubConnectionStatus) Valid() bool {
	switch s {
	case GitHubConnectionActive, GitHubConnectionNeedsReconnect:
		return true
	default:
		return false
	}
}

// GitHubConnectionReason says why a connection needs the owner to connect
// again. Empty means the connection is usable.
type GitHubConnectionReason string

const (
	GitHubReasonNone          GitHubConnectionReason = ""
	GitHubReasonInvalid       GitHubConnectionReason = "invalid"
	GitHubReasonExpired       GitHubConnectionReason = "expired"
	GitHubReasonMissingAccess GitHubConnectionReason = "missing_access"
)

func (r GitHubConnectionReason) Valid() bool {
	switch r {
	case GitHubReasonNone, GitHubReasonInvalid, GitHubReasonExpired, GitHubReasonMissingAccess:
		return true
	default:
		return false
	}
}

// GitHubConnectionSource records how the login was obtained. It is metadata,
// not a credential.
type GitHubConnectionSource string

const (
	GitHubSourceDevice GitHubConnectionSource = "device"
	GitHubSourceHost   GitHubConnectionSource = "host"
	GitHubSourceToken  GitHubConnectionSource = "token"
)

func (s GitHubConnectionSource) Valid() bool {
	switch s {
	case GitHubSourceDevice, GitHubSourceHost, GitHubSourceToken:
		return true
	default:
		return false
	}
}

// GitHubConnection is one owner's GitHub login. The token lives only in the
// sealed envelope. Login, status, and scopes are display metadata.
type GitHubConnection struct {
	ID              GitHubConnectionID
	OwnerUserID     UserID
	Login           string
	Status          GitHubConnectionStatus
	Reason          GitHubConnectionReason
	Scopes          string
	Source          GitHubConnectionSource
	EnvelopeVersion uint16
	KEKID           string
	KEKVersion      uint32
	Nonce           []byte
	Ciphertext      []byte
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CheckedAt       time.Time
	ResourceVersion ResourceVersion
}

// Active reports whether the connection may be used for a grant.
func (c GitHubConnection) Active() bool {
	return c.Status == GitHubConnectionActive && c.Reason == GitHubReasonNone
}
