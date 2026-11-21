package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNoteNameGrammar(t *testing.T) {
	for _, name := range []string{"a", "build-and-test", "0x", strings.Repeat("a", 48)} {
		if !ValidNoteName(name) {
			t.Errorf("%q refused", name)
		}
	}
	for _, name := range []string{"", "-lead", "A", "a.md", "a/b", "a_b", "..", strings.Repeat("a", 49), "é"} {
		if ValidNoteName(name) {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestNoteContentIsNormalizedAndBounded(t *testing.T) {
	if got, err := NormalizeNoteContent("a\r\nb\tc\n"); err != nil || got != "a\nb\tc\n" {
		t.Fatalf("normalize = %q, %v", got, err)
	}
	for _, bad := range []string{"a\x00b", "a\x1bb", "bare\rcr", "\x7f", "\u0085", string([]byte{0xff})} {
		if _, err := NormalizeNoteContent(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
	if _, err := NormalizeNoteContent(strings.Repeat("a", MaxNoteBytes+1)); !errors.Is(err, ErrResourceExhausted) {
		t.Fatalf("oversized note = %v", err)
	}
	if _, err := NormalizeNoteContent(strings.Repeat("a", MaxNoteBytes)); err != nil {
		t.Fatalf("a note at the limit = %v", err)
	}
}

func TestPendingProposalExpiresAfterSevenDays(t *testing.T) {
	created := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	proposal := NoteProposal{State: NoteProposalPending, CreatedAt: created}
	if state := proposal.EffectiveState(created.Add(NoteProposalExpiry - time.Second)); state != NoteProposalPending {
		t.Fatalf("before expiry = %s", state)
	}
	if state := proposal.EffectiveState(created.Add(NoteProposalExpiry)); state != NoteProposalExpired {
		t.Fatalf("at expiry = %s", state)
	}
	proposal.State = NoteProposalApproved
	if state := proposal.EffectiveState(created.Add(30 * 24 * time.Hour)); state != NoteProposalApproved {
		t.Fatalf("approved later = %s", state)
	}
}
