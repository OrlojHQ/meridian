package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/spf13/cobra"
)

func newCapsuleFilesCommand(config *cliConfig) *cobra.Command {
	root := &cobra.Command{Use: "files", Short: "Browse bounded Capsule workspace files"}
	var after string
	var limit int
	list := &cobra.Command{
		Use: "list CAPSULE_ID [PATH]", Args: cobra.RangeArgs(1, 2),
		Short: "List a workspace directory",
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			params := client.ListCapsuleFilesParams{
				CapsuleId: args[0], Limit: client.NewOptInt(limit),
			}
			if len(args) == 2 {
				params.Path = client.NewOptString(args[1])
			}
			if after != "" {
				params.After = client.NewOptString(after)
			}
			response, err := api.ListCapsuleFiles(command.Context(), params)
			if err != nil {
				return apiCallError(err)
			}
			result, ok := response.(*client.WorkspaceFilePage)
			if !ok {
				return responseError(response)
			}
			if config.json {
				return writeJSON(config.stdout, result)
			}
			for _, item := range result.Items {
				fmt.Fprintf(config.stdout, "%s\t%s\tsize=%d\texecutable=%t\n",
					item.Type, item.Name, item.Size, item.Executable)
			}
			if next, ok := result.NextAfter.Get(); ok {
				fmt.Fprintf(config.stdout, "next-after=%s\n", next)
			}
			return nil
		},
	}
	list.Flags().StringVar(&after, "after", "", "lexicographic pagination cursor")
	list.Flags().IntVar(&limit, "limit", 128, "directory page size")
	read := &cobra.Command{
		Use: "read CAPSULE_ID PATH", Args: cobra.ExactArgs(2),
		Short: "Read one bounded regular workspace file",
		RunE: func(command *cobra.Command, args []string) error {
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			response, err := api.ReadCapsuleFile(command.Context(), client.ReadCapsuleFileParams{
				CapsuleId: args[0], Path: args[1],
			})
			if err != nil {
				return apiCallError(err)
			}
			result, ok := response.(*client.WorkspaceFile)
			if !ok {
				return responseError(response)
			}
			if config.json {
				return writeJSON(config.stdout, result)
			}
			_, err = config.stdout.Write(result.Content)
			return err
		},
	}
	root.AddCommand(list, read)
	return root
}

func newCapsuleShipCommand(config *cliConfig) *cobra.Command {
	var yes, openPR bool
	var branch, message, title, body, base, keyFlag string
	command := &cobra.Command{
		Use: "ship CAPSULE_ID", Args: cobra.ExactArgs(1),
		Short: "Commit and push an explicitly approved reviewed state",
		RunE: func(command *cobra.Command, args []string) error {
			if !yes {
				return errors.New("--yes is required; review Git status and diff before approving shipment")
			}
			if config.json {
				return errors.New("--json is not supported by capsule ship because review output precedes mutation")
			}
			if branch == "" {
				return errors.New("--branch is required")
			}
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			if err := printCapsuleReview(command.Context(), config, api, args[0]); err != nil {
				return err
			}
			inspectionResponse, err := api.InspectCapsuleDelivery(
				command.Context(), client.InspectCapsuleDeliveryParams{CapsuleId: args[0]},
			)
			if err != nil {
				return apiCallError(err)
			}
			inspection, ok := inspectionResponse.(*client.DeliveryInspection)
			if !ok {
				return responseError(inspectionResponse)
			}
			fmt.Fprintf(config.stdout, "Reviewed HEAD: %s\nReviewed tree: %s\n",
				inspection.Head, inspection.Tree)
			key, err := idempotencyKey(keyFlag)
			if err != nil {
				return err
			}
			action := client.CreateDeliveryRequestActionPush
			if openPR {
				action = client.CreateDeliveryRequestActionOpenPullRequest
				if title == "" {
					return errors.New("--title is required with --open-pull-request")
				}
			}
			input := &client.CreateDeliveryRequest{
				Action: action, Approved: true,
				ExpectedResourceVersion: inspection.CapsuleResourceVersion,
				ExpectedHead:            inspection.Head, ExpectedTree: inspection.Tree,
				RemoteBranch: branch,
			}
			if message != "" {
				input.CommitMessage = client.NewOptString(message)
			}
			if title != "" {
				input.PullRequestTitle = client.NewOptString(title)
			}
			if body != "" {
				input.PullRequestBody = client.NewOptString(body)
			}
			if base != "" {
				input.BaseBranch = client.NewOptString(base)
			}
			response, err := api.CreateCapsuleDelivery(
				command.Context(), input, client.CreateCapsuleDeliveryParams{
					CapsuleId: args[0], IdempotencyKey: key,
				},
			)
			if err != nil {
				return apiCallError(err)
			}
			result, ok := response.(*client.DeliveryHeaders)
			if !ok {
				return responseError(response)
			}
			if config.json {
				return writeJSON(config.stdout, result.Response)
			}
			commit, _ := result.Response.ResultCommitSha.Get()
			pr, _ := result.Response.ResultPullRequestUrl.Get()
			fmt.Fprintf(config.stdout, "%s\tstate=%s\tcommit=%s", result.Response.ID, result.Response.State, commit)
			if pr.String() != "" {
				fmt.Fprintf(config.stdout, "\tpr=%s", pr.String())
			}
			_, err = fmt.Fprintln(config.stdout)
			return err
		},
	}
	command.Flags().BoolVar(&yes, "yes", false, "approve shipping the exact inspected HEAD and tree")
	command.Flags().BoolVar(&openPR, "open-pull-request", false, "open a GitHub pull request after push")
	command.Flags().StringVar(&branch, "branch", "", "destination branch name")
	command.Flags().StringVar(&message, "commit-message", "", "commit message required for a dirty tree")
	command.Flags().StringVar(&title, "title", "", "pull request title")
	command.Flags().StringVar(&body, "body", "", "pull request body")
	command.Flags().StringVar(&base, "base", "", "pull request base branch")
	command.Flags().StringVar(&keyFlag, "idempotency-key", "", "mutation replay key (generated if omitted)")
	return command
}

func printCapsuleReview(
	ctx context.Context, config *cliConfig, api *client.Client, capsuleID string,
) error {
	statusResponse, err := api.GetCapsuleGitStatus(
		ctx, client.GetCapsuleGitStatusParams{CapsuleId: capsuleID},
	)
	if err != nil {
		return apiCallError(err)
	}
	diffResponse, err := api.GetCapsuleGitDiff(
		ctx, client.GetCapsuleGitDiffParams{CapsuleId: capsuleID},
	)
	if err != nil {
		return apiCallError(err)
	}
	status, ok := statusResponse.(*client.GitResult)
	if !ok {
		return responseError(statusResponse)
	}
	diff, ok := diffResponse.(*client.GitResult)
	if !ok {
		return responseError(diffResponse)
	}
	fmt.Fprintln(config.stdout, "Capsule Git status:")
	if status.Content == "" {
		fmt.Fprintln(config.stdout, "(clean)")
	} else {
		fmt.Fprint(config.stdout, status.Content)
		if !strings.HasSuffix(status.Content, "\n") {
			fmt.Fprintln(config.stdout)
		}
	}
	fmt.Fprintln(config.stdout, "Capsule Git diff:")
	if diff.Content == "" {
		fmt.Fprintln(config.stdout, "(empty)")
	} else {
		fmt.Fprint(config.stdout, diff.Content)
		if !strings.HasSuffix(diff.Content, "\n") {
			fmt.Fprintln(config.stdout)
		}
	}
	return nil
}

func newCapsuleSyncCommand(config *cliConfig) *cobra.Command {
	var target string
	var force bool
	command := &cobra.Command{
		Use: "sync CAPSULE_ID", Args: cobra.ExactArgs(1),
		Short: "Safely mirror a Capsule workspace into a local Git worktree",
		RunE: func(command *cobra.Command, args []string) error {
			if target == "" {
				return errors.New("--to is required")
			}
			if config.json {
				return errors.New("--json is not supported by capsule sync because review output precedes mutation")
			}
			absolute, err := filepath.Abs(target)
			if err != nil {
				return err
			}
			target = filepath.Clean(absolute)
			api, err := newAPI(config)
			if err != nil {
				return err
			}
			projectURL, err := capsuleProjectRepository(command.Context(), api, args[0])
			if err != nil {
				return err
			}
			if err := validateSyncTarget(command.Context(), target, projectURL, force); err != nil {
				return err
			}
			if err := printCapsuleReview(command.Context(), config, api, args[0]); err != nil {
				return err
			}
			response, err := streamWorkspace(command.Context(), config, args[0])
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
				return fmt.Errorf("workspace export failed with status %d", response.StatusCode)
			}
			stage, err := os.MkdirTemp(filepath.Dir(target), ".meridian-sync-stage-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(stage)
			if err := extractSyncArchive(command.Context(), response.Body, stage); err != nil {
				return fmt.Errorf("validate workspace archive: %w", err)
			}
			if err := removeStagePrivateMetadata(stage); err != nil {
				return err
			}
			if !force {
				if err := mergeTargetWithoutConflicts(target, stage); err != nil {
					return err
				}
			}
			if err := mirrorSyncStage(target, stage); err != nil {
				return err
			}
			_, err = fmt.Fprintf(config.stdout, "Synced Capsule %s to %s\n", args[0], target)
			return err
		},
	}
	command.Flags().StringVar(&target, "to", "", "existing matching local Git worktree")
	command.Flags().BoolVar(&force, "force", false, "mirror non-Git content including deletions")
	return command
}

func capsuleProjectRepository(ctx context.Context, api *client.Client, capsuleID string) (string, error) {
	response, err := api.GetCapsule(ctx, client.GetCapsuleParams{CapsuleId: capsuleID})
	if err != nil {
		return "", apiCallError(err)
	}
	capsule, ok := response.(*client.CapsuleHeaders)
	if !ok {
		return "", responseError(response)
	}
	projectResponse, err := api.GetProject(
		ctx, client.GetProjectParams{ProjectId: capsule.Response.ProjectId},
	)
	if err != nil {
		return "", apiCallError(err)
	}
	project, ok := projectResponse.(*client.ProjectHeaders)
	if !ok {
		return "", responseError(projectResponse)
	}
	value, ok := project.Response.RepositoryUrl.Get()
	if !ok || value == "" {
		return "", errors.New("Project repository URL is required for local sync")
	}
	return value, nil
}

func streamWorkspace(ctx context.Context, config *cliConfig, capsuleID string) (*http.Response, error) {
	base, err := url.Parse(config.server)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") ||
		base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("invalid Meridian API URL")
	}
	token, err := apiauth.ReadTokenFile(config.tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read API token: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/capsules/" +
		url.PathEscape(capsuleID) + "/workspace"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	httpClient := *http.DefaultClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := httpClient.Do(request)
	request.Header.Del("Authorization")
	if err != nil {
		return nil, fmt.Errorf("stream workspace: %w", err)
	}
	return response, nil
}

func validateSyncTarget(ctx context.Context, target, projectURL string, force bool) error {
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("sync target must be an existing real directory")
	}
	gitPath := filepath.Join(target, ".git")
	gitInfo, err := os.Lstat(gitPath)
	if err != nil || gitInfo.Mode()&os.ModeSymlink != 0 ||
		(!gitInfo.IsDir() && !gitInfo.Mode().IsRegular()) {
		return errors.New("sync target must be an existing Git worktree")
	}
	top, err := syncGit(ctx, target, "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("sync target is not a Git worktree")
	}
	topPath, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil {
		return err
	}
	targetPath, err := filepath.EvalSymlinks(target)
	if err != nil || topPath != targetPath {
		return errors.New("sync target must be the Git worktree root")
	}
	origin, err := syncGit(ctx, target, "config", "--get", "remote.origin.url")
	if err != nil || !equivalentRepository(strings.TrimSpace(origin), projectURL) {
		return errors.New("sync target origin does not match the Project repository")
	}
	if !force {
		status, err := syncGit(ctx, target, "status", "--porcelain=v1", "--untracked-files=all")
		if err != nil {
			return errors.New("cannot inspect sync target status")
		}
		if strings.TrimSpace(status) != "" {
			return errors.New("sync target is dirty; commit/stash changes or use --force")
		}
	}
	return inspectSafeTree(target)
}

func syncGit(ctx context.Context, target string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", target}, arguments...)...)
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	value, err := command.Output()
	if len(value) > 1<<20 {
		return "", errors.New("Git output exceeds limit")
	}
	return string(value), err
}

func equivalentRepository(first, second string) bool {
	a, errA := canonicalSyncRepository(first)
	b, errB := canonicalSyncRepository(second)
	return errA == nil && errB == nil && a == b
}

func canonicalSyncRepository(value string) (string, error) {
	if strings.HasPrefix(value, "git@github.com:") {
		repository := strings.TrimPrefix(value, "git@github.com:")
		repository = strings.TrimSuffix(strings.Trim(repository, "/"), ".git")
		if validGitHubPair(repository) {
			return "github.com/" + strings.ToLower(repository), nil
		}
		return "", errors.New("invalid GitHub SSH remote")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid repository URL")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "github.com" && (parsed.Scheme == "https" || parsed.Scheme == "ssh") {
		if parsed.Scheme == "https" && parsed.User != nil {
			return "", errors.New("credentialed repository URL")
		}
		repository := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
		if !validGitHubPair(repository) {
			return "", errors.New("invalid GitHub repository")
		}
		return "github.com/" + strings.ToLower(repository), nil
	}
	if parsed.Scheme != "https" || parsed.User != nil || host == "" {
		return "", errors.New("unsupported repository URL")
	}
	repository := strings.TrimSuffix(strings.Trim(path.Clean(parsed.Path), "/"), ".git")
	if repository == "" || repository == "." || strings.Contains(repository, "..") {
		return "", errors.New("invalid repository path")
	}
	port := parsed.Port()
	if port != "" && port != "443" {
		host += ":" + port
	}
	return host + "/" + repository, nil
}

func validGitHubPair(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && validSyncGitHubSlug(parts[0]) && validSyncGitHubSlug(parts[1])
}

func validSyncGitHubSlug(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

const (
	syncMaxFiles      = 100000
	syncMaxPath       = 4096
	syncMaxFileBytes  = int64(2 << 30)
	syncMaxTotalBytes = int64(8 << 30)
	syncMaxArchive    = syncMaxTotalBytes + 256<<20
)

func extractSyncArchive(ctx context.Context, input io.Reader, stage string) error {
	limited := &io.LimitedReader{R: input, N: syncMaxArchive + 1}
	reader := tar.NewReader(limited)
	seen := make(map[string]byte)
	var files int
	var total int64
	type link struct{ name, target string }
	var links []link
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if err := validateSyncArchivePath(name); err != nil {
			return err
		}
		files++
		if files > syncMaxFiles {
			return errors.New("archive file-count limit exceeded")
		}
		kind := header.Typeflag
		if kind == tar.TypeRegA {
			kind = tar.TypeReg
		}
		if kind != tar.TypeReg && kind != tar.TypeDir && kind != tar.TypeSymlink {
			return fmt.Errorf("unsupported archive type for %q", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate archive path %q", name)
		}
		for ancestor := path.Dir(name); ancestor != "."; ancestor = path.Dir(ancestor) {
			if seen[ancestor] != 0 && seen[ancestor] != tar.TypeDir {
				return fmt.Errorf("archive path %q traverses non-directory", name)
			}
		}
		if kind == tar.TypeReg {
			if header.Size < 0 || header.Size > syncMaxFileBytes ||
				total > syncMaxTotalBytes-header.Size {
				return errors.New("archive expansion limit exceeded")
			}
			total += header.Size
		}
		if kind == tar.TypeSymlink && !safeSyncSymlink(name, header.Linkname) {
			return fmt.Errorf("unsafe symlink %q", name)
		}
		seen[name] = kind
		if deniedSyncPath(name) {
			if kind == tar.TypeReg {
				if _, err := io.Copy(io.Discard, reader); err != nil {
					return err
				}
			}
			continue
		}
		target := filepath.Join(stage, filepath.FromSlash(name))
		if !withinSync(stage, target) {
			return errors.New("archive path escapes staging")
		}
		switch kind {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL,
				os.FileMode(header.Mode)&0o777)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			links = append(links, link{name: target, target: header.Linkname})
		}
	}
	if limited.N <= 0 {
		return errors.New("archive size limit exceeded")
	}
	for _, item := range links {
		if err := os.MkdirAll(filepath.Dir(item.name), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(filepath.FromSlash(item.target), item.name); err != nil {
			return err
		}
	}
	return inspectSafeTree(stage)
}

func validateSyncArchivePath(name string) error {
	if name == "" || len(name) > syncMaxPath || !utf8.ValidString(name) ||
		strings.ContainsAny(name, "\x00\\") || path.IsAbs(name) ||
		path.Clean(name) != name || name == "." || name == ".." ||
		strings.HasPrefix(name, "../") {
		return fmt.Errorf("unsafe archive path %q", name)
	}
	return nil
}

func deniedSyncPath(name string) bool {
	first, _, _ := strings.Cut(name, "/")
	return first == ".git" || first == ".meridian-prepared"
}

func safeSyncSymlink(name, target string) bool {
	if target == "" || len(target) > syncMaxPath || !utf8.ValidString(target) ||
		strings.ContainsAny(target, "\x00\\") || path.IsAbs(target) {
		return false
	}
	resolved := path.Clean(path.Join(path.Dir(name), target))
	return resolved != ".." && !strings.HasPrefix(resolved, "../")
}

func removeStagePrivateMetadata(stage string) error {
	for _, name := range []string{".git", ".meridian-prepared"} {
		entry := filepath.Join(stage, name)
		if _, err := os.Lstat(entry); err == nil {
			return fmt.Errorf("private path %s was unexpectedly extracted", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func inspectSafeTree(root string) error {
	return filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == root {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if deniedSyncPath(filepath.ToSlash(relative)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode.IsRegular(), mode.IsDir():
			return nil
		case mode&os.ModeSymlink != 0:
			target, err := os.Readlink(name)
			if err != nil || !safeSyncSymlink(filepath.ToSlash(relative), filepath.ToSlash(target)) {
				return fmt.Errorf("unsafe symlink %q", relative)
			}
			return nil
		default:
			return fmt.Errorf("unsupported special file %q", relative)
		}
	})
}

func mergeTargetWithoutConflicts(target, stage string) error {
	return mergeSyncDirectory(target, stage, target)
}

func mergeSyncDirectory(source, destination, root string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if source == root && (entry.Name() == ".git" || entry.Name() == ".meridian-prepared") {
			continue
		}
		from := filepath.Join(source, entry.Name())
		to := filepath.Join(destination, entry.Name())
		sourceInfo, err := os.Lstat(from)
		if err != nil {
			return err
		}
		targetInfo, err := os.Lstat(to)
		if errors.Is(err, os.ErrNotExist) {
			if err := copySyncEntry(from, to, root); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		switch {
		case sourceInfo.IsDir() && targetInfo.IsDir():
			if err := mergeSyncDirectory(from, to, root); err != nil {
				return err
			}
		case sourceInfo.Mode().IsRegular() && targetInfo.Mode().IsRegular():
			equal, err := equalSyncFiles(from, to, sourceInfo, targetInfo)
			if err != nil {
				return err
			}
			if !equal {
				return fmt.Errorf("sync replacement conflict at %s; use --force", from)
			}
		case sourceInfo.Mode()&os.ModeSymlink != 0 && targetInfo.Mode()&os.ModeSymlink != 0:
			a, _ := os.Readlink(from)
			b, _ := os.Readlink(to)
			if a != b {
				return fmt.Errorf("sync replacement conflict at %s; use --force", from)
			}
		default:
			return fmt.Errorf("sync replacement conflict at %s; use --force", from)
		}
	}
	return nil
}

func copySyncEntry(source, destination, root string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if err := os.Mkdir(destination, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copySyncEntry(filepath.Join(source, entry.Name()),
				filepath.Join(destination, entry.Name()), root); err != nil {
				return err
			}
		}
	case info.Mode().IsRegular():
		input, err := os.Open(source)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		return errors.Join(copyErr, closeErr)
	case info.Mode()&os.ModeSymlink != 0:
		relative, err := filepath.Rel(root, source)
		target, readErr := os.Readlink(source)
		if err != nil || readErr != nil || !safeSyncSymlink(
			filepath.ToSlash(relative), filepath.ToSlash(target),
		) {
			return fmt.Errorf("unsafe target symlink %q", source)
		}
		return os.Symlink(target, destination)
	default:
		return fmt.Errorf("unsupported target file %q", source)
	}
	return nil
}

func equalSyncFiles(first, second string, firstInfo, secondInfo os.FileInfo) (bool, error) {
	if firstInfo.Size() != secondInfo.Size() ||
		firstInfo.Mode().Perm()&0o111 != secondInfo.Mode().Perm()&0o111 {
		return false, nil
	}
	a, err := os.ReadFile(first)
	if err != nil {
		return false, err
	}
	b, err := os.ReadFile(second)
	return bytes.Equal(a, b), err
}

func mirrorSyncStage(target, stage string) error {
	backup, err := os.MkdirTemp(filepath.Dir(target), ".meridian-sync-backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(backup)
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".git" || entry.Name() == ".meridian-prepared" {
			continue
		}
		if err := os.Rename(filepath.Join(target, entry.Name()),
			filepath.Join(backup, entry.Name())); err != nil {
			rollbackSync(target, backup, nil)
			return err
		}
	}
	staged, err := os.ReadDir(stage)
	if err != nil {
		rollbackSync(target, backup, nil)
		return err
	}
	var installed []string
	for _, entry := range staged {
		if entry.Name() == ".git" || entry.Name() == ".meridian-prepared" {
			rollbackSync(target, backup, installed)
			return errors.New("sync stage contains a protected path")
		}
		if err := os.Rename(filepath.Join(stage, entry.Name()),
			filepath.Join(target, entry.Name())); err != nil {
			rollbackSync(target, backup, installed)
			return err
		}
		installed = append(installed, entry.Name())
	}
	return nil
}

func rollbackSync(target, backup string, installed []string) {
	for _, name := range installed {
		_ = os.RemoveAll(filepath.Join(target, name))
	}
	entries, _ := os.ReadDir(backup)
	for _, entry := range entries {
		_ = os.Rename(filepath.Join(backup, entry.Name()), filepath.Join(target, entry.Name()))
	}
}

func withinSync(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
