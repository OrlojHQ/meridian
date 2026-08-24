// Package artifacts implements Meridian's local immutable content-addressed store.
package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

type Store struct {
	root     string
	blobs    string
	temp     string
	syncFile func(*os.File) error
	link     func(string, string) error
	syncDir  func(string) error
}

func Open(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("%w: artifact data directory is required", domain.ErrInvalid)
	}
	if err := secureDirectory(dataDir); err != nil {
		return nil, err
	}
	root := filepath.Join(dataDir, "artifacts")
	blobs, temp := filepath.Join(root, "sha256"), filepath.Join(root, "tmp")
	for _, path := range []string{root, blobs, temp} {
		if err := secureDirectory(path); err != nil {
			return nil, err
		}
	}
	store := &Store{
		root: root, blobs: blobs, temp: temp,
		syncFile: func(file *os.File) error { return file.Sync() },
		link:     os.Link,
		syncDir:  syncDirectory,
	}
	if err := store.cleanTemps(); err != nil {
		return nil, err
	}
	return store, nil
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create artifact directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("artifact path must be a real directory")
	}
	return os.Chmod(path, 0o700)
}

func (s *Store) cleanTemps() error {
	entries, err := os.ReadDir(s.temp)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return errors.New("unsafe entry in artifact temporary directory")
		}
		if err := os.Remove(filepath.Join(s.temp, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Publish(ctx context.Context, source io.Reader) (ports.PublishedArtifact, error) {
	if source == nil {
		return ports.PublishedArtifact{}, fmt.Errorf("%w: artifact source is required", domain.ErrInvalid)
	}
	if err := s.validateRoots(); err != nil {
		return ports.PublishedArtifact{}, err
	}
	temp, err := os.CreateTemp(s.temp, ".publish-*")
	if err != nil {
		return ports.PublishedArtifact{}, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return ports.PublishedArtifact{}, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(temp, hash), &contextReader{ctx: ctx, reader: source})
	if copyErr != nil {
		_ = temp.Close()
		return ports.PublishedArtifact{}, copyErr
	}
	if err := s.syncFile(temp); err != nil {
		_ = temp.Close()
		return ports.PublishedArtifact{}, err
	}
	if err := temp.Close(); err != nil {
		return ports.PublishedArtifact{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	directory := filepath.Join(s.blobs, digest[:2])
	if err := secureDirectory(directory); err != nil {
		return ports.PublishedArtifact{}, err
	}
	final := filepath.Join(directory, digest)
	if err := s.link(tempName, final); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return ports.PublishedArtifact{}, fmt.Errorf("publish artifact: %w", err)
		}
		if err := verifyFile(final, digest, size); err != nil {
			return ports.PublishedArtifact{}, err
		}
	} else {
		if err := s.syncDir(directory); err != nil {
			return ports.PublishedArtifact{}, err
		}
	}
	return ports.PublishedArtifact{Digest: digest, Size: size}, nil
}

func (s *Store) Open(ctx context.Context, digest string) (io.ReadCloser, int64, error) {
	if !validDigest(digest) {
		return nil, 0, fmt.Errorf("%w: invalid SHA-256 digest", domain.ErrInvalid)
	}
	if err := s.validateRoots(); err != nil {
		return nil, 0, err
	}
	prefix := filepath.Join(s.blobs, digest[:2])
	if info, err := os.Lstat(prefix); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, domain.ErrNotFound
		}
		return nil, 0, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, fmt.Errorf("%w: artifact prefix is unsafe", domain.ErrCorrupt)
	}
	path := filepath.Join(prefix, digest)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, domain.ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, fmt.Errorf("%w: artifact is not a regular file", domain.ErrCorrupt)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, 0, fmt.Errorf("%w: artifact changed while opening", domain.ErrCorrupt)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, &contextReader{ctx: ctx, reader: file})
	if err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		_ = file.Close()
		return nil, 0, fmt.Errorf("%w: digest %s does not match content", domain.ErrCorrupt, digest)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	return file, size, nil
}

func (s *Store) validateRoots() error {
	for _, path := range []string{s.root, s.blobs, s.temp} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return errors.New("artifact store directory is unsafe")
		}
	}
	return nil
}

func verifyFile(path, digest string, expectedSize int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != expectedSize {
		return fmt.Errorf("%w: existing artifact is unsafe or differs", domain.ErrCorrupt)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil || size != expectedSize || hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("%w: existing artifact differs", domain.ErrCorrupt)
	}
	return nil
}

func validDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
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

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(value []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(value)
}
