// Package maintenance implements offline backup, restore, verification, and
// content-addressed artifact retention for local single-user installations.
package maintenance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	credentialsecrets "github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
	_ "modernc.org/sqlite"
)

const (
	backupFormat     = "meridian.backup.v1"
	manifestFilename = "manifest.json"
	maxManifestBytes = 4 << 20
	maxBackupFiles   = 1_000_000
)

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	Format                 string                     `json:"format"`
	CreatedAt              time.Time                  `json:"createdAt"`
	SchemaVersion          int                        `json:"schemaVersion"`
	RequiredTranscriptKeys []TranscriptKeyRequirement `json:"requiredTranscriptKeys,omitempty"`
	RequiredSecretKeys     []TranscriptKeyRequirement `json:"requiredSecretKeys,omitempty"`
	Files                  []File                     `json:"files"`
}

type TranscriptKeyRequirement struct {
	ID      string `json:"id"`
	Version uint32 `json:"version"`
}

type CASReport struct {
	ReferencedBlobs int   `json:"referencedBlobs"`
	StoredBlobs     int   `json:"storedBlobs"`
	EligibleBlobs   int   `json:"eligibleBlobs"`
	EligibleBytes   int64 `json:"eligibleBytes"`
	DeletedBlobs    int   `json:"deletedBlobs"`
	DeletedBytes    int64 `json:"deletedBytes"`
}

type TranscriptKeyRotationReport struct {
	KeyID                    string `json:"keyId"`
	KeyVersion               uint32 `json:"keyVersion"`
	ThreadsRewrapped         int    `json:"threadsRewrapped"`
	IntentEnvelopesRewrapped int    `json:"intentEnvelopesRewrapped"`
}

var rotationTestHook func(int) error

// RotateTranscriptKey performs an offline all-Thread rewrap. meridiand and all
// other maintenance commands must remain stopped for the entire operation.
// A transitional restrictive keyring keeps both sides decryptable across a
// crash; the old key is retired only after commit and deep verification.
func RotateTranscriptKey(
	ctx context.Context,
	dataDir, activeKeyFile, newKeyFile string,
) (TranscriptKeyRotationReport, error) {
	if err := validateDataDir(dataDir); err != nil {
		return TranscriptKeyRotationReport{}, err
	}
	var err error
	dataDir, err = filepath.EvalSymlinks(dataDir)
	if err != nil {
		return TranscriptKeyRotationReport{}, err
	}
	if activeKeyFile == "" {
		activeKeyFile = transcripts.DefaultKeyPath(dataDir)
	}
	oldKey, err := transcripts.LoadKey(activeKeyFile)
	if err != nil {
		return TranscriptKeyRotationReport{}, fmt.Errorf("load current transcript key: %w", err)
	}
	defer oldKey.Zero()
	databasePath := filepath.Join(dataDir, "meridian.db")
	if _, err := inspectDatabase(ctx, databasePath, true); err != nil {
		return TranscriptKeyRotationReport{}, err
	}
	if oldKey.Transitional() {
		if newKeyFile != "" {
			return TranscriptKeyRotationReport{}, errors.New(
				"complete the existing transcript key transition before supplying another replacement",
			)
		}
		if err := deepVerifyTranscripts(ctx, databasePath, oldKey); err != nil {
			return TranscriptKeyRotationReport{}, err
		}
		counts, err := rewrapTranscriptKeys(ctx, databasePath, oldKey, oldKey)
		if err != nil {
			return TranscriptKeyRotationReport{}, fmt.Errorf(
				"resume transcript key transition (transitional key retained): %w", err,
			)
		}
		if err := deepVerifyTranscripts(ctx, databasePath, oldKey); err != nil {
			return TranscriptKeyRotationReport{}, fmt.Errorf(
				"verify resumed transcript key transition (transitional key retained): %w", err,
			)
		}
		if err := transcripts.ActivateKey(activeKeyFile, oldKey); err != nil {
			return TranscriptKeyRotationReport{}, fmt.Errorf(
				"retire previous transcript key after resumed transition: %w", err,
			)
		}
		return TranscriptKeyRotationReport{
			KeyID: oldKey.ID, KeyVersion: oldKey.Version,
			ThreadsRewrapped: counts.threads, IntentEnvelopesRewrapped: counts.intents,
		}, nil
	}
	var newKey *transcripts.InstallationKey
	if newKeyFile == "" {
		newKey, err = transcripts.NewInstallationKey(oldKey.ID, oldKey.Version+1)
	} else {
		newKey, err = transcripts.LoadKey(newKeyFile)
	}
	if err != nil {
		return TranscriptKeyRotationReport{}, fmt.Errorf("load or generate new transcript key: %w", err)
	}
	defer newKey.Zero()
	if newKey.ID == oldKey.ID && newKey.Version == oldKey.Version {
		return TranscriptKeyRotationReport{}, fmt.Errorf("%w: new transcript key metadata must differ", domain.ErrInvalid)
	}
	if err := deepVerifyTranscripts(ctx, databasePath, oldKey); err != nil {
		return TranscriptKeyRotationReport{}, err
	}
	if err := transcripts.ActivateTransition(activeKeyFile, newKey, oldKey); err != nil {
		return TranscriptKeyRotationReport{}, fmt.Errorf("activate transcript key transition: %w", err)
	}
	restoreOld := func(operationErr error) error {
		if restoreErr := transcripts.ActivateKey(activeKeyFile, oldKey); restoreErr != nil {
			return errors.Join(operationErr, fmt.Errorf("restore old transcript key: %w", restoreErr))
		}
		return operationErr
	}
	counts, err := rewrapTranscriptKeys(ctx, databasePath, oldKey, newKey)
	if err != nil {
		oldOnly, inspectErr := databaseUsesOnlyTranscriptKey(
			ctx, databasePath, oldKey.ID, oldKey.Version,
		)
		if inspectErr != nil || !oldOnly {
			return TranscriptKeyRotationReport{}, errors.Join(
				err, inspectErr,
				errors.New("transitional transcript key retained because database commit state is ambiguous"),
			)
		}
		return TranscriptKeyRotationReport{}, restoreOld(err)
	}
	if err := deepVerifyTranscripts(ctx, databasePath, newKey); err != nil {
		rollbackErr := rewrapTranscriptKeysTo(ctx, databasePath, newKey, oldKey)
		if rollbackErr != nil {
			return TranscriptKeyRotationReport{}, errors.Join(
				err, rollbackErr,
				errors.New("transitional transcript key retained because rollback failed"),
			)
		}
		return TranscriptKeyRotationReport{}, restoreOld(err)
	}
	if err := transcripts.ActivateKey(activeKeyFile, newKey); err != nil {
		rollbackErr := rewrapTranscriptKeysTo(ctx, databasePath, newKey, oldKey)
		if rollbackErr != nil {
			return TranscriptKeyRotationReport{}, errors.Join(
				err, rollbackErr,
				errors.New("transitional transcript key retained because rollback failed"),
			)
		}
		return TranscriptKeyRotationReport{}, restoreOld(err)
	}
	return TranscriptKeyRotationReport{
		KeyID: newKey.ID, KeyVersion: newKey.Version,
		ThreadsRewrapped: counts.threads, IntentEnvelopesRewrapped: counts.intents,
	}, nil
}

func databaseUsesOnlyTranscriptKey(
	ctx context.Context,
	databasePath, keyID string,
	keyVersion uint32,
) (bool, error) {
	required, err := requiredTranscriptKeys(ctx, databasePath)
	if err != nil {
		return false, err
	}
	return len(required) == 0 ||
		(len(required) == 1 && required[0].ID == keyID && required[0].Version == keyVersion), nil
}

// CreateBackup publishes a SQLite-consistent database and the artifact CAS as
// a restrictive directory. meridiand must be stopped before calling it.
func CreateBackup(ctx context.Context, dataDir, destination string) (Manifest, error) {
	if err := validateDataDir(dataDir); err != nil {
		return Manifest{}, err
	}
	dataDir, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return Manifest{}, err
	}
	parent, base, err := destinationParent(destination)
	if err != nil {
		return Manifest{}, err
	}
	destination = filepath.Join(parent, base)
	dataAbsolute, err := filepath.Abs(dataDir)
	if err != nil {
		return Manifest{}, err
	}
	if relative, err := filepath.Rel(dataAbsolute, destination); err == nil &&
		relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Manifest{}, fmt.Errorf("backup destination must be outside the data directory")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Manifest{}, fmt.Errorf("backup destination already exists")
		}
		return Manifest{}, err
	}
	temp, err := os.MkdirTemp(parent, "."+base+".tmp-")
	if err != nil {
		return Manifest{}, fmt.Errorf("create backup temporary directory: %w", err)
	}
	if err := os.Chmod(temp, 0o700); err != nil {
		_ = os.RemoveAll(temp)
		return Manifest{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temp)
		}
	}()

	sourceDB := filepath.Join(dataDir, "meridian.db")
	schemaVersion, err := inspectDatabase(ctx, sourceDB, true)
	if err != nil {
		return Manifest{}, err
	}
	databaseDestination := filepath.Join(temp, "meridian.db")
	if err := vacuumInto(ctx, sourceDB, databaseDestination); err != nil {
		return Manifest{}, err
	}
	if err := os.Chmod(databaseDestination, 0o600); err != nil {
		return Manifest{}, err
	}
	files := make([]File, 0, 128)
	databaseFile, err := checksumFile(databaseDestination, "meridian.db")
	if err != nil {
		return Manifest{}, err
	}
	files = append(files, databaseFile)

	artifactRoot := filepath.Join(dataDir, "artifacts", "sha256")
	if info, statErr := os.Lstat(artifactRoot); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Manifest{}, fmt.Errorf("artifact CAS root is unsafe")
		}
		err = filepath.WalkDir(artifactRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == artifactRoot {
				return nil
			}
			relative, err := filepath.Rel(artifactRoot, path)
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("artifact CAS contains a symlink: %s", relative)
			}
			if entry.IsDir() {
				if len(strings.Split(filepath.ToSlash(relative), "/")) > 2 {
					return fmt.Errorf("artifact CAS directory depth is invalid")
				}
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("artifact CAS contains a non-regular file")
			}
			if len(files) >= maxBackupFiles {
				return fmt.Errorf("backup file count exceeds limit")
			}
			backupPath := filepath.ToSlash(filepath.Join("artifacts", "sha256", relative))
			if !validCASPath(backupPath) {
				return fmt.Errorf("artifact CAS path is invalid")
			}
			target := filepath.Join(temp, filepath.FromSlash(backupPath))
			if err := copyRegular(path, target); err != nil {
				return err
			}
			item, err := checksumFile(target, backupPath)
			if err != nil {
				return err
			}
			if item.SHA256 != filepath.Base(path) {
				return fmt.Errorf("artifact %s is corrupt", relative)
			}
			files = append(files, item)
			return nil
		})
		if err != nil {
			return Manifest{}, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Manifest{}, statErr
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	requiredKeys, err := requiredTranscriptKeys(ctx, databaseDestination)
	if err != nil {
		return Manifest{}, err
	}
	requiredSecretKeys, err := requiredSecretKeys(ctx, databaseDestination)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		Format: backupFormat, CreatedAt: time.Now().UTC(),
		SchemaVersion: schemaVersion, RequiredTranscriptKeys: requiredKeys,
		RequiredSecretKeys: requiredSecretKeys, Files: files,
	}
	if err := writeManifest(temp, manifest); err != nil {
		return Manifest{}, err
	}
	if err := syncDirectory(temp); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(temp, destination); err != nil {
		return Manifest{}, fmt.Errorf("publish backup: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return Manifest{}, err
	}
	published = true
	return manifest, nil
}

func VerifyBackup(ctx context.Context, backupDir string) (Manifest, error) {
	absolute, err := filepath.Abs(backupDir)
	if err != nil {
		return Manifest{}, err
	}
	originalInfo, err := os.Lstat(absolute)
	if err != nil || originalInfo.Mode()&os.ModeSymlink != 0 {
		return Manifest{}, fmt.Errorf("backup path must be a real directory")
	}
	evaluated, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve backup path: %w", err)
	}
	backupDir = evaluated
	info, err := os.Lstat(backupDir)
	if err != nil {
		return Manifest{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Manifest{}, fmt.Errorf("backup path must be a real directory")
	}
	manifestPath := filepath.Join(backupDir, manifestFilename)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 {
		return Manifest{}, fmt.Errorf("backup manifest is missing or unsafe")
	}
	manifestFile, err := os.Open(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	defer manifestFile.Close()
	var manifest Manifest
	decoder := json.NewDecoder(io.LimitReader(manifestFile, maxManifestBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode backup manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("backup manifest must contain one JSON value")
	}
	if manifest.Format != backupFormat {
		return Manifest{}, fmt.Errorf("unsupported backup format %q", manifest.Format)
	}
	if manifest.SchemaVersion > sqlite.CurrentSchemaVersion {
		return Manifest{}, fmt.Errorf(
			"backup schema %d is newer than supported schema %d",
			manifest.SchemaVersion, sqlite.CurrentSchemaVersion,
		)
	}
	if len(manifest.Files) == 0 || len(manifest.Files) > maxBackupFiles {
		return Manifest{}, fmt.Errorf("backup manifest file count is invalid")
	}
	if err := validateTranscriptKeyRequirements(manifest.RequiredTranscriptKeys); err != nil {
		return Manifest{}, err
	}
	if err := validateTranscriptKeyRequirements(manifest.RequiredSecretKeys); err != nil {
		return Manifest{}, fmt.Errorf("backup credential key requirement is invalid: %w", err)
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	for _, expected := range manifest.Files {
		if !validBackupPath(expected.Path) {
			return Manifest{}, fmt.Errorf("backup manifest path %q is invalid", expected.Path)
		}
		if _, exists := seen[expected.Path]; exists {
			return Manifest{}, fmt.Errorf("backup manifest path %q is duplicated", expected.Path)
		}
		seen[expected.Path] = struct{}{}
		actual, err := checksumFile(filepath.Join(backupDir, filepath.FromSlash(expected.Path)), expected.Path)
		if err != nil {
			return Manifest{}, err
		}
		if actual.Size != expected.Size || actual.SHA256 != expected.SHA256 {
			return Manifest{}, fmt.Errorf("backup file %q failed checksum verification", expected.Path)
		}
	}
	if _, ok := seen["meridian.db"]; !ok {
		return Manifest{}, fmt.Errorf("backup database is not in the manifest")
	}
	discovered := 0
	if err := filepath.WalkDir(backupDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == backupDir {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup contains a symlink")
		}
		relative, err := filepath.Rel(backupDir, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("backup contains a non-regular file")
		}
		if relative != manifestFilename {
			if _, ok := seen[relative]; !ok {
				return fmt.Errorf("backup contains unmanifested file %q", relative)
			}
			discovered++
		}
		return nil
	}); err != nil {
		return Manifest{}, err
	}
	if discovered != len(manifest.Files) {
		return Manifest{}, fmt.Errorf("backup file count does not match manifest")
	}
	version, err := inspectDatabase(ctx, filepath.Join(backupDir, "meridian.db"), false)
	if err != nil {
		return Manifest{}, err
	}
	if version != manifest.SchemaVersion {
		return Manifest{}, fmt.Errorf("backup schema version does not match manifest")
	}
	requiredKeys, err := requiredTranscriptKeys(ctx, filepath.Join(backupDir, "meridian.db"))
	if err != nil {
		return Manifest{}, err
	}
	if !sameTranscriptKeyRequirements(requiredKeys, manifest.RequiredTranscriptKeys) {
		return Manifest{}, fmt.Errorf("backup transcript key requirements do not match database")
	}
	requiredSecretKeys, err := requiredSecretKeys(ctx, filepath.Join(backupDir, "meridian.db"))
	if err != nil {
		return Manifest{}, err
	}
	if !sameTranscriptKeyRequirements(requiredSecretKeys, manifest.RequiredSecretKeys) {
		return Manifest{}, fmt.Errorf("backup credential key requirements do not match database")
	}
	return manifest, nil
}

// VerifyBackupWithKey additionally authenticates every retained transcript
// envelope. The installation key is never copied into or read from the backup.
func VerifyBackupWithKey(
	ctx context.Context,
	backupDir, transcriptKeyFile string,
) (Manifest, error) {
	manifest, err := VerifyBackup(ctx, backupDir)
	if err != nil {
		return Manifest{}, err
	}
	if len(manifest.RequiredTranscriptKeys) == 0 {
		return manifest, nil
	}
	key, err := transcripts.LoadKey(transcriptKeyFile)
	if err != nil {
		return Manifest{}, fmt.Errorf("load separately protected transcript key: %w", err)
	}
	defer key.Zero()
	if err := deepVerifyTranscripts(ctx, filepath.Join(backupDir, "meridian.db"), key); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// RestoreBackup verifies before changing anything and atomically replaces the
// destination directory only when replace is explicitly true.
func RestoreBackup(ctx context.Context, backupDir, dataDir string, replace bool) (Manifest, error) {
	return RestoreBackupWithOptions(ctx, backupDir, dataDir, RestoreOptions{Replace: replace})
}

type RestoreOptions struct {
	Replace           bool
	TranscriptKeyFile string
	SecretKeyFile     string
}

// RestoreBackupWithOptions preserves a separately supplied installation key
// only after it has authenticated all transcript envelopes in the backup.
func RestoreBackupWithOptions(
	ctx context.Context,
	backupDir, dataDir string,
	options RestoreOptions,
) (Manifest, error) {
	manifest, err := VerifyBackup(ctx, backupDir)
	if err != nil {
		return Manifest{}, err
	}
	parent, base, err := destinationParent(dataDir)
	if err != nil {
		return Manifest{}, err
	}
	dataDir = filepath.Join(parent, base)
	existing := false
	if info, statErr := os.Lstat(dataDir); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Manifest{}, fmt.Errorf("restore destination is unsafe")
		}
		entries, err := os.ReadDir(dataDir)
		if err != nil {
			return Manifest{}, err
		}
		existing = len(entries) != 0
		if existing && !options.Replace {
			return Manifest{}, fmt.Errorf("restore destination is not empty; pass --replace and --yes after stopping meridiand")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Manifest{}, statErr
	}
	keyPath := options.TranscriptKeyFile
	defaultKeyPath := transcripts.DefaultKeyPath(dataDir)
	if keyPath == "" {
		if info, statErr := os.Lstat(defaultKeyPath); statErr == nil &&
			info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			keyPath = defaultKeyPath
		}
	}
	if len(manifest.RequiredTranscriptKeys) != 0 {
		if keyPath == "" {
			return Manifest{}, errors.New("restore requires the separately protected transcript key")
		}
		key, err := transcripts.LoadKey(keyPath)
		if err != nil {
			return Manifest{}, fmt.Errorf("load separately protected transcript key: %w", err)
		}
		verifyErr := deepVerifyTranscripts(ctx, filepath.Join(backupDir, "meridian.db"), key)
		key.Zero()
		if verifyErr != nil {
			return Manifest{}, verifyErr
		}
	}
	secretKeyPath := options.SecretKeyFile
	defaultSecretKeyPath := credentialsecrets.DefaultKeyPath(dataDir)
	if secretKeyPath == "" {
		if info, statErr := os.Lstat(defaultSecretKeyPath); statErr == nil &&
			info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			secretKeyPath = defaultSecretKeyPath
		}
	}
	if len(manifest.RequiredSecretKeys) != 0 {
		if secretKeyPath == "" {
			return Manifest{}, errors.New("restore requires the separately protected credential key")
		}
		key, err := credentialsecrets.LoadKey(secretKeyPath)
		if err != nil {
			return Manifest{}, fmt.Errorf("load separately protected credential key: %w", err)
		}
		verifyErr := deepVerifySecrets(ctx, filepath.Join(backupDir, "meridian.db"), key)
		key.Zero()
		if verifyErr != nil {
			return Manifest{}, verifyErr
		}
	}
	temp, err := os.MkdirTemp(parent, "."+base+".restore-")
	if err != nil {
		return Manifest{}, err
	}
	if err := os.Chmod(temp, 0o700); err != nil {
		_ = os.RemoveAll(temp)
		return Manifest{}, err
	}
	defer os.RemoveAll(temp)
	for _, item := range manifest.Files {
		if err := copyRegular(
			filepath.Join(backupDir, filepath.FromSlash(item.Path)),
			filepath.Join(temp, filepath.FromSlash(item.Path)),
		); err != nil {
			return Manifest{}, err
		}
	}
	if keyPath != "" {
		if err := copyRegular(keyPath, transcripts.DefaultKeyPath(temp)); err != nil {
			return Manifest{}, fmt.Errorf("install separately protected transcript key: %w", err)
		}
	}
	if secretKeyPath != "" {
		if err := copyRegular(secretKeyPath, credentialsecrets.DefaultKeyPath(temp)); err != nil {
			return Manifest{}, fmt.Errorf("install separately protected credential key: %w", err)
		}
	}
	if _, err := inspectDatabase(ctx, filepath.Join(temp, "meridian.db"), false); err != nil {
		return Manifest{}, err
	}
	if err := syncTree(temp); err != nil {
		return Manifest{}, err
	}
	rollback := ""
	if existing {
		rollback = filepath.Join(parent, "."+base+".pre-restore-"+time.Now().UTC().Format("20060102T150405.000000000"))
		if err := os.Rename(dataDir, rollback); err != nil {
			return Manifest{}, fmt.Errorf("preserve existing installation: %w", err)
		}
	} else {
		_ = os.Remove(dataDir)
	}
	if err := os.Rename(temp, dataDir); err != nil {
		if rollback != "" {
			_ = os.Rename(rollback, dataDir)
		}
		return Manifest{}, fmt.Errorf("publish restored installation: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return Manifest{}, err
	}
	if rollback != "" {
		if err := os.RemoveAll(rollback); err != nil {
			return Manifest{}, fmt.Errorf("remove pre-restore installation: %w", err)
		}
	}
	return manifest, nil
}

// VerifyCAS checks every visible Moment reference and reports unreachable
// regular blobs. Delete is explicit; callers must stop meridiand first.
func VerifyCAS(ctx context.Context, dataDir string, deleteEligible bool) (CASReport, error) {
	if err := validateDataDir(dataDir); err != nil {
		return CASReport{}, err
	}
	dataDir, err := filepath.EvalSymlinks(dataDir)
	if err != nil {
		return CASReport{}, err
	}
	dbPath := filepath.Join(dataDir, "meridian.db")
	if _, err := inspectDatabase(ctx, dbPath, false); err != nil {
		return CASReport{}, err
	}
	db, err := openDatabase(dbPath)
	if err != nil {
		return CASReport{}, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT archive_sha256, manifest_sha256 FROM moments")
	if err != nil {
		return CASReport{}, err
	}
	referenced := make(map[string]struct{})
	for rows.Next() {
		var archive, manifest string
		if err := rows.Scan(&archive, &manifest); err != nil {
			rows.Close()
			return CASReport{}, err
		}
		referenced[archive] = struct{}{}
		referenced[manifest] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return CASReport{}, err
	}
	report := CASReport{ReferencedBlobs: len(referenced)}
	root := filepath.Join(dataDir, "artifacts", "sha256")
	for digest := range referenced {
		if !validDigest(digest) {
			return CASReport{}, fmt.Errorf("Moment references an invalid artifact digest")
		}
		item, err := checksumFile(filepath.Join(root, digest[:2], digest), "")
		if err != nil {
			return CASReport{}, fmt.Errorf("referenced artifact %s is missing or unsafe: %w", digest, err)
		}
		if item.SHA256 != digest {
			return CASReport{}, fmt.Errorf("referenced artifact %s is corrupt", digest)
		}
	}
	type eligibleBlob struct {
		path string
		size int64
	}
	var eligible []eligibleBlob
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact CAS contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("artifact CAS contains a non-regular file")
		}
		digest := filepath.Base(path)
		if len(digest) != 64 || filepath.Base(filepath.Dir(path)) != digest[:2] {
			return fmt.Errorf("artifact CAS path is invalid")
		}
		item, err := checksumFile(path, "")
		if err != nil || item.SHA256 != digest {
			return fmt.Errorf("artifact %s is corrupt", digest)
		}
		report.StoredBlobs++
		if _, keep := referenced[digest]; keep {
			return nil
		}
		report.EligibleBlobs++
		report.EligibleBytes += item.Size
		eligible = append(eligible, eligibleBlob{path: path, size: item.Size})
		return nil
	})
	if err != nil || !deleteEligible {
		return report, err
	}
	for _, blob := range eligible {
		if err := os.Remove(blob.path); err != nil {
			return report, err
		}
		report.DeletedBlobs++
		report.DeletedBytes += blob.size
	}
	return report, syncDirectory(root)
}

func requiredTranscriptKeys(
	ctx context.Context,
	databasePath string,
) ([]TranscriptKeyRequirement, error) {
	db, err := openDatabase(databasePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var exists int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'threads'`,
	).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT kek_id, kek_version
		FROM threads WHERE wrapped_dek IS NOT NULL
		ORDER BY kek_id, kek_version`)
	if err != nil {
		return nil, err
	}
	requirementsByKey := make(map[TranscriptKeyRequirement]struct{})
	for rows.Next() {
		var requirement TranscriptKeyRequirement
		if err := rows.Scan(&requirement.ID, &requirement.Version); err != nil {
			rows.Close()
			return nil, err
		}
		requirementsByKey[requirement] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'project_thread_intents'`,
	).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 0 {
		rows, err = db.QueryContext(ctx, `
			SELECT DISTINCT kek_id, kek_version
			FROM project_thread_intents WHERE wrapped_dek IS NOT NULL`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var requirement TranscriptKeyRequirement
			if err := rows.Scan(&requirement.ID, &requirement.Version); err != nil {
				rows.Close()
				return nil, err
			}
			requirementsByKey[requirement] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	requirements := make([]TranscriptKeyRequirement, 0, len(requirementsByKey))
	for requirement := range requirementsByKey {
		requirements = append(requirements, requirement)
	}
	sort.Slice(requirements, func(i, j int) bool {
		return requirements[i].ID < requirements[j].ID ||
			(requirements[i].ID == requirements[j].ID &&
				requirements[i].Version < requirements[j].Version)
	})
	return requirements, nil
}

func requiredSecretKeys(
	ctx context.Context,
	databasePath string,
) ([]TranscriptKeyRequirement, error) {
	db, err := openDatabase(databasePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var exists int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'secrets'`,
	).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT kek_id, kek_version
		FROM secrets ORDER BY kek_id, kek_version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var requirements []TranscriptKeyRequirement
	for rows.Next() {
		var requirement TranscriptKeyRequirement
		if err := rows.Scan(&requirement.ID, &requirement.Version); err != nil {
			return nil, err
		}
		requirements = append(requirements, requirement)
	}
	return requirements, rows.Err()
}

func deepVerifySecrets(
	ctx context.Context,
	databasePath string,
	key *credentialsecrets.InstallationKey,
) error {
	db, err := openDatabase(databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `
		SELECT id, name, purpose, envelope_version, kek_id, kek_version, nonce,
			ciphertext, created_at, updated_at, resource_version
		FROM secrets ORDER BY name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var secret domain.Secret
		var created, updated string
		if err := rows.Scan(
			&secret.ID, &secret.Name, &secret.Purpose, &secret.EnvelopeVersion,
			&secret.KEKID, &secret.KEKVersion, &secret.Nonce, &secret.Ciphertext,
			&created, &updated, &secret.ResourceVersion,
		); err != nil {
			return err
		}
		secret.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err == nil {
			secret.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		}
		if err != nil {
			return domain.ErrCorrupt
		}
		if _, err := key.Open(secret); err != nil {
			return fmt.Errorf("credential deep verification failed: %w", err)
		}
	}
	return rows.Err()
}

type wrappedThreadKey struct {
	id      domain.ThreadID
	wrapped []byte
	keyID   string
	version uint32
	intent  bool
}

type rotationCounts struct {
	threads int
	intents int
}

func rewrapTranscriptKeys(
	ctx context.Context,
	databasePath string,
	oldKey, newKey *transcripts.InstallationKey,
) (rotationCounts, error) {
	return rewrapTranscriptKeysWithHook(ctx, databasePath, oldKey, newKey, rotationTestHook)
}

func rewrapTranscriptKeysTo(
	ctx context.Context,
	databasePath string,
	oldKey, newKey *transcripts.InstallationKey,
) error {
	_, err := rewrapTranscriptKeysWithHook(ctx, databasePath, oldKey, newKey, nil)
	return err
}

func rewrapTranscriptKeysWithHook(
	ctx context.Context,
	databasePath string,
	oldKey, newKey *transcripts.InstallationKey,
	hook func(int) error,
) (rotationCounts, error) {
	db, err := openWritableDatabase(databasePath)
	if err != nil {
		return rotationCounts{}, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return rotationCounts{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT id, wrapped_dek, kek_id, kek_version
		FROM threads WHERE state <> 'deleted' ORDER BY id`)
	if err != nil {
		return rotationCounts{}, err
	}
	var keys []wrappedThreadKey
	for rows.Next() {
		var item wrappedThreadKey
		if err := rows.Scan(&item.id, &item.wrapped, &item.keyID, &item.version); err != nil {
			rows.Close()
			return rotationCounts{}, err
		}
		keys = append(keys, item)
	}
	if err := rows.Close(); err != nil {
		return rotationCounts{}, err
	}
	var intentTable int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'project_thread_intents'`,
	).Scan(&intentTable); err != nil {
		return rotationCounts{}, err
	}
	if intentTable != 0 {
		rows, err = tx.QueryContext(ctx, `
			SELECT thread_id, wrapped_dek, kek_id, kek_version
			FROM project_thread_intents
			WHERE wrapped_dek IS NOT NULL ORDER BY thread_id`)
		if err != nil {
			return rotationCounts{}, err
		}
		for rows.Next() {
			var item wrappedThreadKey
			item.intent = true
			if err := rows.Scan(&item.id, &item.wrapped, &item.keyID, &item.version); err != nil {
				rows.Close()
				return rotationCounts{}, err
			}
			keys = append(keys, item)
		}
		if err := rows.Close(); err != nil {
			return rotationCounts{}, err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var counts rotationCounts
	for index, item := range keys {
		dek, err := oldKey.UnwrapDEK(item.id, item.keyID, item.version, item.wrapped)
		if err != nil {
			return rotationCounts{}, fmt.Errorf("authenticate Thread key before rotation: %w", err)
		}
		rewrapped, wrapErr := newKey.WrapDEK(item.id, dek)
		zeroBytes(dek)
		if wrapErr != nil {
			return rotationCounts{}, wrapErr
		}
		var result sql.Result
		var updateErr error
		if item.intent {
			result, updateErr = tx.ExecContext(ctx, `
				UPDATE project_thread_intents
				SET wrapped_dek = ?, kek_id = ?, kek_version = ?,
					updated_at = ?, resource_version = resource_version + 1
				WHERE thread_id = ? AND wrapped_dek = ?
					AND kek_id = ? AND kek_version = ?`,
				rewrapped, newKey.ID, newKey.Version, now, item.id,
				item.wrapped, item.keyID, item.version,
			)
		} else {
			result, updateErr = tx.ExecContext(ctx, `
				UPDATE threads
				SET wrapped_dek = ?, kek_id = ?, kek_version = ?,
					updated_at = ?, resource_version = resource_version + 1
				WHERE id = ? AND state <> 'deleted' AND wrapped_dek = ?
					AND kek_id = ? AND kek_version = ?`,
				rewrapped, newKey.ID, newKey.Version, now, item.id,
				item.wrapped, item.keyID, item.version,
			)
		}
		zeroBytes(rewrapped)
		if updateErr != nil {
			return rotationCounts{}, updateErr
		}
		affected, affectedErr := result.RowsAffected()
		if affectedErr != nil || affected != 1 {
			if affectedErr != nil {
				return rotationCounts{}, affectedErr
			}
			return rotationCounts{}, domain.ErrConflict
		}
		if item.intent {
			counts.intents++
		} else {
			counts.threads++
		}
		if hook != nil {
			if err := hook(index + 1); err != nil {
				return rotationCounts{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return rotationCounts{}, fmt.Errorf("commit transcript key rotation: %w", err)
	}
	return counts, nil
}

func openWritableDatabase(path string) (*sql.DB, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("database path is unsafe")
	}
	dsnURL := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := dsnURL.Query()
	query.Add("mode", "rw")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	dsnURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func validateTranscriptKeyRequirements(requirements []TranscriptKeyRequirement) error {
	for index, requirement := range requirements {
		if requirement.ID == "" || len(requirement.ID) > 128 || requirement.Version == 0 {
			return errors.New("backup transcript key requirement is invalid")
		}
		if index > 0 {
			previous := requirements[index-1]
			if previous.ID > requirement.ID ||
				(previous.ID == requirement.ID && previous.Version >= requirement.Version) {
				return errors.New("backup transcript key requirements are not unique and ordered")
			}
		}
	}
	return nil
}

func sameTranscriptKeyRequirements(left, right []TranscriptKeyRequirement) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func deepVerifyTranscripts(
	ctx context.Context,
	databasePath string,
	key *transcripts.InstallationKey,
) error {
	db, err := openDatabase(databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `
		SELECT t.id, t.wrapped_dek, t.kek_id, t.kek_version,
			m.id, m.sequence, m.role, m.kind,
			b.sequence, b.kind, b.envelope_version, b.ciphertext, b.created_at
		FROM threads t
		LEFT JOIN thread_messages m ON m.thread_id = t.id
		LEFT JOIN thread_blocks b
			ON b.message_id = m.id AND b.thread_id = m.thread_id
		WHERE t.wrapped_dek IS NOT NULL
		ORDER BY t.id, m.sequence, b.sequence`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var currentThread domain.ThreadID
	var dek []byte
	defer func() { zeroBytes(dek) }()
	var frameCount int64
	for rows.Next() {
		var threadID domain.ThreadID
		var wrappedDEK []byte
		var keyID string
		var keyVersion uint32
		var messageID, role, messageKind sql.NullString
		var messageSequence, blockSequence, envelopeVersion sql.NullInt64
		var blockKind, createdAt sql.NullString
		var envelope []byte
		if err := rows.Scan(
			&threadID, &wrappedDEK, &keyID, &keyVersion,
			&messageID, &messageSequence, &role, &messageKind,
			&blockSequence, &blockKind, &envelopeVersion, &envelope, &createdAt,
		); err != nil {
			return err
		}
		if threadID != currentThread {
			zeroBytes(dek)
			dek, err = key.UnwrapDEK(threadID, keyID, keyVersion, wrappedDEK)
			if err != nil {
				return fmt.Errorf("transcript deep verification failed: %w", err)
			}
			currentThread = threadID
			frameCount = 0
		}
		if !messageID.Valid {
			continue
		}
		if !messageSequence.Valid || !role.Valid || !messageKind.Valid ||
			!blockSequence.Valid || !blockKind.Valid || !envelopeVersion.Valid ||
			!createdAt.Valid {
			return domain.ErrTranscriptCorrupt
		}
		frameCount++
		if frameCount > transcripts.MaxMessagesPerThread*transcripts.MaxBlocksPerMessage ||
			envelopeVersion.Int64 != int64(transcripts.EnvelopeVersion) {
			return domain.ErrTranscriptCorrupt
		}
		metadata := transcripts.FrameMetadata{
			ThreadID: threadID, MessageID: domain.ThreadMessageID(messageID.String),
			MessageSequence: messageSequence.Int64, BlockSequence: blockSequence.Int64,
			Role:        domain.ThreadMessageRole(role.String),
			MessageKind: domain.ThreadMessageKind(messageKind.String),
			BlockKind:   domain.ThreadBlockKind(blockKind.String), CreatedAt: createdAt.String,
		}
		plaintext, err := transcripts.Open(dek, metadata, envelope)
		zeroBytes(plaintext)
		if err != nil {
			return fmt.Errorf("transcript deep verification failed: %w", domain.ErrTranscriptCorrupt)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var exists int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'project_thread_intents'`,
	).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	intentRows, err := db.QueryContext(ctx, `
		SELECT thread_id, message_id, wrapped_dek, kek_id, kek_version,
			envelope_version, ciphertext, created_at
		FROM project_thread_intents
		WHERE wrapped_dek IS NOT NULL
		ORDER BY thread_id`)
	if err != nil {
		return err
	}
	defer intentRows.Close()
	for intentRows.Next() {
		var threadID domain.ThreadID
		var messageID domain.ThreadMessageID
		var wrappedDEK, ciphertext []byte
		var keyID, createdAt string
		var keyVersion uint32
		var envelopeVersion int64
		if err := intentRows.Scan(
			&threadID, &messageID, &wrappedDEK, &keyID, &keyVersion,
			&envelopeVersion, &ciphertext, &createdAt,
		); err != nil {
			return err
		}
		if envelopeVersion != int64(transcripts.EnvelopeVersion) {
			return domain.ErrTranscriptCorrupt
		}
		dek, err := key.UnwrapDEK(threadID, keyID, keyVersion, wrappedDEK)
		if err != nil {
			return fmt.Errorf("Project Thread intent verification failed: %w", err)
		}
		plaintext, openErr := transcripts.Open(dek, transcripts.FrameMetadata{
			ThreadID: threadID, MessageID: messageID, MessageSequence: 1,
			BlockSequence: 1, Role: domain.ThreadRoleUser,
			MessageKind: domain.ThreadMessagePrompt, BlockKind: domain.ThreadBlockText,
			CreatedAt: createdAt,
		}, ciphertext)
		zeroBytes(dek)
		zeroBytes(plaintext)
		if openErr != nil {
			return fmt.Errorf("Project Thread intent verification failed: %w", domain.ErrTranscriptCorrupt)
		}
	}
	return intentRows.Err()
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func inspectDatabase(ctx context.Context, path string, requireCurrent bool) (int, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("database path is unsafe")
	}
	db, err := openDatabase(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil || integrity != "ok" {
		return 0, fmt.Errorf("database integrity check failed")
	}
	foreignKeyRows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return 0, fmt.Errorf("database foreign-key check failed")
	}
	if foreignKeyRows.Next() {
		foreignKeyRows.Close()
		return 0, fmt.Errorf("database foreign-key check failed")
	}
	if err := foreignKeyRows.Close(); err != nil {
		return 0, fmt.Errorf("database foreign-key check failed")
	}
	var version int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		return 0, fmt.Errorf("read database schema version: %w", err)
	}
	if version > sqlite.CurrentSchemaVersion {
		return 0, fmt.Errorf("database schema %d is newer than supported schema %d", version, sqlite.CurrentSchemaVersion)
	}
	if requireCurrent && version != sqlite.CurrentSchemaVersion {
		return 0, fmt.Errorf("database schema %d must be upgraded to current schema %d before backup", version, sqlite.CurrentSchemaVersion)
	}
	return version, nil
}

func openDatabase(path string) (*sql.DB, error) {
	dsnURL := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := dsnURL.Query()
	query.Add("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	dsnURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func vacuumInto(ctx context.Context, source, destination string) error {
	dsnURL := url.URL{Scheme: "file", Path: filepath.ToSlash(source)}
	query := dsnURL.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	dsnURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", dsnURL.String())
	if err != nil {
		return err
	}
	defer db.Close()
	escaped := strings.ReplaceAll(filepath.ToSlash(destination), "'", "''")
	if _, err := db.ExecContext(ctx, "VACUUM INTO '"+escaped+"'"); err != nil {
		return fmt.Errorf("create SQLite-consistent backup: %w", err)
	}
	return nil
}

func writeManifest(directory string, manifest Manifest) error {
	temp, err := os.CreateTemp(directory, ".manifest-")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(directory, manifestFilename))
}

func checksumFile(path, manifestPath string) (File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return File{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return File{}, fmt.Errorf("file %q is unsafe", manifestPath)
	}
	file, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return File{}, fmt.Errorf("file %q changed while opening", manifestPath)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return File{}, err
	}
	return File{Path: manifestPath, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

func copyRegular(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("source file is unsafe")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func validateDataDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("data directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	originalInfo, err := os.Lstat(absolute)
	if err != nil || originalInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("data directory must be a real directory")
	}
	evaluated, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	info, err := os.Lstat(evaluated)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("data directory must be a real directory")
	}
	return nil
}

func destinationParent(path string) (string, string, error) {
	if strings.TrimSpace(path) == "" {
		return "", "", fmt.Errorf("destination is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	parent, base := filepath.Dir(absolute), filepath.Base(absolute)
	if base == "." || base == string(filepath.Separator) || len(base) > 255 {
		return "", "", fmt.Errorf("destination path is invalid")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("destination parent must be a real directory")
	}
	evaluated, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", "", fmt.Errorf("resolve destination parent: %w", err)
	}
	return evaluated, base, nil
}

func validBackupPath(path string) bool {
	if !fs.ValidPath(path) || filepath.IsAbs(path) || strings.Contains(path, `\`) {
		return false
	}
	return path == "meridian.db" || validCASPath(path)
}

func validCASPath(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[0] != "artifacts" || parts[1] != "sha256" {
		return false
	}
	digest := parts[3]
	if len(parts[2]) != 2 || len(digest) != 64 || parts[2] != digest[:2] {
		return false
	}
	return validDigest(digest)
}

func validDigest(digest string) bool {
	if len(digest) != 64 || digest != strings.ToLower(digest) {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size
}

func syncTree(root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, directory := range directories {
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}
