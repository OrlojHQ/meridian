package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func (r *repository) UpdateProjectState(
	ctx context.Context,
	project domain.Project,
	expected domain.ResourceVersion,
) error {
	if !project.State.Valid() {
		return domain.ErrInvalid
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE projects SET state = ?, failure = ?, updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		project.State, project.Failure, formatTime(project.UpdatedAt), project.ResourceVersion,
		project.ID, expected,
	)
	if err != nil {
		return mapError(err)
	}
	return projectWriteResult(ctx, r.q, result, project.ID)
}

func (r *repository) ListProjectsInDeletion(ctx context.Context) ([]domain.Project, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+projectColumns+`
		FROM projects WHERE state IN ('deleting', 'failed')
		ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []domain.Project
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, rows.Err()
}

func (r *repository) ArtifactReferenced(ctx context.Context, digest string) (bool, error) {
	var count int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM moments
		WHERE archive_sha256 = ? OR manifest_sha256 = ?`, digest, digest).Scan(&count)
	return count > 0, err
}

// PurgeProjectContents removes the Project's transcripts, Moment rows, environment,
// grants, and exclusive named-secret ciphertext. Capsule rows stay so provider
// cleanup remains auditable. The Project becomes a deleted tombstone.
func (r *repository) PurgeProjectContents(
	ctx context.Context,
	project domain.Project,
	now time.Time,
) (domain.Project, []string, error) {
	if project.State == domain.ProjectDeleted {
		return domain.Project{}, nil, domain.ErrIllegalTransition
	}
	if _, err := r.q.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return domain.Project{}, nil, err
	}
	// Content triggers allow removal only while the Project is deleting or
	// deleted. An active Project whose Capsules were already deleted, and a
	// failed deletion that is now clear to finish, both move here first.
	if project.State != domain.ProjectDeleting {
		previous := project.ResourceVersion
		project.State = domain.ProjectDeleting
		project.Failure = ""
		project.UpdatedAt = now.UTC()
		project.ResourceVersion++
		if err := r.UpdateProjectState(ctx, project, previous); err != nil {
			return domain.Project{}, nil, err
		}
	}
	digests, err := r.projectArtifactDigests(ctx, project.ID)
	if err != nil {
		return domain.Project{}, nil, err
	}
	capsuleIDs := `
		SELECT id FROM capsules WHERE project_id = ?`
	threadIDs := `
		SELECT t.id FROM threads t
		JOIN capsules c ON c.id = t.capsule_id
		WHERE c.project_id = ?`
	for _, statement := range []string{
		`DELETE FROM thread_blocks WHERE thread_id IN (` + threadIDs + `)`,
		`DELETE FROM thread_deliveries WHERE thread_id IN (` + threadIDs + `)`,
		`DELETE FROM thread_messages WHERE thread_id IN (` + threadIDs + `)`,
		`DELETE FROM threads WHERE capsule_id IN (` + capsuleIDs + `)`,
		`DELETE FROM setup_moment_cache WHERE project_id = ?`,
	} {
		if _, err := r.q.ExecContext(ctx, statement, project.ID); err != nil {
			return domain.Project{}, nil, mapError(err)
		}
	}
	if _, err := r.q.ExecContext(ctx, `
		UPDATE capsules SET origin_moment_id = NULL
		WHERE project_id = ? AND origin_moment_id IS NOT NULL`, project.ID); err != nil {
		return domain.Project{}, nil, mapError(err)
	}
	if _, err := r.q.ExecContext(ctx, `
		DELETE FROM moments WHERE project_id = ?`, project.ID); err != nil {
		return domain.Project{}, nil, mapError(err)
	}
	for _, statement := range []string{
		`DELETE FROM project_preparation_policy WHERE project_id = ?`,
		`DELETE FROM project_provider_connections WHERE project_id = ?`,
		`DELETE FROM project_harness_setups WHERE project_id = ?`,
	} {
		if _, err := r.q.ExecContext(ctx, statement, project.ID); err != nil {
			return domain.Project{}, nil, mapError(err)
		}
	}
	if err := r.destroyExclusiveSecrets(ctx, project); err != nil {
		return domain.Project{}, nil, err
	}
	previous := project.ResourceVersion
	project.State = domain.ProjectDeleted
	project.Failure = ""
	project.Setup = []string{}
	project.GitSecretName = ""
	project.HarnessSecretNames = []string{}
	project.GitPushSecretName = ""
	project.GitHubAPISecretName = ""
	project.UpdatedAt = now.UTC()
	project.ResourceVersion++
	result, err := r.q.ExecContext(ctx, `
		UPDATE projects SET
			state = ?, failure = '', setup_argv = '[]',
			git_secret_name = '', harness_secret_names = '[]',
			git_push_secret_name = '', github_api_secret_name = '',
			updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		project.State, formatTime(project.UpdatedAt), project.ResourceVersion,
		project.ID, previous,
	)
	if err != nil {
		return domain.Project{}, nil, mapError(err)
	}
	if err := projectWriteResult(ctx, r.q, result, project.ID); err != nil {
		return domain.Project{}, nil, err
	}
	return project, digests, nil
}

func (r *repository) projectArtifactDigests(ctx context.Context, id domain.ProjectID) ([]string, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT archive_sha256, manifest_sha256 FROM moments WHERE project_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]struct{}{}
	var digests []string
	for rows.Next() {
		var archive, manifest string
		if err := rows.Scan(&archive, &manifest); err != nil {
			return nil, err
		}
		for _, digest := range []string{archive, manifest} {
			if digest == "" {
				continue
			}
			if _, ok := seen[digest]; ok {
				continue
			}
			seen[digest] = struct{}{}
			digests = append(digests, digest)
		}
	}
	return digests, rows.Err()
}

func (r *repository) destroyExclusiveSecrets(ctx context.Context, project domain.Project) error {
	names := projectSecretNames(project)
	if len(names) == 0 {
		return nil
	}
	// Secret names are per owner, so only the owner's other Projects can
	// still name one of these secrets. The owner is read from the row, not
	// trusted from the caller's copy.
	var owner domain.UserID
	if err := r.q.QueryRowContext(ctx, "SELECT owner_user_id FROM projects WHERE id = ?", project.ID).Scan(&owner); err != nil {
		return mapError(err)
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+projectColumns+`
		FROM projects WHERE id <> ? AND state <> 'deleted' AND owner_user_id = ?`,
		project.ID, owner)
	if err != nil {
		return err
	}
	defer rows.Close()
	shared := map[string]struct{}{}
	for rows.Next() {
		other, err := scanProject(rows)
		if err != nil {
			return err
		}
		for _, name := range projectSecretNames(other) {
			shared[name] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range names {
		if _, ok := shared[name]; ok {
			continue
		}
		if _, err := r.q.ExecContext(ctx, `DELETE FROM secrets WHERE owner_user_id = ? AND name = ?`,
			owner, name); err != nil {
			return mapError(err)
		}
	}
	return nil
}

func projectSecretNames(project domain.Project) []string {
	raw := append([]string{
		project.GitSecretName, project.GitPushSecretName, project.GitHubAPISecretName,
	}, project.HarnessSecretNames...)
	seen := map[string]struct{}{}
	names := make([]string, 0, len(raw))
	for _, name := range raw {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func projectWriteResult(ctx context.Context, query queryer, result sql.Result, id domain.ProjectID) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 0 {
		return nil
	}
	var exists int
	if err := query.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE id = ?", id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return domain.ErrNotFound
	}
	return domain.ErrConflict
}
