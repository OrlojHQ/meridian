package sqlite

import (
	"context"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

const remoteCISelect = `
	SELECT id, capsule_id, project_id, owner_user_id, delivery_id, repository_owner,
		repository_name, pull_number, pull_url, git_head, git_tree, state, epoch, attempt,
		counted, passed_count, failed_count, pending_count, poll_count, next_poll_at,
		rework_sent, push_review, superseded, error_code, resource_version, created_at,
		updated_at, finished_at
	FROM remote_ci_watches`

func (r *repository) LatestRemoteCI(ctx context.Context, id domain.CapsuleID) (domain.RemoteCIWatch, error) {
	row := r.q.QueryRowContext(ctx, remoteCISelect+`
		WHERE capsule_id = ? AND superseded = 0
		ORDER BY created_at DESC LIMIT 1`, id)
	return scanRemoteCI(row)
}

func (r *repository) GetRemoteCI(ctx context.Context, id domain.RemoteCIWatchID) (domain.RemoteCIWatch, error) {
	return scanRemoteCI(r.q.QueryRowContext(ctx, remoteCISelect+` WHERE id = ?`, id))
}

func (r *repository) ListDueRemoteCI(ctx context.Context, now time.Time, limit int) ([]domain.RemoteCIWatch, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.q.QueryContext(ctx, remoteCISelect+`
		WHERE superseded = 0 AND next_poll_at <> '' AND next_poll_at <= ?
			AND (
				state IN ('waiting', 'connect', 'reconnect', 'unreadable')
				OR (state = 'failing' AND error_code = 'rework_due')
			)
		ORDER BY next_poll_at LIMIT ?`, pollTime(now), limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var items []domain.RemoteCIWatch
	for rows.Next() {
		item, err := scanRemoteCI(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (r *repository) CountRemoteCIFailures(ctx context.Context, id domain.CapsuleID, epoch int64) (int, error) {
	var count int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM remote_ci_watches
		WHERE capsule_id = ? AND epoch = ? AND counted = 1`, id, epoch).Scan(&count)
	return count, mapError(err)
}

func (r *repository) InsertRemoteCI(ctx context.Context, watch domain.RemoteCIWatch) error {
	if err := watch.Validate(); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO remote_ci_watches(
			id, capsule_id, project_id, owner_user_id, delivery_id, repository_owner,
			repository_name, pull_number, pull_url, git_head, git_tree, state, epoch, attempt,
			counted, passed_count, failed_count, pending_count, poll_count, next_poll_at,
			rework_sent, push_review, superseded, error_code, resource_version, created_at,
			updated_at, finished_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		watch.ID, watch.CapsuleID, watch.ProjectID, watch.OwnerUserID, watch.DeliveryID,
		watch.RepositoryOwner, watch.RepositoryName, watch.PullNumber, watch.PullURL,
		watch.GitHead, watch.GitTree, watch.State, watch.Epoch, watch.Attempt,
		boolInt(watch.Counted), watch.Passed, watch.Failed, watch.Pending, watch.PollCount,
		pollTime(watch.NextPollAt), boolInt(watch.ReworkSent), boolInt(watch.PushReview),
		boolInt(watch.Superseded), watch.ErrorCode, watch.ResourceVersion,
		formatTime(watch.CreatedAt), formatTime(watch.UpdatedAt), formatTime(watch.FinishedAt),
	)
	return mapError(err)
}

func (r *repository) UpdateRemoteCI(ctx context.Context, watch domain.RemoteCIWatch, expected domain.ResourceVersion) error {
	if err := watch.Validate(); err != nil {
		return err
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE remote_ci_watches SET
			state = ?, epoch = ?, attempt = ?, counted = ?, passed_count = ?, failed_count = ?,
			pending_count = ?, poll_count = ?, next_poll_at = ?, rework_sent = ?, push_review = ?,
			superseded = ?, error_code = ?, resource_version = ?, updated_at = ?, finished_at = ?
		WHERE id = ? AND resource_version = ?`,
		watch.State, watch.Epoch, watch.Attempt, boolInt(watch.Counted), watch.Passed, watch.Failed,
		watch.Pending, watch.PollCount, pollTime(watch.NextPollAt), boolInt(watch.ReworkSent),
		boolInt(watch.PushReview), boolInt(watch.Superseded), watch.ErrorCode, watch.ResourceVersion,
		formatTime(watch.UpdatedAt), formatTime(watch.FinishedAt), watch.ID, expected,
	)
	if err != nil {
		return mapError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (r *repository) SupersedeRemoteCI(ctx context.Context, id domain.CapsuleID, keepHead string) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE remote_ci_watches
		SET superseded = 1, next_poll_at = '', resource_version = resource_version + 1
		WHERE capsule_id = ? AND git_head <> ? AND superseded = 0`, id, keepHead)
	return mapError(err)
}

func (r *repository) ReplaceRemoteCIChecks(ctx context.Context, id domain.RemoteCIWatchID, checks []domain.RemoteCICheck) error {
	if _, err := r.q.ExecContext(ctx, `DELETE FROM remote_ci_checks WHERE watch_id = ?`, id); err != nil {
		return mapError(err)
	}
	for _, check := range checks {
		if err := check.Validate(); err != nil {
			return err
		}
		_, err := r.q.ExecContext(ctx, `
			INSERT INTO remote_ci_checks(
				watch_id, position, name, source, conclusion, url, output_truncated,
				output_nonce, output_ciphertext
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, check.Position, check.Name, check.Source, check.Conclusion, check.URL,
			boolInt(check.OutputTruncated), check.OutputNonce, check.OutputCiphertext,
		)
		if err != nil {
			return mapError(err)
		}
	}
	return nil
}

func (r *repository) ListRemoteCIChecks(ctx context.Context, id domain.RemoteCIWatchID) ([]domain.RemoteCICheck, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT position, name, source, conclusion, url, output_truncated, output_nonce, output_ciphertext
		FROM remote_ci_checks WHERE watch_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var items []domain.RemoteCICheck
	for rows.Next() {
		var item domain.RemoteCICheck
		var truncated int
		if err := rows.Scan(
			&item.Position, &item.Name, &item.Source, &item.Conclusion, &item.URL,
			&truncated, &item.OutputNonce, &item.OutputCiphertext,
		); err != nil {
			return nil, err
		}
		item.OutputTruncated = truncated == 1
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func scanRemoteCI(row checkScanner) (domain.RemoteCIWatch, error) {
	var watch domain.RemoteCIWatch
	var counted, rework, review, superseded int
	var next, created, updated, finished string
	err := row.Scan(
		&watch.ID, &watch.CapsuleID, &watch.ProjectID, &watch.OwnerUserID, &watch.DeliveryID,
		&watch.RepositoryOwner, &watch.RepositoryName, &watch.PullNumber, &watch.PullURL,
		&watch.GitHead, &watch.GitTree, &watch.State, &watch.Epoch, &watch.Attempt,
		&counted, &watch.Passed, &watch.Failed, &watch.Pending, &watch.PollCount, &next,
		&rework, &review, &superseded, &watch.ErrorCode, &watch.ResourceVersion,
		&created, &updated, &finished,
	)
	if err != nil {
		return domain.RemoteCIWatch{}, mapError(err)
	}
	watch.Counted = counted == 1
	watch.ReworkSent = rework == 1
	watch.PushReview = review == 1
	watch.Superseded = superseded == 1
	for _, item := range []struct {
		raw string
		dst *time.Time
	}{
		{next, &watch.NextPollAt},
		{created, &watch.CreatedAt},
		{updated, &watch.UpdatedAt},
		{finished, &watch.FinishedAt},
	} {
		if item.raw == "" {
			continue
		}
		at, err := parseTime(item.raw)
		if err != nil {
			return domain.RemoteCIWatch{}, err
		}
		*item.dst = at
	}
	return watch, nil
}

// pollTime writes a poll time that sorts as text: RFC3339Nano drops
// trailing zeros, so "...:05.1Z" would sort after "...:05.101Z" and a watch
// due now could read as not yet due. The zero time keeps its old form, so
// it still sorts before every real time.
func pollTime(value time.Time) string {
	if value.IsZero() {
		return formatTime(value)
	}
	return sortableTime(value)
}
