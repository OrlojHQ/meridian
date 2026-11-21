package app_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

// A lead's notes reach the subagent only as part of its from_lead prompt,
// encrypted like the rest of it (ADR 0059).
func TestSpawnNotesReachTheChildInsideItsPrompt(t *testing.T) {
	f := newCoordFixture(t)
	_, mailbox := f.spawnLead(t, domain.CoordinationPolicy{MaxChildren: 2})
	home := t.TempDir()
	notes := capsuleproto.CoordinationNotesDir(coordproto.SocketPathForHome(home))
	if err := os.MkdirAll(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	note := "plaintext note must stay encrypted: errors are wrapped with %w"
	if err := os.WriteFile(filepath.Join(notes, "conventions.md"), []byte(note+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mailbox.SetNotesDirectory(notes)

	spawned := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-one", Name: "one", Harness: "mock",
		Prompt: "Fix the parser.", ContextFiles: []string{"conventions.md"},
	})
	if !spawned.OK {
		t.Fatalf("spawn = %+v", spawned)
	}
	link, err := f.service.GetThreadLink(f.ctx, domain.ThreadLinkID(spawned.Link.ID))
	if err != nil {
		t.Fatal(err)
	}
	f.ready(t, link.ChildCapsuleID)
	blocks, err := f.service.ThreadBlocks(f.ctx, link.ChildThreadID, 0, 10)
	if err != nil || len(blocks.Items) == 0 {
		t.Fatalf("child blocks = %#v, %v", blocks.Items, err)
	}
	first := blocks.Items[0]
	if !strings.HasPrefix(first.Content, "Fix the parser.\n") ||
		!strings.Contains(first.Content, "### conventions.md (") || !strings.Contains(first.Content, "```text\n"+note+"\n```") ||
		first.Origin == nil || first.Origin.Kind != app.OriginFromLead {
		t.Fatalf("child first message = %#v", first)
	}
	for _, path := range []string{f.store.Path(), f.store.Path() + "-wal"} {
		raw, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte("plaintext note")) || bytes.Contains(raw, []byte("conventions.md")) {
			t.Fatalf("note stored in plaintext in %s", path)
		}
	}

	// A retry after the note changed is a different request under the same key.
	if err := os.WriteFile(filepath.Join(notes, "conventions.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	retry := tool(t, mailbox, coordproto.Request{
		Type: coordproto.KindSpawn, Key: "spawn-one", Name: "one", Harness: "mock",
		Prompt: "Fix the parser.", ContextFiles: []string{"conventions.md"},
	})
	if code(retry) != coordproto.CodeConflict {
		t.Fatalf("retry after the note changed = %+v", retry)
	}
}
