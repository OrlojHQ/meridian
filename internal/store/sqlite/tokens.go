package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

const tokenColumns = `id, token_digest, user_id, name, read_only, created_at, last_used_at, expires_at, revoked_at`

func scanToken(row scanner) (domain.PersonalAccessToken, error) {
	var token domain.PersonalAccessToken
	var digest []byte
	var created, used, expires string
	var revoked sql.NullString
	if err := row.Scan(&token.ID, &digest, &token.UserID, &token.Name, &token.ReadOnly,
		&created, &used, &expires, &revoked); err != nil {
		return domain.PersonalAccessToken{}, mapError(err)
	}
	if len(digest) != len(token.TokenDigest) {
		return domain.PersonalAccessToken{}, domain.ErrCorrupt
	}
	copy(token.TokenDigest[:], digest)
	var err error
	if token.CreatedAt, err = parseTime(created); err != nil {
		return domain.PersonalAccessToken{}, err
	}
	if token.LastUsedAt, err = parseTime(used); err != nil {
		return domain.PersonalAccessToken{}, err
	}
	if token.ExpiresAt, err = parseTime(expires); err != nil {
		return domain.PersonalAccessToken{}, err
	}
	if revoked.Valid {
		if token.RevokedAt, err = parseTime(revoked.String); err != nil {
			return domain.PersonalAccessToken{}, err
		}
	}
	return token, nil
}

func (r *repository) GetPersonalAccessToken(ctx context.Context, id string) (domain.PersonalAccessToken, error) {
	return scanToken(r.q.QueryRowContext(ctx, "SELECT "+tokenColumns+" FROM personal_access_tokens WHERE id = ?", id))
}

func (r *repository) GetPersonalAccessTokenByDigest(ctx context.Context, digest [32]byte) (domain.PersonalAccessToken, error) {
	return scanToken(r.q.QueryRowContext(ctx,
		"SELECT "+tokenColumns+" FROM personal_access_tokens WHERE token_digest = ?", digest[:]))
}

func (r *repository) ListPersonalAccessTokens(ctx context.Context, user domain.UserID) ([]domain.PersonalAccessToken, error) {
	rows, err := r.q.QueryContext(ctx,
		"SELECT "+tokenColumns+" FROM personal_access_tokens WHERE user_id = ? ORDER BY created_at DESC, id LIMIT 200", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []domain.PersonalAccessToken
	for rows.Next() {
		token, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (r *repository) InsertPersonalAccessToken(ctx context.Context, token domain.PersonalAccessToken) error {
	if token.ID == "" || token.UserID == "" {
		return domain.ErrInvalid
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO personal_access_tokens(id, token_digest, user_id, name, read_only, created_at, last_used_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		token.ID, token.TokenDigest[:], token.UserID, token.Name, token.ReadOnly,
		formatTime(token.CreatedAt), formatTime(token.LastUsedAt), formatTime(token.ExpiresAt))
	return mapError(err)
}

func (r *repository) TouchPersonalAccessToken(ctx context.Context, id string, at time.Time) error {
	_, err := r.q.ExecContext(ctx,
		"UPDATE personal_access_tokens SET last_used_at = ? WHERE id = ? AND revoked_at IS NULL", formatTime(at), id)
	return mapError(err)
}

func (r *repository) RevokePersonalAccessToken(ctx context.Context, id string, at time.Time) error {
	result, err := r.q.ExecContext(ctx,
		"UPDATE personal_access_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", formatTime(at), id)
	if err != nil {
		return mapError(err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		var exists int
		if err := r.q.QueryRowContext(ctx, "SELECT COUNT(*) FROM personal_access_tokens WHERE id = ?", id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return domain.ErrNotFound
		}
	}
	return nil
}
