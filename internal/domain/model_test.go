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
