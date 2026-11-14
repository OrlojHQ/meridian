package domain

import "time"

// AuditResult is the outcome an audit record states.
type AuditResult string

const (
	AuditAllowed AuditResult = "allowed"
	AuditDenied  AuditResult = "denied"
	AuditFailed  AuditResult = "failed"
)

// AuditAction names a recorded action (ADR 0046). Actions are fixed names,
// never caller text.
type AuditAction string

const (
	AuditSecretPut              AuditAction = "secret.put"
	AuditSecretDelete           AuditAction = "secret.delete"
	AuditConnectionPut          AuditAction = "provider_connection.put"
	AuditConnectionSignIn       AuditAction = "provider_connection.sign_in"
	AuditConnectionDisconnect   AuditAction = "provider_connection.disconnect"
	AuditProjectSecretsGrant    AuditAction = "project.grant_secrets"
	AuditProjectConnectionGrant AuditAction = "project.grant_connection"
	AuditDeliveryCreate         AuditAction = "delivery.create"
	AuditDeliveryApprove        AuditAction = "delivery.approve"
	AuditDeliveryReject         AuditAction = "delivery.reject"
	AuditSpawnApprove           AuditAction = "coordination_request.approve"
	AuditSpawnDeny              AuditAction = "coordination_request.deny"
	AuditGrantUpdate            AuditAction = "coordination_grant.update"
	AuditGrantRevoke            AuditAction = "coordination_grant.revoke"
	AuditCrossOwnerAccess       AuditAction = "access.cross_owner"
)

// AuditRecord is one append-only audit line. It holds ids only: no secret
// value, prompt, transcript text, diff, or file content.
type AuditRecord struct {
	Sequence      int64
	RecordedAt    time.Time
	PrincipalKind string
	// UserID is the acting user, or the owner a Capsule acted for. It is
	// empty for the system and break-glass principals.
	UserID     UserID
	CapsuleID  CapsuleID
	Action     AuditAction
	ObjectType string
	ObjectID   string
	Result     AuditResult
}

// UserRole is a member's role (ADR 0046).
type UserRole string

const (
	UserMember UserRole = "member"
	UserAdmin  UserRole = "admin"
)

// User is a member of the installation. A member is identified by
// (Issuer, Subject); DisplayName and Email are refreshed at each sign-in
// for display and never authorize anything (ADR 0046).
type User struct {
	ID          UserID
	Issuer      string
	Subject     string
	DisplayName string
	Email       string
	Role        UserRole
	DisabledAt  time.Time
	CreatedAt   time.Time
}

// InstallationMode is single-user until the installation is claimed.
type InstallationMode string

const (
	SingleUserMode InstallationMode = "single_user"
	TeamMode       InstallationMode = "team"
)

// Installation is the installation's one identity row.
type Installation struct {
	Mode       InstallationMode
	OIDCIssuer string
	ClaimedAt  time.Time
}

// UserSession is a browser session. Only the digest of its token is kept.
type UserSession struct {
	ID          string
	TokenDigest [32]byte
	UserID      UserID
	CreatedAt   time.Time
	LastUsedAt  time.Time
	ExpiresAt   time.Time
	RevokedAt   time.Time
}

// PersonalAccessToken lets a member's CLI and TUI act as that member. Only
// the digest of its value is kept.
type PersonalAccessToken struct {
	ID          string
	TokenDigest [32]byte
	UserID      UserID
	Name        string
	ReadOnly    bool
	CreatedAt   time.Time
	LastUsedAt  time.Time
	ExpiresAt   time.Time
	RevokedAt   time.Time
}

const (
	AuditMemberDisable     AuditAction = "user.disable"
	AuditMemberEnable      AuditAction = "user.enable"
	AuditMemberRole        AuditAction = "user.role"
	AuditMemberRevoke      AuditAction = "user.revoke_credentials"
	AuditTokenCreate       AuditAction = "token.create"
	AuditTokenRevoke       AuditAction = "token.revoke"
	AuditSignIn            AuditAction = "auth.sign_in"
	AuditSignOut           AuditAction = "auth.sign_out"
	AuditSessionRevoke     AuditAction = "session.revoke"
	AuditInstallationClaim AuditAction = "installation.claim"
	AuditBreakGlassUse     AuditAction = "break_glass.use"
)
