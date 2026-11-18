package sqlite

import (
	"context"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// Scheduled Project Threads (ADR 0057). The schedule row holds the clock
// and limits; the prompt, model, and effort are one sealed blob.

const scheduleSelect = `
	SELECT id, project_id, owner_user_id, name, harness, cadence, time_zone, max_run_seconds,
		catch_up_seconds, max_kept, enabled, disabled_reason, start_failures, next_fire_at,
		last_fire_at, content_nonce, content_ciphertext, resource_version, created_at, updated_at
	FROM schedules`

const scheduleFireSelect = `
	SELECT id, schedule_id, project_id, owner_user_id, trigger, scheduled_for, state, outcome,
		reason, capsule_id, thread_id, capsule_deleted, model_requested, effort_requested,
		selecting_since, idle_since, deadline_at, started_at, finished_at, resource_version,
		created_at, updated_at
	FROM schedule_fires`

// optionalTime stores the zero time as the empty string, so "not set"
// never sorts before a real time in a due query.
func optionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return formatTime(value)
}

// sortableTimeLayout always writes nine fractional digits. RFC3339Nano drops
// trailing zeros, so "...:00Z" would sort after "...:00.001Z" as text and a
// schedule due exactly now would not be due.
const sortableTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func sortableTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(sortableTimeLayout)
}

func (r *repository) GetSchedule(ctx context.Context, id domain.ScheduleID) (domain.Schedule, error) {
	return scanSchedule(r.q.QueryRowContext(ctx, scheduleSelect+` WHERE id = ?`, id))
}

func (r *repository) ListSchedules(ctx context.Context, project domain.ProjectID) ([]domain.Schedule, error) {
	rows, err := r.q.QueryContext(ctx, scheduleSelect+` WHERE project_id = ? ORDER BY name`, project)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var items []domain.Schedule
	for rows.Next() {
		item, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (r *repository) ListDueSchedules(ctx context.Context, now time.Time, limit int) ([]domain.Schedule, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.q.QueryContext(ctx, scheduleSelect+`
		WHERE enabled = 1 AND next_fire_at <> '' AND next_fire_at <= ?
		ORDER BY next_fire_at LIMIT ?`, sortableTime(now), limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var items []domain.Schedule
	for rows.Next() {
		item, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (r *repository) InsertSchedule(ctx context.Context, schedule domain.Schedule) error {
	if err := schedule.Validate(); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO schedules(
			id, project_id, owner_user_id, name, harness, cadence, time_zone, max_run_seconds,
			catch_up_seconds, max_kept, enabled, disabled_reason, start_failures, next_fire_at,
			last_fire_at, content_nonce, content_ciphertext, resource_version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		schedule.ID, schedule.ProjectID, schedule.OwnerUserID, schedule.Name, schedule.Harness,
		schedule.Cadence, schedule.TimeZone, int64(schedule.MaxRun/time.Second),
		int64(schedule.CatchUp/time.Second), schedule.MaxKept, boolInt(schedule.Enabled),
		schedule.DisabledReason, schedule.StartFailures, sortableTime(schedule.NextFireAt),
		optionalTime(schedule.LastFireAt), schedule.ContentNonce, schedule.ContentCiphertext,
		schedule.ResourceVersion, formatTime(schedule.CreatedAt), formatTime(schedule.UpdatedAt),
	)
	return mapError(err)
}

func (r *repository) UpdateSchedule(ctx context.Context, schedule domain.Schedule, expected domain.ResourceVersion) error {
	if err := schedule.Validate(); err != nil {
		return err
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE schedules SET
			name = ?, harness = ?, cadence = ?, time_zone = ?, max_run_seconds = ?,
			catch_up_seconds = ?, max_kept = ?, enabled = ?, disabled_reason = ?, start_failures = ?,
			next_fire_at = ?, last_fire_at = ?, content_nonce = ?, content_ciphertext = ?,
			resource_version = ?, updated_at = ?
		WHERE id = ? AND resource_version = ?`,
		schedule.Name, schedule.Harness, schedule.Cadence, schedule.TimeZone,
		int64(schedule.MaxRun/time.Second), int64(schedule.CatchUp/time.Second), schedule.MaxKept,
		boolInt(schedule.Enabled), schedule.DisabledReason, schedule.StartFailures,
		sortableTime(schedule.NextFireAt), optionalTime(schedule.LastFireAt),
		schedule.ContentNonce, schedule.ContentCiphertext, schedule.ResourceVersion,
		formatTime(schedule.UpdatedAt), schedule.ID, expected,
	)
	return expectOneRow(result, err)
}

// DeleteSchedule removes the schedule and its fire history. Capsules a fire
// kept are ordinary Capsules and are not touched.
func (r *repository) DeleteSchedule(ctx context.Context, id domain.ScheduleID) error {
	if _, err := r.q.ExecContext(ctx, `DELETE FROM schedule_fires WHERE schedule_id = ?`, id); err != nil {
		return mapError(err)
	}
	result, err := r.q.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id)
	return expectOneRow(result, err)
}

func (r *repository) GetScheduleFire(ctx context.Context, id domain.ScheduleFireID) (domain.ScheduleFire, error) {
	return scanScheduleFire(r.q.QueryRowContext(ctx, scheduleFireSelect+` WHERE id = ?`, id))
}

// GetScheduleFireAt returns the clock or catch-up fire for one scheduled
// time. Manual fires are not keyed by time.
func (r *repository) GetScheduleFireAt(
	ctx context.Context, id domain.ScheduleID, at time.Time,
) (domain.ScheduleFire, error) {
	return scanScheduleFire(r.q.QueryRowContext(ctx, scheduleFireSelect+`
		WHERE schedule_id = ? AND scheduled_for = ? AND trigger <> 'manual'`, id, formatTime(at)))
}

func (r *repository) ListScheduleFires(
	ctx context.Context, id domain.ScheduleID, page ports.Page,
) ([]domain.ScheduleFire, bool, error) {
	limit := page.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, scheduleFireSelect+`
		WHERE schedule_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		id, limit+1, max(page.Offset, 0))
	if err != nil {
		return nil, false, mapError(err)
	}
	defer rows.Close()
	items, err := scanScheduleFires(rows)
	if err != nil {
		return nil, false, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

// ListActiveScheduleFires returns fires that still hold a run in progress,
// oldest first.
func (r *repository) ListActiveScheduleFires(ctx context.Context, limit int) ([]domain.ScheduleFire, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, scheduleFireSelect+`
		WHERE state IN ('starting', 'selecting', 'running', 'cleaning')
		ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	return scanScheduleFires(rows)
}

// ActiveScheduleFire returns the schedule's fire that is still running, or
// domain.ErrNotFound when none is.
func (r *repository) ActiveScheduleFire(ctx context.Context, id domain.ScheduleID) (domain.ScheduleFire, error) {
	return scanScheduleFire(r.q.QueryRowContext(ctx, scheduleFireSelect+`
		WHERE schedule_id = ? AND state IN ('starting', 'selecting', 'running', 'cleaning')
		ORDER BY created_at LIMIT 1`, id))
}

// CountKeptScheduleCapsules counts the schedule's finished fires whose
// Capsule Meridian kept and nobody has deleted yet.
func (r *repository) CountKeptScheduleCapsules(ctx context.Context, id domain.ScheduleID) (int, error) {
	var count int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM schedule_fires f
		JOIN capsules c ON c.id = f.capsule_id
		WHERE f.schedule_id = ? AND f.state = 'finished' AND f.capsule_deleted = 0
			AND c.state NOT IN (?, ?) AND c.desired_state <> ?`,
		id, domain.CapsuleDeleting, domain.CapsuleDeleted, domain.IntentDeleted,
	).Scan(&count)
	return count, mapError(err)
}

// ScheduleFiresByCapsule returns, for each Capsule a fire started, the fire
// that started it.
func (r *repository) ScheduleFiresByCapsule(
	ctx context.Context, ids []domain.CapsuleID,
) (map[domain.CapsuleID]domain.ScheduleFire, error) {
	result := make(map[domain.CapsuleID]domain.ScheduleFire, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := r.q.QueryContext(ctx, scheduleFireSelect+`
		WHERE capsule_id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	fires, err := scanScheduleFires(rows)
	if err != nil {
		return nil, err
	}
	for _, fire := range fires {
		result[fire.CapsuleID] = fire
	}
	return result, nil
}

func (r *repository) InsertScheduleFire(ctx context.Context, fire domain.ScheduleFire) error {
	if err := fire.Validate(); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO schedule_fires(
			id, schedule_id, project_id, owner_user_id, trigger, scheduled_for, state, outcome,
			reason, capsule_id, thread_id, capsule_deleted, model_requested, effort_requested,
			selecting_since, idle_since, deadline_at, started_at, finished_at, resource_version,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fire.ID, fire.ScheduleID, fire.ProjectID, fire.OwnerUserID, fire.Trigger,
		formatTime(fire.ScheduledFor), fire.State, fire.Outcome, fire.Reason, fire.CapsuleID,
		fire.ThreadID, boolInt(fire.CapsuleDeleted), boolInt(fire.ModelRequested),
		boolInt(fire.EffortRequested), optionalTime(fire.SelectingSince), optionalTime(fire.IdleSince),
		optionalTime(fire.DeadlineAt), optionalTime(fire.StartedAt), optionalTime(fire.FinishedAt),
		fire.ResourceVersion, formatTime(fire.CreatedAt), formatTime(fire.UpdatedAt),
	)
	return mapError(err)
}

func (r *repository) UpdateScheduleFire(ctx context.Context, fire domain.ScheduleFire, expected domain.ResourceVersion) error {
	if err := fire.Validate(); err != nil {
		return err
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE schedule_fires SET
			state = ?, outcome = ?, reason = ?, capsule_id = ?, thread_id = ?, capsule_deleted = ?,
			model_requested = ?, effort_requested = ?, selecting_since = ?, idle_since = ?,
			deadline_at = ?, started_at = ?, finished_at = ?, resource_version = ?, updated_at = ?
		WHERE id = ? AND resource_version = ?`,
		fire.State, fire.Outcome, fire.Reason, fire.CapsuleID, fire.ThreadID,
		boolInt(fire.CapsuleDeleted), boolInt(fire.ModelRequested), boolInt(fire.EffortRequested),
		optionalTime(fire.SelectingSince), optionalTime(fire.IdleSince), optionalTime(fire.DeadlineAt),
		optionalTime(fire.StartedAt), optionalTime(fire.FinishedAt), fire.ResourceVersion,
		formatTime(fire.UpdatedAt), fire.ID, expected,
	)
	return expectOneRow(result, err)
}

func expectOneRow(result interface{ RowsAffected() (int64, error) }, err error) error {
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

func scanSchedule(row scanner) (domain.Schedule, error) {
	var schedule domain.Schedule
	var maxRun, catchUp int64
	var enabled int
	var next, last, created, updated string
	err := row.Scan(
		&schedule.ID, &schedule.ProjectID, &schedule.OwnerUserID, &schedule.Name, &schedule.Harness,
		&schedule.Cadence, &schedule.TimeZone, &maxRun, &catchUp, &schedule.MaxKept, &enabled,
		&schedule.DisabledReason, &schedule.StartFailures, &next, &last, &schedule.ContentNonce,
		&schedule.ContentCiphertext, &schedule.ResourceVersion, &created, &updated,
	)
	if err != nil {
		return domain.Schedule{}, mapError(err)
	}
	schedule.MaxRun = time.Duration(maxRun) * time.Second
	schedule.CatchUp = time.Duration(catchUp) * time.Second
	schedule.Enabled = enabled == 1
	for _, item := range []struct {
		raw string
		dst *time.Time
	}{
		{next, &schedule.NextFireAt}, {last, &schedule.LastFireAt},
		{created, &schedule.CreatedAt}, {updated, &schedule.UpdatedAt},
	} {
		if item.raw == "" {
			continue
		}
		at, err := parseTime(item.raw)
		if err != nil {
			return domain.Schedule{}, err
		}
		*item.dst = at
	}
	return schedule, nil
}

func scanScheduleFires(rows interface {
	Next() bool
	Err() error
	Scan(...any) error
}) ([]domain.ScheduleFire, error) {
	var items []domain.ScheduleFire
	for rows.Next() {
		item, err := scanScheduleFire(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func scanScheduleFire(row scanner) (domain.ScheduleFire, error) {
	var fire domain.ScheduleFire
	var deleted, model, effort int
	var scheduled, selecting, idle, deadline, started, finished, created, updated string
	err := row.Scan(
		&fire.ID, &fire.ScheduleID, &fire.ProjectID, &fire.OwnerUserID, &fire.Trigger, &scheduled,
		&fire.State, &fire.Outcome, &fire.Reason, &fire.CapsuleID, &fire.ThreadID, &deleted, &model,
		&effort, &selecting, &idle, &deadline, &started, &finished, &fire.ResourceVersion,
		&created, &updated,
	)
	if err != nil {
		return domain.ScheduleFire{}, mapError(err)
	}
	fire.CapsuleDeleted = deleted == 1
	fire.ModelRequested = model == 1
	fire.EffortRequested = effort == 1
	for _, item := range []struct {
		raw string
		dst *time.Time
	}{
		{scheduled, &fire.ScheduledFor}, {selecting, &fire.SelectingSince}, {idle, &fire.IdleSince},
		{deadline, &fire.DeadlineAt}, {started, &fire.StartedAt}, {finished, &fire.FinishedAt},
		{created, &fire.CreatedAt}, {updated, &fire.UpdatedAt},
	} {
		if item.raw == "" {
			continue
		}
		at, err := parseTime(item.raw)
		if err != nil {
			return domain.ScheduleFire{}, err
		}
		*item.dst = at
	}
	return fire, nil
}
