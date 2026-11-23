package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/OrlojHQ/meridian/internal/domain"
)

// GetUserProfile returns owner's workstation profile (ADR 0049). A user with
// no row has an empty profile at resource version zero.
func (r *repository) GetUserProfile(ctx context.Context, owner domain.UserID) (domain.UserProfile, error) {
	profile := domain.UserProfile{OwnerUserID: owner, DefaultConnections: map[string]string{}, CapsuleGitHubAccess: true}
	var updated string
	var access int
	err := r.q.QueryRowContext(ctx,
		"SELECT git_name, git_email, capsule_github_access, resource_version, updated_at FROM user_profiles WHERE owner_user_id = ?", owner,
	).Scan(&profile.GitName, &profile.GitEmail, &access, &profile.ResourceVersion, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return profile, nil
	}
	if err != nil {
		return domain.UserProfile{}, err
	}
	if profile.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.UserProfile{}, err
	}
	profile.CapsuleGitHubAccess = access != 0
	rows, err := r.q.QueryContext(ctx,
		"SELECT harness, connection_id FROM user_profile_connections WHERE owner_user_id = ? ORDER BY harness", owner)
	if err != nil {
		return domain.UserProfile{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var harness, connection string
		if err := rows.Scan(&harness, &connection); err != nil {
			return domain.UserProfile{}, err
		}
		profile.DefaultConnections[harness] = connection
	}
	return profile, rows.Err()
}

// PutUserProfile writes owner's profile under optimistic concurrency and
// replaces its default connections in the same transaction.
func (r *repository) PutUserProfile(ctx context.Context, profile domain.UserProfile, expected domain.ResourceVersion) error {
	if profile.OwnerUserID == "" || profile.ResourceVersion != expected+1 {
		return domain.ErrInvalid
	}
	if !domain.ValidGitIdentity(profile.GitName, profile.GitEmail) {
		return domain.ErrInvalid
	}
	updated := formatTime(profile.UpdatedAt)
	access := 0
	if profile.CapsuleGitHubAccess {
		access = 1
	}
	if expected == 0 {
		if _, err := r.q.ExecContext(ctx, `INSERT INTO user_profiles(owner_user_id, git_name, git_email, capsule_github_access, resource_version, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, profile.OwnerUserID, profile.GitName, profile.GitEmail, access, profile.ResourceVersion, updated); err != nil {
			return mapError(err)
		}
	} else {
		result, err := r.q.ExecContext(ctx, `UPDATE user_profiles SET git_name = ?, git_email = ?, capsule_github_access = ?, resource_version = ?, updated_at = ?
			WHERE owner_user_id = ? AND resource_version = ?`,
			profile.GitName, profile.GitEmail, access, profile.ResourceVersion, updated, profile.OwnerUserID, expected)
		if err != nil {
			return mapError(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return domain.ErrConflict
		}
	}
	if _, err := r.q.ExecContext(ctx, "DELETE FROM user_profile_connections WHERE owner_user_id = ?", profile.OwnerUserID); err != nil {
		return err
	}
	for harness, connection := range profile.DefaultConnections {
		if !domain.ValidHarnessName(harness) || connection == "" {
			return domain.ErrInvalid
		}
		if _, err := r.q.ExecContext(ctx,
			"INSERT INTO user_profile_connections(owner_user_id, harness, connection_id) VALUES (?, ?, ?)",
			profile.OwnerUserID, harness, connection); err != nil {
			return mapError(err)
		}
	}
	return nil
}

func (r *repository) DeleteUserProfile(ctx context.Context, owner domain.UserID) error {
	if _, err := r.q.ExecContext(ctx, "DELETE FROM user_profile_connections WHERE owner_user_id = ?", owner); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, "DELETE FROM user_profiles WHERE owner_user_id = ?", owner)
	return err
}

func (r *repository) GetHarnessSetupReport(ctx context.Context, revision string) (domain.HarnessSetupReport, error) {
	report := domain.HarnessSetupReport{RevisionID: revision}
	err := r.q.QueryRowContext(ctx,
		"SELECT key_id, nonce, ciphertext FROM harness_setup_reports WHERE revision_id = ?", revision,
	).Scan(&report.KeyID, &report.Nonce, &report.Ciphertext)
	if err != nil {
		return domain.HarnessSetupReport{}, mapError(err)
	}
	return report, nil
}

func (r *repository) InsertHarnessSetupReport(ctx context.Context, report domain.HarnessSetupReport) error {
	if report.RevisionID == "" || report.KeyID == "" || len(report.Nonce) == 0 || len(report.Ciphertext) == 0 {
		return domain.ErrInvalid
	}
	_, err := r.q.ExecContext(ctx,
		"INSERT INTO harness_setup_reports(revision_id, key_id, nonce, ciphertext) VALUES (?, ?, ?, ?)",
		report.RevisionID, report.KeyID, report.Nonce, report.Ciphertext)
	return mapError(err)
}

// ReplaceHarnessSetupReportEnvelope re-seals a report, for re-wrap and
// departed-member transfer.
func (r *repository) ReplaceHarnessSetupReportEnvelope(ctx context.Context, report domain.HarnessSetupReport) error {
	result, err := r.q.ExecContext(ctx,
		"UPDATE harness_setup_reports SET key_id = ?, nonce = ?, ciphertext = ? WHERE revision_id = ?",
		report.KeyID, report.Nonce, report.Ciphertext, report.RevisionID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *repository) GetCapsuleProfilePin(ctx context.Context, id domain.CapsuleID) (domain.CapsuleProfilePin, bool, error) {
	pin := domain.CapsuleProfilePin{CapsuleID: id}
	var pinned string
	err := r.q.QueryRowContext(ctx,
		"SELECT git_name, git_email, pinned_at FROM capsule_profile_pins WHERE capsule_id = ?", id,
	).Scan(&pin.GitName, &pin.GitEmail, &pinned)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CapsuleProfilePin{}, false, nil
	}
	if err != nil {
		return domain.CapsuleProfilePin{}, false, err
	}
	if pin.PinnedAt, err = parseTime(pinned); err != nil {
		return domain.CapsuleProfilePin{}, false, err
	}
	return pin, true, nil
}

func (r *repository) PutCapsuleProfilePin(ctx context.Context, pin domain.CapsuleProfilePin) error {
	if pin.CapsuleID == "" || pin.PinnedAt.IsZero() || !domain.ValidGitIdentity(pin.GitName, pin.GitEmail) {
		return domain.ErrInvalid
	}
	_, err := r.q.ExecContext(ctx, `INSERT INTO capsule_profile_pins(capsule_id, git_name, git_email, pinned_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(capsule_id) DO UPDATE SET git_name = excluded.git_name, git_email = excluded.git_email, pinned_at = excluded.pinned_at`,
		pin.CapsuleID, pin.GitName, pin.GitEmail, formatTime(pin.PinnedAt))
	return mapError(err)
}

func (r *repository) DeleteCapsuleProfilePin(ctx context.Context, id domain.CapsuleID) error {
	_, err := r.q.ExecContext(ctx, "DELETE FROM capsule_profile_pins WHERE capsule_id = ?", id)
	return err
}

func (r *repository) UnpinHarnessSetups(ctx context.Context, id domain.CapsuleID) error {
	_, err := r.q.ExecContext(ctx, "DELETE FROM capsule_harness_setups WHERE capsule_id = ?", id)
	return err
}
