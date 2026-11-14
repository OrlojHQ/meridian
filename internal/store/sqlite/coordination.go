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
		descendants_created, revoked_at, revoke_reason, archive_after_seconds,
		created_at, updated_at, resource_version
	FROM coordination_grants`

func (r *repository) InsertCoordinationGrant(ctx context.Context, grant domain.CoordinationGrant) error {
	harnesses, err := json.Marshal(nonNilStrings(grant.AllowedHarnesses))
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `
		INSERT INTO coordination_grants(
			owner_user_id,
			id, project_id, root_thread_id, root_capsule_id, state, spawn_approval,
			max_depth, max_children, max_descendants, max_live_capsules,
			max_messages_per_link, allowed_harnesses, deadline_at, on_deadline,
			descendants_created, revoked_at, revoke_reason, archive_after_seconds,
			created_at, updated_at, resource_version
		) VALUES (COALESCE((SELECT owner_user_id FROM projects WHERE id = ?), ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		grant.ProjectID, grant.ID, grant.ProjectID, grant.RootThreadID, grant.RootCapsuleID, grant.State,
		grant.SpawnApproval, grant.MaxDepth, grant.MaxChildren, grant.MaxDescendants,
		grant.MaxLiveCapsules, grant.MaxMessagesPerLink, string(harnesses),
		formatTime(grant.DeadlineAt), grant.OnDeadline, grant.DescendantsCreated,
		nullableTime(grant.RevokedAt), grant.RevokeReason, int64(grant.ArchiveAfter/time.Second),
		formatTime(grant.CreatedAt),
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
	harnesses, err := json.Marshal(nonNilStrings(grant.AllowedHarnesses))
	if err != nil {
		return err
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE coordination_grants SET state = ?, spawn_approval = ?, max_children = ?,
			max_descendants = ?, max_live_capsules = ?, max_messages_per_link = ?,
			allowed_harnesses = ?, deadline_at = ?, on_deadline = ?, descendants_created = ?,
			revoked_at = ?, revoke_reason = ?, archive_after_seconds = ?,
			updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		grant.State, grant.SpawnApproval, grant.MaxChildren, grant.MaxDescendants,
		grant.MaxLiveCapsules, grant.MaxMessagesPerLink, string(harnesses),
		formatTime(grant.DeadlineAt), grant.OnDeadline, grant.DescendantsCreated,
		nullableTime(grant.RevokedAt), grant.RevokeReason, int64(grant.ArchiveAfter/time.Second),
		formatTime(grant.UpdatedAt), grant.ResourceVersion,
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
	var archiveAfter int64
	if err := row.Scan(
		&grant.ID, &grant.ProjectID, &grant.RootThreadID, &grant.RootCapsuleID, &grant.State,
		&grant.SpawnApproval, &grant.MaxDepth, &grant.MaxChildren, &grant.MaxDescendants,
		&grant.MaxLiveCapsules, &grant.MaxMessagesPerLink, &harnesses, &deadline,
		&grant.OnDeadline, &grant.DescendantsCreated, &revoked, &grant.RevokeReason,
		&archiveAfter, &created, &updated, &grant.ResourceVersion,
	); err != nil {
		return domain.CoordinationGrant{}, mapError(err)
	}
	grant.ArchiveAfter = time.Duration(archiveAfter) * time.Second
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
		last_report_reason, last_report_at, messages_forwarded, operator_messages,
		operator_noticed, archived_at, promoted_at, base_commit, head, files_changed,
		insertions, deletions, created_at, updated_at, resource_version
	FROM thread_links`

func (r *repository) InsertThreadLink(ctx context.Context, link domain.ThreadLink) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO thread_links(
			owner_user_id,
			id, grant_id, parent_thread_id, parent_capsule_id, child_intent_id,
			child_thread_id, child_capsule_id, depth, name, harness, state, close_reason,
			report_seq, report_delivered_seq, report_message_seq, last_report_status,
			last_report_reason, last_report_at, messages_forwarded, operator_messages,
			operator_noticed, archived_at, promoted_at, base_commit, head, files_changed,
			insertions, deletions, created_at, updated_at, resource_version
		) VALUES (COALESCE((SELECT owner_user_id FROM coordination_grants WHERE id = ?), ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		link.GrantID, link.ID, link.GrantID, link.ParentThreadID, link.ParentCapsuleID, link.ChildIntentID,
		link.ChildThreadID, link.ChildCapsuleID, link.Depth, link.Name, link.Harness,
		link.State, link.CloseReason, link.ReportSeq, link.ReportDeliveredSeq,
		link.ReportMessageSeq, link.LastReportStatus, link.LastReportReason,
		nullableTime(link.LastReportAt), link.MessagesForwarded, link.OperatorMessages,
		link.OperatorNoticed, nullableTime(link.ArchivedAt), nullableTime(link.PromotedAt),
		link.BaseCommit, link.Head, link.FilesChanged, link.Insertions, link.Deletions,
		formatTime(link.CreatedAt), formatTime(link.UpdatedAt), link.ResourceVersion,
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
			operator_messages = ?, operator_noticed = ?, archived_at = ?, promoted_at = ?,
			base_commit = ?, head = ?, files_changed = ?, insertions = ?, deletions = ?,
			updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		link.State, link.CloseReason, link.ReportSeq, link.ReportDeliveredSeq,
		link.ReportMessageSeq, link.LastReportStatus, link.LastReportReason,
		nullableTime(link.LastReportAt), link.MessagesForwarded, link.OperatorMessages,
		link.OperatorNoticed, nullableTime(link.ArchivedAt), nullableTime(link.PromotedAt),
		link.BaseCommit, link.Head, link.FilesChanged, link.Insertions, link.Deletions,
		formatTime(link.UpdatedAt),
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

// ListArchivableThreadLinks returns subagent links the auto-archive sweep should
// consider: not archived or promoted, under a grant with auto-archive
// enabled, and either reported idle or closed for a reason other than
// failure, and unchanged for at least the grant's period. The sweep applies
// the remaining idle and attention checks.
func (r *repository) ListArchivableThreadLinks(
	ctx context.Context, now time.Time, limit int,
) ([]domain.ThreadLink, error) {
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("%w: invalid archive scan", domain.ErrInvalid)
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT l.id, l.grant_id, l.parent_thread_id, l.parent_capsule_id, l.child_intent_id,
			l.child_thread_id, l.child_capsule_id, l.depth, l.name, l.harness, l.state, l.close_reason,
			l.report_seq, l.report_delivered_seq, l.report_message_seq, l.last_report_status,
			l.last_report_reason, l.last_report_at, l.messages_forwarded, l.operator_messages,
			l.operator_noticed, l.archived_at, l.promoted_at, l.base_commit, l.head, l.files_changed,
			l.insertions, l.deletions, l.created_at, l.updated_at, l.resource_version
		FROM thread_links l
			JOIN coordination_grants g ON g.id = l.grant_id
			JOIN capsules c ON c.id = l.child_capsule_id
		WHERE l.archived_at IS NULL AND l.promoted_at IS NULL AND g.archive_after_seconds > 0
			AND c.role = 'sub' AND c.state NOT IN ('Deleting', 'Deleted', 'Failed')
			AND c.desired_state <> 'Deleted'
			AND ((l.state = 'active' AND l.last_report_status IN ('turn_ended', 'done'))
				OR (l.state = 'closed' AND l.close_reason IN ('cancelled', 'ended', 'revoked')))
			AND julianday(l.updated_at) + g.archive_after_seconds / 86400.0 <= julianday(?)
		ORDER BY l.updated_at, l.id LIMIT ?`, formatTime(now), limit)
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

// ListArchivedLiveThreadLinks returns archived, unpromoted subagent links
// whose Capsule is not deleted, under a grant with auto-archive enabled. The
// cleanup sweep deletes those Capsules once their work is saved.
func (r *repository) ListArchivedLiveThreadLinks(ctx context.Context, limit int) ([]domain.ThreadLink, error) {
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("%w: invalid cleanup scan", domain.ErrInvalid)
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT l.id, l.grant_id, l.parent_thread_id, l.parent_capsule_id, l.child_intent_id,
			l.child_thread_id, l.child_capsule_id, l.depth, l.name, l.harness, l.state, l.close_reason,
			l.report_seq, l.report_delivered_seq, l.report_message_seq, l.last_report_status,
			l.last_report_reason, l.last_report_at, l.messages_forwarded, l.operator_messages,
			l.operator_noticed, l.archived_at, l.promoted_at, l.base_commit, l.head, l.files_changed,
			l.insertions, l.deletions, l.created_at, l.updated_at, l.resource_version
		FROM thread_links l
			JOIN coordination_grants g ON g.id = l.grant_id
			JOIN capsules c ON c.id = l.child_capsule_id
		WHERE l.archived_at IS NOT NULL AND l.promoted_at IS NULL AND g.archive_after_seconds > 0
			AND c.role = 'sub' AND c.state NOT IN ('Deleting', 'Deleted', 'Failed')
			AND c.desired_state <> 'Deleted'
		ORDER BY l.archived_at, l.id LIMIT ?`, limit)
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
	var reported, archived, promoted sql.NullString
	if err := row.Scan(
		&link.ID, &link.GrantID, &link.ParentThreadID, &link.ParentCapsuleID, &link.ChildIntentID,
		&link.ChildThreadID, &link.ChildCapsuleID, &link.Depth, &link.Name, &link.Harness,
		&link.State, &link.CloseReason, &link.ReportSeq, &link.ReportDeliveredSeq,
		&link.ReportMessageSeq, &link.LastReportStatus, &link.LastReportReason, &reported,
		&link.MessagesForwarded, &link.OperatorMessages, &link.OperatorNoticed,
		&archived, &promoted, &link.BaseCommit, &link.Head, &link.FilesChanged,
		&link.Insertions, &link.Deletions, &created, &updated, &link.ResourceVersion,
	); err != nil {
		return domain.ThreadLink{}, mapError(err)
	}
	var err error
	for _, field := range []struct {
		value  sql.NullString
		target *time.Time
	}{{reported, &link.LastReportAt}, {archived, &link.ArchivedAt}, {promoted, &link.PromotedAt}} {
		if field.value.Valid {
			if *field.target, err = parseTime(field.value.String); err != nil {
				return domain.ThreadLink{}, err
			}
		}
	}
	if link.CreatedAt, err = parseTime(created); err != nil {
		return domain.ThreadLink{}, err
	}
	link.UpdatedAt, err = parseTime(updated)
	return link, err
}

const coordinationRequestSelect = `
	SELECT id, grant_id, capsule_id, request_key, canonical_digest, kind, link_id,
		state, result_message_id, created_at, updated_at, resource_version
	FROM coordination_requests`

func (r *repository) InsertCoordinationRequest(ctx context.Context, request domain.CoordinationRequest) error {
	if request.State == "" {
		request.State = domain.RequestCompleted
	}
	if request.UpdatedAt.IsZero() {
		request.UpdatedAt = request.CreatedAt
	}
	if request.ResourceVersion == 0 {
		request.ResourceVersion = 1
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO coordination_requests(
			owner_user_id,
			id, grant_id, capsule_id, request_key, canonical_digest, kind, link_id,
			state, result_message_id, created_at, updated_at, resource_version
		) VALUES (COALESCE((SELECT owner_user_id FROM coordination_grants WHERE id = ?), ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		request.GrantID, request.ID, request.GrantID, request.CapsuleID, request.Key, request.CanonicalDigest,
		request.Kind, request.LinkID, request.State, request.ResultMessageID,
		formatTime(request.CreatedAt), formatTime(request.UpdatedAt), request.ResourceVersion,
	)
	return mapError(err)
}

func (r *repository) UpdateCoordinationRequest(
	ctx context.Context, request domain.CoordinationRequest, expected domain.ResourceVersion,
) error {
	if request.ResourceVersion != expected+1 {
		return fmt.Errorf("%w: invalid coordination request resource version", domain.ErrInvalid)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE coordination_requests SET state = ?, updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		request.State, formatTime(request.UpdatedAt), request.ResourceVersion,
		request.ID, expected,
	)
	return r.expectOneRow(ctx, result, err, "coordination_requests", string(request.ID))
}

func scanCoordinationRequest(row scanner) (domain.CoordinationRequest, error) {
	var request domain.CoordinationRequest
	var created, updated string
	if err := row.Scan(
		&request.ID, &request.GrantID, &request.CapsuleID, &request.Key, &request.CanonicalDigest,
		&request.Kind, &request.LinkID, &request.State, &request.ResultMessageID, &created, &updated,
		&request.ResourceVersion,
	); err != nil {
		return domain.CoordinationRequest{}, mapError(err)
	}
	var err error
	if request.CreatedAt, err = parseTime(created); err != nil {
		return domain.CoordinationRequest{}, err
	}
	request.UpdatedAt, err = parseTime(updated)
	return request, err
}

func (r *repository) GetCoordinationRequest(
	ctx context.Context, capsuleID domain.CapsuleID, key string,
) (domain.CoordinationRequest, error) {
	return scanCoordinationRequest(r.q.QueryRowContext(ctx,
		coordinationRequestSelect+" WHERE capsule_id = ? AND request_key = ?", capsuleID, key))
}

func (r *repository) GetCoordinationRequestByID(
	ctx context.Context, id domain.CoordinationRequestID,
) (domain.CoordinationRequest, error) {
	return scanCoordinationRequest(r.q.QueryRowContext(ctx, coordinationRequestSelect+" WHERE id = ?", id))
}

func (r *repository) ListCoordinationRequests(
	ctx context.Context, state domain.CoordinationRequestState,
) ([]domain.CoordinationRequest, error) {
	rows, err := r.q.QueryContext(ctx,
		coordinationRequestSelect+" WHERE state = ? ORDER BY created_at, id LIMIT 100", state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var requests []domain.CoordinationRequest
	for rows.Next() {
		request, err := scanCoordinationRequest(rows)
		if err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

// CountLiveCapsules counts Capsules that hold, or are about to hold, provider
// compute: everything not paused, sealed, failed, or deleted.
func (r *repository) CountLiveCapsules(ctx context.Context) (int, error) {
	var count int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM capsules c
		WHERE c.state NOT IN (?, ?, ?, ?) AND c.desired_state <> ?
			AND NOT EXISTS (
				SELECT 1 FROM thread_links l
				WHERE l.child_capsule_id = c.id
					AND (l.state = ? OR l.close_reason = ?)
			)`,
		domain.CapsulePaused, domain.CapsuleSealed, domain.CapsuleFailed, domain.CapsuleDeleted,
		domain.IntentDeleted, domain.LinkPendingApproval, domain.LinkDenied,
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
