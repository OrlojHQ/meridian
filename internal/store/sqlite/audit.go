package sqlite

import (
	"context"
	"errors"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func (r *repository) AppendAuditRecord(ctx context.Context, record domain.AuditRecord) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO audit_records(
			recorded_at, principal_kind, user_id, capsule_id, action, object_type, object_id, result
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		auditTime(record.RecordedAt), record.PrincipalKind, record.UserID, record.CapsuleID,
		record.Action, record.ObjectType, record.ObjectID, record.Result,
	)
	return mapError(err)
}

func (r *repository) ListAuditRecords(
	ctx context.Context, user domain.UserID, after int64, limit int,
) ([]domain.AuditRecord, bool, error) {
	if limit <= 0 {
		return nil, false, errors.New("audit page limit must be positive")
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT sequence, recorded_at, principal_kind, user_id, capsule_id, action, object_type, object_id, result
		FROM audit_records
		WHERE sequence > ? AND (? = '' OR user_id = ?)
		ORDER BY sequence LIMIT ?`,
		after, user, user, limit+1,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]domain.AuditRecord, 0, limit)
	for rows.Next() {
		var record domain.AuditRecord
		var recorded string
		if err := rows.Scan(
			&record.Sequence, &recorded, &record.PrincipalKind, &record.UserID, &record.CapsuleID,
			&record.Action, &record.ObjectType, &record.ObjectID, &record.Result,
		); err != nil {
			return nil, false, err
		}
		if record.RecordedAt, err = parseTime(recorded); err != nil {
			return nil, false, err
		}
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

func (r *repository) PruneAuditRecords(ctx context.Context, before time.Time) (int, error) {
	result, err := r.q.ExecContext(ctx,
		"DELETE FROM audit_records WHERE recorded_at < ?", auditTime(before))
	if err != nil {
		return 0, mapError(err)
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

// auditTime is fixed-width UTC, so recorded_at sorts and compares as text
// in time order for retention.
func auditTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}
