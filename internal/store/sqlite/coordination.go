package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

const grantSelect = `
	SELECT id, project_id, root_thread_id, root_capsule_id, state, spawn_approval,
		max_depth, max_children, max_descendants, max_live_capsules,
		max_messages_per_link, allowed_harnesses, deadline_at, on_deadline,
		descendants_created, revoked_at, revoke_reason, created_at, updated_at,
		resource_version
	FROM coordination_grants`

func (r *repository) InsertCoordinationGrant(ctx context.Context, grant domain.CoordinationGrant) error {
	harnesses, err := json.Marshal(nonNilStrings(grant.AllowedHarnesses))
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `
		INSERT INTO coordination_grants(
			id, project_id, root_thread_id, root_capsule_id, state, spawn_approval,
			max_depth, max_children, max_descendants, max_live_capsules,
			max_messages_per_link, allowed_harnesses, deadline_at, on_deadline,
			descendants_created, revoked_at, revoke_reason, created_at, updated_at,
			resource_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		grant.ID, grant.ProjectID, grant.RootThreadID, grant.RootCapsuleID, grant.State,
		grant.SpawnApproval, grant.MaxDepth, grant.MaxChildren, grant.MaxDescendants,
		grant.MaxLiveCapsules, grant.MaxMessagesPerLink, string(harnesses),
		formatTime(grant.DeadlineAt), grant.OnDeadline, grant.DescendantsCreated,
		nullableTime(grant.RevokedAt), grant.RevokeReason, formatTime(grant.CreatedAt),
		formatTime(grant.UpdatedAt), grant.ResourceVersion,
	)
	return mapError(err)
}

func (r *repository) UpdateCoordinationGrant(
	ctx context.Context,
	grant domain.CoordinationGrant,
	expected domain.ResourceVersion,
) error {
	if grant.ResourceVersion != expected+1 {
		return fmt.Errorf("%w: invalid grant resource version", domain.ErrInvalid)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE coordination_grants SET state = ?, descendants_created = ?,
			revoked_at = ?, revoke_reason = ?, updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		grant.State, grant.DescendantsCreated, nullableTime(grant.RevokedAt),
		grant.RevokeReason, formatTime(grant.UpdatedAt), grant.ResourceVersion,
		grant.ID, expected,
	)
	return r.expectOneRow(ctx, result, err, "coordination_grants", string(grant.ID))
}

func (r *repository) GetCoordinationGrant(
	ctx context.Context, id domain.CoordinationGrantID,
) (domain.CoordinationGrant, error) {
	return scanGrant(r.q.QueryRowContext(ctx, grantSelect+" WHERE id = ?", id))
}

func (r *repository) GetCoordinationGrantByRootThread(
	ctx context.Context, threadID domain.ThreadID,
) (domain.CoordinationGrant, error) {
	return scanGrant(r.q.QueryRowContext(ctx, grantSelect+" WHERE root_thread_id = ?", threadID))
}

func (r *repository) GetCoordinationGrantByRootCapsule(
	ctx context.Context, capsuleID domain.CapsuleID,
) (domain.CoordinationGrant, error) {
	return scanGrant(r.q.QueryRowContext(ctx, grantSelect+" WHERE root_capsule_id = ?", capsuleID))
}

// ListActiveCoordinationGrants returns grants that still need the
// coordination loop: active grants, grants with open links or undelivered
// reports, and grants with a child stopped since stoppedSince whose Capsule
// is still running.
func (r *repository) ListActiveCoordinationGrants(
	ctx context.Context, stoppedSince time.Time,
) ([]domain.CoordinationGrant, error) {
	rows, err := r.q.QueryContext(ctx, grantSelect+`
		WHERE state = 'active' OR id IN (
			SELECT grant_id FROM thread_links WHERE state = 'active'
				OR report_delivered_seq < report_seq
		) OR id IN (
			SELECT l.grant_id FROM thread_links l
				JOIN capsules c ON c.id = l.child_capsule_id
			WHERE l.state = 'closed' AND l.close_reason IN ('cancelled', 'revoked')
				AND c.desired_state = 'Ready' AND c.state = 'Ready'
				AND julianday(l.updated_at) >= julianday(?)
		)
		ORDER BY created_at, id LIMIT 1000`, formatTime(stoppedSince))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []domain.CoordinationGrant
	for rows.Next() {
		grant, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

func scanGrant(row scanner) (domain.CoordinationGrant, error) {
	var grant domain.CoordinationGrant
	var harnesses, deadline, created, updated string
	var revoked sql.NullString
	if err := row.Scan(
		&grant.ID, &grant.ProjectID, &grant.RootThreadID, &grant.RootCapsuleID, &grant.State,
		&grant.SpawnApproval, &grant.MaxDepth, &grant.MaxChildren, &grant.MaxDescendants,
		&grant.MaxLiveCapsules, &grant.MaxMessagesPerLink, &harnesses, &deadline,
		&grant.OnDeadline, &grant.DescendantsCreated, &revoked, &grant.RevokeReason,
		&created, &updated, &grant.ResourceVersion,
	); err != nil {
		return domain.CoordinationGrant{}, mapError(err)
	}
	if err := json.Unmarshal([]byte(harnesses), &grant.AllowedHarnesses); err != nil {
		return domain.CoordinationGrant{}, err
	}
	var err error
	if grant.DeadlineAt, err = parseTime(deadline); err != nil {
		return domain.CoordinationGrant{}, err
	}
	if revoked.Valid {
		if grant.RevokedAt, err = parseTime(revoked.String); err != nil {
			return domain.CoordinationGrant{}, err
		}
	}
	if grant.CreatedAt, err = parseTime(created); err != nil {
		return domain.CoordinationGrant{}, err
	}
	grant.UpdatedAt, err = parseTime(updated)
	return grant, err
}

const linkSelect = `
	SELECT id, grant_id, parent_thread_id, parent_capsule_id, child_intent_id,
		child_thread_id, child_capsule_id, depth, name, harness, state, close_reason,
		report_seq, report_delivered_seq, report_message_seq, last_report_status,
		last_report_reason, last_report_at, messages_forwarded, created_at,
		updated_at, resource_version
	FROM thread_links`

func (r *repository) InsertThreadLink(ctx context.Context, link domain.ThreadLink) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO thread_links(
			id, grant_id, parent_thread_id, parent_capsule_id, child_intent_id,
			child_thread_id, child_capsule_id, depth, name, harness, state, close_reason,
			report_seq, report_delivered_seq, report_message_seq, last_report_status,
			last_report_reason, last_report_at, messages_forwarded, created_at,
			updated_at, resource_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		link.ID, link.GrantID, link.ParentThreadID, link.ParentCapsuleID, link.ChildIntentID,
		link.ChildThreadID, link.ChildCapsuleID, link.Depth, link.Name, link.Harness,
		link.State, link.CloseReason, link.ReportSeq, link.ReportDeliveredSeq,
		link.ReportMessageSeq, link.LastReportStatus, link.LastReportReason,
		nullableTime(link.LastReportAt), link.MessagesForwarded, formatTime(link.CreatedAt),
		formatTime(link.UpdatedAt), link.ResourceVersion,
	)
	return mapError(err)
}

func (r *repository) UpdateThreadLink(
	ctx context.Context,
	link domain.ThreadLink,
	expected domain.ResourceVersion,
) error {
	if link.ResourceVersion != expected+1 {
		return fmt.Errorf("%w: invalid link resource version", domain.ErrInvalid)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE thread_links SET state = ?, close_reason = ?, report_seq = ?,
			report_delivered_seq = ?, report_message_seq = ?, last_report_status = ?,
			last_report_reason = ?, last_report_at = ?, messages_forwarded = ?,
			updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		link.State, link.CloseReason, link.ReportSeq, link.ReportDeliveredSeq,
		link.ReportMessageSeq, link.LastReportStatus, link.LastReportReason,
		nullableTime(link.LastReportAt), link.MessagesForwarded, formatTime(link.UpdatedAt),
		link.ResourceVersion, link.ID, expected,
	)
	return r.expectOneRow(ctx, result, err, "thread_links", string(link.ID))
}

func (r *repository) GetThreadLink(ctx context.Context, id domain.ThreadLinkID) (domain.ThreadLink, error) {
	return scanLink(r.q.QueryRowContext(ctx, linkSelect+" WHERE id = ?", id))
}

func (r *repository) GetThreadLinkByChildThread(
	ctx context.Context, threadID domain.ThreadID,
) (domain.ThreadLink, error) {
	return scanLink(r.q.QueryRowContext(ctx, linkSelect+" WHERE child_thread_id = ?", threadID))
}

func (r *repository) ListThreadLinksByParent(
	ctx context.Context, threadID domain.ThreadID,
) ([]domain.ThreadLink, error) {
	return r.listLinks(ctx, linkSelect+" WHERE parent_thread_id = ? ORDER BY created_at, id LIMIT 1000", threadID)
}

func (r *repository) ListThreadLinksByGrant(
	ctx context.Context, grantID domain.CoordinationGrantID,
) ([]domain.ThreadLink, error) {
	return r.listLinks(ctx, linkSelect+" WHERE grant_id = ? ORDER BY created_at, id LIMIT 1000", grantID)
}

func (r *repository) listLinks(ctx context.Context, query string, arg any) ([]domain.ThreadLink, error) {
	rows, err := r.q.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []domain.ThreadLink
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func scanLink(row scanner) (domain.ThreadLink, error) {
	var link domain.ThreadLink
	var created, updated string
	var reported sql.NullString
	if err := row.Scan(
		&link.ID, &link.GrantID, &link.ParentThreadID, &link.ParentCapsuleID, &link.ChildIntentID,
		&link.ChildThreadID, &link.ChildCapsuleID, &link.Depth, &link.Name, &link.Harness,
		&link.State, &link.CloseReason, &link.ReportSeq, &link.ReportDeliveredSeq,
		&link.ReportMessageSeq, &link.LastReportStatus, &link.LastReportReason, &reported,
		&link.MessagesForwarded, &created, &updated, &link.ResourceVersion,
	); err != nil {
		return domain.ThreadLink{}, mapError(err)
	}
	var err error
	if reported.Valid {
		if link.LastReportAt, err = parseTime(reported.String); err != nil {
			return domain.ThreadLink{}, err
		}
	}
	if link.CreatedAt, err = parseTime(created); err != nil {
		return domain.ThreadLink{}, err
	}
	link.UpdatedAt, err = parseTime(updated)
	return link, err
}

func (r *repository) InsertCoordinationRequest(ctx context.Context, request domain.CoordinationRequest) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO coordination_requests(
			id, grant_id, capsule_id, request_key, canonical_digest, kind, link_id,
			result_message_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.ID, request.GrantID, request.CapsuleID, request.Key, request.CanonicalDigest,
		request.Kind, request.LinkID, request.ResultMessageID, formatTime(request.CreatedAt),
	)
	return mapError(err)
}

func (r *repository) GetCoordinationRequest(
	ctx context.Context, capsuleID domain.CapsuleID, key string,
) (domain.CoordinationRequest, error) {
	var request domain.CoordinationRequest
	var created string
	if err := r.q.QueryRowContext(ctx, `
		SELECT id, grant_id, capsule_id, request_key, canonical_digest, kind, link_id,
			result_message_id, created_at
		FROM coordination_requests WHERE capsule_id = ? AND request_key = ?`,
		capsuleID, key,
	).Scan(
		&request.ID, &request.GrantID, &request.CapsuleID, &request.Key, &request.CanonicalDigest,
		&request.Kind, &request.LinkID, &request.ResultMessageID, &created,
	); err != nil {
		return domain.CoordinationRequest{}, mapError(err)
	}
	var err error
	request.CreatedAt, err = parseTime(created)
	return request, err
}

// CountLiveCapsules counts Capsules that hold, or are about to hold, provider
// compute: everything not paused, sealed, failed, or deleted.
func (r *repository) CountLiveCapsules(ctx context.Context) (int, error) {
	var count int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM capsules
		WHERE state NOT IN (?, ?, ?, ?) AND desired_state <> ?`,
		domain.CapsulePaused, domain.CapsuleSealed, domain.CapsuleFailed, domain.CapsuleDeleted,
		domain.IntentDeleted,
	).Scan(&count)
	return count, err
}

func (r *repository) expectOneRow(
	ctx context.Context, result sql.Result, err error, table, id string,
) error {
	if err != nil {
		return mapError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var exists int
	if err := r.q.QueryRowContext(
		ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", id,
	).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return domain.ErrNotFound
	}
	return domain.ErrConflict
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
