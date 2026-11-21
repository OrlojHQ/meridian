package capsuleproto

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

// A wake headline leads with the subagent's name, so the mailbox's own
// requests take a name wherever they take a link ID.
func TestMailboxResolvesSubagentNames(t *testing.T) {
	mailbox, dir := fileMailbox(t)

	result := mailbox.Handle(context.Background(), coordproto.Request{
		Protocol: coordproto.Version, Type: coordproto.KindResult, Link: "a",
	})
	if !result.OK || result.Link == nil || result.Link.ID != "link-a" || len(result.Reports) != 1 ||
		result.Reports[0].Summary != "full report text" {
		t.Fatalf("result by name = %+v", result)
	}

	mailbox.Notify(coordproto.Notification{Reports: []coordproto.Report{
		{Link: "link-a", Name: "a", Seq: 4, Status: "done", Summary: "again"},
	}})
	wait := mailbox.Handle(context.Background(), coordproto.Request{
		Protocol: coordproto.Version, Type: coordproto.KindWait, Links: []string{"a"}, TimeoutSeconds: 1,
	})
	if !wait.OK || wait.TimedOut || len(wait.Reports) != 1 || wait.Reports[0].Seq != 4 {
		t.Fatalf("wait by name = %+v", wait)
	}

	// patch is relayed as the lead wrote it, so meridiand resolves the name
	// and a retry's digest does not depend on what the mailbox knows. The
	// file is still named by the link ID.
	generation := mailbox.Poll(context.Background(), 0, 0, 10*time.Millisecond).ConsumedGeneration
	done := make(chan coordproto.Response, 1)
	go func() {
		done <- mailbox.Handle(context.Background(), coordproto.Request{
			Protocol: coordproto.Version, Type: coordproto.KindPatch, Link: "a", ToFile: true,
		})
	}()
	poll := mailbox.Poll(context.Background(), 0, generation, 2*time.Second)
	if len(poll.Requests) != 1 || poll.Requests[0].Request.Link != "a" {
		t.Fatalf("relayed patch = %+v", poll.Requests)
	}
	response := coordproto.OK()
	response.Patch = "diff\n"
	mailbox.Reply([]coordproto.Reply{{Cursor: poll.Requests[0].Cursor, Response: response}})
	if patch := <-done; !patch.OK || patch.File != filepath.Join(dir, "link-a", "3.patch") {
		t.Fatalf("patch toFile by name = %+v", patch)
	}

	for _, ref := range []string{"b", "link-b", "A"} {
		missing := mailbox.Handle(context.Background(), coordproto.Request{
			Protocol: coordproto.Version, Type: coordproto.KindResult, Link: ref,
		})
		if missing.OK || missing.Error.Code != coordproto.CodeNotFound {
			t.Fatalf("result for %q = %+v", ref, missing)
		}
	}
}
