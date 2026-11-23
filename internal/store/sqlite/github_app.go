package sqlite

import (
	"context"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func (r *repository) GetGitHubApp(ctx context.Context) (domain.GitHubApp, error) {
	return scanGitHubApp(r.q.QueryRowContext(ctx, `
		SELECT id, app_id, slug, name, client_id, html_url, owner_login, owner_type,
			webhook_active, created_by, envelope_version, kek_id, kek_version,
			nonce, ciphertext, created_at, updated_at, resource_version
		FROM github_app WHERE id = ?`, domain.GitHubAppRowID))
}

func (r *repository) PutGitHubApp(ctx context.Context, item domain.GitHubApp) error {
	if item.ID != domain.GitHubAppRowID || item.AppID <= 0 || item.Slug == "" ||
		item.ResourceVersion <= 0 || len(item.Nonce) == 0 || len(item.Ciphertext) == 0 {
		return domain.ErrInvalid
	}
	webhook := 0
	if item.WebhookActive {
		webhook = 1
	}
	if item.ResourceVersion == 1 {
		_, err := r.q.ExecContext(ctx, `
			INSERT INTO github_app (
				id, app_id, slug, name, client_id, html_url, owner_login, owner_type,
				webhook_active, created_by, envelope_version, kek_id, kek_version,
				nonce, ciphertext, created_at, updated_at, resource_version
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ID, item.AppID, item.Slug, item.Name, item.ClientID, item.HTMLURL,
			item.OwnerLogin, item.OwnerType, webhook, item.CreatedBy,
			item.EnvelopeVersion, item.KEKID, item.KEKVersion, item.Nonce, item.Ciphertext,
			formatTime(item.CreatedAt), formatTime(item.UpdatedAt), item.ResourceVersion,
		)
		return mapError(err)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE github_app SET
			app_id = ?, slug = ?, name = ?, client_id = ?, html_url = ?,
			owner_login = ?, owner_type = ?, webhook_active = ?, created_by = ?,
			envelope_version = ?, kek_id = ?, kek_version = ?, nonce = ?, ciphertext = ?,
			updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		item.AppID, item.Slug, item.Name, item.ClientID, item.HTMLURL,
		item.OwnerLogin, item.OwnerType, webhook, item.CreatedBy,
		item.EnvelopeVersion, item.KEKID, item.KEKVersion, item.Nonce, item.Ciphertext,
		formatTime(item.UpdatedAt), item.ResourceVersion,
		item.ID, item.ResourceVersion-1,
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

func (r *repository) DeleteGitHubApp(ctx context.Context, expected domain.ResourceVersion) error {
	result, err := r.q.ExecContext(ctx, `DELETE FROM github_app WHERE id = ? AND resource_version = ?`,
		domain.GitHubAppRowID, expected)
	if err != nil {
		return mapError(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return domain.ErrConflict
	}
	if _, err := r.q.ExecContext(ctx, `DELETE FROM github_app_installations`); err != nil {
		return err
	}
	return nil
}

func (r *repository) ListGitHubAppInstallations(ctx context.Context) ([]domain.GitHubAppInstallation, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT installation_id, account_login, account_type, repository_selection, suspended, updated_at
		FROM github_app_installations ORDER BY account_login, installation_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.GitHubAppInstallation
	for rows.Next() {
		var item domain.GitHubAppInstallation
		var suspended int
		var updated string
		if err := rows.Scan(&item.ID, &item.AccountLogin, &item.AccountType, &item.RepositorySelection, &suspended, &updated); err != nil {
			return nil, err
		}
		item.Suspended = suspended != 0
		item.UpdatedAt, err = parseTime(updated)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *repository) ReplaceGitHubAppInstallations(ctx context.Context, items []domain.GitHubAppInstallation) error {
	if _, err := r.q.ExecContext(ctx, `DELETE FROM github_app_installations`); err != nil {
		return err
	}
	for _, item := range items {
		if item.ID <= 0 || item.AccountLogin == "" {
			return domain.ErrInvalid
		}
		selection := item.RepositorySelection
		if selection != "all" {
			selection = "selected"
		}
		accountType := item.AccountType
		if accountType != "Organization" {
			accountType = "User"
		}
		suspended := 0
		if item.Suspended {
			suspended = 1
		}
		if _, err := r.q.ExecContext(ctx, `
			INSERT INTO github_app_installations (
				installation_id, account_login, account_type, repository_selection, suspended, updated_at
			) VALUES (?, ?, ?, ?, ?, ?)`,
			item.ID, item.AccountLogin, accountType, selection, suspended, formatTime(item.UpdatedAt),
		); err != nil {
			return mapError(err)
		}
	}
	return nil
}

func scanGitHubApp(row scanner) (domain.GitHubApp, error) {
	var item domain.GitHubApp
	var webhook int
	var created, updated string
	err := row.Scan(
		&item.ID, &item.AppID, &item.Slug, &item.Name, &item.ClientID, &item.HTMLURL,
		&item.OwnerLogin, &item.OwnerType, &webhook, &item.CreatedBy,
		&item.EnvelopeVersion, &item.KEKID, &item.KEKVersion, &item.Nonce, &item.Ciphertext,
		&created, &updated, &item.ResourceVersion,
	)
	if err != nil {
		return domain.GitHubApp{}, mapError(err)
	}
	item.WebhookActive = webhook != 0
	item.CreatedAt, err = parseTime(created)
	if err == nil {
		item.UpdatedAt, err = parseTime(updated)
	}
	if err != nil {
		return domain.GitHubApp{}, err
	}
	return item, nil
}
