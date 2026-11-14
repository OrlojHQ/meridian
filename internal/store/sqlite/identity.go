package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func (r *repository) GetInstallation(ctx context.Context) (domain.Installation, error) {
	var installation domain.Installation
	var claimed sql.NullString
	err := r.q.QueryRowContext(ctx,
		"SELECT mode, oidc_issuer, claimed_at FROM installation WHERE id = 1",
	).Scan(&installation.Mode, &installation.OIDCIssuer, &claimed)
	if err != nil {
		return domain.Installation{}, mapError(err)
	}
	if claimed.Valid {
		if installation.ClaimedAt, err = parseTime(claimed.String); err != nil {
			return domain.Installation{}, err
		}
	}
	return installation, nil
}

func (r *repository) ClaimInstallation(ctx context.Context, issuer string, owner domain.User, at time.Time) error {
	if issuer == "" || owner.Issuer != issuer || owner.Subject == "" {
		return domain.ErrInvalid
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE users SET issuer = ?, subject = ?, display_name = ?, email = ?, updated_at = ?
		WHERE id = ? AND issuer = '' AND subject = ''`,
		owner.Issuer, owner.Subject, owner.DisplayName, owner.Email, formatTime(at), owner.ID)
	if err != nil {
		return mapError(err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return domain.ErrConflict
	}
	result, err = r.q.ExecContext(ctx, `
		UPDATE installation SET mode = 'team', oidc_issuer = ?, claimed_at = ?
		WHERE id = 1 AND mode = 'single_user'`, issuer, formatTime(at))
	if err != nil {
		return mapError(err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return domain.ErrConflict
	}
	return nil
}

const userColumns = `id, issuer, subject, display_name, email, role, disabled_at, created_at`

func scanUser(row scanner) (domain.User, error) {
	var user domain.User
	var disabled sql.NullString
	var created string
	if err := row.Scan(
		&user.ID, &user.Issuer, &user.Subject, &user.DisplayName, &user.Email,
		&user.Role, &disabled, &created,
	); err != nil {
		return domain.User{}, mapError(err)
	}
	var err error
	if disabled.Valid && disabled.String != "" {
		if user.DisabledAt, err = parseTime(disabled.String); err != nil {
			return domain.User{}, err
		}
	}
	user.CreatedAt, err = parseTime(created)
	return user, err
}

func (r *repository) GetUser(ctx context.Context, id domain.UserID) (domain.User, error) {
	return scanUser(r.q.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", id))
}

func (r *repository) GetUserByIdentity(ctx context.Context, issuer, subject string) (domain.User, error) {
	if issuer == "" || subject == "" {
		return domain.User{}, domain.ErrNotFound
	}
	return scanUser(r.q.QueryRowContext(ctx,
		"SELECT "+userColumns+" FROM users WHERE issuer = ? AND subject = ?", issuer, subject))
}

func (r *repository) InsertUser(ctx context.Context, user domain.User) error {
	if user.ID == "" || user.Issuer == "" || user.Subject == "" {
		return domain.ErrInvalid
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO users(id, issuer, subject, display_name, email, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID, user.Issuer, user.Subject, user.DisplayName, user.Email, user.Role,
		formatTime(user.CreatedAt), formatTime(user.CreatedAt))
	return mapError(err)
}

func (r *repository) UpdateUserProfile(ctx context.Context, id domain.UserID, displayName, email string) error {
	_, err := r.q.ExecContext(ctx,
		"UPDATE users SET display_name = ?, email = ? WHERE id = ?", displayName, email, id)
	return mapError(err)
}

const sessionColumns = `id, token_digest, user_id, created_at, last_used_at, expires_at, revoked_at`

func scanSession(row scanner) (domain.UserSession, error) {
	var session domain.UserSession
	var digest []byte
	var created, used, expires string
	var revoked sql.NullString
	if err := row.Scan(&session.ID, &digest, &session.UserID, &created, &used, &expires, &revoked); err != nil {
		return domain.UserSession{}, mapError(err)
	}
	if len(digest) != len(session.TokenDigest) {
		return domain.UserSession{}, domain.ErrCorrupt
	}
	copy(session.TokenDigest[:], digest)
	var err error
	if session.CreatedAt, err = parseTime(created); err != nil {
		return domain.UserSession{}, err
	}
	if session.LastUsedAt, err = parseTime(used); err != nil {
		return domain.UserSession{}, err
	}
	if session.ExpiresAt, err = parseTime(expires); err != nil {
		return domain.UserSession{}, err
	}
	if revoked.Valid {
		if session.RevokedAt, err = parseTime(revoked.String); err != nil {
			return domain.UserSession{}, err
		}
	}
	return session, nil
}

func (r *repository) GetUserSession(ctx context.Context, id string) (domain.UserSession, error) {
	return scanSession(r.q.QueryRowContext(ctx, "SELECT "+sessionColumns+" FROM user_sessions WHERE id = ?", id))
}

func (r *repository) GetUserSessionByDigest(ctx context.Context, digest [32]byte) (domain.UserSession, error) {
	return scanSession(r.q.QueryRowContext(ctx,
		"SELECT "+sessionColumns+" FROM user_sessions WHERE token_digest = ?", digest[:]))
}

func (r *repository) ListUserSessions(ctx context.Context, user domain.UserID) ([]domain.UserSession, error) {
	rows, err := r.q.QueryContext(ctx,
		"SELECT "+sessionColumns+" FROM user_sessions WHERE user_id = ? ORDER BY created_at DESC, id LIMIT 200", user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []domain.UserSession
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (r *repository) InsertUserSession(ctx context.Context, session domain.UserSession) error {
	if session.ID == "" || session.UserID == "" {
		return domain.ErrInvalid
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO user_sessions(id, token_digest, user_id, created_at, last_used_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		session.ID, session.TokenDigest[:], session.UserID, formatTime(session.CreatedAt),
		formatTime(session.LastUsedAt), formatTime(session.ExpiresAt))
	return mapError(err)
}

func (r *repository) TouchUserSession(ctx context.Context, id string, at time.Time) error {
	_, err := r.q.ExecContext(ctx,
		"UPDATE user_sessions SET last_used_at = ? WHERE id = ? AND revoked_at IS NULL", formatTime(at), id)
	return mapError(err)
}

func (r *repository) RevokeUserSession(ctx context.Context, id string, at time.Time) error {
	result, err := r.q.ExecContext(ctx,
		"UPDATE user_sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", formatTime(at), id)
	if err != nil {
		return mapError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var exists int
		if err := r.q.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_sessions WHERE id = ?", id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return domain.ErrNotFound
		}
	}
	return nil
}
