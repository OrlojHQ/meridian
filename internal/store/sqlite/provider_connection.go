package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func (r *repository) ListProviderConnections(ctx context.Context) ([]domain.ProviderConnection, error) {
	rows, err := r.q.QueryContext(ctx, "SELECT metadata,owner_user_id FROM provider_connections ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.ProviderConnection{}
	for rows.Next() {
		var raw string
		var item domain.ProviderConnection
		var owner domain.UserID
		if err := rows.Scan(&raw, &owner); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		item.OwnerUserID = owner
		items = append(items, item)
	}
	return items, rows.Err()
}
func (r *repository) GetProviderConnection(ctx context.Context, id string) (domain.ProviderConnection, error) {
	var item domain.ProviderConnection
	var raw string
	var owner domain.UserID
	err := r.q.QueryRowContext(ctx, "SELECT metadata,key_id,nonce,ciphertext,owner_user_id FROM provider_connections WHERE id=?", id).Scan(&raw, &item.KeyID, &item.Nonce, &item.Ciphertext, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		return item, domain.ErrNotFound
	}
	if err != nil {
		return item, err
	}
	err = json.Unmarshal([]byte(raw), &item)
	item.OwnerUserID = owner
	return item, err
}
func (r *repository) PutProviderConnection(ctx context.Context, item domain.ProviderConnection) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	// An existing row is replaced only by its owner; for anyone else the
	// connection does not exist.
	result, err := r.q.ExecContext(ctx, "INSERT INTO provider_connections(id,metadata,key_id,nonce,ciphertext,owner_user_id) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET metadata=excluded.metadata,key_id=excluded.key_id,nonce=excluded.nonce,ciphertext=excluded.ciphertext WHERE owner_user_id=excluded.owner_user_id", item.ID, string(raw), item.KeyID, item.Nonce, item.Ciphertext, item.OwnerUserID)
	return ownedUpsertResult(result, err)
}
func (r *repository) GetProjectProviderConnection(ctx context.Context, id domain.ProjectID, harness string) (string, error) {
	var connection string
	err := r.q.QueryRowContext(ctx, "SELECT connection_id FROM project_provider_connections WHERE project_id=? AND harness=?", id, harness).Scan(&connection)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return connection, err
}
func (r *repository) GrantProviderConnection(ctx context.Context, id domain.ProjectID, harness, connection string) error {
	if connection == "" {
		_, err := r.q.ExecContext(ctx, "DELETE FROM project_provider_connections WHERE project_id=? AND harness=?", id, harness)
		return err
	}
	_, err := r.q.ExecContext(ctx, "INSERT INTO project_provider_connections(project_id,harness,connection_id) VALUES(?,?,?) ON CONFLICT(project_id,harness) DO UPDATE SET connection_id=excluded.connection_id", id, harness, connection)
	return mapOwnerViolation(err)
}
