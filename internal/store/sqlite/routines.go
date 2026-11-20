package sqlite

import (
	"context"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// Routines (ADR 0057). The routine row holds the clock
// and limits; the prompt, model, and effort are one sealed blob.

const routineSelect = `
	SELECT id, project_id, owner_user_id, name, harness, cadence, time_zone, max_run_seconds,
		catch_up_seconds, max_kept, enabled, disabled_reason, start_failures, next_fire_at,
		last_fire_at, content_nonce, content_ciphertext, include_notes, resource_version, created_at, updated_at
	FROM routines`

const routineFireSelect = `
	SELECT id, routine_id, project_id, owner_user_id, trigger, scheduled_for, state, outcome,
		reason, capsule_id, thread_id, capsule_deleted, model_requested, effort_requested,
		selecting_since, idle_since, deadline_at, started_at, finished_at, resource_version,
		created_at, updated_at
	FROM routine_fires`

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
// routine due exactly now would not be due.
const sortableTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func sortableTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(sortableTimeLayout)
}

func (r *repository) GetRoutine(ctx context.Context, id domain.RoutineID) (domain.Routine, error) {
	return scanRoutine(r.q.QueryRowContext(ctx, routineSelect+` WHERE id = ?`, id))
}

func (r *repository) ListRoutines(ctx context.Context, project domain.ProjectID) ([]domain.Routine, error) {
	rows, err := r.q.QueryContext(ctx, routineSelect+` WHERE project_id = ? ORDER BY name`, project)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var items []domain.Routine
	for rows.Next() {
		item, err := scanRoutine(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

// ListOwnedRoutines lists every routine one owner holds, across Projects.
func (r *repository) ListOwnedRoutines(ctx context.Context, owner domain.UserID) ([]domain.Routine, error) {
	rows, err := r.q.QueryContext(ctx, routineSelect+` WHERE owner_user_id = ? ORDER BY project_id, name`, owner)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var items []domain.Routine
	for rows.Next() {
		item, err := scanRoutine(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (r *repository) ListDueRoutines(ctx context.Context, now time.Time, limit int) ([]domain.Routine, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.q.QueryContext(ctx, routineSelect+`
		WHERE enabled = 1 AND next_fire_at <> '' AND next_fire_at <= ?
		ORDER BY next_fire_at LIMIT ?`, sortableTime(now), limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	var items []domain.Routine
	for rows.Next() {
		item, err := scanRoutine(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func (r *repository) InsertRoutine(ctx context.Context, routine domain.Routine) error {
	if err := routine.Validate(); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO routines(
			id, project_id, owner_user_id, name, harness, cadence, time_zone, max_run_seconds,
			catch_up_seconds, max_kept, enabled, disabled_reason, start_failures, next_fire_at,
			last_fire_at, content_nonce, content_ciphertext, include_notes, resource_version, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		routine.ID, routine.ProjectID, routine.OwnerUserID, routine.Name, routine.Harness,
		routine.Cadence, routine.TimeZone, int64(routine.MaxRun/time.Second),
		int64(routine.CatchUp/time.Second), routine.MaxKept, boolInt(routine.Enabled),
		routine.DisabledReason, routine.StartFailures, sortableTime(routine.NextFireAt),
		optionalTime(routine.LastFireAt), routine.ContentNonce, routine.ContentCiphertext,
		boolInt(routine.IncludeNotes), routine.ResourceVersion, formatTime(routine.CreatedAt),
		formatTime(routine.UpdatedAt),
	)
	return mapError(err)
}

func (r *repository) UpdateRoutine(ctx context.Context, routine domain.Routine, expected domain.ResourceVersion) error {
	if err := routine.Validate(); err != nil {
		return err
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE routines SET
			name = ?, harness = ?, cadence = ?, time_zone = ?, max_run_seconds = ?,
			catch_up_seconds = ?, max_kept = ?, enabled = ?, disabled_reason = ?, start_failures = ?,
			next_fire_at = ?, last_fire_at = ?, content_nonce = ?, content_ciphertext = ?,
			include_notes = ?, resource_version = ?, updated_at = ?
		WHERE id = ? AND resource_version = ?`,
		routine.Name, routine.Harness, routine.Cadence, routine.TimeZone,
		int64(routine.MaxRun/time.Second), int64(routine.CatchUp/time.Second), routine.MaxKept,
		boolInt(routine.Enabled), routine.DisabledReason, routine.StartFailures,
		sortableTime(routine.NextFireAt), optionalTime(routine.LastFireAt),
		routine.ContentNonce, routine.ContentCiphertext, boolInt(routine.IncludeNotes),
		routine.ResourceVersion, formatTime(routine.UpdatedAt), routine.ID, expected,
	)
	return expectOneRow(result, err)
}

// DeleteRoutine removes the routine and its fire history. Capsules a fire
// kept are ordinary Capsules and are not touched.
func (r *repository) DeleteRoutine(ctx context.Context, id domain.RoutineID) error {
	if _, err := r.q.ExecContext(ctx, `DELETE FROM routine_fires WHERE routine_id = ?`, id); err != nil {
		return mapError(err)
	}
	result, err := r.q.ExecContext(ctx, `DELETE FROM routines WHERE id = ?`, id)
	return expectOneRow(result, err)
}

func (r *repository) GetRoutineFire(ctx context.Context, id domain.RoutineFireID) (domain.RoutineFire, error) {
	return scanRoutineFire(r.q.QueryRowContext(ctx, routineFireSelect+` WHERE id = ?`, id))
}

// GetRoutineFireAt returns the clock or catch-up fire for one scheduled
// time. Manual fires are not keyed by time.
func (r *repository) GetRoutineFireAt(
	ctx context.Context, id domain.RoutineID, at time.Time,
) (domain.RoutineFire, error) {
	return scanRoutineFire(r.q.QueryRowContext(ctx, routineFireSelect+`
		WHERE routine_id = ? AND scheduled_for = ? AND trigger <> 'manual'`, id, formatTime(at)))
}

func (r *repository) ListRoutineFires(
	ctx context.Context, id domain.RoutineID, page ports.Page,
) ([]domain.RoutineFire, bool, error) {
	limit := page.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, routineFireSelect+`
		WHERE routine_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		id, limit+1, max(page.Offset, 0))
	if err != nil {
		return nil, false, mapError(err)
	}
	defer rows.Close()
	items, err := scanRoutineFires(rows)
	if err != nil {
		return nil, false, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}

// ListActiveRoutineFires returns fires that still hold a run in progress,
// oldest first.
func (r *repository) ListActiveRoutineFires(ctx context.Context, limit int) ([]domain.RoutineFire, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, routineFireSelect+`
		WHERE state IN ('starting', 'selecting', 'running', 'cleaning')
		ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	return scanRoutineFires(rows)
}

// ActiveRoutineFire returns the routine's fire that is still running, or
// domain.ErrNotFound when none is.
func (r *repository) ActiveRoutineFire(ctx context.Context, id domain.RoutineID) (domain.RoutineFire, error) {
	return scanRoutineFire(r.q.QueryRowContext(ctx, routineFireSelect+`
		WHERE routine_id = ? AND state IN ('starting', 'selecting', 'running', 'cleaning')
		ORDER BY created_at LIMIT 1`, id))
}

// CountKeptRoutineCapsules counts the routine's finished fires whose
// Capsule Meridian kept and nobody has deleted yet.
func (r *repository) CountKeptRoutineCapsules(ctx context.Context, id domain.RoutineID) (int, error) {
	var count int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM routine_fires f
		JOIN capsules c ON c.id = f.capsule_id
		WHERE f.routine_id = ? AND f.state = 'finished' AND f.capsule_deleted = 0
			AND c.state NOT IN (?, ?) AND c.desired_state <> ?`,
		id, domain.CapsuleDeleting, domain.CapsuleDeleted, domain.IntentDeleted,
	).Scan(&count)
	return count, mapError(err)
}

// RoutineFiresByCapsule returns, for each Capsule a fire started, the fire
// that started it.
func (r *repository) RoutineFiresByCapsule(
	ctx context.Context, ids []domain.CapsuleID,
) (map[domain.CapsuleID]domain.RoutineFire, error) {
	result := make(map[domain.CapsuleID]domain.RoutineFire, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := r.q.QueryContext(ctx, routineFireSelect+`
		WHERE capsule_id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, mapError(err)
	}
	defer rows.Close()
	fires, err := scanRoutineFires(rows)
	if err != nil {
		return nil, err
	}
	for _, fire := range fires {
		result[fire.CapsuleID] = fire
	}
	return result, nil
}

func (r *repository) InsertRoutineFire(ctx context.Context, fire domain.RoutineFire) error {
	if err := fire.Validate(); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO routine_fires(
			id, routine_id, project_id, owner_user_id, trigger, scheduled_for, state, outcome,
			reason, capsule_id, thread_id, capsule_deleted, model_requested, effort_requested,
			selecting_since, idle_since, deadline_at, started_at, finished_at, resource_version,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fire.ID, fire.RoutineID, fire.ProjectID, fire.OwnerUserID, fire.Trigger,
		formatTime(fire.ScheduledFor), fire.State, fire.Outcome, fire.Reason, fire.CapsuleID,
		fire.ThreadID, boolInt(fire.CapsuleDeleted), boolInt(fire.ModelRequested),
		boolInt(fire.EffortRequested), optionalTime(fire.SelectingSince), optionalTime(fire.IdleSince),
		optionalTime(fire.DeadlineAt), optionalTime(fire.StartedAt), optionalTime(fire.FinishedAt),
		fire.ResourceVersion, formatTime(fire.CreatedAt), formatTime(fire.UpdatedAt),
	)
	return mapError(err)
}

func (r *repository) UpdateRoutineFire(ctx context.Context, fire domain.RoutineFire, expected domain.ResourceVersion) error {
	if err := fire.Validate(); err != nil {
		return err
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE routine_fires SET
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

func scanRoutine(row scanner) (domain.Routine, error) {
	var routine domain.Routine
	var maxRun, catchUp int64
	var enabled, includeNotes int
	var next, last, created, updated string
	err := row.Scan(
		&routine.ID, &routine.ProjectID, &routine.OwnerUserID, &routine.Name, &routine.Harness,
		&routine.Cadence, &routine.TimeZone, &maxRun, &catchUp, &routine.MaxKept, &enabled,
		&routine.DisabledReason, &routine.StartFailures, &next, &last, &routine.ContentNonce,
		&routine.ContentCiphertext, &includeNotes, &routine.ResourceVersion, &created, &updated,
	)
	if err != nil {
		return domain.Routine{}, mapError(err)
	}
	routine.MaxRun = time.Duration(maxRun) * time.Second
	routine.CatchUp = time.Duration(catchUp) * time.Second
	routine.Enabled = enabled == 1
	routine.IncludeNotes = includeNotes == 1
	for _, item := range []struct {
		raw string
		dst *time.Time
	}{
		{next, &routine.NextFireAt}, {last, &routine.LastFireAt},
		{created, &routine.CreatedAt}, {updated, &routine.UpdatedAt},
	} {
		if item.raw == "" {
			continue
		}
		at, err := parseTime(item.raw)
		if err != nil {
			return domain.Routine{}, err
		}
		*item.dst = at
	}
	return routine, nil
}

func scanRoutineFires(rows interface {
	Next() bool
	Err() error
	Scan(...any) error
}) ([]domain.RoutineFire, error) {
	var items []domain.RoutineFire
	for rows.Next() {
		item, err := scanRoutineFire(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, mapError(rows.Err())
}

func scanRoutineFire(row scanner) (domain.RoutineFire, error) {
	var fire domain.RoutineFire
	var deleted, model, effort int
	var scheduled, selecting, idle, deadline, started, finished, created, updated string
	err := row.Scan(
		&fire.ID, &fire.RoutineID, &fire.ProjectID, &fire.OwnerUserID, &fire.Trigger, &scheduled,
		&fire.State, &fire.Outcome, &fire.Reason, &fire.CapsuleID, &fire.ThreadID, &deleted, &model,
		&effort, &selecting, &idle, &deadline, &started, &finished, &fire.ResourceVersion,
		&created, &updated,
	)
	if err != nil {
		return domain.RoutineFire{}, mapError(err)
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
			return domain.RoutineFire{}, err
		}
		*item.dst = at
	}
	return fire, nil
}
