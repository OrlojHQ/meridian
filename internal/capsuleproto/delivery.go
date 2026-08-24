package capsuleproto

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	maxDeliveryMessageBytes = 16 << 10
	maxDeliveryAuthorBytes  = 320
	maxDeliveryRefBytes     = 1024
	maxDeliveryGitOutput    = 1 << 20
	maxDeliveryConfigBytes  = 1 << 20
)

type DeliveryStateResponse struct {
	HEAD          string `json:"head"`
	Branch        string `json:"branch,omitempty"`
	Dirty         bool   `json:"dirty"`
	OriginURL     string `json:"originUrl,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	Tree          string `json:"tree"`
}

type DeliveryCommitRequest struct {
	Message      string `json:"message"`
	AuthorName   string `json:"authorName"`
	AuthorEmail  string `json:"authorEmail"`
	ExpectedHEAD string `json:"expectedHead"`
	ExpectedTree string `json:"expectedTree"`
}

type DeliveryCommitResponse struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
}

type DeliveryPushRequest struct {
	SourceCommit   string              `json:"sourceCommit"`
	DestinationRef string              `json:"destinationRef"`
	ExpectedOldRef *string             `json:"expectedOldRef,omitempty"`
	GitCredential  *GitHTTPSCredential `json:"gitCredential"`
}

type DeliveryPushResponse struct {
	Commit         string `json:"commit"`
	DestinationRef string `json:"destinationRef"`
}

func (s *Server) deliveryState(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()
	state, err := inspectDeliveryState(ctx, s.config.Workspace)
	if err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "delivery_state_unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, state)
}

func (s *Server) deliveryCommit(writer http.ResponseWriter, request *http.Request) {
	var input DeliveryCommitRequest
	if !decodeRequest(writer, request, s.config.BodyLimit, &input) ||
		validateCommitRequest(input) != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
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
	ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
	defer cancel()
	if err := validateDeliveryRepository(ctx, s.config.Workspace); err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "unsafe_repository")
		return
	}
	head, err := deliveryRevision(ctx, s.config.Workspace, "HEAD")
	if err != nil || head != input.ExpectedHEAD {
		writeProtocolError(writer, http.StatusConflict, "head_precondition_failed")
		return
	}
	currentTree, err := deliveryWorktreeTree(ctx, s.config.Workspace)
	if err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "commit_failed")
		return
	}
	if currentTree != input.ExpectedTree {
		writeProtocolError(writer, http.StatusConflict, "tree_precondition_failed")
		return
	}
	if err := stageDeliveryWorkspace(ctx, s.config.Workspace, nil); err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "commit_failed")
		return
	}
	tree, err := deliveryRevision(ctx, s.config.Workspace, "HEAD^{tree}")
	if err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "commit_failed")
		return
	}
	stagedTree, err := deliveryGitString(ctx, s.config.Workspace, nil, "write-tree")
	if err != nil || stagedTree != currentTree {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "commit_failed")
		return
	}
	if tree == stagedTree {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "nothing_to_commit")
		return
	}
	environment := []string{
		"GIT_AUTHOR_NAME=" + input.AuthorName,
		"GIT_AUTHOR_EMAIL=" + input.AuthorEmail,
		"GIT_COMMITTER_NAME=" + input.AuthorName,
		"GIT_COMMITTER_EMAIL=" + input.AuthorEmail,
	}
	if _, err := deliveryGit(
		ctx, s.config.Workspace, environment,
		"commit", "--no-verify", "--no-gpg-sign", "-m", input.Message, "--",
	); err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "commit_failed")
		return
	}
	commit, err := deliveryRevision(ctx, s.config.Workspace, "HEAD")
	if err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "commit_failed")
		return
	}
	resultTree, err := deliveryRevision(ctx, s.config.Workspace, "HEAD^{tree}")
	if err != nil || resultTree != input.ExpectedTree {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "commit_failed")
		return
	}
	writeJSON(writer, http.StatusOK, DeliveryCommitResponse{Commit: commit, Tree: resultTree})
}

func (s *Server) deliveryPush(writer http.ResponseWriter, request *http.Request) {
	var input DeliveryPushRequest
	if !decodeRequest(writer, request, s.config.BodyLimit, &input) {
		return
	}
	if input.GitCredential != nil {
		defer func() {
			input.GitCredential.Username = ""
			input.GitCredential.Password = ""
			input.GitCredential = nil
		}()
	}
	if validatePushRequest(input) != nil {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
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
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Minute)
	defer cancel()
	if err := validateDeliveryRepository(ctx, s.config.Workspace); err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "unsafe_repository")
		return
	}
	head, err := deliveryRevision(ctx, s.config.Workspace, "HEAD")
	if err != nil || head != input.SourceCommit {
		writeProtocolError(writer, http.StatusConflict, "source_precondition_failed")
		return
	}
	if kind, err := deliveryGitString(
		ctx, s.config.Workspace, nil, "cat-file", "-t", input.SourceCommit,
	); err != nil || kind != "commit" {
		writeProtocolError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	origin, err := deliveryOrigin(ctx, s.config.Workspace)
	if err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "origin_unavailable")
		return
	}
	helper, err := createGitAskpass()
	if err != nil {
		writeProtocolError(writer, http.StatusInternalServerError, "push_failed")
		return
	}
	defer os.Remove(helper)
	if input.ExpectedOldRef != nil {
		old, err := secureRemoteRef(
			ctx, s.config.Workspace, origin, input.DestinationRef, helper, *input.GitCredential,
		)
		if err != nil {
			writeProtocolError(writer, http.StatusConflict, "remote_precondition_failed")
			return
		}
		// A retry after an ambiguous successful push observes the exact source
		// commit and is already complete. Any other unexpected existing ref
		// conflicts rather than opportunistically updating a remote branch.
		if *input.ExpectedOldRef == "" && old == input.SourceCommit {
			writeJSON(writer, http.StatusOK, DeliveryPushResponse{
				Commit: input.SourceCommit, DestinationRef: input.DestinationRef,
			})
			return
		}
		if old != *input.ExpectedOldRef {
			writeProtocolError(writer, http.StatusConflict, "remote_precondition_failed")
			return
		}
	}
	if err := secureGitPush(
		ctx, s.config.Workspace, origin, input.SourceCommit, input.DestinationRef,
		helper, *input.GitCredential,
	); err != nil {
		writeProtocolError(writer, http.StatusUnprocessableEntity, "push_failed")
		return
	}
	writeJSON(writer, http.StatusOK, DeliveryPushResponse{
		Commit: input.SourceCommit, DestinationRef: input.DestinationRef,
	})
}

func validateCommitRequest(input DeliveryCommitRequest) error {
	if input.Message == "" || len(input.Message) > maxDeliveryMessageBytes ||
		strings.ContainsRune(input.Message, '\x00') ||
		input.AuthorName == "" || len(input.AuthorName) > maxDeliveryAuthorBytes ||
		input.AuthorEmail == "" || len(input.AuthorEmail) > maxDeliveryAuthorBytes ||
		strings.ContainsAny(input.AuthorName+input.AuthorEmail, "\x00\r\n<>") ||
		!validObjectID(input.ExpectedHEAD) || !validObjectID(input.ExpectedTree) {
		return errors.New("invalid commit request")
	}
	return nil
}

func validatePushRequest(input DeliveryPushRequest) error {
	if !validObjectID(input.SourceCommit) ||
		!strings.HasPrefix(input.DestinationRef, "refs/heads/") ||
		len(input.DestinationRef) > maxDeliveryRefBytes ||
		input.GitCredential == nil || input.GitCredential.Password == "" ||
		len(input.GitCredential.Username) > maxDeliveryAuthorBytes ||
		len(input.GitCredential.Password) > maxDeliveryMessageBytes ||
		strings.ContainsAny(input.GitCredential.Username+input.GitCredential.Password, "\x00\r\n") {
		return errors.New("invalid push request")
	}
	if input.ExpectedOldRef != nil && *input.ExpectedOldRef != "" &&
		!validObjectID(*input.ExpectedOldRef) {
		return errors.New("invalid expected remote ref")
	}
	name := strings.TrimPrefix(input.DestinationRef, "refs/heads/")
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\x00\r\n") {
		return errors.New("invalid destination ref")
	}
	command := exec.Command("git", "check-ref-format", "--branch", name)
	command.Env = deliveryEnvironment(nil)
	if command.Run() != nil {
		return errors.New("invalid destination ref")
	}
	return nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func inspectDeliveryState(ctx context.Context, workspace string) (DeliveryStateResponse, error) {
	if err := validateDeliveryRepository(ctx, workspace); err != nil {
		return DeliveryStateResponse{}, err
	}
	head, err := deliveryRevision(ctx, workspace, "HEAD")
	if err != nil {
		return DeliveryStateResponse{}, err
	}
	headTree, err := deliveryRevision(ctx, workspace, "HEAD^{tree}")
	if err != nil {
		return DeliveryStateResponse{}, err
	}
	tree, err := deliveryWorktreeTree(ctx, workspace)
	if err != nil {
		return DeliveryStateResponse{}, err
	}
	branch, _ := deliveryGitString(ctx, workspace, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	origin, _ := deliveryOrigin(ctx, workspace)
	defaultRef, _ := deliveryGitString(
		ctx, workspace, nil, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD",
	)
	defaultBranch := strings.TrimPrefix(defaultRef, "origin/")
	if defaultBranch == defaultRef {
		defaultBranch = ""
	}
	return DeliveryStateResponse{
		HEAD: head, Branch: branch, Dirty: tree != headTree, OriginURL: origin,
		DefaultBranch: defaultBranch, Tree: tree,
	}, nil
}

func validateDeliveryRepository(ctx context.Context, workspace string) error {
	workspaceInfo, err := os.Lstat(workspace)
	if err != nil || !workspaceInfo.IsDir() || workspaceInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe workspace")
	}
	gitDirectory := filepath.Join(workspace, ".git")
	gitInfo, err := os.Lstat(gitDirectory)
	if err != nil || !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("linked or unsafe Git worktree")
	}
	top, err := deliveryGitString(ctx, workspace, nil, "rev-parse", "--show-toplevel")
	if err != nil || !sameDirectory(top, workspace) {
		return errors.New("repository root mismatch")
	}
	actualGit, err := deliveryGitString(ctx, workspace, nil, "rev-parse", "--absolute-git-dir")
	if err != nil || !sameDirectory(actualGit, gitDirectory) {
		return errors.New("Git directory mismatch")
	}
	if _, err := os.Lstat(filepath.Join(workspace, ".gitmodules")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("submodules are unsupported")
	}
	index, err := deliveryGit(ctx, workspace, nil, "ls-files", "--stage", "-z")
	if err != nil {
		return err
	}
	for _, entry := range strings.Split(string(index), "\x00") {
		if strings.HasPrefix(entry, "160000 ") {
			return errors.New("submodules are unsupported")
		}
		_, name, _ := strings.Cut(entry, "\t")
		if name == ".meridian-prepared" {
			return errors.New("supervisor marker must not be tracked")
		}
	}
	config, err := deliveryGitLimit(
		ctx, workspace, nil, maxDeliveryConfigBytes,
		"config", "--file", filepath.Join(workspace, ".git", "config"), "--null", "--list",
	)
	if err != nil {
		return err
	}
	for _, item := range strings.Split(string(config), "\x00") {
		key, _, _ := strings.Cut(item, "\n")
		if unsafeDeliveryConfig(strings.ToLower(key)) {
			return errors.New("repository config can invoke external behavior")
		}
	}
	return nil
}

func sameDirectory(first, second string) bool {
	firstInfo, firstErr := os.Stat(first)
	secondInfo, secondErr := os.Stat(second)
	return firstErr == nil && secondErr == nil && firstInfo.IsDir() &&
		secondInfo.IsDir() && os.SameFile(firstInfo, secondInfo)
}

func unsafeDeliveryConfig(key string) bool {
	if strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.") ||
		strings.HasPrefix(key, "credential.") || strings.HasPrefix(key, "filter.") ||
		strings.HasPrefix(key, "gpg.") || strings.HasPrefix(key, "http.") ||
		strings.HasPrefix(key, "https.") || strings.HasPrefix(key, "push.") {
		return true
	}
	if strings.HasPrefix(key, "url.") &&
		(strings.HasSuffix(key, ".insteadof") || strings.HasSuffix(key, ".pushinsteadof")) {
		return true
	}
	if strings.HasPrefix(key, "diff.") &&
		(strings.HasSuffix(key, ".command") || strings.HasSuffix(key, ".textconv")) {
		return true
	}
	if strings.HasPrefix(key, "merge.") && strings.HasSuffix(key, ".driver") {
		return true
	}
	switch key {
	case "core.hookspath", "core.askpass", "core.sshcommand", "core.fsmonitor",
		"core.gitproxy", "core.attributesfile", "core.worktree",
		"remote.origin.pushurl", "remote.origin.receivepack", "remote.origin.uploadpack":
		return true
	default:
		return strings.HasPrefix(key, "remote.") &&
			(strings.HasSuffix(key, ".proxy") || strings.HasSuffix(key, ".proxyauthmethod"))
	}
}

func deliveryWorktreeTree(ctx context.Context, workspace string) (string, error) {
	index, err := os.CreateTemp("", ".meridian-delivery-index-*")
	if err != nil {
		return "", err
	}
	name := index.Name()
	if err := index.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if err := os.Remove(name); err != nil {
		return "", err
	}
	defer os.Remove(name)
	environment := []string{"GIT_INDEX_FILE=" + name}
	if _, err := deliveryGit(ctx, workspace, environment, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if err := stageDeliveryWorkspace(ctx, workspace, environment); err != nil {
		return "", err
	}
	return deliveryGitString(ctx, workspace, environment, "write-tree")
}

func stageDeliveryWorkspace(
	ctx context.Context, workspace string, environment []string,
) error {
	_, err := deliveryGit(
		ctx, workspace, environment,
		"add", "-A", "--", ".", ":(exclude).meridian-prepared",
	)
	return err
}

func deliveryOrigin(ctx context.Context, workspace string) (string, error) {
	value, err := deliveryGitString(ctx, workspace, nil, "config", "--local", "--get", "remote.origin.url")
	if err != nil || len(value) > 4096 {
		return "", errors.New("origin URL unavailable")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("origin must be a credential-free HTTPS URL")
	}
	return value, nil
}

func deliveryRevision(ctx context.Context, workspace, revision string) (string, error) {
	value, err := deliveryGitString(ctx, workspace, nil, "rev-parse", "--verify", revision)
	if err != nil || !validObjectID(value) {
		return "", errors.New("invalid Git revision")
	}
	return value, nil
}

func deliveryGitString(
	ctx context.Context, workspace string, extraEnvironment []string, arguments ...string,
) (string, error) {
	value, err := deliveryGit(ctx, workspace, extraEnvironment, arguments...)
	if err != nil {
		return "", err
	}
	result := strings.TrimSpace(string(value))
	if strings.ContainsAny(result, "\x00\r\n") {
		return "", errors.New("unexpected Git output")
	}
	return result, nil
}

func deliveryGit(
	ctx context.Context, workspace string, extraEnvironment []string, arguments ...string,
) ([]byte, error) {
	return deliveryGitLimit(ctx, workspace, extraEnvironment, maxDeliveryGitOutput, arguments...)
}

func deliveryGitLimit(
	ctx context.Context,
	workspace string,
	extraEnvironment []string,
	limit int64,
	arguments ...string,
) ([]byte, error) {
	base := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "push.gpgSign=false",
		"-c", "push.followTags=false",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "credential.helper=",
		"-c", "protocol.file.allow=never",
		"-C", workspace,
	}
	command := exec.CommandContext(ctx, "git", append(base, arguments...)...)
	command.Env = deliveryEnvironment(extraEnvironment)
	var output boundedBuffer
	output.limit = limit
	command.Stdout = &output
	command.Stderr = io.Discard
	err := command.Run()
	clearEnvironment(command.Env)
	command.Env = nil
	if err != nil {
		return nil, errors.New("bounded Git command failed")
	}
	return append([]byte(nil), output.value...), nil
}

func deliveryEnvironment(extra []string) []string {
	result := make([]string, 0, len(os.Environ())+len(extra)+3)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "GIT_") || key == "SSH_ASKPASS" ||
			key == "MERIDIAN_GIT_USERNAME" || key == "MERIDIAN_GIT_PASSWORD" {
			continue
		}
		result = append(result, item)
	}
	result = append(result,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	return append(result, extra...)
}

func createGitAskpass() (string, error) {
	helper, err := os.CreateTemp("", ".meridian-askpass-*")
	if err != nil {
		return "", err
	}
	name := helper.Name()
	ok := false
	defer func() {
		_ = helper.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	const script = "#!/bin/sh\ncase \"$1\" in\n*Username*) printf '%s\\n' \"$MERIDIAN_GIT_USERNAME\";;\n*) printf '%s\\n' \"$MERIDIAN_GIT_PASSWORD\";;\nesac\n"
	if err := helper.Chmod(0o700); err != nil {
		return "", err
	}
	if _, err := io.WriteString(helper, script); err != nil {
		return "", err
	}
	if err := helper.Sync(); err != nil {
		return "", err
	}
	if err := helper.Close(); err != nil {
		return "", err
	}
	ok = true
	return name, nil
}

func secureRemoteRef(
	ctx context.Context,
	workspace, origin, destination, helper string,
	credential GitHTTPSCredential,
) (string, error) {
	value, err := secureDeliveryGit(
		ctx, workspace, helper, credential,
		"ls-remote", "--refs", "--", origin, destination,
	)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(value))
	if line == "" {
		return "", nil
	}
	sha, ref, ok := strings.Cut(line, "\t")
	if !ok || ref != destination || !validObjectID(sha) || strings.Contains(line, "\n") {
		return "", errors.New("unexpected remote ref response")
	}
	return sha, nil
}

func secureGitPush(
	ctx context.Context,
	workspace, origin, source, destination, helper string,
	credential GitHTTPSCredential,
) error {
	_, err := secureDeliveryGit(
		ctx, workspace, helper, credential,
		"push", "--porcelain", "--no-verify", "--", origin, source+":"+destination,
	)
	return err
}

func secureDeliveryGit(
	ctx context.Context,
	workspace, helper string,
	credential GitHTTPSCredential,
	arguments ...string,
) ([]byte, error) {
	base := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "credential.helper=",
		"-c", "core.askPass=" + helper,
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "push.gpgSign=false",
		"-c", "push.followTags=false",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "protocol.file.allow=never",
		"-C", workspace,
	}
	command := exec.Command("git", append(base, arguments...)...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Env = secureGitEnvironment(deliveryEnvironment(nil), helper, credential)
	var output boundedBuffer
	output.limit = maxDeliveryGitOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		clearEnvironment(command.Env)
		command.Env = nil
		return nil, errors.New("secure Git command failed")
	}
	clearEnvironment(command.Env)
	command.Env = nil
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return nil, errors.New("secure Git command failed")
		}
		return append([]byte(nil), output.value...), nil
	case <-ctx.Done():
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		return nil, ctx.Err()
	}
}
