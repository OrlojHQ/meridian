package capsuleproto

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type ArchiveLimits struct {
	MaxFiles        int
	MaxPathBytes    int
	MaxFileBytes    int64
	MaxTotalBytes   int64
	MaxArchiveBytes int64
}

func (l ArchiveLimits) defaults() ArchiveLimits {
	if l.MaxFiles <= 0 {
		l.MaxFiles = 100_000
	}
	if l.MaxPathBytes <= 0 {
		l.MaxPathBytes = 4096
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = 2 << 30
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = 8 << 30
	}
	if l.MaxArchiveBytes <= 0 {
		l.MaxArchiveBytes = l.MaxTotalBytes + 256<<20
	}
	return l
}

// WriteWorkspaceArchive emits a deterministic, filesystem-only tar stream.
func WriteWorkspaceArchive(ctx context.Context, workspace string, output io.Writer, limits ArchiveLimits) error {
	return writeWorkspaceArchive(ctx, workspace, output, limits, "")
}

func writeWorkspaceArchive(
	ctx context.Context,
	workspace string,
	output io.Writer,
	limits ArchiveLimits,
	exclude string,
) error {
	limits = limits.defaults()
	var names []string
	err := filepath.WalkDir(workspace, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == workspace {
			return nil
		}
		if exclude != "" && filePath == exclude {
			return nil
		}
		relative, err := filepath.Rel(workspace, filePath)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if err := validateArchivePath(name, limits.MaxPathBytes); err != nil {
			return err
		}
		names = append(names, name)
		if len(names) > limits.MaxFiles {
			return errors.New("workspace exceeds archive file-count limit")
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(names)
	writer := tar.NewWriter(output)
	var total int64
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			_ = writer.Close()
			return err
		}
		full := filepath.Join(workspace, filepath.FromSlash(name))
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		mode := info.Mode()
		header := &tar.Header{
			Name: name, Mode: int64(mode.Perm()), Uid: 0, Gid: 0,
			ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Time{}, ChangeTime: time.Time{},
			Format: tar.FormatPAX,
		}
		switch {
		case mode.IsDir():
			header.Typeflag = tar.TypeDir
			header.Name += "/"
		case mode.IsRegular():
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
				return fmt.Errorf("hard-linked file %q is unsupported", name)
			}
			if info.Size() > limits.MaxFileBytes || total > limits.MaxTotalBytes-info.Size() {
				return errors.New("workspace exceeds archive expansion limits")
			}
			header.Typeflag, header.Size = tar.TypeReg, info.Size()
			total += info.Size()
		case mode&os.ModeSymlink != 0:
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			if err := validateSymlink(name, filepath.ToSlash(target), limits.MaxPathBytes); err != nil {
				return err
			}
			header.Typeflag, header.Linkname = tar.TypeSymlink, filepath.ToSlash(target)
		default:
			return fmt.Errorf("unsupported workspace file type %q", name)
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if mode.IsRegular() {
			file, err := openRegularNoSwap(full, info)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(writer, &archiveContextReader{ctx: ctx, reader: file})
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	return writer.Close()
}

type archiveEntry struct {
	header tar.Header
	offset int64
}

// RestoreWorkspaceArchive validates fully before replacing workspace contents.
func RestoreWorkspaceArchive(ctx context.Context, workspace string, input io.Reader, limits ArchiveLimits) error {
	return restoreWorkspaceArchive(ctx, workspace, input, limits, syncDirectoryPath)
}

// ExtractWorkspaceArchive validates the complete portable archive before
// extracting it into an existing empty staging directory. It never mutates a
// live workspace and is used by local sync before a separate atomic mirror.
func ExtractWorkspaceArchive(
	ctx context.Context, stage string, input io.Reader, limits ArchiveLimits,
) error {
	limits = limits.defaults()
	info, err := os.Lstat(stage)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("archive staging path must be a real directory")
	}
	entries, err := os.ReadDir(stage)
	if err != nil || len(entries) != 0 {
		return errors.New("archive staging directory must be empty")
	}
	archive, err := os.CreateTemp(filepath.Dir(stage), ".meridian-sync-archive-*")
	if err != nil {
		return err
	}
	name := archive.Name()
	defer os.Remove(name)
	if err := archive.Chmod(0o600); err != nil {
		_ = archive.Close()
		return err
	}
	written, err := io.Copy(
		archive,
		io.LimitReader(&archiveContextReader{ctx: ctx, reader: input}, limits.MaxArchiveBytes+1),
	)
	if err != nil || written > limits.MaxArchiveBytes {
		_ = archive.Close()
		if err != nil {
			return err
		}
		return errors.New("archive exceeds compressed-size limit")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		_ = archive.Close()
		return err
	}
	if err := validateTar(ctx, archive, limits); err != nil {
		_ = archive.Close()
		return err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		_ = archive.Close()
		return err
	}
	if err := extractTar(ctx, archive, stage, limits); err != nil {
		_ = archive.Close()
		return err
	}
	return archive.Close()
}

func restoreWorkspaceArchive(
	ctx context.Context,
	workspace string,
	input io.Reader,
	limits ArchiveLimits,
	syncWorkspace func(string) error,
) error {
	limits = limits.defaults()
	if err := cleanupRestoreState(workspace); err != nil {
		return err
	}
	archive, err := os.CreateTemp(workspace, ".meridian-restore-archive-*")
	if err != nil {
		return err
	}
	archiveName := archive.Name()
	defer os.Remove(archiveName)
	if err := archive.Chmod(0o600); err != nil {
		_ = archive.Close()
		return err
	}
	written, err := io.Copy(archive, io.LimitReader(&archiveContextReader{ctx: ctx, reader: input}, limits.MaxArchiveBytes+1))
	if err != nil {
		_ = archive.Close()
		return err
	}
	if written > limits.MaxArchiveBytes {
		_ = archive.Close()
		return errors.New("archive exceeds compressed-size limit")
	}
	if err := archive.Sync(); err != nil {
		_ = archive.Close()
		return err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		_ = archive.Close()
		return err
	}
	if err := validateTar(ctx, archive, limits); err != nil {
		_ = archive.Close()
		return err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		_ = archive.Close()
		return err
	}
	stage, err := os.MkdirTemp(workspace, ".meridian-restore-stage-*")
	if err != nil {
		_ = archive.Close()
		return err
	}
	defer os.RemoveAll(stage)
	if err := extractTar(ctx, archive, stage, limits); err != nil {
		_ = archive.Close()
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return replaceWorkspace(workspace, stage, archiveName, syncWorkspace)
}

func validateTar(ctx context.Context, reader io.Reader, limits ArchiveLimits) error {
	tr := tar.NewReader(reader)
	seen := make(map[string]byte)
	var count int
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name := strings.TrimSuffix(header.Name, "/")
		if err := validateArchivePath(name, limits.MaxPathBytes); err != nil {
			return err
		}
		count++
		if count > limits.MaxFiles {
			return errors.New("archive exceeds file-count limit")
		}
		kind := byte(header.Typeflag)
		if kind == tar.TypeRegA {
			kind = tar.TypeReg
		}
		if kind != tar.TypeReg && kind != tar.TypeDir && kind != tar.TypeSymlink {
			return fmt.Errorf("archive entry %q has unsupported type", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("archive contains duplicate entry %q", name)
		}
		for ancestor := path.Dir(name); ancestor != "."; ancestor = path.Dir(ancestor) {
			if seen[ancestor] == tar.TypeSymlink || seen[ancestor] == tar.TypeReg {
				return fmt.Errorf("archive entry %q traverses non-directory %q", name, ancestor)
			}
		}
		for existing := range seen {
			if strings.HasPrefix(existing, name+"/") && kind != tar.TypeDir {
				return fmt.Errorf("archive entry %q conflicts with child %q", name, existing)
			}
		}
		if kind == tar.TypeSymlink {
			if err := validateSymlink(name, header.Linkname, limits.MaxPathBytes); err != nil {
				return err
			}
		}
		if kind == tar.TypeReg {
			if header.Size < 0 || header.Size > limits.MaxFileBytes || total > limits.MaxTotalBytes-header.Size {
				return errors.New("archive exceeds expansion limits")
			}
			total += header.Size
		}
		if header.PAXRecords != nil {
			for key := range header.PAXRecords {
				if strings.HasPrefix(strings.ToLower(key), "schily.xattr.") {
					return fmt.Errorf("archive entry %q contains unsupported xattrs", name)
				}
			}
		}
		seen[name] = kind
	}
}

func extractTar(ctx context.Context, reader io.Reader, stage string, limits ArchiveLimits) error {
	tr := tar.NewReader(reader)
	type pendingLink struct{ name, target string }
	var links []pendingLink
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		target := filepath.Join(stage, filepath.FromSlash(name))
		if !within(stage, target) {
			return errors.New("archive path escapes staging directory")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			mode := os.FileMode(header.Mode) & 0o777
			if err := os.MkdirAll(target, mode); err != nil {
				return err
			}
			if err := os.Chmod(target, mode); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := ensureNoSymlinkParents(stage, filepath.Dir(target)); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, &archiveContextReader{ctx: ctx, reader: tr})
			if copyErr == nil {
				copyErr = file.Sync()
			}
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			links = append(links, pendingLink{name: target, target: header.Linkname})
		}
	}
	for _, link := range links {
		if err := os.MkdirAll(filepath.Dir(link.name), 0o755); err != nil {
			return err
		}
		if err := ensureNoSymlinkParents(stage, filepath.Dir(link.name)); err != nil {
			return err
		}
		if err := os.Symlink(filepath.FromSlash(link.target), link.name); err != nil {
			return err
		}
	}
	return syncTree(stage)
}

func validateArchivePath(name string, max int) error {
	if name == "" || len(name) > max || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') ||
		strings.Contains(name, "\\") || path.IsAbs(name) || path.Clean(name) != name ||
		name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return fmt.Errorf("unsafe archive path %q", name)
	}
	return nil
}

func validateSymlink(name, target string, max int) error {
	if target == "" || len(target) > max || !utf8.ValidString(target) || strings.ContainsRune(target, '\x00') ||
		strings.Contains(target, "\\") || path.IsAbs(target) {
		return fmt.Errorf("unsafe symlink target for %q", name)
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("symlink %q escapes workspace", name)
	}
	return nil
}

func openRegularNoSwap(name string, expected os.FileInfo) (*os.File, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		_ = file.Close()
		return nil, errors.New("workspace file changed during capture")
	}
	return file, nil
}

func ensureNoSymlinkParents(root, directory string) error {
	for current := directory; current != root; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("archive extraction encountered unsafe parent")
		}
	}
	return nil
}

func replaceWorkspace(
	workspace, stage, archiveName string,
	syncWorkspace func(string) error,
) error {
	backup, err := os.MkdirTemp(workspace, ".meridian-restore-backup-*")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			_ = os.RemoveAll(backup)
		}
	}()
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		full := filepath.Join(workspace, entry.Name())
		if full == stage || full == backup || full == archiveName {
			continue
		}
		if err := os.Rename(full, filepath.Join(backup, entry.Name())); err != nil {
			return rollbackInstalled(workspace, backup, nil, err)
		}
	}
	staged, err := os.ReadDir(stage)
	if err != nil {
		return rollbackInstalled(workspace, backup, nil, err)
	}
	installed := make([]string, 0, len(staged))
	for _, entry := range staged {
		if err := os.Rename(filepath.Join(stage, entry.Name()), filepath.Join(workspace, entry.Name())); err != nil {
			return rollbackInstalled(workspace, backup, installed, err)
		}
		installed = append(installed, entry.Name())
	}
	if err := syncWorkspace(workspace); err != nil {
		return rollbackInstalled(workspace, backup, installed, err)
	}
	committed = true
	return nil
}

func rollbackInstalled(workspace, backup string, installed []string, cause error) error {
	rollbackErrors := []error{cause}
	for _, name := range installed {
		if err := os.RemoveAll(filepath.Join(workspace, name)); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("remove installed %q: %w", name, err))
		}
	}
	if err := rollbackWorkspace(workspace, backup); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("restore original workspace: %w", err))
	} else if err := os.RemoveAll(backup); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("remove restore backup: %w", err))
	}
	return errors.Join(rollbackErrors...)
}

func rollbackWorkspace(workspace, backup string) error {
	entries, err := os.ReadDir(backup)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(backup, entry.Name()), filepath.Join(workspace, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func cleanupRestoreState(workspace string) error {
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return err
	}
	var backups []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".meridian-restore-backup-") && entry.IsDir() {
			backups = append(backups, filepath.Join(workspace, entry.Name()))
		}
	}
	if len(backups) > 1 {
		return errors.New("multiple restore backups require manual recovery")
	}
	if len(backups) == 1 {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".meridian-restore-") {
				continue
			}
			if err := os.RemoveAll(filepath.Join(workspace, entry.Name())); err != nil {
				return err
			}
		}
		if err := rollbackWorkspace(workspace, backups[0]); err != nil {
			return err
		}
	}
	entries, err = os.ReadDir(workspace)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".meridian-restore-") ||
			strings.HasPrefix(entry.Name(), ".meridian-capture-") {
			if err := os.RemoveAll(filepath.Join(workspace, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func syncTree(root string) error {
	return filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return err
		}
		return syncDirectoryPath(name)
	})
}

func syncDirectoryPath(name string) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type archiveContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *archiveContextReader) Read(value []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(value)
}
