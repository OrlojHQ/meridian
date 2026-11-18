package capsuleproto

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestBrowseListsMetadataPaginationAndReadsBinary(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dir", "a.bin"), []byte{0, 1, 2, 0xff}, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "dir", "b.txt"), []byte("text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.bin", filepath.Join(workspace, "dir", "link")); err != nil {
		t.Fatal(err)
	}
	client, closeServer := browseTestClient(t, workspace)
	defer closeServer()

	first, err := client.BrowseList(context.Background(), BrowseListRequest{Path: "dir", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.NextAfter != "b.txt" {
		t.Fatalf("first page = %#v", first)
	}
	if first.Items[0].Name != "a.bin" || first.Items[0].Type != "file" ||
		first.Items[0].Size != 4 || !first.Items[0].Executable {
		t.Fatalf("file metadata = %#v", first.Items[0])
	}
	second, err := client.BrowseList(context.Background(), BrowseListRequest{
		Path: "dir", After: first.NextAfter, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].Name != "link" ||
		second.Items[0].Type != "symlink" || second.Items[0].Executable {
		t.Fatalf("second page = %#v", second)
	}
	read, err := client.BrowseRead(context.Background(), BrowseReadRequest{Path: "dir/a.bin"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read.Content, []byte{0, 1, 2, 0xff}) || read.Size != 4 || !read.Executable {
		t.Fatalf("read response = %#v", read)
	}
}

func TestBrowseRejectsTraversalDeniedInternalsSymlinksAndBounds(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(filepath.Join(workspace, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git", "config"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".meridian-prepared"), []byte("marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "large"), bytes.Repeat([]byte("x"), maxBrowseFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := makeFIFO(filepath.Join(workspace, "pipe")); err != nil {
		t.Fatal(err)
	}
	client, closeServer := browseTestClient(t, workspace)
	defer closeServer()

	rootList, err := client.BrowseList(context.Background(), BrowseListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(rootList.Items))
	for _, item := range rootList.Items {
		names = append(names, item.Name)
	}
	if slices.Contains(names, ".git") || slices.Contains(names, ".meridian-prepared") {
		t.Fatalf("denied internals listed: %v", names)
	}
	for _, path := range []string{"../outside/secret", "/etc/passwd", "dir/../large", ".git/config", ".meridian-prepared", "escape/secret", "pipe"} {
		if _, err := client.BrowseRead(context.Background(), BrowseReadRequest{Path: path}); err == nil {
			t.Fatalf("unsafe read %q succeeded", path)
		}
	}
	if _, err := client.BrowseRead(context.Background(), BrowseReadRequest{Path: "escape"}); err == nil {
		t.Fatal("symlink content read succeeded")
	}
	if _, err := client.BrowseRead(context.Background(), BrowseReadRequest{Path: "large"}); err == nil {
		t.Fatal("oversized file read succeeded")
	}
	if _, err := client.BrowseList(context.Background(), BrowseListRequest{
		Limit: maxBrowseEntries + 1,
	}); err == nil {
		t.Fatal("oversized list limit succeeded")
	}
	if _, err := client.BrowseRead(context.Background(), BrowseReadRequest{
		Path: strings.Repeat("x", maxBrowsePathBytes+1),
	}); err == nil {
		t.Fatal("oversized path succeeded")
	}
	if _, err := client.BrowseList(context.Background(), BrowseListRequest{
		After: strings.Repeat("x", maxBrowseNameBytes+1),
	}); err == nil {
		t.Fatal("oversized pagination name succeeded")
	}
}

func TestBrowseReadResistsSymlinkSwap(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	safe := filepath.Join(workspace, "safe")
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(safe, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, closeServer := browseTestClient(t, workspace)
	defer closeServer()

	stop := make(chan struct{})
	var swap sync.WaitGroup
	swap.Add(1)
	go func() {
		defer swap.Done()
		alternate := filepath.Join(workspace, "alternate")
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Remove(alternate)
			_ = os.Symlink(outside, alternate)
			_ = os.Rename(alternate, safe)
			_ = os.Remove(alternate)
			_ = os.WriteFile(alternate, []byte("safe"), 0o600)
			_ = os.Rename(alternate, safe)
		}
	}()
	for range 500 {
		response, err := client.BrowseRead(context.Background(), BrowseReadRequest{Path: "safe"})
		if err == nil && string(response.Content) != "safe" {
			close(stop)
			swap.Wait()
			t.Fatalf("symlink swap exposed content %q", response.Content)
		}
	}
	close(stop)
	swap.Wait()
}

func TestBrowseWriteKeepsAttachmentsInsideTheirTree(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(workspace, filepath.Join(workspace, "escape")); err != nil {
		t.Fatal(err)
	}
	client, closeServer := browseTestClient(t, workspace)
	defer closeServer()
	path := ".meridian/attachments/0123456789abcdef/1-notes.txt"
	if err := client.BrowseWrite(context.Background(), BrowseWriteRequest{
		Path: path, Content: []byte("notes"),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(workspace, path))
	if err != nil || string(got) != "notes" {
		t.Fatalf("written = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(workspace, path))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info, err)
	}
	for _, bad := range []string{
		"src/notes.txt",
		".meridian/attachments/../src/notes.txt",
		"escape/outside.txt",
		".meridian/attachments/0123456789abcdef/../notes.txt",
	} {
		err := client.BrowseWrite(context.Background(), BrowseWriteRequest{Path: bad, Content: []byte("x")})
		if err == nil {
			t.Fatalf("write %s succeeded", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, "src", "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("attachment write escaped the attachment tree")
	}
}

// An attachment is the operator's file, not a project change. It must not
// make the worktree dirty or reach a delivery commit, even when the project
// .gitignore tries to un-ignore it or the repository tracks a file there.
func TestAttachmentsStayOutOfGitAndDelivery(t *testing.T) {
	workspace := deliveryRepository(t)
	if err := os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte("!.meridian/\n!.meridian/**\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, workspace, "add", "--", ".gitignore")
	gitRun(t, workspace, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
		"commit", "--no-gpg-sign", "-m", "negate")
	gitRun(t, workspace, "update-ref", "refs/remotes/origin/main", "HEAD")
	client, closeServer := browseTestClient(t, workspace)
	defer closeServer()
	for _, path := range []string{
		".meridian/attachments/0123456789abcdef/1-screen.png",
		".meridian/attachments/0123456789abcdef/2-notes.txt",
	} {
		if err := client.BrowseWrite(context.Background(), BrowseWriteRequest{
			Path: path, Content: []byte("private bytes"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if status := gitOutput(t, workspace, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("attachments show in git status:\n%s", status)
	}
	state, err := inspectDeliveryState(context.Background(), workspace)
	if err != nil || state.Dirty {
		t.Fatalf("attachments made the worktree dirty: %#v, %v", state, err)
	}

	// Even with a tracked file in the tree, staging leaves the tree as HEAD has it.
	if err := os.WriteFile(filepath.Join(workspace, ".meridian", "attachments", "tracked.txt"), []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, workspace, "add", "-f", "--", ".meridian/attachments/tracked.txt")
	gitRun(t, workspace, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
		"commit", "--no-gpg-sign", "-m", "tracked")
	if err := os.WriteFile(filepath.Join(workspace, ".meridian", "attachments", ".gitignore"), []byte("!*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "src.txt"), []byte("project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := stageDeliveryWorkspace(context.Background(), workspace, nil); err != nil {
		t.Fatal(err)
	}
	if staged := gitOutput(t, workspace, "diff", "--cached", "--name-only"); staged != "src.txt" {
		t.Fatalf("staged = %q, want only src.txt", staged)
	}
}

func browseTestClient(t *testing.T, workspace string) (*Client, func()) {
	t.Helper()
	server, err := NewServer(ServerConfig{Token: testToken, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	client, err := NewClient(httpServer.URL, testToken, httpServer.Client())
	if err != nil {
		httpServer.Close()
		t.Fatal(err)
	}
	return client, httpServer.Close
}
