package capsuleproto

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	BrowseListPath       = "/v1/workspace/files/list"
	BrowseReadPath       = "/v1/workspace/files/read"
	maxBrowsePathBytes   = 4096
	maxBrowseNameBytes   = 255
	maxBrowseEntries     = 512
	maxBrowseDirEntries  = 4096
	defaultBrowseEntries = 128
	maxBrowseFileBytes   = 1 << 20
)

type BrowseListRequest struct {
	Path  string `json:"path,omitempty"`
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type BrowseEntry struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable,omitempty"`
}

type BrowseListResponse struct {
	Path      string        `json:"path,omitempty"`
	Items     []BrowseEntry `json:"items"`
	NextAfter string        `json:"nextAfter,omitempty"`
}

type BrowseReadRequest struct {
	Path string `json:"path"`
}

type BrowseReadResponse struct {
	Path       string `json:"path"`
	Content    []byte `json:"content"`
	Size       int64  `json:"size"`
	Executable bool   `json:"executable,omitempty"`
}

func (s *Server) browseList(writer http.ResponseWriter, request *http.Request) {
	var input BrowseListRequest
	if !decodeRequest(writer, request, s.config.BodyLimit, &input) ||
		validateBrowsePath(input.Path, true) != nil ||
		validateBrowseName(input.After, true) != nil ||
		input.Limit < 0 || input.Limit > maxBrowseEntries {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	limit := input.Limit
	if limit == 0 {
		limit = defaultBrowseEntries
	}
	directory, err := openWorkspaceAt(s.config.Workspace, input.Path, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		writeProtocolError(writer, browseStatus(err), "browse_unavailable")
		return
	}
	defer directory.Close()
	entries, err := directory.ReadDir(maxBrowseDirEntries + 1)
	if err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "browse_unavailable")
		return
	}
	if len(entries) > maxBrowseDirEntries {
		writeProtocolError(writer, http.StatusRequestEntityTooLarge, "directory_too_large")
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	items := make([]BrowseEntry, 0, min(limit, len(entries)))
	next := ""
	for _, entry := range entries {
		name := entry.Name()
		if browseDeniedName(name) || name <= input.After {
			continue
		}
		if validateBrowseName(name, false) != nil {
			writeProtocolError(writer, http.StatusUnprocessableEntity, "browse_unavailable")
			return
		}
		if len(items) == limit {
			next = items[len(items)-1].Name
			break
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(
			int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW,
		); err != nil {
			writeProtocolError(writer, http.StatusUnprocessableEntity, "browse_unavailable")
			return
		}
		mode := uint32(stat.Mode)
		items = append(items, BrowseEntry{
			Name: name, Type: browseFileType(mode), Size: stat.Size,
			Executable: mode&unix.S_IFMT == unix.S_IFREG && mode&0o111 != 0,
		})
	}
	writeJSON(writer, http.StatusOK, BrowseListResponse{
		Path: input.Path, Items: items, NextAfter: next,
	})
}

func (s *Server) browseRead(writer http.ResponseWriter, request *http.Request) {
	var input BrowseReadRequest
	if !decodeRequest(writer, request, s.config.BodyLimit, &input) ||
		validateBrowsePath(input.Path, false) != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	file, err := openWorkspaceRegular(s.config.Workspace, input.Path)
	if err != nil {
		writeProtocolError(writer, browseStatus(err), "browse_unavailable")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "not_regular_file")
		return
	}
	if info.Size() < 0 || info.Size() > maxBrowseFileBytes {
		writeProtocolError(writer, http.StatusRequestEntityTooLarge, "file_too_large")
		return
	}
	content, err := io.ReadAll(io.LimitReader(file, maxBrowseFileBytes+1))
	if err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "browse_unavailable")
		return
	}
	if len(content) > maxBrowseFileBytes {
		writeProtocolError(writer, http.StatusRequestEntityTooLarge, "file_too_large")
		return
	}
	writeJSON(writer, http.StatusOK, BrowseReadResponse{
		Path: input.Path, Content: content, Size: int64(len(content)),
		Executable: info.Mode().Perm()&0o111 != 0,
	})
}

func validateBrowsePath(value string, allowRoot bool) error {
	if value == "" {
		if allowRoot {
			return nil
		}
		return errors.New("file path is required")
	}
	if len(value) > maxBrowsePathBytes || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\x00\\") || path.IsAbs(value) ||
		path.Clean(value) != value || value == "." || value == ".." ||
		strings.HasPrefix(value, "../") {
		return errors.New("unsafe browse path")
	}
	for _, component := range strings.Split(value, "/") {
		if validateBrowseName(component, false) != nil || browseDeniedName(component) {
			return errors.New("denied browse path")
		}
	}
	return nil
}

func validateBrowseName(value string, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	if value == "" || len(value) > maxBrowseNameBytes || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\x00/\\") || value == "." || value == ".." {
		return errors.New("invalid browse name")
	}
	return nil
}

func browseDeniedName(name string) bool {
	return name == ".git" || name == ".meridian-prepared"
}

func openWorkspaceAt(workspace, relative string, finalFlags int) (*os.File, error) {
	rootFD, err := unix.Open(workspace, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	current := rootFD
	parts := []string(nil)
	if relative != "" {
		parts = strings.Split(relative, "/")
	}
	for index, component := range parts {
		flags := unix.O_RDONLY | unix.O_DIRECTORY
		if index == len(parts)-1 {
			flags = finalFlags
		}
		next, openErr := unix.Openat(current, component, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if current != rootFD || openErr != nil {
			_ = unix.Close(current)
		}
		if openErr != nil {
			if current == rootFD {
				_ = unix.Close(rootFD)
			}
			return nil, openErr
		}
		if current == rootFD {
			_ = unix.Close(rootFD)
		}
		current = next
	}
	return os.NewFile(uintptr(current), "workspace:"+relative), nil
}

func openWorkspaceRegular(workspace, relative string) (*os.File, error) {
	parentName, name := path.Split(relative)
	parentName = strings.TrimSuffix(parentName, "/")
	parent, err := openWorkspaceAt(workspace, parentName, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	var expected unix.Stat_t
	if err := unix.Fstatat(
		int(parent.Fd()), name, &expected, unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return nil, err
	}
	if uint32(expected.Mode)&unix.S_IFMT != unix.S_IFREG {
		return nil, errors.New("browse target is not a regular file")
	}
	fd, err := unix.Openat(
		int(parent.Fd()), name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0,
	)
	if err != nil {
		return nil, err
	}
	var actual unix.Stat_t
	if err := unix.Fstat(fd, &actual); err != nil ||
		uint32(actual.Mode)&unix.S_IFMT != unix.S_IFREG ||
		actual.Dev != expected.Dev || actual.Ino != expected.Ino {
		_ = unix.Close(fd)
		return nil, errors.New("browse target changed while opening")
	}
	return os.NewFile(uintptr(fd), "workspace:"+relative), nil
}

func browseFileType(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return "file"
	case unix.S_IFDIR:
		return "directory"
	case unix.S_IFLNK:
		return "symlink"
	default:
		return "other"
	}
}

func browseStatus(err error) int {
	if errors.Is(err, os.ErrNotExist) {
		return http.StatusNotFound
	}
	return http.StatusUnprocessableEntity
}
