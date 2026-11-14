// Package principal names who a control-plane call acts for (ADR 0046).
//
// Every API request and every background job runs as exactly one principal.
// internal/httpapi authenticates a request and attaches its principal to the
// request context; daemon-internal work attaches the system principal itself.
// internal/app reads the principal and fails closed when it is missing.
//
// A Principal has no exported fields and no parser. The only way to build one
// is a constructor below, so nothing an API request sends (a header, a cookie,
// a path, a body) can name a kind. In particular the system principal is
// reachable only from daemon code that calls System.
package principal

import (
	"context"
	"errors"

	"github.com/OrlojHQ/meridian/internal/domain"
)

// Kind is one of the four principal kinds in ADR 0046.
type Kind uint8

const (
	kindNone Kind = iota
	// KindUser is a member. In single-user mode the only user is the
	// bootstrap owner.
	KindUser
	// KindBreakGlass is the installation bearer in team mode. Single-user
	// mode never constructs it: there the bearer is the bootstrap owner.
	KindBreakGlass
	// KindSystem is daemon-internal work: the reconciler, idle pause,
	// refresh, coordination, cleanup sweeps, and startup recovery.
	KindSystem
	// KindCapsule is a Capsule acting under its owner's authority, narrowed
	// by its gateway lease or Coordination Grant.
	KindCapsule
)

func (k Kind) String() string {
	switch k {
	case KindUser:
		return "user"
	case KindBreakGlass:
		return "break_glass"
	case KindSystem:
		return "system"
	case KindCapsule:
		return "capsule"
	default:
		return "none"
	}
}

// UserID identifies a member. It is opaque outside the users table, which a
// later rollout step adds.
type UserID string

// BootstrapOwner is the user every authenticated caller is in single-user
// mode. The users table must give the bootstrap owner this id, so rows
// stamped before and after that migration agree.
const BootstrapOwner UserID = "usr_bootstrap_owner"

// ErrMissing reports a call that reached the control plane without a
// principal. Its text names no object, caller, or credential.
var ErrMissing = errors.New("principal required")

// Principal is who a call acts for. The zero value is not a principal.
type Principal struct {
	kind    Kind
	user    UserID
	capsule domain.CapsuleID
}

// User returns the principal for member id. It returns false for an empty id.
func User(id UserID) (Principal, bool) {
	if id == "" {
		return Principal{}, false
	}
	return Principal{kind: KindUser, user: id}, true
}

// Owner returns the bootstrap owner, the single-user mode principal.
func Owner() Principal {
	return Principal{kind: KindUser, user: BootstrapOwner}
}

// BreakGlass returns the team-mode installation bearer principal.
func BreakGlass() Principal {
	return Principal{kind: KindBreakGlass}
}

// System returns the principal for daemon-internal work. Call it only from
// code no API request controls: daemon startup, background loops, and
// workers the daemon starts.
func System() Principal {
	return Principal{kind: KindSystem}
}

// Capsule returns the principal for Capsule id acting for owner. It returns
// false when either is empty: a Capsule never acts without an owner.
func Capsule(owner UserID, id domain.CapsuleID) (Principal, bool) {
	if owner == "" || id == "" {
		return Principal{}, false
	}
	return Principal{kind: KindCapsule, user: owner, capsule: id}, true
}

// Kind reports the principal's kind.
func (p Principal) Kind() Kind { return p.kind }

// Valid reports whether p was built by a constructor.
func (p Principal) Valid() bool { return p.kind != kindNone }

// UserID is the user a user principal is, or the owner a Capsule principal
// acts for. It is empty for system and break-glass principals.
func (p Principal) UserID() UserID { return p.user }

// CapsuleID is the Capsule a Capsule principal is. It is empty otherwise.
func (p Principal) CapsuleID() domain.CapsuleID { return p.capsule }

type contextKey struct{}

// With returns ctx carrying p. Attaching the zero Principal clears any
// principal ctx carried.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

// WithSystem returns ctx carrying the system principal.
func WithSystem(ctx context.Context) context.Context {
	return With(ctx, System())
}

// Without returns ctx carrying no principal, whatever its parent carried.
func Without(ctx context.Context) context.Context {
	return With(ctx, Principal{})
}

// From returns the principal ctx carries.
func From(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok && p.Valid()
}

// Require returns the principal ctx carries, or ErrMissing.
func Require(ctx context.Context) (Principal, error) {
	p, ok := From(ctx)
	if !ok {
		return Principal{}, ErrMissing
	}
	return p, nil
}
