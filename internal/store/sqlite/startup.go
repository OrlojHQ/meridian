package sqlite

import (
	"context"
	"strings"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func (r *repository) GetStartup(ctx context.Context, id domain.CapsuleID) (domain.StartupProgress, error) {
	var phase, phases, updated string
	var current, total int64
	err := r.q.QueryRowContext(ctx, `
		SELECT phase, phases, current_bytes, total_bytes, updated_at
		FROM capsule_startup WHERE capsule_id = ?`, id,
	).Scan(&phase, &phases, &current, &total, &updated)
	if err != nil {
		return domain.StartupProgress{}, mapError(err)
	}
	at, err := parseTime(updated)
	if err != nil {
		return domain.StartupProgress{}, err
	}
	names := strings.Split(phases, ",")
	parsed := make([]domain.StartupPhase, 0, len(names))
	for _, name := range names {
		parsed = append(parsed, domain.StartupPhase(name))
	}
	progress, err := domain.NewStartupProgress(id, domain.StartupPhase(phase), parsed, current, total, at)
	if err != nil {
		return domain.StartupProgress{}, err
	}
	return progress, nil
}

func (r *repository) PutStartup(ctx context.Context, progress domain.StartupProgress) error {
	if err := progress.Validate(); err != nil {
		return err
	}
	names := make([]string, len(progress.Phases))
	for i, phase := range progress.Phases {
		names[i] = string(phase)
	}
	current, total := int64(0), int64(0)
	if progress.Pull != nil {
		current = progress.Pull.CurrentBytes
		total = progress.Pull.TotalBytes
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO capsule_startup(
			owner_user_id,
			capsule_id, phase, phases, current_bytes, total_bytes, updated_at
		) VALUES (COALESCE((SELECT owner_user_id FROM capsules WHERE id = ?), ''), ?, ?, ?, ?, ?, ?)
		ON CONFLICT(capsule_id) DO UPDATE SET
			phase = excluded.phase,
			phases = excluded.phases,
			current_bytes = excluded.current_bytes,
			total_bytes = excluded.total_bytes,
			updated_at = excluded.updated_at`,
		progress.CapsuleID, progress.CapsuleID, progress.Phase, strings.Join(names, ","),
		current, total, formatTime(progress.UpdatedAt),
	)
	return mapError(err)
}

func (r *repository) DeleteStartup(ctx context.Context, id domain.CapsuleID) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM capsule_startup WHERE capsule_id = ?`, id)
	return mapError(err)
}
