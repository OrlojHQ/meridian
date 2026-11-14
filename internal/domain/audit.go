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

// User is a member of the installation. Display fields are not here: they
// never authorize anything.
type User struct {
	ID         UserID
	Role       UserRole
	DisabledAt time.Time
}
