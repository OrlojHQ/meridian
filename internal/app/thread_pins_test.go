package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/principal"
)

func TestPinThreadIsVersionedIdempotentAndClearedWithTheThread(t *testing.T) {
	ctx := operatorContext()
	store, service, _, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()

	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", IdempotencyKey: "create-pinned",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread := created.Thread
	if _, err := service.PinThread(ctx, thread.ID, 0, "pin-0"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("pin without a version = %v", err)
	}
	if _, err := service.PinThread(ctx, thread.ID, thread.ResourceVersion, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("pin without an Idempotency-Key = %v", err)
	}
	pinned, err := service.PinThread(ctx, thread.ID, thread.ResourceVersion, "pin-1")
	if err != nil {
		t.Fatal(err)
	}
	if pinned.PinnedAt.IsZero() || pinned.ResourceVersion != thread.ResourceVersion+1 ||
		!pinned.UpdatedAt.Equal(thread.UpdatedAt) {
		t.Fatalf("pinned Thread = %+v", pinned)
	}
	// A replay returns the same Thread even though its version moved on.
	if replay, err := service.PinThread(ctx, thread.ID, thread.ResourceVersion, "pin-1"); err != nil ||
		replay.ResourceVersion != pinned.ResourceVersion || !replay.PinnedAt.Equal(pinned.PinnedAt) {
		t.Fatalf("replayed pin = %+v, %v", replay, err)
	}
	if _, err := service.PinThread(ctx, thread.ID, thread.ResourceVersion, "pin-stale"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("pin at a stale version = %v", err)
	}
	// Pinning a pinned Thread changes nothing.
	if again, err := service.PinThread(ctx, thread.ID, pinned.ResourceVersion, "pin-2"); err != nil ||
		again.ResourceVersion != pinned.ResourceVersion {
		t.Fatalf("second pin = %+v, %v", again, err)
	}
	unpinned, err := service.UnpinThread(ctx, thread.ID, pinned.ResourceVersion, "unpin-1")
	if err != nil || !unpinned.PinnedAt.IsZero() || unpinned.ResourceVersion != pinned.ResourceVersion+1 {
		t.Fatalf("unpinned Thread = %+v, %v", unpinned, err)
	}
	if again, err := service.UnpinThread(ctx, thread.ID, unpinned.ResourceVersion, "unpin-2"); err != nil ||
		again.ResourceVersion != unpinned.ResourceVersion {
		t.Fatalf("unpin of an unpinned Thread = %+v, %v", again, err)
	}

	// Concurrent pins at one version: exactly one wins, the rest conflict.
	var wins, conflicts int
	var mu sync.Mutex
	var group sync.WaitGroup
	for index := range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := service.PinThread(ctx, thread.ID, unpinned.ResourceVersion, "race-"+string(rune('a'+index)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, domain.ErrConflict):
				conflicts++
			default:
				t.Errorf("concurrent pin = %v", err)
			}
		}()
	}
	group.Wait()
	if wins != 1 || conflicts != 3 {
		t.Fatalf("concurrent pins: %d won, %d conflicted", wins, conflicts)
	}

	// Archiving clears the pin, and an archived Thread cannot be pinned.
	current, err := service.GetThread(ctx, thread.ID)
	if err != nil || current.PinnedAt.IsZero() {
		t.Fatalf("Thread before archive = %+v, %v", current, err)
	}
	archived, err := service.ArchiveThread(ctx, thread.ID, current.ResourceVersion, "archive-pinned")
	if err != nil || !archived.PinnedAt.IsZero() {
		t.Fatalf("archived Thread = %+v, %v", archived, err)
	}
	if _, err := service.PinThread(ctx, thread.ID, archived.ResourceVersion, "pin-archived"); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("pin an archived Thread = %v", err)
	}

	// Crypto-shred clears it too.
	second, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", IdempotencyKey: "create-shredded",
	})
	if err != nil {
		t.Fatal(err)
	}
	shredMe, err := service.PinThread(ctx, second.Thread.ID, second.Thread.ResourceVersion, "pin-shred")
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := service.DeleteThread(ctx, shredMe.ID, shredMe.ResourceVersion, "shred-pinned")
	if err != nil || deleted.State != domain.ThreadDeleted || !deleted.PinnedAt.IsZero() {
		t.Fatalf("crypto-shredded Thread = %+v, %v", deleted, err)
	}
}

func TestPausedThreadKeepsItsPin(t *testing.T) {
	ctx := operatorContext()
	store, service, _, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", FirstMessage: "hello", Start: true, IdempotencyKey: "create-paused",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := service.GetThread(ctx, created.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if thread, err = service.PinThread(ctx, thread.ID, thread.ResourceVersion, "pin-paused"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelThread(ctx, thread.ID, thread.ResourceVersion, "cancel-paused"); err != nil {
		t.Fatal(err)
	}
	paused, err := service.GetThread(ctx, thread.ID)
	if err != nil || paused.State != domain.ThreadPaused || paused.PinnedAt.IsZero() {
		t.Fatalf("paused Thread = %+v, %v", paused, err)
	}
}

// Only the owner pins (ADR 0046). Another member, the break-glass admin, a
// Capsule acting for the owner, and daemon-internal work all see the Thread
// as not found, and nothing changes.
func TestOnlyTheOwnerPinsAThread(t *testing.T) {
	ctx := operatorContext()
	store, service, _, key, capsuleID := threadService(t, ctx)
	defer store.Close()
	defer key.Zero()
	created, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: capsuleID, Harness: "mock", IdempotencyKey: "create-owned",
	})
	if err != nil {
		t.Fatal(err)
	}
	thread := created.Thread
	other, ok := principal.User("usr_other_member")
	if !ok {
		t.Fatal("other member principal")
	}
	asCapsule, ok := principal.Capsule(principal.BootstrapOwner, capsuleID)
	if !ok {
		t.Fatal("Capsule principal")
	}
	for name, caller := range map[string]context.Context{
		"another member": principal.With(context.Background(), other),
		"break-glass":    principal.With(context.Background(), principal.BreakGlass()),
		"capsule":        principal.With(context.Background(), asCapsule),
		"system":         principal.WithSystem(context.Background()),
	} {
		if _, err := service.PinThread(caller, thread.ID, thread.ResourceVersion, "pin-"+name); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s pinned: %v", name, err)
		}
		if _, err := service.UnpinThread(caller, thread.ID, thread.ResourceVersion, "unpin-"+name); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s unpinned: %v", name, err)
		}
	}
	if _, err := service.PinThread(context.Background(), thread.ID, thread.ResourceVersion, "pin-anonymous"); err == nil {
		t.Fatal("pinned without a principal")
	}
	current, err := service.GetThread(ctx, thread.ID)
	if err != nil || !current.PinnedAt.IsZero() || current.ResourceVersion != thread.ResourceVersion {
		t.Fatalf("Thread after refused pins = %+v, %v", current, err)
	}
	if _, err := service.PinThread(ctx, thread.ID, thread.ResourceVersion, "pin-owner"); err != nil {
		t.Fatalf("owner pin = %v", err)
	}
}

// A pinned lead is still an ordinary lead: pinning grants nothing and is not
// operator presence, a subagent cannot be pinned, and a message to the
// pinned lead renews its grant like any operator message (ADR 0059).
func TestPinnedLeadRenewsOnlyOnOperatorMessages(t *testing.T) {
	f := newCoordFixture(t)
	lead, mailbox := f.spawnLead(t, domain.CoordinationPolicy{UnattendedLimit: 10 * time.Minute})
	link := f.spawnSubagent(t, mailbox, "one")
	created := f.leadGrant(t, lead)

	f.advance(time.Minute)
	thread, err := f.service.GetThread(f.ctx, lead.Intent.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := f.service.PinThread(f.ctx, thread.ID, thread.ResourceVersion, "pin-lead")
	if err != nil || pinned.PinnedAt.IsZero() {
		t.Fatalf("pinned lead = %+v, %v", pinned, err)
	}
	grant := f.leadGrant(t, lead)
	if !grant.LastOperatorAt.Equal(created.LastOperatorAt) || grant.ResourceVersion != created.ResourceVersion {
		t.Fatalf("pinning touched the grant: %+v -> %+v", created, grant)
	}

	sub, err := f.service.GetThread(f.ctx, link.ChildThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PinThread(f.ctx, sub.ID, sub.ResourceVersion, "pin-sub"); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("pin a subagent = %v", err)
	}

	// The grant lapses with nobody watching; the pin stays.
	f.advance(15 * time.Minute)
	f.service.CoordinationTick(asSystem(f.ctx))
	lapsed := f.leadGrant(t, lead)
	if lapsed.StateAt(f.clock.Now()) != domain.GrantLapsed {
		t.Fatalf("grant after unattended time = %+v", lapsed)
	}
	current, err := f.service.GetThread(f.ctx, thread.ID)
	if err != nil || current.PinnedAt.IsZero() {
		t.Fatalf("pin after lapse = %+v, %v", current, err)
	}

	// Sending to the pinned lead is the normal operator message path.
	f.messageLead(t, lead, "back-to-pinned")
	reopened := f.leadGrant(t, lead)
	if !reopened.LastOperatorAt.After(lapsed.LastOperatorAt) || !reopened.LapsedAt.IsZero() ||
		!reopened.Usable(f.clock.Now()) {
		t.Fatalf("grant after a message to the pinned lead = %+v", reopened)
	}
}
