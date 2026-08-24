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
