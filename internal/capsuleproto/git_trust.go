package capsuleproto

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Kubernetes mounts a Capsule's workspace PVC with the Pod's fsGroup, which
// changes only the volume root's group. The root stays owned by root, so git
// refuses the uid-10001 repository there as "dubious ownership". capsuled
// clones that repository itself and already runs git on it as hostile content
// (hooks and fsmonitor off), so trusting exactly the workspace path, never
// "*", gives a planted repository nothing it lacked.

// TrustWorkspaceForGit adds safe.directory for the workspace to this
// process's environment as command-line git configuration
// (GIT_CONFIG_COUNT), which git honors even with global and system
// configuration ignored. capsuled's own git commands and every child it
// starts (harness, terminal, setup, checks) inherit it. Delivery strips GIT_*
// variables and passes gitSafeDirectoryArguments instead.
func TrustWorkspaceForGit(workspace string) error {
	entries, err := gitSafeDirectoryEnvironment(os.Environ(), workspace)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		key, value, _ := strings.Cut(entry, "=")
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

// gitSafeDirectoryEnvironment returns the variables that append
// safe.directory entries for workspace after any GIT_CONFIG_COUNT entries
// already in environment, or nothing when they are already there.
func gitSafeDirectoryEnvironment(environment []string, workspace string) ([]string, error) {
	paths, err := gitTrustedPaths(workspace)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	count := 0
	if raw, ok := values["GIT_CONFIG_COUNT"]; ok {
		count, err = strconv.Atoi(raw)
		if err != nil || count < 0 {
			return nil, fmt.Errorf("GIT_CONFIG_COUNT is %q, not a count", raw)
		}
	}
	trusted := make(map[string]bool, count)
	for index := range count {
		suffix := strconv.Itoa(index)
		if values["GIT_CONFIG_KEY_"+suffix] == "safe.directory" {
			trusted[values["GIT_CONFIG_VALUE_"+suffix]] = true
		}
	}
	var result []string
	for _, path := range paths {
		if trusted[path] {
			continue
		}
		suffix := strconv.Itoa(count)
		result = append(result, "GIT_CONFIG_KEY_"+suffix+"=safe.directory", "GIT_CONFIG_VALUE_"+suffix+"="+path)
		count++
	}
	if len(result) == 0 {
		return nil, nil
	}
	return append(result, "GIT_CONFIG_COUNT="+strconv.Itoa(count)), nil
}

// gitSafeDirectoryArguments is the same trust as -c options, for git commands
// that run with a scrubbed environment.
func gitSafeDirectoryArguments(workspace string) []string {
	paths, err := gitTrustedPaths(workspace)
	if err != nil {
		return nil
	}
	result := make([]string, 0, 2*len(paths))
	for _, path := range paths {
		result = append(result, "-c", "safe.directory="+path)
	}
	return result
}

// gitTrustedPaths is the workspace and, when it differs, its resolved path:
// git compares safe.directory against the repository's real path.
func gitTrustedPaths(workspace string) ([]string, error) {
	if workspace == "" || !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return nil, errors.New("workspace must be a clean absolute path")
	}
	paths := []string{workspace}
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil && resolved != workspace {
		paths = append(paths, resolved)
	}
	return paths, nil
}
