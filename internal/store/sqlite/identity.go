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

// RebindUserIdentity preserves the member id, role, disabled state, and objects.
// The previous identity makes concurrent recovery attempts fail closed.
func (r *repository) RebindUserIdentity(ctx context.Context, previous, next domain.User, at time.Time) error {
	if previous.ID == "" || previous.ID != next.ID || next.Issuer == "" || next.Subject == "" {
		return domain.ErrInvalid
	}
	result, err := r.q.ExecContext(ctx, `UPDATE users SET issuer=?,subject=?,display_name=?,email=?,updated_at=?
        WHERE id=? AND issuer=? AND subject=?`, next.Issuer, next.Subject, next.DisplayName, next.Email,
		formatTime(at), previous.ID, previous.Issuer, previous.Subject)
	if err != nil {
		return mapError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return domain.ErrConflict
	}
	return nil
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

func (r *repository) ListUsers(ctx context.Context) ([]domain.User, error) {
	rows, err := r.q.QueryContext(ctx, "SELECT "+userColumns+" FROM users ORDER BY created_at, id LIMIT 1000")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []domain.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (r *repository) SetUserDisabled(ctx context.Context, id domain.UserID, at time.Time) error {
	var disabled any
	if !at.IsZero() {
		disabled = formatTime(at)
	}
	result, err := r.q.ExecContext(ctx, "UPDATE users SET disabled_at = ? WHERE id = ?", disabled, id)
	if err != nil {
		return mapError(err)
	}
	return oneRow(result)
}

func (r *repository) SetUserRole(ctx context.Context, id domain.UserID, role domain.UserRole) error {
	if role != domain.UserMember && role != domain.UserAdmin {
		return domain.ErrInvalid
	}
	result, err := r.q.ExecContext(ctx, "UPDATE users SET role = ? WHERE id = ?", role, id)
	if err != nil {
		return mapError(err)
	}
	return oneRow(result)
}

func (r *repository) RevokeUserCredentials(ctx context.Context, id domain.UserID, at time.Time) (int, error) {
	revoked := 0
	for _, table := range []string{"user_sessions", "personal_access_tokens"} {
		result, err := r.q.ExecContext(ctx,
			"UPDATE "+table+" SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL", formatTime(at), id)
		if err != nil {
			return 0, mapError(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		revoked += int(affected)
	}
	return revoked, nil
}

func oneRow(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return domain.ErrNotFound
	}
	return nil
}

// transferredTables are the owned tables a departed-member transfer moves.
// provider_connections is absent: a personal login stays with its holder.
// user_profiles and user_profile_connections are absent too: a git identity
// stays with the person it names (ADR 0049). github_connections is absent
// too: a GitHub login stays with the person who connected it (ADR 0051).
var transferredTables = []string{
	"projects", "secrets", "harness_setups",
	"capsules", "timelines", "moments", "project_thread_intents", "setup_moment_cache",
	"project_preparation_policy", "coordination_grants", "runs", "threads", "deliveries",
	"capsule_preparations", "capsule_startup", "thread_messages", "thread_blocks",
	"thread_deliveries", "thread_links", "coordination_requests", "harness_setup_revisions",
}

func (r *repository) TransferOwnership(ctx context.Context, from, to domain.UserID) error {
	if from == "" || to == "" || from == to {
		return domain.ErrInvalid
	}
	if _, err := r.q.ExecContext(ctx,
		"INSERT INTO owner_transfers(id, from_user, to_user) VALUES (1, ?, ?)", from, to); err != nil {
		return mapError(err)
	}
	if _, err := r.q.ExecContext(ctx, `
		DELETE FROM project_provider_connections
		WHERE project_id IN (SELECT id FROM projects WHERE owner_user_id = ?)`, from); err != nil {
		return mapError(err)
	}
	// Moved setups arrive without their default flag, so the recipient's
	// workstation profile changes only by their own action (ADR 0049). The
	// source's profile row and its default connections do not move.
	if _, err := r.q.ExecContext(ctx, `
		UPDATE harness_setups
		SET metadata = json_set(metadata,
			'$.default', json('false'),
			'$.resourceVersion', json_extract(metadata, '$.resourceVersion') + 1)
		WHERE owner_user_id = ? AND json_extract(metadata, '$.default') = 1`, from); err != nil {
		return mapError(err)
	}
	for _, table := range transferredTables {
		if _, err := r.q.ExecContext(ctx,
			"UPDATE "+table+" SET owner_user_id = ? WHERE owner_user_id = ?", to, from); err != nil {
			return mapError(err)
		}
	}
	_, err := r.q.ExecContext(ctx, "DELETE FROM owner_transfers WHERE id = 1")
	return mapError(err)
}
