package capsuleproto

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGitSafeDirectoryEnvironmentAppendsAfterExistingEntries(t *testing.T) {
	workspace := realTempDir(t)
	got, err := gitSafeDirectoryEnvironment([]string{"PATH=/bin"}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=" + workspace, "GIT_CONFIG_COUNT=1",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("empty environment = %q, want %q", got, want)
	}

	existing := []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=core.editor", "GIT_CONFIG_VALUE_0=true",
		"GIT_CONFIG_KEY_1=color.ui", "GIT_CONFIG_VALUE_1=never",
	}
	got, err = gitSafeDirectoryEnvironment(existing, workspace)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		"GIT_CONFIG_KEY_2=safe.directory", "GIT_CONFIG_VALUE_2=" + workspace, "GIT_CONFIG_COUNT=3",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("existing entries = %q, want %q", got, want)
	}

	// Applying it twice adds nothing.
	got, err = gitSafeDirectoryEnvironment(append(existing[1:], want...), workspace)
	if err != nil || got != nil {
		t.Fatalf("already trusted = %q, %v; want nothing", got, err)
	}
}

func TestGitSafeDirectoryEnvironmentTrustsTheResolvedPath(t *testing.T) {
	target := realTempDir(t)
	link := filepath.Join(realTempDir(t), "workspace")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got, err := gitSafeDirectoryEnvironment(nil, link)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "GIT_CONFIG_VALUE_0="+link) || !slices.Contains(got, "GIT_CONFIG_VALUE_1="+target) ||
		!slices.Contains(got, "GIT_CONFIG_COUNT=2") {
		t.Fatalf("symlinked workspace = %q, want both the link and %s", got, target)
	}
}

func TestGitSafeDirectoryEnvironmentRefusesUnsafeInput(t *testing.T) {
	for _, workspace := range []string{"", "workspace", "/workspace/../etc", "/workspace/"} {
		if _, err := gitSafeDirectoryEnvironment(nil, workspace); err == nil {
			t.Errorf("workspace %q was trusted", workspace)
		}
		if arguments := gitSafeDirectoryArguments(workspace); arguments != nil {
			t.Errorf("workspace %q produced arguments %q", workspace, arguments)
		}
	}
	if _, err := gitSafeDirectoryEnvironment([]string{"GIT_CONFIG_COUNT=lots"}, "/workspace"); err == nil {
		t.Error("a malformed GIT_CONFIG_COUNT was overwritten")
	}
}

// GIT_TEST_ASSUME_DIFFERENT_OWNER makes git treat the repository as owned by
// another user, which is what a Kubernetes fsGroup volume root looks like to
// the Capsule user.
func TestTrustedWorkspaceGitAcceptsAForeignOwnedRepository(t *testing.T) {
	if _, ok := os.LookupEnv("GIT_CONFIG_COUNT"); ok {
		t.Skip("GIT_CONFIG_COUNT is already set in the test environment")
	}
	workspace := deliveryRepository(t)
	head := gitOutput(t, workspace, "rev-parse", "HEAD")
	t.Setenv("GIT_TEST_ASSUME_DIFFERENT_OWNER", "1")

	// Without the trust, git refuses and capsuled's reads come back empty.
	if got := preparationHead(context.Background(), workspace); got != "" {
		t.Fatalf("untrusted preparation HEAD = %q; the ownership check did not engage", got)
	}

	t.Cleanup(func() {
		for _, key := range []string{
			"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_KEY_1", "GIT_CONFIG_VALUE_1",
		} {
			_ = os.Unsetenv(key)
		}
	})
	if err := TrustWorkspaceForGit(workspace); err != nil {
		t.Fatal(err)
	}
	// capsuled's own reads and children inherit it.
	if got := preparationHead(context.Background(), workspace); got != head {
		t.Fatalf("trusted preparation HEAD = %q, want %s", got, head)
	}
	child := exec.Command("git", "-C", workspace, "rev-parse", "HEAD")
	child.Env = childEnvironment(nil)
	if output, err := child.CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != head {
		t.Fatalf("harness git = %q, %v; want %s", output, err, head)
	}
	// Delivery scrubs GIT_* and ignores global config, so it carries its own.
	output, err := deliveryGit(context.Background(), workspace,
		[]string{"GIT_TEST_ASSUME_DIFFERENT_OWNER=1"}, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(output)) != head {
		t.Fatalf("delivery git = %q, %v; want %s", output, err, head)
	}
}

func realTempDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}
