package domain

import "time"

// GitHubAppRowID is the only github_app primary key. A Station has one App.
const GitHubAppRowID = "station"

// GitHubApp is the Station's GitHub App (ADR 0061). The private key, webhook
// secret, and client secret live only in the sealed envelope.
type GitHubApp struct {
	ID               string
	AppID            int64
	Slug             string
	Name             string
	ClientID         string
	HTMLURL          string
	OwnerLogin       string
	OwnerType        string
	WebhookActive    bool
	CreatedBy        UserID
	EnvelopeVersion  uint16
	KEKID            string
	KEKVersion       uint32
	Nonce            []byte
	Ciphertext       []byte
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ResourceVersion  ResourceVersion
}

// GitHubAppInstallation is one install of the Station's App, on a user
// account or an organization. RepositorySelection is "all" or "selected".
type GitHubAppInstallation struct {
	ID                  int64
	AccountLogin        string
	AccountType         string
	RepositorySelection string
	Suspended           bool
	UpdatedAt           time.Time
}
