package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func (r *repository) ListProviderConnections(ctx context.Context) ([]domain.ProviderConnection, error) {
	rows, err := r.q.QueryContext(ctx, "SELECT metadata FROM provider_connections ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []domain.ProviderConnection{}
	for rows.Next() {
		var raw string
		var item domain.ProviderConnection
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
func (r *repository) GetProviderConnection(ctx context.Context, id string) (domain.ProviderConnection, error) {
	var item domain.ProviderConnection
	var raw string
	err := r.q.QueryRowContext(ctx, "SELECT metadata,key_id,nonce,ciphertext FROM provider_connections WHERE id=?", id).Scan(&raw, &item.KeyID, &item.Nonce, &item.Ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return item, domain.ErrNotFound
	}
	if err != nil {
		return item, err
	}
	err = json.Unmarshal([]byte(raw), &item)
	return item, err
}
func (r *repository) PutProviderConnection(ctx context.Context, item domain.ProviderConnection) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, "INSERT INTO provider_connections(id,metadata,key_id,nonce,ciphertext) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET metadata=excluded.metadata,key_id=excluded.key_id,nonce=excluded.nonce,ciphertext=excluded.ciphertext", item.ID, string(raw), item.KeyID, item.Nonce, item.Ciphertext)
	return err
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
	return err
}
