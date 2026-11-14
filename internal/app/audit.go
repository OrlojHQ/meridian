package app

import (
	"context"
	"errors"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

const maxAuditPage = 500

// AuditPage is a page of audit records, oldest first.
type AuditPage struct {
	Items      []domain.AuditRecord
	NextCursor int64
	More       bool
}

// auditRecordFor builds a record for ctx's principal. It reports false when
// ctx carries none: every call that reaches an audited action has one.
func auditRecordFor(
	ctx context.Context, at time.Time, action domain.AuditAction,
	objectType, objectID string, result domain.AuditResult,
) (domain.AuditRecord, bool) {
	caller, ok := principal.From(ctx)
	if !ok {
		return domain.AuditRecord{}, false
	}
	return domain.AuditRecord{
		RecordedAt: at.UTC(), PrincipalKind: caller.Kind().String(),
		UserID: caller.UserID(), CapsuleID: caller.CapsuleID(),
		Action: action, ObjectType: objectType, ObjectID: objectID, Result: result,
	}, true
}

// audit records an allowed action in the transaction that performs it, so
// the record and the change commit or roll back together.
func (s *Service) audit(
	ctx context.Context, tx ports.Transaction, action domain.AuditAction, objectType, objectID string,
) error {
	record, ok := auditRecordFor(ctx, s.clock.Now(), action, objectType, objectID, domain.AuditAllowed)
	if !ok {
		return principal.ErrMissing
	}
	return tx.AppendAuditRecord(ctx, record)
}

// auditFailure records an audited action that failed. It runs after the
// action's transaction rolled back. A not-found answer is not recorded
// here: a denied cross-owner access is recorded by the owner scope, and a
// missing object is not an action. Recording is best effort and never
// changes the caller's answer.
func (s *Service) auditFailure(
	ctx context.Context, action domain.AuditAction, objectType, objectID string, err error,
) {
	if err == nil || errors.Is(err, domain.ErrNotFound) || errors.Is(err, principal.ErrMissing) {
		return
	}
	record, ok := auditRecordFor(ctx, s.clock.Now(), action, objectType, objectID, domain.AuditFailed)
	if !ok {
		return
	}
	_ = s.store.Transact(context.WithoutCancel(ctx), func(tx ports.Transaction) error {
		return tx.AppendAuditRecord(ctx, record)
	})
}

// ListAuditRecords lists audit records after a cursor, oldest first. A user
// reads their own records; an admin, and the break-glass admin, read every
// record (ADR 0046). A Capsule reads none.
func (s *Service) ListAuditRecords(ctx context.Context, after int64, limit int) (AuditPage, error) {
	caller, err := principal.Require(ctx)
	if err != nil {
		return AuditPage{}, err
	}
	if after < 0 {
		after = 0
	}
	if limit <= 0 || limit > maxAuditPage {
		limit = defaultPageSize
	}
	readCtx, user := ctx, caller.UserID()
	switch caller.Kind() {
	case principal.KindUser:
		var member domain.User
		if err := s.store.View(ctx, func(reader ports.Reader) error {
			var err error
			member, err = reader.GetUser(ctx, user)
			return err
		}); err != nil {
			return AuditPage{}, err
		}
		if member.Role == domain.UserAdmin && member.DisabledAt.IsZero() {
			user = ""
		}
	case principal.KindBreakGlass:
		// The break-glass admin reaches no object, but the audit log is
		// one of the few things it is for.
		readCtx, user = principal.WithSystem(ctx), ""
	case principal.KindSystem:
		user = ""
	default:
		return AuditPage{}, domain.ErrNotFound
	}
	var page AuditPage
	err = s.store.View(readCtx, func(reader ports.Reader) error {
		var err error
		page.Items, page.More, err = reader.ListAuditRecords(readCtx, user, after, limit)
		return err
	})
	if err != nil {
		return AuditPage{}, err
	}
	page.NextCursor = after
	if len(page.Items) > 0 {
		page.NextCursor = page.Items[len(page.Items)-1].Sequence
	}
	return page, nil
}

// PruneAuditRecords deletes records older than retention. It is the
// daemon's retention sweep; zero retention keeps every record.
func (s *Service) PruneAuditRecords(ctx context.Context, retention time.Duration) (int, error) {
	if err := requireSystem(ctx); err != nil {
		return 0, err
	}
	if retention <= 0 {
		return 0, nil
	}
	var pruned int
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		pruned, err = tx.PruneAuditRecords(ctx, s.clock.Now().Add(-retention))
		return err
	})
	return pruned, err
}
