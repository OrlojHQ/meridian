package capsuleproto

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

// coordFiles writes toFile patches and reports for a lead Capsule into a
// private directory under the Capsule user's home (ADR 0059). The directory
// is outside the workspace, so a Delivery never captures it, and nothing in
// it leaves the Capsule. Its contents are hostile subagent output, exactly as
// the inline forms are; the files only keep that output out of the lead's
// transcript until the agent reads it with the shell.
type coordFiles struct {
	mu    sync.Mutex
	dir   string
	quota int64
}

// CoordinationFilesDir is where capsuled keeps toFile output for a mailbox
// socket at $HOME/.meridian/coord.sock: $HOME/.meridian/coord.
func CoordinationFilesDir(socket string) string {
	if socket == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(socket), coordproto.FilesDirName)
}

// OutsideWorkspace reports whether dir lies outside workspace, so files
// written there never reach a Delivery.
func OutsideWorkspace(workspace, dir string) bool {
	relative, err := filepath.Rel(workspace, dir)
	return err == nil && (relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// validFileLink limits a link ID used as a directory name to characters that
// cannot form a path.
func validFileLink(link string) bool {
	if link == "" || len(link) > coordproto.MaxIDBytes || strings.HasPrefix(link, ".") {
		return false
	}
	for _, r := range link {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

// write stores data as <dir>/<link>/<seq>.<ext> with mode 0600 in 0700
// directories, replacing an earlier file of the same name, after removing
// the oldest files until the directory fits its quota. It returns the
// file's absolute path.
func (f *coordFiles) write(link string, seq int, ext string, data []byte) (string, error) {
	if !validFileLink(link) || seq < 0 || (ext != "patch" && ext != "md") {
		return "", errors.New("invalid coordination file name")
	}
	if int64(len(data)) > f.quota {
		return "", errors.New("coordination file exceeds the file quota")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	root, err := f.openRoot()
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.Mkdir(link, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("create coordination file directory: %w", err)
	}
	if info, err := root.Lstat(link); err != nil || !info.IsDir() {
		return "", errors.New("coordination file directory is not a directory")
	}
	if err := root.Chmod(link, 0o700); err != nil {
		return "", fmt.Errorf("protect coordination file directory: %w", err)
	}
	name := path.Join(link, strconv.Itoa(seq)+"."+ext)
	if err := f.evict(root, name, int64(len(data))); err != nil {
		return "", err
	}
	temporary := path.Join(link, "."+strconv.Itoa(seq)+"."+ext+"."+randomHex(6)+".tmp")
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create coordination file: %w", err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = root.Remove(temporary)
		return "", fmt.Errorf("write coordination file: %w", err)
	}
	if err := root.Chmod(temporary, 0o600); err != nil {
		_ = root.Remove(temporary)
		return "", fmt.Errorf("protect coordination file: %w", err)
	}
	if err := root.Rename(temporary, name); err != nil {
		_ = root.Remove(temporary)
		return "", fmt.Errorf("place coordination file: %w", err)
	}
	return filepath.Join(f.dir, filepath.FromSlash(name)), nil
}

// openRoot creates the private directory if needed and opens it as a root,
// so no symlink planted inside it can redirect a write elsewhere.
func (f *coordFiles) openRoot() (*os.Root, error) {
	// The parent is $HOME/.meridian, which also holds the mailbox socket.
	if err := os.MkdirAll(filepath.Dir(f.dir), 0o700); err != nil {
		return nil, fmt.Errorf("create coordination file directory: %w", err)
	}
	if err := os.Mkdir(f.dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create coordination file directory: %w", err)
	}
	info, err := os.Lstat(f.dir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("coordination file directory is not a directory")
	}
	if err := os.Chmod(f.dir, 0o700); err != nil {
		return nil, fmt.Errorf("protect coordination file directory: %w", err)
	}
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return nil, fmt.Errorf("open coordination file directory: %w", err)
	}
	return root, nil
}

type coordFile struct {
	name     string
	size     int64
	modified time.Time
}

// evict removes the oldest files until incoming bytes fit the quota. The
// file being replaced is not counted, since the new one takes its place.
func (f *coordFiles) evict(root *os.Root, replacing string, incoming int64) error {
	var files []coordFile
	var total int64
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.Type().IsRegular() || name == replacing {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		files = append(files, coordFile{name: name, size: info.Size(), modified: info.ModTime()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan coordination files: %w", err)
	}
	if total+incoming <= f.quota {
		return nil
	}
	slices.SortFunc(files, func(a, b coordFile) int {
		if order := a.modified.Compare(b.modified); order != 0 {
			return order
		}
		return strings.Compare(a.name, b.name)
	})
	for _, file := range files {
		if total+incoming <= f.quota {
			break
		}
		if err := root.Remove(file.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove old coordination file: %w", err)
		}
		total -= file.size
	}
	return nil
}
