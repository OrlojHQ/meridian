package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func (r *repository) GetPreparation(ctx context.Context, id domain.CapsuleID) (domain.Preparation, error) {
	var item domain.Preparation
	var raw string
	if err := r.q.QueryRowContext(ctx, "SELECT metadata FROM capsule_preparations WHERE capsule_id=?", id).Scan(&raw); err != nil {
		return item, mapError(err)
	}
	err := json.Unmarshal([]byte(raw), &item)
	return item, err
}
func (r *repository) PutPreparation(ctx context.Context, item domain.Preparation) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, "INSERT INTO capsule_preparations(owner_user_id, capsule_id,metadata) VALUES(COALESCE((SELECT owner_user_id FROM capsules WHERE id = ?), ''), ?,?) ON CONFLICT(capsule_id) DO UPDATE SET metadata=excluded.metadata", item.CapsuleID, item.CapsuleID, string(raw))
	return mapError(err)
}

func (r *repository) GetProjectThreadIntentByCapsule(ctx context.Context, id domain.CapsuleID) (domain.ProjectThreadIntent, error) {
	return scanProjectThreadIntent(r.q.QueryRowContext(ctx, projectThreadIntentSelect+" WHERE capsule_id = ?", id))
}

func (r *repository) GetPreparationPolicy(ctx context.Context, id domain.ProjectID) (domain.PreparationPolicy, error) {
	var policy domain.PreparationPolicy
	err := r.q.QueryRowContext(ctx, "SELECT disabled,generation FROM project_preparation_policy WHERE project_id=?", id).Scan(&policy.Disabled, &policy.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		return policy, nil
	}
	return policy, mapError(err)
}
func (r *repository) PutPreparationPolicy(ctx context.Context, id domain.ProjectID, p domain.PreparationPolicy) error {
	_, err := r.q.ExecContext(ctx, "INSERT INTO project_preparation_policy(owner_user_id, project_id,disabled,generation) VALUES(COALESCE((SELECT owner_user_id FROM projects WHERE id = ?), ''), ?,?,?) ON CONFLICT(project_id) DO UPDATE SET disabled=excluded.disabled,generation=excluded.generation", id, id, p.Disabled, p.Generation)
	return mapError(err)
}
