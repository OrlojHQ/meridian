package capsuleproto

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

func enabledMailbox(t *testing.T) *Mailbox {
	t.Helper()
	mailbox := NewMailbox()
	mailbox.replyLatency = 2 * time.Second
	mailbox.Configure(coordproto.Config{Enabled: true, Self: &coordproto.Self{
		Role: "lead", GrantID: "grant-1", MaxChildren: 4, AllowedHarnesses: []string{"mock"},
	}})
	return mailbox
}

func spawnRequest(key, name string) coordproto.Request {
	return coordproto.Request{
		Protocol: coordproto.Version, Type: coordproto.KindSpawn,
		Key: key, Name: name, Harness: "mock", Prompt: "work",
	}
}

// answer replies to every polled request with a link named after the request.
func answer(t *testing.T, mailbox *Mailbox, after uint64) uint64 {
	t.Helper()
	poll := mailbox.Poll(context.Background(), after, 0, 2*time.Second)
	var replies []coordproto.Reply
	for _, pending := range poll.Requests {
		response := coordproto.OK()
		response.Link = &coordproto.Link{ID: "link-" + pending.Request.Name, Name: pending.Request.Name, State: "active", Phase: coordproto.PhaseWorking}
		replies = append(replies, coordproto.Reply{Cursor: pending.Cursor, Response: response})
		after = pending.Cursor
	}
	mailbox.Reply(replies)
	return after
}

func TestMailboxDisabledAnswersEverything(t *testing.T) {
	mailbox := NewMailbox()
	for _, request := range []coordproto.Request{
		{Protocol: coordproto.Version, Type: coordproto.KindWhoAmI},
		{Protocol: coordproto.Version, Type: coordproto.KindList},
		spawnRequest("k", "a"),
	} {
		response := mailbox.Handle(context.Background(), request)
		if response.OK || response.Error.Code != coordproto.CodeCoordinationDisabled {
			t.Fatalf("%s: %+v", request.Type, response)
		}
	}
}

func TestMailboxRelaysAndCachesMutatingReplies(t *testing.T) {
	mailbox := enabledMailbox(t)
	whoami := mailbox.Handle(context.Background(), coordproto.Request{Protocol: coordproto.Version, Type: coordproto.KindWhoAmI})
	if !whoami.OK || whoami.Self.GrantID != "grant-1" {
		t.Fatalf("whoami: %+v", whoami)
	}
	done := make(chan coordproto.Response, 1)
	go func() { done <- mailbox.Handle(context.Background(), spawnRequest("spawn-a", "a")) }()
	answer(t, mailbox, 0)
	first := <-done
	if !first.OK || first.Link.ID != "link-a" {
		t.Fatalf("spawn: %+v", first)
	}
	// A retry with the same key is served from the cache without meridiand.
	if again := mailbox.Handle(context.Background(), spawnRequest("spawn-a", "a")); !again.OK || again.Link.ID != "link-a" {
		t.Fatalf("retry: %+v", again)
	}
	conflict := mailbox.Handle(context.Background(), spawnRequest("spawn-a", "b"))
	if conflict.OK || conflict.Error.Code != coordproto.CodeConflict {
		t.Fatalf("reused key: %+v", conflict)
	}
	if poll := mailbox.Poll(context.Background(), 0, 0, 10*time.Millisecond); len(poll.Requests) != 0 {
		t.Fatalf("cached replies were relayed again: %+v", poll.Requests)
	}
}

func TestMailboxPendingAnswersRetryable(t *testing.T) {
	mailbox := enabledMailbox(t)
	mailbox.replyLatency = 20 * time.Millisecond
	response := mailbox.Handle(context.Background(), spawnRequest("spawn-a", "a"))
	if response.OK || response.Error.Code != coordproto.CodePending || !response.Error.Retryable {
		t.Fatalf("unanswered request: %+v", response)
	}
	// The retry joins the original entry instead of queueing a duplicate.
	_ = mailbox.Handle(context.Background(), spawnRequest("spawn-a", "a"))
	if poll := mailbox.Poll(context.Background(), 0, 0, 10*time.Millisecond); len(poll.Requests) != 1 {
		t.Fatalf("pending requests: %d", len(poll.Requests))
	}
}

func TestMailboxRateLimitAndCapacity(t *testing.T) {
	mailbox := enabledMailbox(t)
	mailbox.replyLatency = time.Millisecond
	now := time.Unix(1_800_000_000, 0)
	mailbox.now = func() time.Time { return now }
	for index := range coordproto.RequestsPerMinute {
		key := "k" + strings.Repeat("x", index)
		if response := mailbox.Handle(context.Background(), spawnRequest(key, "a")); response.Error.Code != coordproto.CodePending {
			t.Fatalf("request %d: %+v", index, response)
		}
	}
	limited := mailbox.Handle(context.Background(), spawnRequest("over", "a"))
	if limited.Error == nil || limited.Error.Code != coordproto.CodeRateLimited || !limited.Error.Retryable {
		t.Fatalf("rate limit: %+v", limited)
	}
	now = now.Add(61 * time.Second)
	for index := len(mailbox.pending); index < coordproto.MaxPendingRequests; index++ {
		mailbox.accepted = nil
		key := "fill" + strings.Repeat("y", index)
		_ = mailbox.Handle(context.Background(), spawnRequest(key, "a"))
	}
	mailbox.accepted = nil
	busy := mailbox.Handle(context.Background(), spawnRequest("full", "a"))
	if busy.Error == nil || busy.Error.Code != coordproto.CodeBusy {
		t.Fatalf("full mailbox: %+v", busy)
	}
}

func TestMailboxWaitConsumesReportsOnce(t *testing.T) {
	mailbox := enabledMailbox(t)
	mailbox.Notify(coordproto.Notification{Links: []coordproto.Link{
		{ID: "link-a", Name: "a", State: "active"}, {ID: "link-b", Name: "b", State: "active"},
	}})
	wait := coordproto.Request{Protocol: coordproto.Version, Type: coordproto.KindWait, Links: []string{"link-a", "link-b"}, All: true, TimeoutSeconds: 5}
	done := make(chan coordproto.Response, 1)
	go func() { done <- mailbox.Handle(context.Background(), wait) }()
	mailbox.Notify(coordproto.Notification{Reports: []coordproto.Report{{Link: "link-a", Name: "a", Seq: 1, Status: "turn_ended", Summary: "A"}}})
	select {
	case response := <-done:
		t.Fatalf("wait --all returned before every subagent reported: %+v", response)
	case <-time.After(50 * time.Millisecond):
	}
	mailbox.Notify(coordproto.Notification{Reports: []coordproto.Report{{Link: "link-b", Name: "b", Seq: 1, Status: "turn_ended", Summary: "B"}}})
	response := <-done
	if !response.OK || len(response.Reports) != 2 {
		t.Fatalf("wait: %+v", response)
	}
	poll := mailbox.Poll(context.Background(), 0, 0, 10*time.Millisecond)
	if poll.Consumed["link-a"] != 1 || poll.Consumed["link-b"] != 1 || poll.ConsumedGeneration == 0 {
		t.Fatalf("consumed: %+v", poll)
	}
	again := wait
	again.TimeoutSeconds = 1
	if response := mailbox.Handle(context.Background(), again); !response.TimedOut || len(response.Reports) != 0 {
		t.Fatalf("reports delivered twice: %+v", response)
	}
	unknown := coordproto.Request{Protocol: coordproto.Version, Type: coordproto.KindWait, Links: []string{"link-z"}}
	if response := mailbox.Handle(context.Background(), unknown); response.Error == nil || response.Error.Code != coordproto.CodeNotFound {
		t.Fatalf("unknown link: %+v", response)
	}
}

func TestMailboxWaitReturnsForClosedLinks(t *testing.T) {
	mailbox := enabledMailbox(t)
	mailbox.Notify(coordproto.Notification{Links: []coordproto.Link{{ID: "link-a", Name: "a", State: "closed"}}})
	start := time.Now()
	response := mailbox.Handle(context.Background(), coordproto.Request{
		Protocol: coordproto.Version, Type: coordproto.KindWait, Links: []string{"link-a"}, TimeoutSeconds: 30,
	})
	if !response.OK || response.TimedOut || time.Since(start) > 5*time.Second {
		t.Fatalf("wait on a closed link: %+v", response)
	}
}

func TestMailboxDisableFailsPendingAndForgetsLinks(t *testing.T) {
	mailbox := enabledMailbox(t)
	mailbox.Notify(coordproto.Notification{Links: []coordproto.Link{{ID: "link-a", State: "active"}}})
	done := make(chan coordproto.Response, 1)
	go func() { done <- mailbox.Handle(context.Background(), spawnRequest("k", "a")) }()
	if poll := mailbox.Poll(context.Background(), 0, 0, 2*time.Second); len(poll.Requests) != 1 {
		t.Fatal("request not queued")
	}
	mailbox.Configure(coordproto.Config{})
	if response := <-done; response.Error == nil || response.Error.Code != coordproto.CodeCoordinationDisabled {
		t.Fatalf("pending after disable: %+v", response)
	}
	if len(mailbox.links) != 0 || len(mailbox.pending) != 0 {
		t.Fatal("disabled mailbox kept state")
	}
}

func TestCoordinationSocketIsPrivateAndStrict(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "coord")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "meridian", "coord.sock")
	listener, err := ListenCoordination(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{coord: enabledMailbox(t)}
	var serving sync.WaitGroup
	serving.Add(1)
	go func() {
		defer serving.Done()
		_ = server.ServeCoordination(listener)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		serving.Wait()
	})
	for target, mode := range map[string]os.FileMode{filepath.Dir(path): 0o700, path: 0o600} {
		info, err := os.Stat(target)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode %v: %v", target, info.Mode().Perm(), err)
		}
	}
	exchange := func(body string) coordproto.Response {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		record, err := coordproto.ReadRecord(bufio.NewReader(conn))
		if err != nil {
			t.Fatal(err)
		}
		response, err := coordproto.DecodeResponse(record)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	if response := exchange(`{"protocol":"meridian.coord.v1","type":"whoami"}` + "\n"); !response.OK {
		t.Fatalf("whoami: %+v", response)
	}
	if response := exchange(`{"protocol":"meridian.coord.v1","type":"whoami","capsule":"x"}` + "\n"); response.Error == nil || response.Error.Code != coordproto.CodeInvalidRequest {
		t.Fatalf("unknown field: %+v", response)
	}
	if _, err := ListenCoordination("relative/coord.sock"); err == nil {
		t.Fatal("relative socket path accepted")
	}
	regular := filepath.Join(dir, "file")
	if err := os.WriteFile(regular, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListenCoordination(regular); err == nil {
		t.Fatal("replaced a regular file")
	}
}
