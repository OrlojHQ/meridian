// Package capsuleproto defines Meridian's private, versioned Capsule supervisor protocol.
package capsuleproto

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	Version       = "meridian.capsule.v1"
	VersionHeader = "Meridian-Protocol-Version"
	HealthPath    = "/healthz"
	ReadinessPath = "/v1/readyz"
	StatusPath    = "/v1/status"
	PreparePath   = "/v1/prepare"
	RunStartPath  = "/v1/runs"
	GitStatusPath = "/v1/git/status"
	GitDiffPath   = "/v1/git/diff"
	CapturePath   = "/v1/workspace/capture"
	RestorePath   = "/v1/workspace/restore"
	PreviewsPath  = "/v1/previews"
	DeliveryPath  = "/v1/delivery"

	defaultBodyLimit   = int64(64 << 10)
	defaultOutputLimit = int64(1 << 20)
	defaultRunLimit    = 8
)

type PreparationState string

const (
	StateUnprepared PreparationState = "unprepared"
	StatePreparing  PreparationState = "preparing"
	StateReady      PreparationState = "ready"
	StateFailed     PreparationState = "failed"
)

type ProbeResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

type StatusResponse struct {
	Version     string           `json:"version"`
	Preparation PreparationState `json:"preparation"`
	Error       string           `json:"error,omitempty"`
}

type PrepareRequest struct {
	RepositoryURL string              `json:"repositoryUrl,omitempty"`
	Destination   string              `json:"destination"`
	Setup         []string            `json:"setup,omitempty"`
	GitCredential *GitHTTPSCredential `json:"gitCredential,omitempty"`
}

type GitHTTPSCredential struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password"`
}

type PrepareResponse struct {
	Status PreparationState `json:"status"`
}

type ServerConfig struct {
	Token                   string
	Workspace               string
	TrustedHarnessDirectory string
	SetupTimeout            time.Duration
	BodyLimit               int64
	OutputLimit             int64
	CommandRunner           func(context.Context, string, []string, string, int64) error
	RunLimit                int
	EventLimit              int
	DiffLimit               int64
	PreviewTimeout          time.Duration
	PreviewBodyLimit        int64
	PreviewPortDiscovery    func() ([]uint16, error)
	ArchiveLimits           ArchiveLimits
}

type Server struct {
	config     ServerConfig
	token      [sha256.Size]byte
	gate       sync.Mutex
	mu         sync.Mutex
	state      PreparationState
	err        string
	runs       map[string]*supervisedRun
	structured map[string]*structuredSession
}

func NewServer(config ServerConfig) (*Server, error) {
	if len(config.Token) < 32 {
		return nil, errors.New("protocol token must contain at least 32 characters")
	}
	if !filepath.IsAbs(config.Workspace) || filepath.Clean(config.Workspace) != config.Workspace {
		return nil, errors.New("workspace must be an absolute clean path")
	}
	if config.TrustedHarnessDirectory == "" {
		config.TrustedHarnessDirectory = defaultTrustedHarnessDirectory
	}
	if !filepath.IsAbs(config.TrustedHarnessDirectory) ||
		filepath.Clean(config.TrustedHarnessDirectory) != config.TrustedHarnessDirectory {
		return nil, errors.New("trusted harness directory must be an absolute clean path")
	}
	if config.SetupTimeout <= 0 {
		config.SetupTimeout = 10 * time.Minute
	}
	if config.BodyLimit <= 0 {
		config.BodyLimit = defaultBodyLimit
	}
	if config.OutputLimit <= 0 {
		config.OutputLimit = defaultOutputLimit
	}
	if config.CommandRunner == nil {
		config.CommandRunner = runCommand
	}
	if config.RunLimit <= 0 {
		config.RunLimit = defaultRunLimit
	}
	if config.EventLimit <= 0 {
		config.EventLimit = 2048
	}
	if config.DiffLimit <= 0 {
		config.DiffLimit = defaultOutputLimit
	}
	if config.PreviewTimeout <= 0 || config.PreviewTimeout > previewRequestTimeout {
		config.PreviewTimeout = previewRequestTimeout
	}
	if config.PreviewBodyLimit <= 0 || config.PreviewBodyLimit > maxPreviewBodyBytes {
		config.PreviewBodyLimit = maxPreviewBodyBytes
	}
	if config.PreviewPortDiscovery == nil {
		config.PreviewPortDiscovery = discoverListeningPorts
	}
	server := &Server{
		config:     config,
		token:      sha256.Sum256([]byte(config.Token)),
		state:      StateUnprepared,
		runs:       make(map[string]*supervisedRun),
		structured: make(map[string]*structuredSession),
	}
	if err := ensureWorkspace(config.Workspace); err != nil {
		return nil, err
	}
	if err := cleanupRestoreState(config.Workspace); err != nil {
		return nil, err
	}
	if _, err := server.prepared(config.Workspace, PrepareRequest{}); err != nil {
		return nil, err
	}
	return server, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+HealthPath, s.health)
	mux.Handle("GET "+ReadinessPath, s.authenticate(http.HandlerFunc(s.readiness)))
	mux.Handle("GET "+StatusPath, s.authenticate(http.HandlerFunc(s.status)))
	mux.Handle("POST "+PreparePath, s.authenticate(http.HandlerFunc(s.prepare)))
	mux.Handle("POST "+RunStartPath, s.authenticate(http.HandlerFunc(s.startRun)))
	mux.Handle("GET /v1/runs/{runID}", s.authenticate(http.HandlerFunc(s.runStatus)))
	mux.Handle("POST /v1/runs/{runID}/cancel", s.authenticate(http.HandlerFunc(s.cancelRun)))
	mux.Handle("GET /v1/runs/{runID}/events", s.authenticate(http.HandlerFunc(s.runEvents)))
	mux.Handle("GET /v1/runs/{runID}/attach", s.authenticate(http.HandlerFunc(s.attachRun)))
	mux.Handle("POST /v1/structured/sessions", s.authenticate(http.HandlerFunc(s.startStructured)))
	mux.Handle("GET /v1/structured/sessions/{runID}", s.authenticate(http.HandlerFunc(s.structuredStatus)))
	mux.Handle("POST /v1/structured/sessions/{runID}/frames", s.authenticate(http.HandlerFunc(s.sendStructured)))
	mux.Handle("GET /v1/structured/sessions/{runID}/events", s.authenticate(http.HandlerFunc(s.structuredEvents)))
	mux.Handle("POST /v1/structured/sessions/{runID}/cancel", s.authenticate(http.HandlerFunc(s.cancelStructured)))
	mux.Handle("GET /v1/harness-profiles", s.authenticate(http.HandlerFunc(s.harnessProfiles)))
	mux.Handle("GET "+GitStatusPath, s.authenticate(http.HandlerFunc(s.gitStatus)))
	mux.Handle("GET "+GitDiffPath, s.authenticate(http.HandlerFunc(s.gitDiff)))
	mux.Handle("GET "+CapturePath, s.authenticate(http.HandlerFunc(s.captureWorkspace)))
	mux.Handle("PUT "+RestorePath, s.authenticate(http.HandlerFunc(s.restoreWorkspace)))
	mux.Handle("POST "+BrowseListPath, s.authenticate(http.HandlerFunc(s.browseList)))
	mux.Handle("POST "+BrowseReadPath, s.authenticate(http.HandlerFunc(s.browseRead)))
	mux.Handle("GET "+DeliveryPath+"/state", s.authenticate(http.HandlerFunc(s.deliveryState)))
	mux.Handle("POST "+DeliveryPath+"/commit", s.authenticate(http.HandlerFunc(s.deliveryCommit)))
	mux.Handle("POST "+DeliveryPath+"/push", s.authenticate(http.HandlerFunc(s.deliveryPush)))
	mux.Handle("GET "+PreviewsPath, s.authenticate(http.HandlerFunc(s.previewPorts)))
	mux.Handle(PreviewsPath+"/{port}/{path...}", s.authenticate(http.HandlerFunc(s.forwardPreview)))
	return mux
}

func (s *Server) captureWorkspace(writer http.ResponseWriter, request *http.Request) {
	s.gate.Lock()
	defer s.gate.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasActiveRunLocked() {
		writeProtocolError(writer, http.StatusConflict, "run_active")
		return
	}
	branch, head, dirty := gitSnapshotMetadata(request.Context(), s.config.Workspace)
	archive, err := os.CreateTemp(s.config.Workspace, ".meridian-capture-*")
	if err != nil {
		writeProtocolError(writer, http.StatusInternalServerError, "capture_failed")
		return
	}
	archiveName := archive.Name()
	defer os.Remove(archiveName)
	if err := archive.Chmod(0o600); err != nil {
		_ = archive.Close()
		writeProtocolError(writer, http.StatusInternalServerError, "capture_failed")
		return
	}
	if err := writeWorkspaceArchive(
		request.Context(), s.config.Workspace, archive, s.config.ArchiveLimits, archiveName,
	); err != nil {
		_ = archive.Close()
		writeProtocolError(writer, http.StatusUnprocessableEntity, "capture_failed")
		return
	}
	if err := archive.Sync(); err != nil {
		_ = archive.Close()
		writeProtocolError(writer, http.StatusInternalServerError, "capture_failed")
		return
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		_ = archive.Close()
		writeProtocolError(writer, http.StatusInternalServerError, "capture_failed")
		return
	}
	writer.Header().Set("Content-Type", "application/x-tar")
	writer.Header().Set("Meridian-Git-Branch", encodeMetadata(branch))
	writer.Header().Set("Meridian-Git-Head", encodeMetadata(head))
	writer.Header().Set("Meridian-Git-Dirty", encodeMetadata(dirty))
	writer.Header().Set(VersionHeader, Version)
	writer.WriteHeader(http.StatusOK)
	_, _ = io.Copy(writer, archive)
	_ = archive.Close()
}

func (s *Server) restoreWorkspace(writer http.ResponseWriter, request *http.Request) {
	expected := request.Header.Get("Meridian-Archive-SHA256")
	decodedDigest, digestErr := hex.DecodeString(expected)
	if len(expected) != 64 || expected != strings.ToLower(expected) ||
		digestErr != nil || len(decodedDigest) != sha256.Size {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_digest")
		return
	}
	s.gate.Lock()
	defer s.gate.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasActiveRunLocked() {
		writeProtocolError(writer, http.StatusConflict, "run_active")
		return
	}
	limits := s.config.ArchiveLimits.defaults()
	temp, err := os.CreateTemp(s.config.Workspace, ".meridian-restore-upload-*")
	if err != nil {
		writeProtocolError(writer, http.StatusInternalServerError, "restore_failed")
		return
	}
	name := temp.Name()
	defer os.Remove(name)
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(request.Body, limits.MaxArchiveBytes+1))
	if err != nil || size > limits.MaxArchiveBytes ||
		hex.EncodeToString(hash.Sum(nil)) != expected {
		_ = temp.Close()
		writeProtocolError(writer, http.StatusUnprocessableEntity, "archive_digest_mismatch")
		return
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		_ = temp.Close()
		writeProtocolError(writer, http.StatusInternalServerError, "restore_failed")
		return
	}
	if err := RestoreWorkspaceArchive(request.Context(), s.config.Workspace, temp, limits); err != nil {
		_ = temp.Close()
		writeProtocolError(writer, http.StatusUnprocessableEntity, "archive_invalid")
		return
	}
	_ = temp.Close()
	s.state, s.err = StateReady, ""
	writeJSON(writer, http.StatusOK, map[string]string{"status": "restored", "digest": expected})
}

func (s *Server) hasActiveRunLocked() bool {
	for _, run := range s.runs {
		run.mu.Lock()
		active := run.state != RunSucceeded && run.state != RunFailed && run.state != RunCancelled
		run.mu.Unlock()
		if active {
			return true
		}
	}
	return false
}

func gitSnapshotMetadata(ctx context.Context, workspace string) (string, string, string) {
	run := func(arguments ...string) string {
		command := exec.CommandContext(ctx, "git", append([]string{"-C", workspace}, arguments...)...)
		value, err := command.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(value))
	}
	branch := run("symbolic-ref", "--quiet", "--short", "HEAD")
	head := run("rev-parse", "--verify", "HEAD")
	dirty := run("status", "--porcelain=v1", "--untracked-files=normal")
	if len(branch) > 512 {
		branch = branch[:512]
	}
	if len(head) > 128 {
		head = head[:128]
	}
	if len(dirty) > 4096 {
		dirty = dirty[:4096]
	}
	return branch, head, dirty
}

func encodeMetadata(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, ProbeResponse{Status: "ok", Version: Version})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get(VersionHeader) != Version {
			writeProtocolError(writer, http.StatusUpgradeRequired, "protocol_version_mismatch")
			return
		}
		const prefix = "Bearer "
		header := request.Header.Get("Authorization")
		if !strings.HasPrefix(header, prefix) || len(header) == len(prefix) {
			writeProtocolError(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		candidate := sha256.Sum256([]byte(header[len(prefix):]))
		if subtle.ConstantTimeCompare(candidate[:], s.token[:]) != 1 {
			writeProtocolError(writer, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) status(writer http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(writer, http.StatusOK, StatusResponse{
		Version: Version, Preparation: s.state, Error: s.err,
	})
}

func (s *Server) readiness(writer http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := http.StatusServiceUnavailable
	if s.state == StateReady {
		status = http.StatusOK
	}
	writeJSON(writer, status, StatusResponse{
		Version: Version, Preparation: s.state, Error: s.err,
	})
}

func (s *Server) prepare(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, s.config.BodyLimit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input PrepareRequest
	if err := decoder.Decode(&input); err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := s.validateRequest(input); err != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	if input.GitCredential != nil {
		defer func() {
			input.GitCredential.Username = ""
			input.GitCredential.Password = ""
			input.GitCredential = nil
		}()
	}

	s.gate.Lock()
	defer s.gate.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	prepared, err := s.prepared(input.Destination, input)
	if err != nil {
		s.state, s.err = StateFailed, "workspace validation failed"
		writeProtocolError(writer, http.StatusUnprocessableEntity, "workspace_invalid")
		return
	}
	if prepared {
		s.state, s.err = StateReady, ""
		writeJSON(writer, http.StatusOK, PrepareResponse{Status: StateReady})
		return
	}
	s.state, s.err = StatePreparing, ""
	ctx, cancel := context.WithTimeout(request.Context(), s.config.SetupTimeout)
	defer cancel()
	if err := s.prepareWorkspace(ctx, input); err != nil {
		s.state, s.err = StateFailed, boundedError(err)
		writeProtocolError(writer, http.StatusUnprocessableEntity, "preparation_failed")
		return
	}
	s.state, s.err = StateReady, ""
	writeJSON(writer, http.StatusOK, PrepareResponse{Status: StateReady})
}

func (s *Server) validateRequest(input PrepareRequest) error {
	if input.Destination != s.config.Workspace || filepath.Clean(input.Destination) != input.Destination {
		return errors.New("destination must be the configured workspace")
	}
	if strings.ContainsAny(input.RepositoryURL, "\x00\r\n") || strings.HasPrefix(input.RepositoryURL, "-") {
		return errors.New("invalid repository URL")
	}
	if len(input.RepositoryURL) > 4096 || len(input.Setup) > 128 {
		return errors.New("request exceeds argument limits")
	}
	if input.GitCredential != nil {
		parsed, err := url.Parse(input.RepositoryURL)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
			input.GitCredential.Password == "" ||
			strings.ContainsAny(input.GitCredential.Username+input.GitCredential.Password, "\x00\r\n") {
			return errors.New("Git credential requires a safe HTTPS repository URL")
		}
	}
	for _, argument := range input.Setup {
		if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, '\x00') {
			return errors.New("invalid setup argument")
		}
	}
	return nil
}

func (s *Server) prepareWorkspace(ctx context.Context, input PrepareRequest) error {
	if err := ensureWorkspace(input.Destination); err != nil {
		return err
	}
	if err := clearWorkspace(input.Destination); err != nil {
		return err
	}
	if input.RepositoryURL != "" {
		var err error
		if input.GitCredential == nil {
			err = s.config.CommandRunner(
				ctx, "git", []string{"clone", "--", input.RepositoryURL, input.Destination},
				filepath.Dir(input.Destination), s.config.OutputLimit,
			)
		} else {
			err = secureHTTPSClone(ctx, input.RepositoryURL, input.Destination,
				*input.GitCredential, s.config.OutputLimit)
		}
		if err != nil {
			return fmt.Errorf("clone repository: %w", err)
		}
	}
	if len(input.Setup) > 0 {
		if err := s.config.CommandRunner(
			ctx, input.Setup[0], input.Setup[1:], input.Destination, s.config.OutputLimit,
		); err != nil {
			return fmt.Errorf("run setup command: %w", err)
		}
	}
	return writeMarker(input.Destination, requestHash(input))
}

func ensureWorkspace(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(path, 0o750)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("workspace is not a real directory")
	}
	return nil
}

func clearWorkspace(path string) error {
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

type marker struct {
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

func markerPath(workspace string) string {
	return filepath.Join(workspace, ".meridian-prepared")
}

func requestHash(input PrepareRequest) string {
	value, _ := json.Marshal(struct {
		RepositoryURL string   `json:"repositoryUrl,omitempty"`
		Destination   string   `json:"destination"`
		Setup         []string `json:"setup,omitempty"`
	}{
		RepositoryURL: input.RepositoryURL, Destination: input.Destination, Setup: input.Setup,
	})
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func secureHTTPSClone(
	ctx context.Context,
	repositoryURL, destination string,
	credential GitHTTPSCredential,
	outputLimit int64,
) error {
	// The Capsule root and /workspace are intentionally not writable places
	// for credential helpers. The bounded private /tmp tmpfs is outside the
	// captured workspace and is removed on every outcome.
	helper, err := os.CreateTemp("", ".meridian-askpass-*")
	if err != nil {
		return err
	}
	helperPath := helper.Name()
	defer os.Remove(helperPath)
	const script = "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' \"$MERIDIAN_GIT_USERNAME\";;\n*) printf '%s\\n' \"$MERIDIAN_GIT_PASSWORD\";;\nesac\n"
	if err := helper.Chmod(0o700); err == nil {
		_, err = io.WriteString(helper, script)
	}
	if err == nil {
		err = helper.Sync()
	}
	if closeErr := helper.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	command := exec.Command("git",
		"-c", "credential.helper=",
		"-c", "core.askPass="+helperPath,
		"clone", "--", repositoryURL, destination)
	command.Dir = filepath.Dir(destination)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Env = secureGitEnvironment(os.Environ(), helperPath, credential)
	output := &limitWriter{remaining: outputLimit}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		clearEnvironment(command.Env)
		command.Env = nil
		return err
	}
	clearEnvironment(command.Env)
	command.Env = nil
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		return ctx.Err()
	}
}

func clearEnvironment(environment []string) {
	for index, item := range environment {
		environment[index] = strings.Repeat("\x00", len(item))
	}
}

func secureGitEnvironment(
	host []string, helper string, credential GitHTTPSCredential,
) []string {
	result := make([]string, 0, len(host)+7)
	for _, item := range host {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "GIT_") || key == "SSH_ASKPASS" ||
			key == "MERIDIAN_GIT_USERNAME" || key == "MERIDIAN_GIT_PASSWORD" {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"GIT_ASKPASS="+helper,
		"SSH_ASKPASS="+helper,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"MERIDIAN_GIT_USERNAME="+credential.Username,
		"MERIDIAN_GIT_PASSWORD="+credential.Password,
	)
}

func writeMarker(workspace, hash string) error {
	value, err := json.Marshal(marker{Version: Version, Hash: hash})
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(workspace, ".meridian-prepared-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(value); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, markerPath(workspace))
}

func (s *Server) prepared(workspace string, input PrepareRequest) (bool, error) {
	info, err := os.Lstat(workspace)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("workspace is unsafe")
	}
	value, err := os.ReadFile(markerPath(workspace))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var saved marker
	if json.Unmarshal(value, &saved) != nil || saved.Version != Version {
		return false, nil
	}
	if input.Destination == "" {
		s.state = StateReady
		return true, nil
	}
	return subtle.ConstantTimeCompare([]byte(saved.Hash), []byte(requestHash(input))) == 1, nil
}

type limitWriter struct {
	remaining int64
}

func (w *limitWriter) Write(value []byte) (int, error) {
	if int64(len(value)) > w.remaining {
		w.remaining = 0
		return 0, errors.New("command output limit exceeded")
	}
	w.remaining -= int64(len(value))
	return len(value), nil
}

func runCommand(ctx context.Context, name string, arguments []string, directory string, outputLimit int64) error {
	command := exec.Command(name, arguments...)
	command.Dir = directory
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	output := &limitWriter{remaining: outputLimit}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		return ctx.Err()
	}
}

func boundedError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "preparation timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "preparation canceled"
	}
	return "preparation command failed"
}

func writeProtocolError(writer http.ResponseWriter, status int, code string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set(VersionHeader, Version)
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
