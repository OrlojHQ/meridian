package app

import (
	"context"
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

// MemberDisabled runs after a member is disabled, so components that hold
// that member's live credentials outside the database (gateway leases, PTY
// and preview tickets) drop them at once.
type MemberDisabled func(context.Context, domain.UserID)

// OnMemberDisabled registers a hook. Hooks run in registration order.
func (i *Identity) OnMemberDisabled(hook MemberDisabled) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.disabled = append(i.disabled, hook)
}

// authorizeAdmin accepts an enabled admin member or the break-glass admin
// (ADR 0046). Admins manage membership; they never reach another member's
// objects. Anyone else gets the content-free not-found.
func (i *Identity) authorizeAdmin(ctx context.Context) (principal.Principal, error) {
	caller, err := principal.Require(ctx)
	if err != nil {
		return principal.Principal{}, err
	}
	if !i.team.Load() {
		return principal.Principal{}, domain.ErrNotFound
	}
	switch caller.Kind() {
	case principal.KindBreakGlass:
		return caller, nil
	case principal.KindUser:
		var member domain.User
		system := principal.WithSystem(ctx)
		if err := i.store.View(system, func(reader ports.Reader) error {
			var err error
			member, err = reader.GetUser(system, caller.UserID())
			return err
		}); err != nil || member.Role != domain.UserAdmin || !member.DisabledAt.IsZero() {
			return principal.Principal{}, domain.ErrNotFound
		}
		return caller, nil
	default:
		return principal.Principal{}, domain.ErrNotFound
	}
}

// Members lists every member, for an admin.
func (i *Identity) Members(ctx context.Context) ([]domain.User, error) {
	if _, err := i.authorizeAdmin(ctx); err != nil {
		return nil, err
	}
	system := principal.WithSystem(ctx)
	var users []domain.User
	err := i.store.View(system, func(reader ports.Reader) error {
		var err error
		users, err = reader.ListUsers(system)
		return err
	})
	return users, err
}

// DisableMember disables a member: their sessions and tokens are revoked in
// the same transaction, they cannot sign in, and the disable hooks stop
// their gateway leases and tickets.
func (i *Identity) DisableMember(ctx context.Context, id domain.UserID) error {
	now := i.clock.Now()
	err := i.administer(ctx, id, domain.AuditMemberDisable, func(system context.Context, tx ports.Transaction, target domain.User, others []domain.User) error {
		if target.Role == domain.UserAdmin && !otherEnabledAdmin(target.ID, others) {
			return fmt.Errorf("%w: the last enabled admin cannot be disabled", domain.ErrConflict)
		}
		if err := tx.SetUserDisabled(system, target.ID, now); err != nil {
			return err
		}
		_, err := tx.RevokeUserCredentials(system, target.ID, now)
		return err
	})
	if err != nil {
		return err
	}
	i.mu.Lock()
	hooks := append([]MemberDisabled(nil), i.disabled...)
	i.mu.Unlock()
	for _, hook := range hooks {
		hook(context.WithoutCancel(ctx), id)
	}
	return nil
}

// EnableMember re-enables a member. Their old sessions and tokens stay
// revoked; they sign in again.
func (i *Identity) EnableMember(ctx context.Context, id domain.UserID) error {
	return i.administer(ctx, id, domain.AuditMemberEnable, func(system context.Context, tx ports.Transaction, target domain.User, _ []domain.User) error {
		return tx.SetUserDisabled(system, target.ID, time.Time{})
	})
}

// SetMemberRole grants or removes the admin role. The last enabled admin
// keeps it.
func (i *Identity) SetMemberRole(ctx context.Context, id domain.UserID, role domain.UserRole) error {
	if role != domain.UserMember && role != domain.UserAdmin {
		return fmt.Errorf("%w: role must be member or admin", domain.ErrInvalid)
	}
	return i.administer(ctx, id, domain.AuditMemberRole, func(system context.Context, tx ports.Transaction, target domain.User, others []domain.User) error {
		if target.Role == domain.UserAdmin && role != domain.UserAdmin && target.DisabledAt.IsZero() &&
			!otherEnabledAdmin(target.ID, others) {
			return fmt.Errorf("%w: the last enabled admin keeps the admin role", domain.ErrConflict)
		}
		return tx.SetUserRole(system, target.ID, role)
	})
}

// RevokeMemberCredentials revokes every session and token of a member,
// leaving the member enabled.
func (i *Identity) RevokeMemberCredentials(ctx context.Context, id domain.UserID) error {
	now := i.clock.Now()
	return i.administer(ctx, id, domain.AuditMemberRevoke, func(system context.Context, tx ports.Transaction, target domain.User, _ []domain.User) error {
		_, err := tx.RevokeUserCredentials(system, target.ID, now)
		return err
	})
}

// administer runs one authorized membership change as the system and
// audits it as the admin who made it.
func (i *Identity) administer(
	ctx context.Context, id domain.UserID, action domain.AuditAction,
	change func(context.Context, ports.Transaction, domain.User, []domain.User) error,
) error {
	caller, err := i.authorizeAdmin(ctx)
	if err != nil {
		return err
	}
	now := i.clock.Now()
	system := principal.WithSystem(ctx)
	return i.store.Transact(system, func(tx ports.Transaction) error {
		users, err := tx.ListUsers(system)
		if err != nil {
			return err
		}
		var target domain.User
		found := false
		for _, user := range users {
			if user.ID == id {
				target, found = user, true
			}
		}
		if !found {
			return domain.ErrNotFound
		}
		if err := change(system, tx, target, users); err != nil {
			return err
		}
		record, ok := auditRecordFor(principal.With(ctx, caller), now, action, "user", string(id), domain.AuditAllowed)
		if !ok {
			return principal.ErrMissing
		}
		return tx.AppendAuditRecord(system, record)
	})
}

func otherEnabledAdmin(id domain.UserID, users []domain.User) bool {
	for _, user := range users {
		if user.ID != id && user.Role == domain.UserAdmin && user.DisabledAt.IsZero() {
			return true
		}
	}
	return false
}

// MemberActive reports whether a user principal's member may still act. A
// ticket minted before its member was disabled must not outlive them.
func (i *Identity) MemberActive(ctx context.Context, caller principal.Principal) bool {
	if i == nil || !i.team.Load() || caller.Kind() != principal.KindUser {
		return true
	}
	system := principal.WithSystem(ctx)
	var member domain.User
	if err := i.store.View(system, func(reader ports.Reader) error {
		var err error
		member, err = reader.GetUser(system, caller.UserID())
		return err
	}); err != nil {
		return false
	}
	return member.DisabledAt.IsZero()
}
