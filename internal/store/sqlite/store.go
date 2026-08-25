// Package sqlite implements Meridian's transactional persistence ports.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/transcripts"
	"github.com/OrlojHQ/meridian/migrations"
	_ "modernc.org/sqlite"
)

type Store struct {
	db    *sql.DB
	path  string
	write sync.Mutex
}

// CurrentSchemaVersion is the newest migration this binary understands.
const CurrentSchemaVersion = 11

func Open(ctx context.Context, dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("%w: data directory is required", domain.ErrInvalid)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	info, err := os.Lstat(dataDir)
	if err != nil {
		return nil, fmt.Errorf("inspect data directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("data path must be a real directory")
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure data directory: %w", err)
	}

	path := filepath.Join(dataDir, "meridian.db")
	if databaseInfo, err := os.Lstat(path); err == nil {
		if !databaseInfo.Mode().IsRegular() || databaseInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("database path must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect database path: %w", err)
	}
	dsnURL := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := dsnURL.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	dsnURL.RawQuery = query.Encode()
	dsn := dsnURL.String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	store := &Store{db: db, path: path}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure database: %w", err)
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	return version, err
}

func (s *Store) View(ctx context.Context, fn func(ports.Reader) error) error {
	return fn(&repository{q: s.db})
}

func (s *Store) Transact(ctx context.Context, fn func(ports.Transaction) error) error {
	s.write.Lock()
	defer s.write.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	repository := &repository{q: tx}
	if err := fn(repository); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	var installedVersion int
	if err := s.db.QueryRowContext(
		ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&installedVersion); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if installedVersion > CurrentSchemaVersion {
		return fmt.Errorf(
			"%w: database schema version %d is newer than supported version %d",
			domain.ErrUnsupported, installedVersion, CurrentSchemaVersion,
		)
	}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return fmt.Errorf("invalid migration name %q", entry.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("invalid migration version %q: %w", prefix, err)
		}
		var appliedName string
		err = s.db.QueryRowContext(
			ctx,
			"SELECT name FROM schema_migrations WHERE version = ?",
			version,
		).Scan(&appliedName)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if appliedName != entry.Name() {
				return fmt.Errorf(
					"%w: migration version %d is recorded as %q, expected %q",
					domain.ErrCorrupt, version, appliedName, entry.Name(),
				)
			}
			continue
		}
		sqlBytes, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.ExecContext(
			ctx,
			"INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)",
			version, entry.Name(), formatTime(time.Now()),
		); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type repository struct {
	q queryer
}

func (r *repository) InsertProject(ctx context.Context, project domain.Project) error {
	setup, err := json.Marshal(project.Setup)
	if err != nil {
		return fmt.Errorf("encode setup arguments: %w", err)
	}
	harnessSecrets, err := json.Marshal(project.HarnessSecretNames)
	if err != nil {
		return fmt.Errorf("encode harness secret names: %w", err)
	}
	images := project.HarnessImages
	if images == nil {
		images = []domain.HarnessImage{}
	}
	harnessImages, err := json.Marshal(images)
	if err != nil {
		return fmt.Errorf("encode harness images: %w", err)
	}
	_, err = r.q.ExecContext(ctx, `
		INSERT INTO projects(
			id, name, repository_url, setup_argv, image_reference, harness_images,
			git_secret_name, harness_secret_names,
			git_push_secret_name, github_api_secret_name, commit_author_name,
			commit_author_email, default_base_branch,
			created_at, updated_at, resource_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		project.ID, project.Name, project.RepositoryURL, string(setup), project.ImageReference,
		string(harnessImages),
		project.GitSecretName, string(harnessSecrets), project.GitPushSecretName,
		project.GitHubAPISecretName, project.CommitAuthorName, project.CommitAuthorEmail,
		project.DefaultBaseBranch,
		formatTime(project.CreatedAt), formatTime(project.UpdatedAt), project.ResourceVersion,
	)
	return mapError(err)
}

func (r *repository) UpdateProject(
	ctx context.Context,
	project domain.Project,
	expected domain.ResourceVersion,
) error {
	images := project.HarnessImages
	if images == nil {
		images = []domain.HarnessImage{}
	}
	harnessImages, err := json.Marshal(images)
	if err != nil {
		return fmt.Errorf("encode harness images: %w", err)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE projects SET harness_images = ?, updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		string(harnessImages), formatTime(project.UpdatedAt), project.ResourceVersion,
		project.ID, expected,
	)
	if err != nil {
		return mapError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var exists int
		if err := r.q.QueryRowContext(
			ctx, "SELECT COUNT(*) FROM projects WHERE id = ?", project.ID,
		).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return domain.ErrNotFound
		}
		return domain.ErrConflict
	}
	return nil
}

func (r *repository) GetProject(ctx context.Context, id domain.ProjectID) (domain.Project, error) {
	return scanProject(r.q.QueryRowContext(ctx, `
		SELECT id, name, repository_url, setup_argv, image_reference, harness_images,
			git_secret_name, harness_secret_names,
			git_push_secret_name, github_api_secret_name, commit_author_name,
			commit_author_email, default_base_branch,
			created_at, updated_at, resource_version
		FROM projects WHERE id = ?`, id,
	))
}

func (r *repository) ListProjects(
	ctx context.Context,
	page ports.Page,
) ([]domain.Project, bool, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, name, repository_url, setup_argv, image_reference, harness_images,
			git_secret_name, harness_secret_names,
			git_push_secret_name, github_api_secret_name, commit_author_name,
			commit_author_email, default_base_branch,
			created_at, updated_at, resource_version
		FROM projects ORDER BY created_at, id LIMIT ? OFFSET ?`,
		page.Limit+1, page.Offset,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	projects := make([]domain.Project, 0, page.Limit)
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, false, err
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(projects) > page.Limit
	if more {
		projects = projects[:page.Limit]
	}
	return projects, more, nil
}

func scanProject(row scanner) (domain.Project, error) {
	var project domain.Project
	var setup, harnessImages, harnessSecrets, created, updated string
	err := row.Scan(
		&project.ID, &project.Name, &project.RepositoryURL, &setup, &project.ImageReference,
		&harnessImages,
		&project.GitSecretName, &harnessSecrets,
		&project.GitPushSecretName, &project.GitHubAPISecretName,
		&project.CommitAuthorName, &project.CommitAuthorEmail, &project.DefaultBaseBranch,
		&created, &updated, &project.ResourceVersion,
	)
	if err != nil {
		return domain.Project{}, mapError(err)
	}
	if err := json.Unmarshal([]byte(setup), &project.Setup); err != nil {
		return domain.Project{}, fmt.Errorf("decode setup arguments: %w", err)
	}
	if err := json.Unmarshal([]byte(harnessImages), &project.HarnessImages); err != nil {
		return domain.Project{}, fmt.Errorf("decode harness images: %w", err)
	}
	if err := json.Unmarshal([]byte(harnessSecrets), &project.HarnessSecretNames); err != nil {
		return domain.Project{}, fmt.Errorf("decode harness secret names: %w", err)
	}
	project.CreatedAt, err = parseTime(created)
	if err != nil {
		return domain.Project{}, err
	}
	project.UpdatedAt, err = parseTime(updated)
	return project, err
}

func (r *repository) InsertSecret(ctx context.Context, secret domain.Secret) error {
	if err := secret.Validate(); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO secrets(
			id, name, purpose, envelope_version, kek_id, kek_version, nonce,
			ciphertext, created_at, updated_at, resource_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		secret.ID, secret.Name, secret.Purpose, secret.EnvelopeVersion, secret.KEKID,
		secret.KEKVersion, secret.Nonce, secret.Ciphertext, formatTime(secret.CreatedAt),
		formatTime(secret.UpdatedAt), secret.ResourceVersion,
	)
	return mapError(err)
}

func (r *repository) UpdateSecret(
	ctx context.Context, secret domain.Secret, expected domain.ResourceVersion,
) error {
	if err := secret.Validate(); err != nil {
		return err
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE secrets SET purpose = ?, envelope_version = ?, kek_id = ?, kek_version = ?,
			nonce = ?, ciphertext = ?, updated_at = ?, resource_version = ?
		WHERE name = ? AND resource_version = ?`,
		secret.Purpose, secret.EnvelopeVersion, secret.KEKID, secret.KEKVersion,
		secret.Nonce, secret.Ciphertext, formatTime(secret.UpdatedAt), secret.ResourceVersion,
		secret.Name, expected,
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

func (r *repository) DeleteSecret(
	ctx context.Context, name string, expected domain.ResourceVersion,
) error {
	result, err := r.q.ExecContext(ctx,
		"DELETE FROM secrets WHERE name = ? AND resource_version = ?", name, expected)
	if err != nil {
		return mapError(err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (r *repository) GetSecret(ctx context.Context, name string) (domain.Secret, error) {
	return scanSecret(r.q.QueryRowContext(ctx, `
		SELECT id, name, purpose, envelope_version, kek_id, kek_version, nonce,
			ciphertext, created_at, updated_at, resource_version
		FROM secrets WHERE name = ?`, name))
}

func (r *repository) ListSecrets(
	ctx context.Context, page ports.Page,
) ([]domain.Secret, bool, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, name, purpose, envelope_version, kek_id, kek_version, nonce,
			ciphertext, created_at, updated_at, resource_version
		FROM secrets ORDER BY name LIMIT ? OFFSET ?`, page.Limit+1, page.Offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]domain.Secret, 0, page.Limit)
	for rows.Next() {
		item, err := scanSecret(rows)
		if err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(items) > page.Limit
	if more {
		items = items[:page.Limit]
	}
	return items, more, nil
}

func scanSecret(row scanner) (domain.Secret, error) {
	var secret domain.Secret
	var created, updated string
	err := row.Scan(
		&secret.ID, &secret.Name, &secret.Purpose, &secret.EnvelopeVersion,
		&secret.KEKID, &secret.KEKVersion, &secret.Nonce, &secret.Ciphertext,
		&created, &updated, &secret.ResourceVersion,
	)
	if err != nil {
		return domain.Secret{}, mapError(err)
	}
	secret.CreatedAt, err = parseTime(created)
	if err == nil {
		secret.UpdatedAt, err = parseTime(updated)
	}
	return secret, err
}

func (r *repository) InsertCapsule(ctx context.Context, capsule domain.Capsule) error {
	implicitRoot := capsule.TimelineID == ""
	if implicitRoot {
		capsule.TimelineID = domain.TimelineID("root-" + string(capsule.ID))
		capsule.RestoreComplete = true
	}
	if capsule.LastActivityAt.IsZero() {
		capsule.LastActivityAt = capsule.CreatedAt
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO capsules(
			id, project_id, timeline_id, name, launcher_harness, state, desired_state, provider_resource_id,
			origin_moment_id, restore_complete, maintenance, failure, image_reference,
			last_activity_at, created_at, updated_at, resource_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		capsule.ID, capsule.ProjectID, capsule.TimelineID, capsule.Name, capsule.LauncherHarness,
		capsule.State, capsule.DesiredState,
		capsule.ProviderResourceID, nullableString(string(capsule.OriginMomentID)), capsule.RestoreComplete,
		capsule.Maintenance, capsule.Failure, capsule.ImageReference, formatTime(capsule.LastActivityAt),
		formatTime(capsule.CreatedAt), formatTime(capsule.UpdatedAt), capsule.ResourceVersion,
	)
	if err != nil {
		return mapError(err)
	}
	if implicitRoot {
		return r.InsertTimeline(ctx, domain.Timeline{
			ID: capsule.TimelineID, ProjectID: capsule.ProjectID, CapsuleID: capsule.ID,
			Reason: domain.TimelineRoot, CreatedAt: capsule.CreatedAt,
		})
	}
	return nil
}

func (r *repository) UpdateCapsule(
	ctx context.Context,
	capsule domain.Capsule,
	expected domain.ResourceVersion,
) error {
	result, err := r.q.ExecContext(ctx, `
		UPDATE capsules SET
			name = ?, state = ?, desired_state = ?, provider_resource_id = ?,
			origin_moment_id = ?, restore_complete = ?, maintenance = ?,
			failure = ?, last_activity_at = ?, updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		capsule.Name, capsule.State, capsule.DesiredState, capsule.ProviderResourceID,
		nullableString(string(capsule.OriginMomentID)), capsule.RestoreComplete, capsule.Maintenance,
		capsule.Failure, formatTime(capsule.LastActivityAt), formatTime(capsule.UpdatedAt), capsule.ResourceVersion,
		capsule.ID, expected,
	)
	if err != nil {
		return mapError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var exists int
		if err := r.q.QueryRowContext(
			ctx, "SELECT COUNT(*) FROM capsules WHERE id = ?", capsule.ID,
		).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return domain.ErrNotFound
		}
		return domain.ErrConflict
	}
	return nil
}

func (r *repository) TouchCapsuleActivity(
	ctx context.Context, id domain.CapsuleID, at time.Time,
) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE capsules SET last_activity_at = ?
		WHERE id = ? AND last_activity_at < ?`,
		formatTime(at), id, formatTime(at),
	)
	return mapError(err)
}

func (r *repository) GetCapsule(ctx context.Context, id domain.CapsuleID) (domain.Capsule, error) {
	return scanCapsule(r.q.QueryRowContext(ctx, `
		SELECT id, project_id, timeline_id, name, launcher_harness, state, desired_state, provider_resource_id,
			origin_moment_id, restore_complete, maintenance, failure, image_reference,
			last_activity_at, created_at, updated_at, resource_version
		FROM capsules WHERE id = ?`, id,
	))
}

func (r *repository) ListCapsules(
	ctx context.Context,
	projectID domain.ProjectID,
	page ports.Page,
) ([]domain.Capsule, bool, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, project_id, timeline_id, name, launcher_harness, state, desired_state, provider_resource_id,
			origin_moment_id, restore_complete, maintenance, failure, image_reference,
			last_activity_at, created_at, updated_at, resource_version
		FROM capsules WHERE project_id = ?
		ORDER BY created_at, id LIMIT ? OFFSET ?`,
		projectID, page.Limit+1, page.Offset,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	capsules := make([]domain.Capsule, 0, page.Limit)
	for rows.Next() {
		capsule, err := scanCapsule(rows)
		if err != nil {
			return nil, false, err
		}
		capsules = append(capsules, capsule)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(capsules) > page.Limit
	if more {
		capsules = capsules[:page.Limit]
	}
	return capsules, more, nil
}

func (r *repository) ListIdleCapsules(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]domain.Capsule, error) {
	if before.IsZero() || limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("%w: invalid idle Capsule scan", domain.ErrInvalid)
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, project_id, timeline_id, name, launcher_harness, state, desired_state, provider_resource_id,
			origin_moment_id, restore_complete, maintenance, failure, image_reference,
			last_activity_at, created_at, updated_at, resource_version
		FROM capsules
		WHERE state = ? AND desired_state = ? AND maintenance = ''
			AND last_activity_at <= ?
		ORDER BY last_activity_at, id LIMIT ?`,
		domain.CapsuleReady, domain.IntentReady, formatTime(before), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	capsules := make([]domain.Capsule, 0, limit)
	for rows.Next() {
		capsule, err := scanCapsule(rows)
		if err != nil {
			return nil, err
		}
		capsules = append(capsules, capsule)
	}
	return capsules, rows.Err()
}

func (r *repository) ListRecoverableCapsules(ctx context.Context) ([]domain.Capsule, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, project_id, timeline_id, name, launcher_harness, state, desired_state, provider_resource_id,
			origin_moment_id, restore_complete, maintenance, failure, image_reference,
			last_activity_at, created_at, updated_at, resource_version
		FROM capsules WHERE state NOT IN (?, ?)
		ORDER BY created_at, id`, domain.CapsuleSealed, domain.CapsuleDeleted,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var capsules []domain.Capsule
	for rows.Next() {
		capsule, err := scanCapsule(rows)
		if err != nil {
			return nil, err
		}
		capsules = append(capsules, capsule)
	}
	return capsules, rows.Err()
}

type scanner interface {
	Scan(...any) error
}

func scanCapsule(row scanner) (domain.Capsule, error) {
	var capsule domain.Capsule
	var activity, created, updated string
	var origin sql.NullString
	err := row.Scan(
		&capsule.ID, &capsule.ProjectID, &capsule.TimelineID, &capsule.Name, &capsule.LauncherHarness,
		&capsule.State,
		&capsule.DesiredState, &capsule.ProviderResourceID, &origin, &capsule.RestoreComplete,
		&capsule.Maintenance, &capsule.Failure, &capsule.ImageReference,
		&activity, &created, &updated, &capsule.ResourceVersion,
	)
	if err != nil {
		return domain.Capsule{}, mapError(err)
	}
	capsule.OriginMomentID = domain.MomentID(origin.String)
	capsule.LastActivityAt, err = parseTime(activity)
	if err != nil {
		return domain.Capsule{}, err
	}
	capsule.CreatedAt, err = parseTime(created)
	if err != nil {
		return domain.Capsule{}, err
	}
	capsule.UpdatedAt, err = parseTime(updated)
	return capsule, err
}

func (r *repository) InsertTimeline(ctx context.Context, timeline domain.Timeline) error {
	if err := timeline.Validate(); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO timelines(id, project_id, capsule_id, forked_from_moment_id, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		timeline.ID, timeline.ProjectID, timeline.CapsuleID,
		nullableString(string(timeline.ForkedFromMomentID)), timeline.Reason,
		formatTime(timeline.CreatedAt),
	)
	return mapError(err)
}

func (r *repository) GetTimeline(ctx context.Context, id domain.TimelineID) (domain.Timeline, error) {
	return scanTimeline(r.q.QueryRowContext(ctx, `
		SELECT id, project_id, capsule_id, forked_from_moment_id, reason, created_at
		FROM timelines WHERE id = ?`, id))
}

func scanTimeline(row scanner) (domain.Timeline, error) {
	var timeline domain.Timeline
	var fork sql.NullString
	var created string
	if err := row.Scan(
		&timeline.ID, &timeline.ProjectID, &timeline.CapsuleID, &fork,
		&timeline.Reason, &created,
	); err != nil {
		return domain.Timeline{}, mapError(err)
	}
	timeline.ForkedFromMomentID = domain.MomentID(fork.String)
	var err error
	timeline.CreatedAt, err = parseTime(created)
	return timeline, err
}

func (r *repository) ListTimelineAncestry(
	ctx context.Context,
	id domain.TimelineID,
) ([]domain.Timeline, error) {
	rows, err := r.q.QueryContext(ctx, `
		WITH RECURSIVE ancestry(id, project_id, capsule_id, forked_from_moment_id, reason, created_at, depth) AS (
			SELECT id, project_id, capsule_id, forked_from_moment_id, reason, created_at, 0
			FROM timelines WHERE id = ?
			UNION ALL
			SELECT parent.id, parent.project_id, parent.capsule_id, parent.forked_from_moment_id,
			       parent.reason, parent.created_at, ancestry.depth + 1
			FROM ancestry
			JOIN moments fork ON fork.id = ancestry.forked_from_moment_id
			JOIN timelines parent ON parent.id = fork.timeline_id
			WHERE ancestry.depth < 10000
		)
		SELECT id, project_id, capsule_id, forked_from_moment_id, reason, created_at
		FROM ancestry ORDER BY depth`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var timelines []domain.Timeline
	for rows.Next() {
		timeline, err := scanTimeline(rows)
		if err != nil {
			return nil, err
		}
		timelines = append(timelines, timeline)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(timelines) == 0 {
		return nil, domain.ErrNotFound
	}
	return timelines, nil
}

func (r *repository) InsertMoment(ctx context.Context, moment domain.Moment) error {
	if err := moment.Validate(); err != nil {
		return err
	}
	if moment.Kind == "" {
		moment.Kind = domain.MomentTimeline
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO moments(
			id, project_id, capsule_id, timeline_id, parent_moment_id, name,
			archive_sha256, archive_size, manifest_sha256, image_digest,
			project_setup_hash, git_branch, git_head, git_dirty_summary, created_at, final, kind
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		moment.ID, moment.ProjectID, moment.CapsuleID, moment.TimelineID,
		nullableString(string(moment.ParentMomentID)), moment.Name, moment.ArchiveSHA256,
		moment.ArchiveSize, moment.ManifestSHA256, moment.ImageDigest, moment.ProjectSetupHash,
		moment.GitBranch, moment.GitHEAD, moment.GitDirtySummary,
		formatTime(moment.CreatedAt), moment.Final, moment.Kind,
	)
	return mapError(err)
}

func (r *repository) GetMoment(ctx context.Context, id domain.MomentID) (domain.Moment, error) {
	return scanMoment(r.q.QueryRowContext(ctx, momentSelect+" WHERE id = ?", id))
}

const momentSelect = `
	SELECT id, project_id, capsule_id, timeline_id, parent_moment_id, name,
		archive_sha256, archive_size, manifest_sha256, image_digest,
		project_setup_hash, git_branch, git_head, git_dirty_summary, created_at, final, kind
	FROM moments`

func scanMoment(row scanner) (domain.Moment, error) {
	var moment domain.Moment
	var parent sql.NullString
	var created string
	if err := row.Scan(
		&moment.ID, &moment.ProjectID, &moment.CapsuleID, &moment.TimelineID, &parent,
		&moment.Name, &moment.ArchiveSHA256, &moment.ArchiveSize, &moment.ManifestSHA256,
		&moment.ImageDigest, &moment.ProjectSetupHash, &moment.GitBranch, &moment.GitHEAD,
		&moment.GitDirtySummary, &created, &moment.Final, &moment.Kind,
	); err != nil {
		return domain.Moment{}, mapError(err)
	}
	moment.ParentMomentID = domain.MomentID(parent.String)
	var err error
	moment.CreatedAt, err = parseTime(created)
	return moment, err
}

func (r *repository) ListMoments(
	ctx context.Context,
	timelineID domain.TimelineID,
	page ports.Page,
) ([]domain.Moment, bool, error) {
	rows, err := r.q.QueryContext(ctx, momentSelect+`
		WHERE timeline_id = ? AND kind = 'timeline'
		ORDER BY created_at, id LIMIT ? OFFSET ?`,
		timelineID, page.Limit+1, page.Offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var moments []domain.Moment
	for rows.Next() {
		moment, err := scanMoment(rows)
		if err != nil {
			return nil, false, err
		}
		moments = append(moments, moment)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(moments) > page.Limit
	if more {
		moments = moments[:page.Limit]
	}
	return moments, more, nil
}

func (r *repository) LatestMoment(
	ctx context.Context,
	timelineID domain.TimelineID,
) (domain.Moment, error) {
	return scanMoment(r.q.QueryRowContext(ctx, momentSelect+`
		WHERE timeline_id = ? AND kind = 'timeline'
		ORDER BY created_at DESC, id DESC LIMIT 1`, timelineID))
}

func (r *repository) GetSetupMomentCache(
	ctx context.Context,
	projectID domain.ProjectID,
	configHash string,
) (domain.SetupMomentCache, error) {
	var cache domain.SetupMomentCache
	var created string
	err := r.q.QueryRowContext(ctx, `
		SELECT project_id, config_hash, moment_id, created_at
		FROM setup_moment_cache
		WHERE project_id = ? AND config_hash = ?`,
		projectID, configHash,
	).Scan(&cache.ProjectID, &cache.ConfigHash, &cache.MomentID, &created)
	if err != nil {
		return domain.SetupMomentCache{}, mapError(err)
	}
	cache.CreatedAt, err = parseTime(created)
	return cache, err
}

func (r *repository) PutSetupMomentCache(
	ctx context.Context,
	cache domain.SetupMomentCache,
) error {
	if err := cache.Validate(); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO setup_moment_cache(project_id, config_hash, moment_id, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			config_hash = excluded.config_hash,
			moment_id = excluded.moment_id,
			created_at = excluded.created_at`,
		cache.ProjectID, cache.ConfigHash, cache.MomentID, formatTime(cache.CreatedAt))
	return mapError(err)
}

func (r *repository) HasActiveRun(ctx context.Context, capsuleID domain.CapsuleID) (bool, error) {
	var count int
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM runs
		WHERE capsule_id = ? AND state IN ('Queued', 'Starting', 'Running', 'Cancelling')`,
		capsuleID).Scan(&count)
	return count != 0, err
}

func (r *repository) InsertRun(ctx context.Context, run domain.Run) error {
	var exitStatus any
	if run.HasExitStatus {
		exitStatus = run.ExitStatus
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO runs(
			id, capsule_id, harness, state, exit_status, failure, event_cursor, created_at,
			started_at, finished_at, updated_at, resource_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.CapsuleID, run.Harness, run.State, exitStatus, run.Failure,
		run.EventCursor, formatTime(run.CreatedAt), nullableTime(run.StartedAt), nullableTime(run.FinishedAt),
		formatTime(run.UpdatedAt), run.ResourceVersion,
	)
	return mapError(err)
}

func (r *repository) UpdateRun(
	ctx context.Context,
	run domain.Run,
	expected domain.ResourceVersion,
) error {
	var exitStatus any
	if run.HasExitStatus {
		exitStatus = run.ExitStatus
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE runs SET state = ?, exit_status = ?, failure = ?, event_cursor = ?, started_at = ?,
			finished_at = ?, updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		run.State, exitStatus, run.Failure, run.EventCursor, nullableTime(run.StartedAt),
		nullableTime(run.FinishedAt), formatTime(run.UpdatedAt), run.ResourceVersion,
		run.ID, expected,
	)
	if err != nil {
		return mapError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var exists int
		if err := r.q.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE id = ?", run.ID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return domain.ErrNotFound
		}
		return domain.ErrConflict
	}
	return nil
}

func (r *repository) GetRun(ctx context.Context, id domain.RunID) (domain.Run, error) {
	return scanRun(r.q.QueryRowContext(ctx, `
		SELECT id, capsule_id, harness, state, exit_status, failure, event_cursor, created_at,
			started_at, finished_at, updated_at, resource_version
		FROM runs WHERE id = ?`, id))
}

func (r *repository) ListRuns(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	page ports.Page,
) ([]domain.Run, bool, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, capsule_id, harness, state, exit_status, failure, event_cursor, created_at,
			started_at, finished_at, updated_at, resource_version
		FROM runs WHERE capsule_id = ? ORDER BY created_at, id LIMIT ? OFFSET ?`,
		capsuleID, page.Limit+1, page.Offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	runs := make([]domain.Run, 0, page.Limit)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, false, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(runs) > page.Limit
	if more {
		runs = runs[:page.Limit]
	}
	return runs, more, nil
}

func (r *repository) ListRecoverableRuns(ctx context.Context) ([]domain.Run, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, capsule_id, harness, state, exit_status, failure, event_cursor, created_at,
			started_at, finished_at, updated_at, resource_version
		FROM runs WHERE state IN (?, ?, ?, ?) ORDER BY created_at, id`,
		domain.RunQueued, domain.RunStarting, domain.RunRunning, domain.RunCancelling)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []domain.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func scanRun(row scanner) (domain.Run, error) {
	var run domain.Run
	var exit sql.NullInt64
	var created, updated string
	var started, finished sql.NullString
	err := row.Scan(
		&run.ID, &run.CapsuleID, &run.Harness, &run.State, &exit, &run.Failure, &run.EventCursor,
		&created, &started, &finished, &updated, &run.ResourceVersion,
	)
	if err != nil {
		return domain.Run{}, mapError(err)
	}
	run.ExitStatus, run.HasExitStatus = int(exit.Int64), exit.Valid
	run.CreatedAt, err = parseTime(created)
	if err != nil {
		return domain.Run{}, err
	}
	run.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return domain.Run{}, err
	}
	if started.Valid {
		run.StartedAt, err = parseTime(started.String)
		if err != nil {
			return domain.Run{}, err
		}
	}
	if finished.Valid {
		run.FinishedAt, err = parseTime(finished.String)
	}
	return run, err
}

func (r *repository) AppendEvent(ctx context.Context, event domain.Event) error {
	if len(event.Data) > 64<<10 {
		return fmt.Errorf("%w: event metadata exceeds limit", domain.ErrInvalid)
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO events(
			event_id, aggregate_type, aggregate_id, event_type,
			timestamp, resource_version, data
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.AggregateType, event.AggregateID, event.Type,
		formatTime(event.Timestamp), event.ResourceVersion, event.Data,
	)
	return mapError(err)
}

func (r *repository) ListRunEvents(
	ctx context.Context,
	runID domain.RunID,
	after int64,
	limit int,
) ([]domain.Event, bool, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT event_id, sequence, aggregate_type, aggregate_id, event_type,
			timestamp, resource_version, data
		FROM events
		WHERE aggregate_type = 'run' AND aggregate_id = ? AND sequence > ?
		ORDER BY sequence LIMIT ?`, runID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var events []domain.Event
	for rows.Next() {
		var event domain.Event
		var timestamp string
		if err := rows.Scan(
			&event.ID, &event.Sequence, &event.AggregateType, &event.AggregateID,
			&event.Type, &timestamp, &event.ResourceVersion, &event.Data,
		); err != nil {
			return nil, false, err
		}
		event.Timestamp, err = parseTime(timestamp)
		if err != nil {
			return nil, false, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(events) > limit
	if more {
		events = events[:limit]
	}
	return events, more, nil
}

func (r *repository) ListEvents(
	ctx context.Context,
	aggregateType, aggregateID string,
) ([]domain.Event, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT event_id, sequence, aggregate_type, aggregate_id, event_type,
			timestamp, resource_version, data
		FROM events WHERE aggregate_type = ? AND aggregate_id = ?
		ORDER BY sequence`, aggregateType, aggregateID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []domain.Event
	for rows.Next() {
		var event domain.Event
		var timestamp string
		if err := rows.Scan(
			&event.ID, &event.Sequence, &event.AggregateType, &event.AggregateID,
			&event.Type, &timestamp, &event.ResourceVersion, &event.Data,
		); err != nil {
			return nil, err
		}
		event.Timestamp, err = parseTime(timestamp)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (r *repository) PutIdempotency(ctx context.Context, record ports.IdempotencyRecord) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO idempotency_outcomes(scope, key, outcome, created_at)
		VALUES (?, ?, ?, ?)`,
		record.Scope, record.Key, record.Outcome, formatTime(record.CreatedAt),
	)
	return mapError(err)
}

func (r *repository) GetIdempotency(
	ctx context.Context,
	scope, key string,
) (ports.IdempotencyRecord, error) {
	var record ports.IdempotencyRecord
	var created string
	err := r.q.QueryRowContext(ctx, `
		SELECT scope, key, outcome, created_at
		FROM idempotency_outcomes WHERE scope = ? AND key = ?`,
		scope, key,
	).Scan(&record.Scope, &record.Key, &record.Outcome, &created)
	if err != nil {
		return ports.IdempotencyRecord{}, mapError(err)
	}
	record.CreatedAt, err = parseTime(created)
	return record, err
}

const threadSelect = `
	SELECT id, capsule_id, state, current_run_id, adapter_id,
		wrapped_dek, kek_id, kek_version, envelope_version, message_count,
		encrypted_bytes, created_at, updated_at, deleted_at, resource_version
	FROM threads`

func (r *repository) InsertThread(ctx context.Context, thread domain.Thread) error {
	if err := thread.Validate(); err != nil {
		return err
	}
	if thread.State != domain.ThreadActive || thread.MessageCount != 0 || thread.EncryptedBytes != 0 {
		return fmt.Errorf("%w: new Thread must be empty and active", domain.ErrInvalid)
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO threads(
			id, capsule_id, state, current_run_id, adapter_id,
			wrapped_dek, kek_id, kek_version, envelope_version, message_count,
			encrypted_bytes, created_at, updated_at, deleted_at, resource_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		thread.ID, thread.CapsuleID, thread.State, nullableString(string(thread.CurrentRunID)),
		thread.AdapterID, nullableBytes(thread.WrappedDEK),
		thread.KEKID, thread.KEKVersion, thread.EnvelopeVersion, thread.MessageCount,
		thread.EncryptedBytes, formatTime(thread.CreatedAt), formatTime(thread.UpdatedAt),
		nullableTime(thread.DeletedAt), thread.ResourceVersion,
	)
	return mapError(err)
}

func (r *repository) GetThread(ctx context.Context, id domain.ThreadID) (domain.Thread, error) {
	return scanThread(r.q.QueryRowContext(ctx, threadSelect+" WHERE id = ?", id))
}

func (r *repository) GetActiveThread(
	ctx context.Context,
	capsuleID domain.CapsuleID,
) (domain.Thread, error) {
	return scanThread(r.q.QueryRowContext(
		ctx, threadSelect+" WHERE capsule_id = ? AND state = 'active'", capsuleID,
	))
}

func (r *repository) ListThreads(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	page ports.Page,
) ([]domain.Thread, bool, error) {
	if page.Limit <= 0 || page.Limit > 1000 {
		page.Limit = 100
	}
	if page.Offset < 0 {
		return nil, false, fmt.Errorf("%w: invalid page", domain.ErrInvalid)
	}
	rows, err := r.q.QueryContext(ctx, threadSelect+`
		WHERE capsule_id = ? AND state <> 'deleted'
		ORDER BY created_at, id LIMIT ? OFFSET ?`,
		capsuleID, page.Limit+1, page.Offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	threads := make([]domain.Thread, 0, page.Limit)
	for rows.Next() {
		thread, err := scanThread(rows)
		if err != nil {
			return nil, false, err
		}
		threads = append(threads, thread)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(threads) > page.Limit
	if more {
		threads = threads[:page.Limit]
	}
	return threads, more, nil
}

func (r *repository) ListRecoverableThreads(ctx context.Context) ([]domain.Thread, error) {
	rows, err := r.q.QueryContext(ctx, threadSelect+`
		WHERE state IN ('active', 'paused') AND current_run_id IS NOT NULL
		ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var threads []domain.Thread
	for rows.Next() {
		thread, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		threads = append(threads, thread)
	}
	return threads, rows.Err()
}

func scanThread(row scanner) (domain.Thread, error) {
	var thread domain.Thread
	var currentRun, deleted sql.NullString
	var wrappedDEK []byte
	var created, updated string
	if err := row.Scan(
		&thread.ID, &thread.CapsuleID, &thread.State, &currentRun, &thread.AdapterID,
		&wrappedDEK, &thread.KEKID, &thread.KEKVersion,
		&thread.EnvelopeVersion, &thread.MessageCount, &thread.EncryptedBytes,
		&created, &updated, &deleted, &thread.ResourceVersion,
	); err != nil {
		return domain.Thread{}, mapError(err)
	}
	thread.CurrentRunID = domain.RunID(currentRun.String)
	thread.WrappedDEK = wrappedDEK
	var err error
	thread.CreatedAt, err = parseTime(created)
	if err != nil {
		return domain.Thread{}, domain.ErrTranscriptCorrupt
	}
	thread.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return domain.Thread{}, domain.ErrTranscriptCorrupt
	}
	if deleted.Valid {
		thread.DeletedAt, err = parseTime(deleted.String)
		if err != nil {
			return domain.Thread{}, domain.ErrTranscriptCorrupt
		}
	}
	return thread, nil
}

func (r *repository) UpdateThread(
	ctx context.Context,
	thread domain.Thread,
	expected domain.ResourceVersion,
) error {
	if err := thread.Validate(); err != nil {
		return err
	}
	if thread.State == domain.ThreadDeleted {
		return fmt.Errorf("%w: use transcript crypto-shred", domain.ErrInvalid)
	}
	if thread.ResourceVersion != expected+1 {
		return fmt.Errorf("%w: invalid Thread resource version", domain.ErrInvalid)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE threads SET state = ?, current_run_id = ?,
			updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ? AND wrapped_dek = ? AND kek_id = ?
			AND kek_version = ?`,
		thread.State, nullableString(string(thread.CurrentRunID)),
		formatTime(thread.UpdatedAt), thread.ResourceVersion, thread.ID, expected,
		thread.WrappedDEK, thread.KEKID, thread.KEKVersion,
	)
	if err != nil {
		return mapError(err)
	}
	return ensureThreadAffected(ctx, r.q, result, thread.ID)
}

func (r *repository) AppendThreadMessage(ctx context.Context, message domain.ThreadMessage) error {
	if err := message.Validate(); err != nil {
		return err
	}
	if len(message.Blocks) > transcripts.MaxBlocksPerMessage {
		return fmt.Errorf("%w: ThreadMessage block count exceeds limit", domain.ErrResourceExhausted)
	}
	total := 0
	for _, block := range message.Blocks {
		if err := transcripts.ValidateEnvelope(block.Ciphertext); err != nil {
			return err
		}
		if len(block.Ciphertext) > transcripts.MaxFrameBytes ||
			total > transcripts.MaxMessagePlaintextBytes-len(block.Ciphertext) {
			return fmt.Errorf("%w: ThreadMessage exceeds limit", domain.ErrResourceExhausted)
		}
		total += len(block.Ciphertext)
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO thread_messages(id, thread_id, sequence, role, kind, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		message.ID, message.ThreadID, message.Sequence, message.Role, message.Kind,
		formatTime(message.CreatedAt))
	if err != nil {
		return mapError(err)
	}
	for _, block := range message.Blocks {
		_, err = r.q.ExecContext(ctx, `
			INSERT INTO thread_blocks(
				id, thread_id, message_id, message_sequence, sequence, kind,
				envelope_version, ciphertext, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			block.ID, block.ThreadID, block.MessageID, block.MessageSequence,
			block.Sequence, block.Kind, block.EnvelopeVersion, block.Ciphertext,
			formatTime(block.CreatedAt))
		if err != nil {
			return mapError(err)
		}
	}
	return nil
}

func (r *repository) ListThreadMessages(
	ctx context.Context,
	threadID domain.ThreadID,
	after int64,
	limit int,
) ([]domain.ThreadMessage, bool, error) {
	if after < 0 {
		return nil, false, fmt.Errorf("%w: invalid ThreadMessage cursor", domain.ErrInvalid)
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, thread_id, sequence, role, kind, created_at
		FROM thread_messages
		WHERE thread_id = ? AND sequence > ?
		ORDER BY sequence LIMIT ?`, threadID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	messages := make([]domain.ThreadMessage, 0, limit)
	for rows.Next() {
		var message domain.ThreadMessage
		var created string
		if err := rows.Scan(
			&message.ID, &message.ThreadID, &message.Sequence, &message.Role,
			&message.Kind, &created,
		); err != nil {
			return nil, false, err
		}
		message.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, false, domain.ErrTranscriptCorrupt
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(messages) > limit
	if more {
		messages = messages[:limit]
	}
	for index := range messages {
		blocks, err := r.listThreadBlocks(ctx, messages[index])
		if err != nil {
			return nil, false, err
		}
		messages[index].Blocks = blocks
	}
	return messages, more, nil
}

func (r *repository) GetThreadDelivery(
	ctx context.Context,
	messageID domain.ThreadMessageID,
) (domain.ThreadDelivery, error) {
	var delivery domain.ThreadDelivery
	var created string
	var delivered sql.NullString
	err := r.q.QueryRowContext(ctx, `
		SELECT thread_id, message_id, run_id, controller_id, created_at, delivered_at
		FROM thread_deliveries WHERE message_id = ?`, messageID,
	).Scan(
		&delivery.ThreadID, &delivery.MessageID, &delivery.RunID,
		&delivery.ControllerID, &created, &delivered,
	)
	if err != nil {
		return domain.ThreadDelivery{}, mapError(err)
	}
	delivery.CreatedAt, err = parseTime(created)
	if err != nil {
		return domain.ThreadDelivery{}, domain.ErrTranscriptCorrupt
	}
	if delivered.Valid {
		delivery.DeliveredAt, err = parseTime(delivered.String)
		if err != nil {
			return domain.ThreadDelivery{}, domain.ErrTranscriptCorrupt
		}
	}
	return delivery, nil
}

func (r *repository) FindUndeliveredUserMessage(
	ctx context.Context,
	threadID domain.ThreadID,
) (domain.ThreadMessage, error) {
	var message domain.ThreadMessage
	var created string
	err := r.q.QueryRowContext(ctx, `
		SELECT m.id, m.thread_id, m.sequence, m.role, m.kind, m.created_at
		FROM thread_messages m
		LEFT JOIN thread_deliveries d ON d.message_id = m.id
		WHERE m.thread_id = ? AND m.role = 'user' AND d.message_id IS NULL
		ORDER BY m.sequence LIMIT 1`, threadID,
	).Scan(
		&message.ID, &message.ThreadID, &message.Sequence, &message.Role,
		&message.Kind, &created,
	)
	if err != nil {
		return domain.ThreadMessage{}, mapError(err)
	}
	message.CreatedAt, err = parseTime(created)
	if err != nil {
		return domain.ThreadMessage{}, domain.ErrTranscriptCorrupt
	}
	return message, nil
}

func (r *repository) InsertThreadDelivery(
	ctx context.Context,
	delivery domain.ThreadDelivery,
) error {
	if delivery.ThreadID == "" || delivery.MessageID == "" || delivery.RunID == "" ||
		delivery.ControllerID == "" || len(delivery.ControllerID) > adapterproto.MaxIDBytes ||
		delivery.CreatedAt.IsZero() || !delivery.DeliveredAt.IsZero() {
		return fmt.Errorf("%w: invalid Thread delivery", domain.ErrInvalid)
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO thread_deliveries(
			message_id, thread_id, run_id, controller_id, created_at, delivered_at
		) VALUES (?, ?, ?, ?, ?, NULL)`,
		delivery.MessageID, delivery.ThreadID, delivery.RunID, delivery.ControllerID,
		formatTime(delivery.CreatedAt),
	)
	return mapError(err)
}

func (r *repository) AcknowledgeThreadDelivery(
	ctx context.Context,
	messageID domain.ThreadMessageID,
	deliveredAt time.Time,
) error {
	if messageID == "" || deliveredAt.IsZero() {
		return fmt.Errorf("%w: invalid Thread delivery acknowledgement", domain.ErrInvalid)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE thread_deliveries SET delivered_at = ?
		WHERE message_id = ? AND delivered_at IS NULL`,
		formatTime(deliveredAt), messageID,
	)
	if err != nil {
		return mapError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 0 {
		return err
	}
	var exists int
	if err := r.q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM thread_deliveries WHERE message_id = ?", messageID,
	).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return domain.ErrNotFound
	}
	return nil
}

const projectThreadIntentSelect = `
	SELECT id, project_id, capsule_id, thread_id, run_id, message_id, block_id,
		capsule_name, requested_name, harness, idempotency_key, state,
		failure_code, failure_message, wrapped_dek, kek_id, kek_version,
		envelope_version, ciphertext, created_at, updated_at, resource_version
	FROM project_thread_intents`

func (r *repository) InsertProjectThreadIntent(
	ctx context.Context,
	intent domain.ProjectThreadIntent,
) error {
	if err := intent.Validate(); err != nil {
		return err
	}
	if intent.State != domain.ProjectThreadProvisioning || len(intent.PendingMessage.Blocks) != 1 {
		return fmt.Errorf("%w: new Project Thread intent must be provisioning", domain.ErrInvalid)
	}
	block := intent.PendingMessage.Blocks[0]
	if err := transcripts.ValidateEnvelope(block.Ciphertext); err != nil {
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO project_thread_intents(
			id, project_id, capsule_id, thread_id, run_id, message_id, block_id,
			capsule_name, requested_name, harness, idempotency_key, state,
			failure_code, failure_message, wrapped_dek, kek_id, kek_version,
			envelope_version, ciphertext, created_at, updated_at, resource_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		intent.ID, intent.ProjectID, intent.CapsuleID, intent.ThreadID, intent.RunID,
		intent.MessageID, block.ID, intent.CapsuleName, intent.RequestedName,
		intent.Harness, intent.IdempotencyKey, intent.State, intent.FailureCode,
		intent.FailureMessage, intent.WrappedDEK, intent.KEKID, intent.KEKVersion,
		intent.EnvelopeVersion, block.Ciphertext, formatTime(intent.CreatedAt),
		formatTime(intent.UpdatedAt), intent.ResourceVersion,
	)
	return mapError(err)
}

func (r *repository) GetProjectThreadIntent(
	ctx context.Context,
	id domain.ProjectThreadIntentID,
) (domain.ProjectThreadIntent, error) {
	return scanProjectThreadIntent(r.q.QueryRowContext(
		ctx, projectThreadIntentSelect+" WHERE id = ?", id,
	))
}

func (r *repository) GetProjectThreadIntentByKey(
	ctx context.Context,
	projectID domain.ProjectID,
	key string,
) (domain.ProjectThreadIntent, error) {
	return scanProjectThreadIntent(r.q.QueryRowContext(
		ctx, projectThreadIntentSelect+" WHERE project_id = ? AND idempotency_key = ?",
		projectID, key,
	))
}

func (r *repository) ListProvisioningProjectThreadIntents(
	ctx context.Context,
) ([]domain.ProjectThreadIntent, error) {
	rows, err := r.q.QueryContext(ctx, projectThreadIntentSelect+
		" WHERE state = 'provisioning' ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var intents []domain.ProjectThreadIntent
	for rows.Next() {
		intent, err := scanProjectThreadIntent(rows)
		if err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	return intents, rows.Err()
}

func (r *repository) UpdateProjectThreadIntent(
	ctx context.Context,
	intent domain.ProjectThreadIntent,
	expected domain.ResourceVersion,
) error {
	if err := intent.Validate(); err != nil {
		return err
	}
	if intent.ResourceVersion != expected+1 {
		return fmt.Errorf("%w: invalid Project Thread intent resource version", domain.ErrInvalid)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE project_thread_intents
		SET state = ?, failure_code = ?, failure_message = ?, wrapped_dek = ?,
			updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		intent.State, intent.FailureCode, intent.FailureMessage,
		nullableBytes(intent.WrappedDEK), formatTime(intent.UpdatedAt),
		intent.ResourceVersion, intent.ID, expected,
	)
	if err != nil {
		return mapError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var exists int
	if err := r.q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM project_thread_intents WHERE id = ?", intent.ID,
	).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return domain.ErrNotFound
	}
	return domain.ErrConflict
}

func scanProjectThreadIntent(row scanner) (domain.ProjectThreadIntent, error) {
	var intent domain.ProjectThreadIntent
	var blockID domain.ThreadBlockID
	var ciphertext []byte
	var created, updated string
	if err := row.Scan(
		&intent.ID, &intent.ProjectID, &intent.CapsuleID, &intent.ThreadID,
		&intent.RunID, &intent.MessageID, &blockID, &intent.CapsuleName,
		&intent.RequestedName, &intent.Harness, &intent.IdempotencyKey,
		&intent.State, &intent.FailureCode, &intent.FailureMessage,
		&intent.WrappedDEK, &intent.KEKID, &intent.KEKVersion,
		&intent.EnvelopeVersion, &ciphertext, &created, &updated,
		&intent.ResourceVersion,
	); err != nil {
		return domain.ProjectThreadIntent{}, mapError(err)
	}
	var err error
	intent.CreatedAt, err = parseTime(created)
	if err != nil {
		return domain.ProjectThreadIntent{}, domain.ErrTranscriptCorrupt
	}
	intent.UpdatedAt, err = parseTime(updated)
	if err != nil {
		return domain.ProjectThreadIntent{}, domain.ErrTranscriptCorrupt
	}
	intent.PendingMessage = domain.ThreadMessage{
		ID: intent.MessageID, ThreadID: intent.ThreadID, Sequence: 1,
		Role: domain.ThreadRoleUser, Kind: domain.ThreadMessagePrompt,
		CreatedAt: intent.CreatedAt,
		Blocks: []domain.ThreadBlock{{
			ID: blockID, ThreadID: intent.ThreadID, MessageID: intent.MessageID,
			MessageSequence: 1, Sequence: 1, Kind: domain.ThreadBlockText,
			EnvelopeVersion: intent.EnvelopeVersion, Ciphertext: ciphertext,
			CreatedAt: intent.CreatedAt,
		}},
	}
	if err := intent.Validate(); err != nil {
		return domain.ProjectThreadIntent{}, err
	}
	return intent, nil
}

func (r *repository) listThreadBlocks(
	ctx context.Context,
	message domain.ThreadMessage,
) ([]domain.ThreadBlock, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, thread_id, message_id, message_sequence, sequence, kind,
			envelope_version, ciphertext, created_at
		FROM thread_blocks WHERE message_id = ? ORDER BY sequence`, message.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	blocks := make([]domain.ThreadBlock, 0, 4)
	for rows.Next() {
		var block domain.ThreadBlock
		var created string
		if err := rows.Scan(
			&block.ID, &block.ThreadID, &block.MessageID, &block.MessageSequence,
			&block.Sequence, &block.Kind, &block.EnvelopeVersion, &block.Ciphertext,
			&created,
		); err != nil {
			return nil, err
		}
		if len(blocks) >= transcripts.MaxBlocksPerMessage ||
			transcripts.ValidateEnvelope(block.Ciphertext) != nil {
			return nil, domain.ErrTranscriptCorrupt
		}
		block.CreatedAt, err = parseTime(created)
		if err != nil {
			return nil, domain.ErrTranscriptCorrupt
		}
		blocks = append(blocks, block)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, domain.ErrTranscriptCorrupt
	}
	return blocks, nil
}

func (r *repository) RewrapThreadKey(
	ctx context.Context,
	id domain.ThreadID,
	expected domain.ResourceVersion,
	wrapped []byte,
	keyID string,
	keyVersion uint32,
	updatedAt time.Time,
) error {
	if id == "" || expected <= 0 || len(wrapped) != 64 || keyID == "" ||
		len(keyID) > 128 || keyVersion == 0 || updatedAt.IsZero() {
		return fmt.Errorf("%w: invalid transcript key rotation", domain.ErrInvalid)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE threads SET wrapped_dek = ?, kek_id = ?, kek_version = ?,
			updated_at = ?, resource_version = resource_version + 1
		WHERE id = ? AND resource_version = ? AND state <> 'deleted'`,
		wrapped, keyID, keyVersion, formatTime(updatedAt), id, expected)
	if err != nil {
		return mapError(err)
	}
	return ensureThreadAffected(ctx, r.q, result, id)
}

func (r *repository) CryptoShredThread(
	ctx context.Context,
	id domain.ThreadID,
	expected domain.ResourceVersion,
	deletedAt time.Time,
) error {
	if id == "" || expected <= 0 || deletedAt.IsZero() {
		return fmt.Errorf("%w: invalid transcript deletion", domain.ErrInvalid)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE threads SET state = 'deleted', current_run_id = NULL,
			wrapped_dek = NULL, deleted_at = ?,
			updated_at = ?, resource_version = resource_version + 1
		WHERE id = ? AND resource_version = ? AND state <> 'deleted'`,
		formatTime(deletedAt), formatTime(deletedAt), id, expected)
	if err != nil {
		return mapError(err)
	}
	return ensureThreadAffected(ctx, r.q, result, id)
}

func ensureThreadAffected(
	ctx context.Context,
	query queryer,
	result sql.Result,
	id domain.ThreadID,
) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 0 {
		return nil
	}
	var exists int
	if err := query.QueryRowContext(ctx, "SELECT COUNT(*) FROM threads WHERE id = ?", id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return domain.ErrNotFound
	}
	return domain.ErrConflict
}

const deliverySelect = `
	SELECT id, capsule_id, project_id, state, action, approved, approved_at,
		expected_capsule_version, expected_head, expected_tree, remote_branch,
		destination_ref, base_branch, commit_message, pr_title, pr_body,
		result_commit_sha, result_pr_url, result_pr_number, failure,
		idempotency_key, created_at, updated_at, resource_version
	FROM deliveries`

func (r *repository) InsertDelivery(ctx context.Context, delivery domain.Delivery) error {
	if err := delivery.Validate(); err != nil {
		return err
	}
	if delivery.State != domain.DeliveryQueued {
		return fmt.Errorf("%w: new Delivery must be queued", domain.ErrInvalid)
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO deliveries(
			id, capsule_id, project_id, state, action, approved, approved_at,
			expected_capsule_version, expected_head, expected_tree, remote_branch,
			destination_ref, base_branch, commit_message, pr_title, pr_body,
			result_commit_sha, result_pr_url, result_pr_number, failure,
			idempotency_key, created_at, updated_at, resource_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		delivery.ID, delivery.CapsuleID, delivery.ProjectID, delivery.State, delivery.Action,
		delivery.Approved, formatTime(delivery.ApprovedAt), delivery.ExpectedCapsuleVersion,
		delivery.ExpectedHEAD, delivery.ExpectedTree, delivery.RemoteBranch,
		delivery.DestinationRef, delivery.BaseBranch, delivery.CommitMessage,
		delivery.PullRequestTitle, delivery.PullRequestBody, delivery.ResultCommitSHA,
		delivery.ResultPullRequestURL, delivery.ResultPullRequestNumber, delivery.Failure,
		delivery.IdempotencyKey, formatTime(delivery.CreatedAt), formatTime(delivery.UpdatedAt),
		delivery.ResourceVersion,
	)
	return mapError(err)
}

func (r *repository) UpdateDelivery(
	ctx context.Context,
	delivery domain.Delivery,
	expected domain.ResourceVersion,
) error {
	if err := delivery.Validate(); err != nil {
		return err
	}
	if delivery.ResourceVersion != expected+1 {
		return fmt.Errorf("%w: invalid Delivery resource version", domain.ErrInvalid)
	}
	result, err := r.q.ExecContext(ctx, `
		UPDATE deliveries SET state = ?, result_commit_sha = ?, result_pr_url = ?,
			result_pr_number = ?, failure = ?, updated_at = ?, resource_version = ?
		WHERE id = ? AND resource_version = ?`,
		delivery.State, delivery.ResultCommitSHA, delivery.ResultPullRequestURL,
		delivery.ResultPullRequestNumber, delivery.Failure, formatTime(delivery.UpdatedAt),
		delivery.ResourceVersion, delivery.ID, expected,
	)
	if err != nil {
		return mapError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var exists int
	if err := r.q.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM deliveries WHERE id = ?", delivery.ID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return domain.ErrNotFound
	}
	return domain.ErrConflict
}

func (r *repository) GetDelivery(
	ctx context.Context,
	id domain.DeliveryID,
) (domain.Delivery, error) {
	return scanDelivery(r.q.QueryRowContext(ctx, deliverySelect+" WHERE id = ?", id))
}

func (r *repository) ListDeliveries(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	page ports.Page,
) ([]domain.Delivery, bool, error) {
	if page.Limit <= 0 || page.Limit > 1000 {
		page.Limit = 100
	}
	rows, err := r.q.QueryContext(ctx, deliverySelect+`
		WHERE capsule_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		capsuleID, page.Limit+1, page.Offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var items []domain.Delivery
	for rows.Next() {
		item, err := scanDelivery(rows)
		if err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(items) > page.Limit
	if more {
		items = items[:page.Limit]
	}
	return items, more, nil
}

func (r *repository) ListRecoverableDeliveries(ctx context.Context) ([]domain.Delivery, error) {
	rows, err := r.q.QueryContext(ctx, deliverySelect+`
		WHERE state NOT IN ('succeeded', 'failed') ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.Delivery
	for rows.Next() {
		item, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanDelivery(row scanner) (domain.Delivery, error) {
	var delivery domain.Delivery
	var approvedAt, createdAt, updatedAt string
	if err := row.Scan(
		&delivery.ID, &delivery.CapsuleID, &delivery.ProjectID, &delivery.State,
		&delivery.Action, &delivery.Approved, &approvedAt,
		&delivery.ExpectedCapsuleVersion, &delivery.ExpectedHEAD, &delivery.ExpectedTree,
		&delivery.RemoteBranch, &delivery.DestinationRef, &delivery.BaseBranch,
		&delivery.CommitMessage, &delivery.PullRequestTitle, &delivery.PullRequestBody,
		&delivery.ResultCommitSHA, &delivery.ResultPullRequestURL,
		&delivery.ResultPullRequestNumber, &delivery.Failure, &delivery.IdempotencyKey,
		&createdAt, &updatedAt, &delivery.ResourceVersion,
	); err != nil {
		return domain.Delivery{}, mapError(err)
	}
	var err error
	delivery.ApprovedAt, err = parseTime(approvedAt)
	if err == nil {
		delivery.CreatedAt, err = parseTime(createdAt)
	}
	if err == nil {
		delivery.UpdatedAt, err = parseTime(updatedAt)
	}
	if err != nil {
		return domain.Delivery{}, err
	}
	return delivery, delivery.Validate()
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatTime(value)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func mapError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique constraint") || strings.Contains(message, "primary key") {
		return fmt.Errorf("%w: %v", domain.ErrConflict, err)
	}
	if strings.Contains(message, "append order") || strings.Contains(message, "lifecycle violation") {
		return domain.ErrConflict
	}
	if strings.Contains(message, "quota violation") {
		return domain.ErrResourceExhausted
	}
	return err
}
