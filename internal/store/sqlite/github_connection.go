package sqlite

import (
	"context"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func (r *repository) GetGitHubConnection(
	ctx context.Context, owner domain.UserID,
) (domain.GitHubConnection, error) {
	return scanGitHubConnection(r.q.QueryRowContext(ctx, `
		SELECT id, owner_user_id, login, status, reason, scopes, source,
			envelope_version, kek_id, kek_version, nonce, ciphertext,
			created_at, updated_at, checked_at, resource_version
		FROM github_connections WHERE owner_user_id = ?`, owner))
}

func (r *repository) InsertGitHubConnection(ctx context.Context, item domain.GitHubConnection) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO github_connections (
			id, owner_user_id, login, status, reason, scopes, source,
			envelope_version, kek_id, kek_version, nonce, ciphertext,
			created_at, updated_at, checked_at, resource_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.OwnerUserID, item.Login, item.Status, item.Reason, item.Scopes, item.Source,
		item.EnvelopeVersion, item.KEKID, item.KEKVersion, item.Nonce, item.Ciphertext,
		formatTime(item.CreatedAt), formatTime(item.UpdatedAt), formatTime(item.CheckedAt),
		item.ResourceVersion,
	)
	return mapError(err)
}

func (r *repository) UpdateGitHubConnection(
	ctx context.Context, item domain.GitHubConnection, expected domain.ResourceVersion,
) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE github_connections SET
			login = ?, status = ?, reason = ?, scopes = ?, source = ?,
			envelope_version = ?, kek_id = ?, kek_version = ?, nonce = ?, ciphertext = ?,
			updated_at = ?, checked_at = ?, resource_version = ?
		WHERE owner_user_id = ? AND id = ? AND resource_version = ?`,
		item.Login, item.Status, item.Reason, item.Scopes, item.Source,
		item.EnvelopeVersion, item.KEKID, item.KEKVersion, item.Nonce, item.Ciphertext,
		formatTime(item.UpdatedAt), formatTime(item.CheckedAt), item.ResourceVersion,
		item.OwnerUserID, item.ID, expected,
	)
	if err != nil {
		return mapError(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (r *repository) DeleteGitHubConnection(ctx context.Context, owner domain.UserID) error {
	result, err := r.q.ExecContext(ctx, `DELETE FROM github_connections WHERE owner_user_id = ?`, owner)
	if err != nil {
		return mapError(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return domain.ErrNotFound
	}
	return nil
}

func scanGitHubConnection(row scanner) (domain.GitHubConnection, error) {
	var item domain.GitHubConnection
	var created, updated, checked string
	err := row.Scan(
		&item.ID, &item.OwnerUserID, &item.Login, &item.Status, &item.Reason, &item.Scopes, &item.Source,
		&item.EnvelopeVersion, &item.KEKID, &item.KEKVersion, &item.Nonce, &item.Ciphertext,
		&created, &updated, &checked, &item.ResourceVersion,
	)
	if err != nil {
		return domain.GitHubConnection{}, mapError(err)
	}
	item.CreatedAt, err = parseTime(created)
	if err == nil {
		item.UpdatedAt, err = parseTime(updated)
	}
	if err == nil {
		item.CheckedAt, err = parseTime(checked)
	}
	return item, err
}
