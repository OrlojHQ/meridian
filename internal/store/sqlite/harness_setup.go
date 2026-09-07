package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func (r *repository) ListHarnessSetups(ctx context.Context) ([]domain.HarnessSetup, error) {
	rows, err := r.q.QueryContext(ctx, "SELECT metadata FROM harness_setups ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.HarnessSetup{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var item domain.HarnessSetup
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (r *repository) PutHarnessSetup(ctx context.Context, item domain.HarnessSetup) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, "INSERT INTO harness_setups(id,harness,metadata) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET metadata=excluded.metadata", item.ID, item.Harness, string(raw))
	return err
}
func (r *repository) GetHarnessSetupRevision(ctx context.Context, id string) (domain.HarnessSetupRevision, error) {
	var item domain.HarnessSetupRevision
	var raw string
	err := r.q.QueryRowContext(ctx, "SELECT metadata,key_id,nonce,ciphertext FROM harness_setup_revisions WHERE id=?", id).Scan(&raw, &item.KeyID, &item.Nonce, &item.Ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return item, domain.ErrNotFound
	}
	if err != nil {
		return item, err
	}
	err = json.Unmarshal([]byte(raw), &item)
	return item, err
}
func (r *repository) ListHarnessSetupRevisions(ctx context.Context, id string) ([]domain.HarnessSetupRevision, error) {
	rows, err := r.q.QueryContext(ctx, "SELECT metadata FROM harness_setup_revisions WHERE setup_id=? ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.HarnessSetupRevision{}
	for rows.Next() {
		var raw string
		var item domain.HarnessSetupRevision
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (r *repository) InsertHarnessSetupRevision(ctx context.Context, item domain.HarnessSetupRevision) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, "INSERT INTO harness_setup_revisions(id,setup_id,metadata,key_id,nonce,ciphertext) VALUES(?,?,?,?,?,?)", item.ID, item.SetupID, string(raw), item.KeyID, item.Nonce, item.Ciphertext)
	return err
}
func (r *repository) PinHarnessSetup(ctx context.Context, id domain.CapsuleID, harness, revision string) error {
	_, err := r.q.ExecContext(ctx, "INSERT INTO capsule_harness_setups(capsule_id,harness,revision_id) VALUES(?,?,?)", id, harness, revision)
	return err
}
func (r *repository) GetPinnedHarnessSetup(ctx context.Context, id domain.CapsuleID, harness string) (string, error) {
	var revision string
	err := r.q.QueryRowContext(ctx, "SELECT revision_id FROM capsule_harness_setups WHERE capsule_id=? AND harness=?", id, harness).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return revision, err
}

func (r *repository) ProjectHarnessSetups(ctx context.Context, id domain.ProjectID) (map[string]string, error) {
	rows, err := r.q.QueryContext(ctx, "SELECT harness,setup_id FROM project_harness_setups WHERE project_id=?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var harness, setup string
		if err := rows.Scan(&harness, &setup); err != nil {
			return nil, err
		}
		result[harness] = setup
	}
	return result, rows.Err()
}
func (r *repository) SetProjectHarnessSetup(ctx context.Context, id domain.ProjectID, harness, setup string) error {
	if setup == "" {
		_, err := r.q.ExecContext(ctx, "DELETE FROM project_harness_setups WHERE project_id=? AND harness=?", id, harness)
		return err
	}
	_, err := r.q.ExecContext(ctx, "INSERT INTO project_harness_setups(project_id,harness,setup_id) VALUES(?,?,?) ON CONFLICT(project_id,harness) DO UPDATE SET setup_id=excluded.setup_id", id, harness, setup)
	return err
}

func (r *repository) CollectDeletedHarnessSetups(ctx context.Context) error {
	if _, err := r.q.ExecContext(ctx, "DELETE FROM capsule_harness_setups WHERE capsule_id IN (SELECT id FROM capsules WHERE state='Deleted')"); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `DELETE FROM harness_setup_revisions
 WHERE setup_id IN (SELECT id FROM harness_setups WHERE json_extract(metadata,'$.deleted')=1)
 AND id NOT IN (SELECT revision_id FROM capsule_harness_setups)`)
	return err
}
