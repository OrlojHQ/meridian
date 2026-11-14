package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func (r *repository) ListHarnessSetups(ctx context.Context) ([]domain.HarnessSetup, error) {
	rows, err := r.q.QueryContext(ctx, "SELECT metadata,owner_user_id FROM harness_setups ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.HarnessSetup{}
	for rows.Next() {
		var raw string
		var owner domain.UserID
		if err := rows.Scan(&raw, &owner); err != nil {
			return nil, err
		}
		var item domain.HarnessSetup
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		item.OwnerUserID = owner
		items = append(items, item)
	}
	return items, rows.Err()
}
func (r *repository) PutHarnessSetup(ctx context.Context, item domain.HarnessSetup) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	// An existing row is replaced only by its owner; for anyone else the
	// setup does not exist.
	result, err := r.q.ExecContext(ctx, "INSERT INTO harness_setups(id,harness,metadata,owner_user_id) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET metadata=excluded.metadata WHERE owner_user_id=excluded.owner_user_id", item.ID, item.Harness, string(raw), item.OwnerUserID)
	return ownedUpsertResult(result, err)
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
	_, err = r.q.ExecContext(ctx, "INSERT INTO harness_setup_revisions(owner_user_id, id,setup_id,metadata,key_id,nonce,ciphertext) VALUES(COALESCE((SELECT owner_user_id FROM harness_setups WHERE id = ?), ''), ?,?,?,?,?,?)", item.SetupID, item.ID, item.SetupID, string(raw), item.KeyID, item.Nonce, item.Ciphertext)
	return err
}
func (r *repository) PinHarnessSetup(ctx context.Context, id domain.CapsuleID, harness, revision string) error {
	_, err := r.q.ExecContext(ctx, "INSERT INTO capsule_harness_setups(capsule_id,harness,revision_id) VALUES(?,?,?)", id, harness, revision)
	return mapOwnerViolation(err)
}
func (r *repository) ListPinnedHarnessSetups(ctx context.Context, ids []domain.CapsuleID) (map[domain.CapsuleID][]domain.PinnedHarnessSetup, error) {
	result := map[domain.CapsuleID][]domain.PinnedHarnessSetup{}
	if len(ids) == 0 {
		return result, nil
	}
	const chunk = 100
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		if err := r.appendPinnedHarnessSetups(ctx, ids[start:end], result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (r *repository) appendPinnedHarnessSetups(ctx context.Context, ids []domain.CapsuleID, result map[domain.CapsuleID][]domain.PinnedHarnessSetup) error {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = string(id)
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT p.capsule_id, p.harness, p.revision_id, s.metadata, rev.metadata
		FROM capsule_harness_setups p
		LEFT JOIN harness_setup_revisions rev ON rev.id = p.revision_id
		LEFT JOIN harness_setups s ON s.id = rev.setup_id
		WHERE p.capsule_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY p.harness`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var capsuleID, harness, revision string
		var setupRaw, revisionRaw sql.NullString
		if err := rows.Scan(&capsuleID, &harness, &revision, &setupRaw, &revisionRaw); err != nil {
			return err
		}
		if !setupRaw.Valid || !revisionRaw.Valid {
			return fmt.Errorf("%w: pinned harness setup %s/%s is missing", domain.ErrCorrupt, capsuleID, harness)
		}
		var setup domain.HarnessSetup
		var saved domain.HarnessSetupRevision
		if err := json.Unmarshal([]byte(setupRaw.String), &setup); err != nil {
			return fmt.Errorf("%w: pinned harness setup", domain.ErrCorrupt)
		}
		if err := json.Unmarshal([]byte(revisionRaw.String), &saved); err != nil {
			return fmt.Errorf("%w: pinned harness setup", domain.ErrCorrupt)
		}
		if setup.ID == "" || setup.ID != saved.SetupID || saved.ID != revision || setup.Harness != harness {
			return fmt.Errorf("%w: pinned harness setup %s/%s does not match its revision", domain.ErrCorrupt, capsuleID, harness)
		}
		id := domain.CapsuleID(capsuleID)
		result[id] = append(result[id], domain.PinnedHarnessSetup{
			Harness: harness, SetupID: setup.ID, Name: setup.Name, Revision: revision, RevisedAt: saved.CreatedAt,
		})
	}
	return rows.Err()
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
	return mapOwnerViolation(err)
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
