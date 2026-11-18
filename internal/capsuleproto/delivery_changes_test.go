package capsuleproto

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveryStateNormalizesMissingDeliverableFields(t *testing.T) {
	ahead, unpushed, deliverable := (DeliveryStateResponse{Dirty: true}).NormalizedCounts()
	if ahead != 0 || unpushed != 0 || !deliverable {
		t.Fatalf("missing counts = %d %d %v", ahead, unpushed, deliverable)
	}
	ahead, unpushed, deliverable = (DeliveryStateResponse{Dirty: false, Branch: "master", DefaultBranch: "main"}).NormalizedCounts()
	if ahead != 0 || unpushed != 0 || deliverable {
		t.Fatalf("branch name was treated as a change: %d %d %v", ahead, unpushed, deliverable)
	}
	zero := 0
	explicit := false
	ahead, unpushed, deliverable = (DeliveryStateResponse{
		Dirty: true, AheadCount: &zero, UnpushedCount: &zero, HasDeliverableChanges: &explicit,
	}).NormalizedCounts()
	if ahead != 0 || unpushed != 0 || deliverable {
		t.Fatalf("explicit false was replaced: %d %d %v", ahead, unpushed, deliverable)
	}
	huge := maxReportedAhead * 3
	ahead, unpushed, _ = (DeliveryStateResponse{AheadCount: &huge, UnpushedCount: &huge}).NormalizedCounts()
	if ahead != maxReportedAhead || unpushed != maxReportedAhead {
		t.Fatalf("counts were not bounded: %d %d", ahead, unpushed)
	}
}

// A project .gitignore takes precedence over info/exclude. A negation must
// not put a supervisor file into a delivery commit or make it look dirty.
func TestDeliveryKeepsSupervisorFilesOutDespiteGitignoreNegation(t *testing.T) {
	workspace, _ := cloneDeliveryFixture(t, "master")
	if err := os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte("!/.meridian-*\n!/.meridian-*/**\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, workspace, "add", "--", ".gitignore")
	gitRun(t, workspace, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", "negate")
	gitRun(t, workspace, "push", "origin", "master")
	for _, name := range []string{".meridian-prepared", ".meridian-setup-complete", ".meridian-capture-cache"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("supervisor\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(workspace, ".meridian-restore-x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".meridian-restore-x", "part"), []byte("supervisor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := mustInspect(t, workspace)
	if state.Dirty || flag(state.HasDeliverableChanges) {
		t.Fatalf("negated supervisor files counted as changes = %#v", state)
	}
	if err := os.WriteFile(filepath.Join(workspace, "notes.txt"), []byte("project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stageDeliveryWorkspace(context.Background(), workspace, nil); err != nil {
		t.Fatal(err)
	}
	staged := gitOutput(t, workspace, "diff", "--cached", "--name-only")
	if staged != "notes.txt" {
		t.Fatalf("staged = %q, want only notes.txt", staged)
	}
}

// The exclude file is written through a root on .git. A link in place of
// info cannot carry the write out of the git directory.
func TestPrivateExcludesStayInsideTheGitDirectory(t *testing.T) {
	workspace, _ := cloneDeliveryFixture(t, "master")
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(workspace, ".git", "info")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, ".git", "info")); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateWorktreeExcludes(workspace); err == nil {
		t.Fatal("a linked info directory was accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote outside .git: %v %v", entries, err)
	}
}

func TestDeliveryInspectionReportsDeliverableChanges(t *testing.T) {
	t.Run("fresh clone", func(t *testing.T) {
		workspace, _ := cloneDeliveryFixture(t, "master")
		state := mustInspect(t, workspace)
		if state.Branch != "master" || state.Dirty || count(state.AheadCount) != 0 ||
			count(state.UnpushedCount) != 0 || flag(state.HasDeliverableChanges) {
			t.Fatalf("fresh clone = %#v", state)
		}
	})

	t.Run("default branch name differs", func(t *testing.T) {
		workspace, _ := cloneDeliveryFixture(t, "master")
		gitRun(t, workspace, "update-ref", "refs/remotes/origin/main", "HEAD")
		gitRun(t, workspace, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		state := mustInspect(t, workspace)
		if state.Branch != "master" || state.DefaultBranch != "main" || state.Dirty ||
			count(state.AheadCount) != 0 || count(state.UnpushedCount) != 0 ||
			flag(state.HasDeliverableChanges) {
			t.Fatalf("same commit under another default name = %#v", state)
		}
	})

	t.Run("new branch with no commits", func(t *testing.T) {
		workspace, _ := cloneDeliveryFixture(t, "master")
		gitRun(t, workspace, "checkout", "-b", "topic")
		state := mustInspect(t, workspace)
		if state.Branch != "topic" || state.Dirty || count(state.AheadCount) != 0 ||
			count(state.UnpushedCount) != 0 || flag(state.HasDeliverableChanges) {
			t.Fatalf("empty topic branch = %#v", state)
		}
	})

	t.Run("commits ahead", func(t *testing.T) {
		workspace, _ := cloneDeliveryFixture(t, "master")
		gitRun(t, workspace, "checkout", "-b", "topic")
		commitDeliveryFile(t, workspace, "notes.txt", "notes\n", "add notes")
		state := mustInspect(t, workspace)
		if state.Dirty || count(state.AheadCount) != 1 || count(state.UnpushedCount) != 1 ||
			!flag(state.HasDeliverableChanges) {
			t.Fatalf("one commit ahead = %#v", state)
		}
	})

	t.Run("untracked user file", func(t *testing.T) {
		workspace, _ := cloneDeliveryFixture(t, "master")
		if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "src", "notes.txt"), []byte("hello\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		state := mustInspect(t, workspace)
		if !state.Dirty || count(state.AheadCount) != 0 || !flag(state.HasDeliverableChanges) {
			t.Fatalf("untracked project file = %#v", state)
		}
	})

	t.Run("meridian files ignored", func(t *testing.T) {
		workspace, _ := cloneDeliveryFixture(t, "master")
		for _, name := range []string{".meridian-prepared", ".meridian-setup-complete", ".meridian-capture-cache"} {
			if err := os.WriteFile(filepath.Join(workspace, name), []byte("supervisor\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		state := mustInspect(t, workspace)
		if state.Dirty || flag(state.HasDeliverableChanges) || count(state.AheadCount) != 0 {
			t.Fatalf("supervisor files counted as changes = %#v", state)
		}
		for _, name := range []string{".meridian-prepared", ".meridian-setup-complete", ".meridian-capture-cache"} {
			if !gitIgnored(t, workspace, name) {
				t.Fatalf("%s was not excluded", name)
			}
		}
		exclude, err := os.ReadFile(filepath.Join(workspace, ".git", "info", "exclude"))
		if err != nil || !strings.Contains(string(exclude), privateExcludeHeader) {
			t.Fatalf("exclude file = %q, err = %v", exclude, err)
		}
		if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "src", ".meridian-prepared"), []byte("project\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if gitIgnored(t, workspace, "src/.meridian-prepared") {
			t.Fatal("a project file with a similar name was excluded")
		}
		state = mustInspect(t, workspace)
		if !state.Dirty || !flag(state.HasDeliverableChanges) {
			t.Fatalf("nested project file = %#v", state)
		}
	})

	t.Run("delivery push to a URL clears unpushed", func(t *testing.T) {
		workspace, remote := cloneDeliveryFixture(t, "master")
		gitRun(t, workspace, "checkout", "-b", "feature")
		commitDeliveryFile(t, workspace, "first.txt", "one\n", "first")
		head := gitOutput(t, workspace, "rev-parse", "HEAD")
		// A delivery pushes to a URL with an explicit refspec, which leaves
		// refs/remotes/origin/feature alone.
		gitRun(t, workspace, "push", remote, head+":refs/heads/feature")
		state := mustInspect(t, workspace)
		if count(state.UnpushedCount) != 1 {
			t.Fatalf("before recording the push = %#v", state)
		}
		recordPushedBranch(context.Background(), workspace, "refs/heads/feature", head)
		state = mustInspect(t, workspace)
		if count(state.AheadCount) != 1 || count(state.UnpushedCount) != 0 || !flag(state.HasDeliverableChanges) {
			t.Fatalf("after recording the push = %#v", state)
		}
		recordPushedBranch(context.Background(), workspace, "refs/tags/v1", head)
		recordPushedBranch(context.Background(), workspace, "refs/heads/-bad", head)
		if refs := gitOutput(t, workspace, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/"); strings.Contains(refs, "v1") || strings.Contains(refs, "-bad") {
			t.Fatalf("recorded an invalid destination: %s", refs)
		}
	})

	t.Run("unpushed commits on a published branch", func(t *testing.T) {
		workspace, remote := cloneDeliveryFixture(t, "master")
		gitRun(t, workspace, "checkout", "-b", "feature")
		commitDeliveryFile(t, workspace, "first.txt", "one\n", "first")
		gitRun(t, workspace, "push", "origin", "feature")
		published := gitOutput(t, workspace, "rev-parse", "HEAD")
		if gitOutput(t, remote, "rev-parse", "refs/heads/feature") != published {
			t.Fatalf("feature was not published")
		}
		if tracking := gitOutput(t, workspace, "rev-parse", "refs/remotes/origin/feature"); tracking != published {
			gitRun(t, workspace, "update-ref", "refs/remotes/origin/feature", published)
		}
		state := mustInspect(t, workspace)
		if state.Dirty || count(state.AheadCount) != 1 || count(state.UnpushedCount) != 0 ||
			!flag(state.HasDeliverableChanges) {
			t.Fatalf("published branch = %#v", state)
		}
		commitDeliveryFile(t, workspace, "second.txt", "two\n", "second")
		state = mustInspect(t, workspace)
		if state.Dirty || count(state.AheadCount) != 2 || count(state.UnpushedCount) != 1 ||
			!flag(state.HasDeliverableChanges) {
			t.Fatalf("unpushed commit = %#v", state)
		}
	})
}

func cloneDeliveryFixture(t *testing.T, branch string) (workspace, remote string) {
	t.Helper()
	remote = filepath.Join(t.TempDir(), "remote.git")
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "init", "--template=", "--initial-branch="+branch)
	if err := os.WriteFile(filepath.Join(seed, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, seed, "add", "--", "tracked.txt")
	gitRun(t, seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", "initial")
	gitRun(t, remote, "init", "--bare", "--template=", "--initial-branch="+branch)
	gitRun(t, seed, "remote", "add", "origin", remote)
	gitRun(t, seed, "push", "origin", "HEAD:refs/heads/"+branch)
	workspace = filepath.Join(t.TempDir(), "workspace")
	command := exec.Command("git", "clone", "--template=", "--branch", branch, remote, workspace)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, output)
	}
	return workspace, remote
}

func commitDeliveryFile(t *testing.T, workspace, name, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspace, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, workspace, "add", "--", name)
	gitRun(t, workspace, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-gpg-sign", "-m", message)
}

func mustInspect(t *testing.T, workspace string) DeliveryStateResponse {
	t.Helper()
	state, err := inspectDeliveryState(context.Background(), workspace)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return state
}

func count(value *int) int {
	if value == nil {
		return -1
	}
	return *value
}

func flag(value *bool) bool {
	return value != nil && *value
}

func gitIgnored(t *testing.T, workspace, name string) bool {
	t.Helper()
	command := exec.Command("git", "check-ignore", "--", name)
	command.Dir = workspace
	err := command.Run()
	if err == nil {
		return true
	}
	if _, ok := err.(*exec.ExitError); ok {
		return false
	}
	t.Fatal(err)
	return false
}
