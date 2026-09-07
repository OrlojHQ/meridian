package domain

import (
	"errors"
	"testing"
	"time"
)

func TestCapsuleTransitionTable(t *testing.T) {
	states := []CapsuleState{
		CapsuleCreating, CapsulePreparing, CapsuleReady, CapsulePaused,
		CapsuleFailed, CapsuleSealed, CapsuleDeleting, CapsuleDeleted,
	}
	legal := map[[2]CapsuleState]bool{
		{CapsuleCreating, CapsulePreparing}: true,
		{CapsuleCreating, CapsuleFailed}:    true,
		{CapsuleCreating, CapsuleDeleting}:  true,
		{CapsulePreparing, CapsuleReady}:    true,
		{CapsulePreparing, CapsuleFailed}:   true,
		{CapsulePreparing, CapsuleDeleting}: true,
		{CapsuleReady, CapsulePaused}:       true,
		{CapsuleReady, CapsuleFailed}:       true,
		{CapsuleReady, CapsuleSealed}:       true,
		{CapsuleReady, CapsuleDeleting}:     true,
		{CapsulePaused, CapsuleReady}:       true,
		{CapsulePaused, CapsuleFailed}:      true,
		{CapsulePaused, CapsuleSealed}:      true,
		{CapsulePaused, CapsuleDeleting}:    true,
		{CapsuleFailed, CapsuleCreating}:    true,
		{CapsuleFailed, CapsulePreparing}:   true,
		{CapsuleFailed, CapsuleReady}:       true,
		{CapsuleFailed, CapsulePaused}:      true,
		{CapsuleFailed, CapsuleDeleting}:    true,
		{CapsuleDeleting, CapsuleDeleted}:   true,
		{CapsuleDeleting, CapsuleFailed}:    true,
	}
	for _, from := range states {
		for _, to := range states {
			want := from == to || legal[[2]CapsuleState{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %t, want %t", from, to, got, want)
			}
		}
	}
}

func TestTerminalCapsulesRejectMutations(t *testing.T) {
	for _, state := range []CapsuleState{CapsuleSealed, CapsuleDeleted} {
		capsule := Capsule{State: state}
		if err := capsule.CanMutate(); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s CanMutate error = %v", state, err)
		}
		if err := capsule.Transition(CapsuleReady, time.Now()); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s transition error = %v", state, err)
		}
	}
}

func TestRunTransitionTableAndTerminalStates(t *testing.T) {
	states := []RunState{
		RunQueued, RunStarting, RunRunning, RunSucceeded, RunFailed, RunCancelling, RunCancelled,
	}
	legal := map[[2]RunState]bool{
		{RunQueued, RunStarting}:      true,
		{RunQueued, RunCancelling}:    true,
		{RunQueued, RunFailed}:        true,
		{RunStarting, RunRunning}:     true,
		{RunStarting, RunSucceeded}:   true,
		{RunStarting, RunFailed}:      true,
		{RunStarting, RunCancelling}:  true,
		{RunStarting, RunCancelled}:   true,
		{RunRunning, RunSucceeded}:    true,
		{RunRunning, RunFailed}:       true,
		{RunRunning, RunCancelling}:   true,
		{RunRunning, RunCancelled}:    true,
		{RunCancelling, RunCancelled}: true,
		{RunCancelling, RunFailed}:    true,
	}
	for _, from := range states {
		for _, to := range states {
			want := from == to || legal[[2]RunState{from, to}]
			if got := CanTransitionRun(from, to); got != want {
				t.Errorf("CanTransitionRun(%s, %s) = %t, want %t", from, to, got, want)
			}
		}
	}
	for _, terminal := range []RunState{RunSucceeded, RunFailed, RunCancelled} {
		run := Run{State: terminal, ResourceVersion: 1}
		if err := run.Transition(RunRunning, time.Now()); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s transition error = %v", terminal, err)
		}
	}
}

func TestTimelineValidationRejectsInvalidLineageShapes(t *testing.T) {
	root := Timeline{
		ID: "timeline", ProjectID: "project", CapsuleID: "capsule",
		Reason: TimelineRoot, CreatedAt: time.Now(),
	}
	if err := root.Validate(); err != nil {
		t.Fatal(err)
	}
	root.ForkedFromMomentID = "moment"
	if err := root.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("root fork validation = %v", err)
	}
	root.Reason = TimelineShard
	root.ForkedFromMomentID = ""
	if err := root.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("descendant validation = %v", err)
	}
}

func TestThreadTransitionTableAndCryptoShred(t *testing.T) {
	states := []ThreadState{ThreadActive, ThreadPaused, ThreadArchived, ThreadDeleted}
	legal := map[[2]ThreadState]bool{
		{ThreadActive, ThreadPaused}:    true,
		{ThreadActive, ThreadArchived}:  true,
		{ThreadActive, ThreadDeleted}:   true,
		{ThreadPaused, ThreadActive}:    true,
		{ThreadPaused, ThreadArchived}:  true,
		{ThreadPaused, ThreadDeleted}:   true,
		{ThreadArchived, ThreadDeleted}: true,
	}
	for _, from := range states {
		for _, to := range states {
			want := from == to || legal[[2]ThreadState{from, to}]
			if got := CanTransitionThread(from, to); got != want {
				t.Errorf("CanTransitionThread(%s, %s) = %t, want %t", from, to, got, want)
			}
		}
	}
	now := time.Now()
	thread := Thread{
		State: ThreadActive, WrappedDEK: []byte("wrapped"), CurrentRunID: "run",
		ResourceVersion: 1,
	}
	if err := thread.Transition(ThreadDeleted, now); err != nil {
		t.Fatal(err)
	}
	if thread.WrappedDEK != nil || thread.CurrentRunID != "" || thread.DeletedAt.IsZero() ||
		thread.ResourceVersion != 2 {
		t.Fatalf("crypto-shredded Thread = %#v", thread)
	}
}

func TestThreadMessageSequenceShape(t *testing.T) {
	now := time.Now().UTC()
	for blockCount := 1; blockCount <= 128; blockCount++ {
		message := ThreadMessage{
			ID: "message", ThreadID: "thread", Sequence: 42,
			Role: ThreadRoleAssistant, Kind: ThreadMessageResponse, CreatedAt: now,
		}
		for sequence := 1; sequence <= blockCount; sequence++ {
			message.Blocks = append(message.Blocks, ThreadBlock{
				ID: "block", ThreadID: message.ThreadID, MessageID: message.ID,
				MessageSequence: message.Sequence, Sequence: int64(sequence),
				Kind: ThreadBlockText, EnvelopeVersion: 1,
				Ciphertext: []byte("ciphertext"), CreatedAt: now,
			})
		}
		if err := message.Validate(); err != nil {
			t.Fatalf("block count %d: %v", blockCount, err)
		}
		message.Blocks[len(message.Blocks)-1].Sequence++
		if err := message.Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("out-of-order block count %d: %v", blockCount, err)
		}
	}
}

func TestImageForHarnessAllowlist(t *testing.T) {
	project := Project{ImageReference: "meridian-capsule:dev"}
	image, err := project.ImageForHarness("mock")
	if err != nil || image != "meridian-capsule:dev" {
		t.Fatalf("default image = %q, %v", image, err)
	}

	project.HarnessImages = []HarnessImage{
		{Name: "opencode", ImageReference: "meridian-capsule-opencode:dev"},
		{Name: "mock", ImageReference: "meridian-capsule:dev"},
	}
	image, err = project.ImageForHarness("opencode")
	if err != nil || image != "meridian-capsule-opencode:dev" {
		t.Fatalf("allowlisted image = %q, %v", image, err)
	}
	if _, err := project.ImageForHarness("pi"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown harness error = %v", err)
	}
	if _, err := ParseHarnessImageSpec("opencode"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bare name error = %v", err)
	}
	items, err := ParseHarnessImageSpecs("opencode=meridian-capsule-opencode:dev, mock=meridian-capsule:dev")
	if err != nil || len(items) != 2 || items[0].Name != "opencode" {
		t.Fatalf("specs = %#v, %v", items, err)
	}
	catalog := InstallationHarnessImages("meridian-capsule:dev", "")
	if len(catalog) != 5 || catalog[0].Name != "mock" || catalog[0].ImageReference != "meridian-capsule:dev" ||
		catalog[1].Name != "opencode" || catalog[1].ImageReference != "meridian-capsule-opencode:dev" ||
		catalog[2].Name != "pi" || catalog[2].ImageReference != "meridian-capsule-pi:dev" ||
		catalog[3].Name != "claude" || catalog[3].ImageReference != "meridian-capsule-claude:dev" ||
		catalog[4].Name != "codex" || catalog[4].ImageReference != "meridian-capsule-codex:dev" {
		t.Fatalf("catalog = %#v", catalog)
	}
	published := InstallationHarnessImages("ghcr.io/orlojhq/meridian-capsule:v1.2.3", "")
	if published[0].ImageReference != "ghcr.io/orlojhq/meridian-capsule:v1.2.3" ||
		published[1].ImageReference != "ghcr.io/orlojhq/meridian-capsule-opencode:v1.2.3" ||
		published[2].ImageReference != "ghcr.io/orlojhq/meridian-capsule-pi:v1.2.3" ||
		published[3].ImageReference != "ghcr.io/orlojhq/meridian-capsule-claude:v1.2.3" ||
		published[4].ImageReference != "ghcr.io/orlojhq/meridian-capsule-codex:v1.2.3" {
		t.Fatalf("published catalog = %#v", published)
	}
	pinned := InstallationHarnessImages(
		"ghcr.io/orlojhq/meridian-capsule@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"v1.2.3",
	)
	if pinned[0].ImageReference != "ghcr.io/orlojhq/meridian-capsule@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		pinned[1].ImageReference != "ghcr.io/orlojhq/meridian-capsule-opencode:v1.2.3" {
		t.Fatalf("digest-pinned catalog = %#v", pinned)
	}
	digestOnly := InstallationHarnessImages(
		"ghcr.io/orlojhq/meridian-capsule@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"",
	)
	if digestOnly[1].ImageReference != "ghcr.io/orlojhq/meridian-capsule-opencode:dev" {
		t.Fatalf("digest-only catalog = %#v", digestOnly)
	}
	mirror := InstallationHarnessImages("localhost:5000/orloj/meridian-capsule:v9", "")
	if mirror[1].ImageReference != "localhost:5000/orloj/meridian-capsule-opencode:v9" {
		t.Fatalf("mirrored catalog = %#v", mirror)
	}
	if DefaultCapsuleImage("dev") != LocalCapsuleImage ||
		DefaultCapsuleImage("1.2.3-next") != LocalCapsuleImage ||
		DefaultCapsuleImage("1.2.3") != "ghcr.io/orlojhq/meridian-capsule:v1.2.3" ||
		DefaultCapsuleImage("v1.2.3") != "ghcr.io/orlojhq/meridian-capsule:v1.2.3" {
		t.Fatalf("defaults = %q %q %q %q", DefaultCapsuleImage("dev"), DefaultCapsuleImage("1.2.3-next"), DefaultCapsuleImage("1.2.3"), DefaultCapsuleImage("v1.2.3"))
	}
	if RegistryQualifiedImage("meridian-capsule:dev") ||
		!RegistryQualifiedImage("ghcr.io/orlojhq/meridian-capsule:v1.2.3") ||
		!RegistryQualifiedImage("localhost:5000/orloj/meridian-capsule:v9") {
		t.Fatal("registry qualification mismatch")
	}
}
