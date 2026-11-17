package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func (r *repository) GetProjectChecks(ctx context.Context, id domain.ProjectID) (domain.ProjectChecks, error) {
	var raw string
	var auto, attempts int
	var updated string
	err := r.q.QueryRowContext(ctx, `
		SELECT auto_rework, max_rework_attempts, checks_json, updated_at
		FROM project_checks WHERE project_id = ?`, id).Scan(&auto, &attempts, &raw, &updated)
	if err != nil {
		return domain.ProjectChecks{}, mapError(err)
	}
	checks, err := decodeChecks(raw)
	if err != nil {
		return domain.ProjectChecks{}, err
	}
	at, err := parseTime(updated)
	if err != nil {
		return domain.ProjectChecks{}, err
	}
	return domain.ProjectChecks{
		ProjectID: id, Checks: checks, AutoRework: auto == 1,
		MaxReworkAttempts: attempts, UpdatedAt: at,
	}, nil
}

func (r *repository) PutProjectChecks(ctx context.Context, checks domain.ProjectChecks) error {
	raw, err := json.Marshal(checks.Checks)
	if err != nil {
		return err
	}
	auto := 0
	if checks.AutoRework {
		auto = 1
	}
	_, err = r.q.ExecContext(ctx, `
		INSERT INTO project_checks(owner_user_id, project_id, auto_rework, max_rework_attempts, checks_json, updated_at)
		VALUES (COALESCE((SELECT owner_user_id FROM projects WHERE id = ?), ''), ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			auto_rework = excluded.auto_rework,
			max_rework_attempts = excluded.max_rework_attempts,
			checks_json = excluded.checks_json,
			updated_at = excluded.updated_at`,
		checks.ProjectID, checks.ProjectID, auto, checks.MaxReworkAttempts, string(raw), formatTime(checks.UpdatedAt))
	return mapError(err)
}

func (r *repository) GetCheckControl(ctx context.Context, id domain.CapsuleID) (domain.CheckControl, error) {
	var epoch, stopped int64
	err := r.q.QueryRowContext(ctx, `
		SELECT epoch, auto_rework_stopped FROM capsule_check_controls WHERE capsule_id = ?`, id).Scan(&epoch, &stopped)
	if err == sql.ErrNoRows {
		return domain.CheckControl{CapsuleID: id}, nil
	}
	if err != nil {
		return domain.CheckControl{}, mapError(err)
	}
	return domain.CheckControl{CapsuleID: id, Epoch: epoch, Stopped: stopped == 1}, nil
}

func (r *repository) PutCheckControl(ctx context.Context, control domain.CheckControl) error {
	stopped := 0
	if control.Stopped {
		stopped = 1
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO capsule_check_controls(owner_user_id, capsule_id, epoch, auto_rework_stopped)
		VALUES (COALESCE((SELECT owner_user_id FROM capsules WHERE id = ?), ''), ?, ?, ?)
		ON CONFLICT(capsule_id) DO UPDATE SET
			epoch = excluded.epoch,
			auto_rework_stopped = excluded.auto_rework_stopped`,
		control.CapsuleID, control.CapsuleID, control.Epoch, stopped)
	return mapError(err)
}

const checkRunSelect = `
	SELECT id, capsule_id, project_id, owner_user_id, thread_id, attempt, trigger, state,
		git_head, git_tree, started_at, finished_at, not_before, epoch, basis_message_count,
		basis_run_id, rework_allowed, rework_sent, restart_retried, passed_count, failed_count,
		error_code, resource_version, created_at
	FROM check_runs`

func (r *repository) GetCheckRun(ctx context.Context, id domain.CheckRunID) (domain.CheckRun, error) {
	row := r.q.QueryRowContext(ctx, checkRunSelect+` WHERE id = ?`, id)
	return scanCheckRun(row)
}

func (r *repository) LatestCheckRun(ctx context.Context, id domain.CapsuleID) (domain.CheckRun, error) {
	row := r.q.QueryRowContext(ctx, checkRunSelect+`
		WHERE capsule_id = ? AND state <> 'cancelled'
		ORDER BY created_at DESC LIMIT 1`, id)
	return scanCheckRun(row)
}

func (r *repository) ActiveCheckRun(ctx context.Context, id domain.CapsuleID) (domain.CheckRun, error) {
	row := r.q.QueryRowContext(ctx, checkRunSelect+`
		WHERE capsule_id = ? AND state IN ('queued', 'running')
		ORDER BY created_at DESC LIMIT 1`, id)
	return scanCheckRun(row)
}

func (r *repository) ListCheckRuns(ctx context.Context, id domain.CapsuleID, page ports.Page) ([]domain.CheckRun, bool, error) {
	limit := page.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, checkRunSelect+`
		WHERE capsule_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`, id, limit+1, page.Offset)
	if err != nil {
		return nil, false, mapError(err)
	}
	defer rows.Close()
	var items []domain.CheckRun
	for rows.Next() {
		item, err := scanCheckRun(rows)
		if err != nil {
			return nil, false, err
		}
		items = append(items, item)
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

func (r *repository) ListDueCheckRuns(ctx context.Context, now time.Time, limit int) ([]domain.CheckRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.q.QueryContext(ctx, checkRunSelect+`
		WHERE state = 'queued' AND not_before <= ?
		ORDER BY not_before LIMIT ?`, formatTime(now), limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	return scanCheckRuns(rows)
}

func (r *repository) ListRunningCheckRuns(ctx context.Context) ([]domain.CheckRun, error) {
	rows, err := r.q.QueryContext(ctx, checkRunSelect+` WHERE state = 'running'`)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	return scanCheckRuns(rows)
}

func (r *repository) ListReadyCheckedCapsules(ctx context.Context, limit int) ([]domain.CapsuleID, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT c.id FROM capsules c
		JOIN project_checks p ON p.project_id = c.project_id
		WHERE c.state = 'Ready' AND c.desired_state = 'Ready' AND c.maintenance = ''
			AND p.checks_json NOT IN ('', '[]', 'null')
		ORDER BY c.updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var ids []domain.CapsuleID
	for rows.Next() {
		var id domain.CapsuleID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *repository) LatestCheckRuns(ctx context.Context, ids []domain.CapsuleID) (map[domain.CapsuleID]domain.CheckRun, error) {
	result := make(map[domain.CapsuleID]domain.CheckRun, len(ids))
	for _, id := range ids {
		run, err := r.LatestCheckRun(ctx, id)
		if err == domain.ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		result[id] = run
	}
	return result, nil
}

func (r *repository) CountFailedCheckRuns(ctx context.Context, id domain.CapsuleID, epoch int64) (int, error) {
	var count int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM check_runs
		WHERE capsule_id = ? AND epoch = ? AND state = 'failed'`, id, epoch).Scan(&count)
	return count, mapError(err)
}

func (r *repository) InsertCheckRun(ctx context.Context, run domain.CheckRun) error {
	if err := run.Validate(); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO check_runs(
			owner_user_id, id, capsule_id, project_id, thread_id, attempt, trigger, state,
			git_head, git_tree, started_at, finished_at, not_before, epoch, basis_message_count,
			basis_run_id, rework_allowed, rework_sent, restart_retried, passed_count, failed_count,
			error_code, resource_version, created_at
		) VALUES (
			COALESCE((SELECT owner_user_id FROM capsules WHERE id = ?), ''),
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		)`,
		run.CapsuleID,
		run.ID, run.CapsuleID, run.ProjectID, run.ThreadID, run.Attempt, run.Trigger, run.State,
		run.GitHead, run.GitTree, formatOptionalTime(run.StartedAt), formatOptionalTime(run.FinishedAt),
		formatTime(run.NotBefore), run.Epoch, run.BasisMessageCount, run.BasisRunID,
		boolInt(run.ReworkAllowed), boolInt(run.ReworkSent), boolInt(run.RestartRetried),
		run.Passed, run.Failed, run.ErrorCode, run.ResourceVersion, formatTime(run.CreatedAt),
	)
	return mapError(err)
}

func (r *repository) UpdateCheckRun(ctx context.Context, run domain.CheckRun, expected domain.ResourceVersion) error {
	if err := run.Validate(); err != nil {
		return err
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE check_runs SET
			thread_id = ?, attempt = ?, trigger = ?, state = ?, git_head = ?, git_tree = ?,
			started_at = ?, finished_at = ?, not_before = ?, epoch = ?, basis_message_count = ?,
			basis_run_id = ?, rework_allowed = ?, rework_sent = ?, restart_retried = ?,
			passed_count = ?, failed_count = ?, error_code = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		run.ThreadID, run.Attempt, run.Trigger, run.State, run.GitHead, run.GitTree,
		formatOptionalTime(run.StartedAt), formatOptionalTime(run.FinishedAt), formatTime(run.NotBefore),
		run.Epoch, run.BasisMessageCount, run.BasisRunID,
		boolInt(run.ReworkAllowed), boolInt(run.ReworkSent), boolInt(run.RestartRetried),
		run.Passed, run.Failed, run.ErrorCode, run.ResourceVersion,
		run.ID, expected,
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

func (r *repository) CancelQueuedCheckRuns(ctx context.Context, id domain.CapsuleID, now time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE check_runs SET state = 'cancelled', finished_at = ?, resource_version = resource_version + 1
		WHERE capsule_id = ? AND state = 'queued'`, formatTime(now), id)
	return mapError(err)
}

func (r *repository) ListCheckResults(ctx context.Context, id domain.CheckRunID) ([]domain.CheckResult, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT position, name, argv_json, exit_code, duration_ms, timed_out, output_truncated,
			output_nonce, output_ciphertext
		FROM check_results WHERE run_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var items []domain.CheckResult
	for rows.Next() {
		var item domain.CheckResult
		var argv string
		var exit sql.NullInt64
		var duration int64
		var timed, truncated int
		if err := rows.Scan(&item.Position, &item.Name, &argv, &exit, &duration, &timed, &truncated,
			&item.OutputNonce, &item.OutputCiphertext); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(argv), &item.Argv); err != nil {
			return nil, err
		}
		if exit.Valid {
			item.HasExit = true
			item.ExitCode = int(exit.Int64)
		}
		item.Duration = time.Duration(duration) * time.Millisecond
		item.TimedOut = timed == 1
		item.OutputTruncated = truncated == 1
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *repository) ReplaceCheckResults(ctx context.Context, id domain.CheckRunID, results []domain.CheckResult) error {
	if _, err := r.q.ExecContext(ctx, `DELETE FROM check_results WHERE run_id = ?`, id); err != nil {
		return mapError(err)
	}
	for _, item := range results {
		argv, err := json.Marshal(item.Argv)
		if err != nil {
			return err
		}
		var exit any
		if item.HasExit {
			exit = item.ExitCode
		}
		if _, err := r.q.ExecContext(ctx, `
			INSERT INTO check_results(
				run_id, position, name, argv_json, exit_code, duration_ms, timed_out,
				output_truncated, output_nonce, output_ciphertext
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, item.Position, item.Name, string(argv), exit, item.Duration.Milliseconds(),
			boolInt(item.TimedOut), boolInt(item.OutputTruncated),
			nullableBytes(item.OutputNonce), nullableBytes(item.OutputCiphertext),
		); err != nil {
			return mapError(err)
		}
	}
	return nil
}

func (r *repository) CapsuleHasCheckGate(ctx context.Context, id domain.CapsuleID) (bool, error) {
	var count int
	err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM delivery_check_gates WHERE capsule_id = ?`, id).Scan(&count)
	return count > 0, mapError(err)
}

func (r *repository) InsertDeliveryCheckGate(ctx context.Context, delivery domain.DeliveryID, capsule domain.CapsuleID, at time.Time) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO delivery_check_gates(owner_user_id, delivery_id, capsule_id, created_at)
		VALUES (COALESCE((SELECT owner_user_id FROM capsules WHERE id = ?), ''), ?, ?, ?)`,
		capsule, delivery, capsule, formatTime(at))
	return mapError(err)
}

func (r *repository) DeleteDeliveryCheckGates(ctx context.Context, id domain.CapsuleID) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM delivery_check_gates WHERE capsule_id = ?`, id)
	return mapError(err)
}

func decodeChecks(raw string) ([]domain.CheckCommand, error) {
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var checks []domain.CheckCommand
	if err := json.Unmarshal([]byte(raw), &checks); err != nil {
		return nil, err
	}
	return checks, nil
}

type checkScanner interface {
	Scan(dest ...any) error
}

func scanCheckRun(row checkScanner) (domain.CheckRun, error) {
	var run domain.CheckRun
	var started, finished, notBefore, created string
	var reworkAllowed, reworkSent, retried int
	err := row.Scan(
		&run.ID, &run.CapsuleID, &run.ProjectID, &run.OwnerUserID, &run.ThreadID, &run.Attempt,
		&run.Trigger, &run.State, &run.GitHead, &run.GitTree, &started, &finished, &notBefore,
		&run.Epoch, &run.BasisMessageCount, &run.BasisRunID, &reworkAllowed, &reworkSent, &retried,
		&run.Passed, &run.Failed, &run.ErrorCode, &run.ResourceVersion, &created,
	)
	if err != nil {
		return domain.CheckRun{}, mapError(err)
	}
	run.ReworkAllowed = reworkAllowed == 1
	run.ReworkSent = reworkSent == 1
	run.RestartRetried = retried == 1
	if run.StartedAt, err = parseOptionalTime(started); err != nil {
		return domain.CheckRun{}, err
	}
	if run.FinishedAt, err = parseOptionalTime(finished); err != nil {
		return domain.CheckRun{}, err
	}
	if run.NotBefore, err = parseTime(notBefore); err != nil {
		return domain.CheckRun{}, err
	}
	if run.CreatedAt, err = parseTime(created); err != nil {
		return domain.CheckRun{}, err
	}
	return run, nil
}

func scanCheckRuns(rows *sql.Rows) ([]domain.CheckRun, error) {
	var items []domain.CheckRun
	for rows.Next() {
		item, err := scanCheckRun(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return formatTime(value)
}

func parseOptionalTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return parseTime(value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
