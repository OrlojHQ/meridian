package main

import (
	"bufio"
	"bytes"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

// serveMailbox answers every request OK on a fresh socket and sends each
// decoded request to the returned channel.
func serveMailbox(t *testing.T) (string, <-chan coordproto.Request) {
	t.Helper()
	// Unix socket paths are short; t.TempDir can exceed the limit on macOS.
	dir, err := os.MkdirTemp("/tmp", "coord")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "coord.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.RemoveAll(dir)
	})
	requests := make(chan coordproto.Request, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			record, err := coordproto.ReadRecord(bufio.NewReader(conn))
			if err == nil {
				if request, err := coordproto.DecodeRequest(record); err == nil {
					requests <- request
				}
			}
			response := coordproto.OK()
			response.Link = &coordproto.Link{ID: "link-a", Name: "a", State: "active"}
			encoded, _ := coordproto.Encode(response)
			_, _ = conn.Write(encoded)
			_ = conn.Close()
		}
	}()
	return socket, requests
}

func runCoord(t *testing.T, socket, stdin string, arguments ...string) (string, error) {
	t.Helper()
	command := newRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetIn(strings.NewReader(stdin))
	command.SetArgs(append([]string{"--socket", socket}, arguments...))
	err := command.Execute()
	return output.String(), err
}

func TestSpawnSendsRepeatedContextFiles(t *testing.T) {
	socket, requests := serveMailbox(t)
	if output, err := runCoord(t, socket, "Fix the parser.",
		"spawn", "--name", "a", "--harness", "mock",
		"--context-file", "conventions.md", "--context-file", "decisions/api notes.md"); err != nil {
		t.Fatalf("spawn: %v\n%s", err, output)
	}
	request := <-requests
	if request.Type != coordproto.KindSpawn || request.Prompt != "Fix the parser." ||
		!slices.Equal(request.ContextFiles, []string{"conventions.md", "decisions/api notes.md"}) {
		t.Fatalf("spawn request = %+v", request)
	}
	if output, err := runCoord(t, socket, "Fix the parser.", "spawn", "--name", "b", "--harness", "mock"); err != nil {
		t.Fatalf("spawn without notes: %v\n%s", err, output)
	}
	if request := <-requests; request.ContextFiles != nil {
		t.Fatalf("spawn without notes sent contextFiles %q", request.ContextFiles)
	}
}

func TestSpawnRefusesContextFilesOutsideTheNotesDirectory(t *testing.T) {
	socket, requests := serveMailbox(t)
	for _, path := range []string{"/etc/passwd", "../coord.sock", "a/../../b"} {
		if _, err := runCoord(t, socket, "task", "spawn", "--name", "a", "--harness", "mock", "--context-file", path); err == nil {
			t.Errorf("--context-file %q was accepted", path)
		}
	}
	files := make([]string, 0, 2*(coordproto.MaxContextFiles+1))
	for index := range coordproto.MaxContextFiles + 1 {
		files = append(files, "--context-file", string(rune('a'+index))+".md")
	}
	if _, err := runCoord(t, socket, "task", append([]string{"spawn", "--name", "a", "--harness", "mock"}, files...)...); err == nil {
		t.Error("nine context files were accepted")
	}
	select {
	case request := <-requests:
		t.Fatalf("invalid spawn reached the mailbox: %+v", request)
	default:
	}
}

func TestSpawnHelpDescribesContextFiles(t *testing.T) {
	output, err := runCoord(t, "", "", "spawn", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--context-file", "$HOME/.meridian/notes/", "128 KiB"} {
		if !strings.Contains(output, want) {
			t.Fatalf("spawn help lacks %q:\n%s", want, output)
		}
	}
}
