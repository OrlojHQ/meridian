package transcripts

import (
	"bytes"
	"errors"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
)

func TestNotesKeyWrapBindsProjectGrantOwnerAndKEK(t *testing.T) {
	key, err := NewInstallationKey("notes-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	dek, err := key.NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	scope := NoteKeyScope{ProjectID: "p1", GrantID: "g1", Owner: "usr_a"}
	wrapped, err := key.WrapNotesDEK(scope, dek)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := key.UnwrapNotesDEK(scope, key.ID, key.Version, wrapped)
	if err != nil || !bytes.Equal(opened, dek) {
		t.Fatalf("unwrap = %v", err)
	}
	for name, moved := range map[string]NoteKeyScope{
		"another Project": {ProjectID: "p2", GrantID: "g1", Owner: "usr_a"},
		"another tree":    {ProjectID: "p1", GrantID: "g2", Owner: "usr_a"},
		"the Project key": {ProjectID: "p1", Owner: "usr_a"},
		"another owner":   {ProjectID: "p1", GrantID: "g1", Owner: "usr_b"},
	} {
		if _, err := key.UnwrapNotesDEK(moved, key.ID, key.Version, wrapped); !errors.Is(err, domain.ErrTranscriptCorrupt) {
			t.Errorf("key moved to %s opened: %v", name, err)
		}
	}
	// A Thread key slot does not accept a notes key either.
	if _, err := key.UnwrapDEK("p1", key.ID, key.Version, wrapped); err == nil {
		t.Fatal("a notes key opened as a Thread key")
	}
	if _, err := key.UnwrapNotesDEK(scope, key.ID, key.Version+1, wrapped); !errors.Is(err, domain.ErrKeyMismatch) {
		t.Fatalf("wrong KEK version = %v", err)
	}
	next, err := NewInstallationKey("notes-test", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Zero()
	rewrapped, err := RewrapNotesDEK(scope, scope, key.ID, key.Version, wrapped, key, next)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := next.UnwrapNotesDEK(scope, next.ID, next.Version, rewrapped); err != nil || !bytes.Equal(again, dek) {
		t.Fatalf("rewrapped key = %v", err)
	}
	transferred := NoteKeyScope{ProjectID: "p1", GrantID: "g1", Owner: "usr_b"}
	moved, err := RewrapNotesDEK(scope, transferred, key.ID, key.Version, wrapped, key, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := key.UnwrapNotesDEK(scope, key.ID, key.Version, moved); err == nil {
		t.Fatal("a transferred key still opens for the old owner")
	}
}

func TestNoteEnvelopesBindTheirRevisionAndProposal(t *testing.T) {
	dek := bytes.Repeat([]byte{7}, 32)
	aad := NoteRevisionAAD{ProjectID: "p1", GrantID: "g1", NoteID: "n1", Seq: 3,
		Kind: domain.NoteRevisionAgentWrite, CapsuleID: "c1"}
	sealed, err := SealNoteRevision(dek, aad, []byte("the tests run with make test"))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := OpenNoteRevision(dek, aad, sealed); err != nil || string(plain) != "the tests run with make test" {
		t.Fatalf("open = %q, %v", plain, err)
	}
	moves := []NoteRevisionAAD{aad, aad, aad, aad, aad, aad, aad}
	moves[0].ProjectID = "p2"
	moves[1].GrantID = ""
	moves[2].NoteID = "n2"
	moves[3].Seq = 4
	moves[4].Kind = domain.NoteRevisionOperatorWrite
	moves[5].CapsuleID = "c2"
	moves[6].ProposalID = "x"
	for index, moved := range moves {
		if _, err := OpenNoteRevision(dek, moved, sealed); !errors.Is(err, domain.ErrTranscriptCorrupt) {
			t.Errorf("revision moved (%d) opened: %v", index, err)
		}
	}
	proposal := NoteProposalAAD{ProjectID: "p1", ProposalID: "np1", BaseSeq: 2, ThreadID: "t1"}
	sealedProposal, err := SealNoteProposal(dek, proposal, []byte("proposed"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenNoteRevision(dek, aad, sealedProposal); err == nil {
		t.Fatal("a proposal envelope opened as a revision")
	}
	for index, moved := range []NoteProposalAAD{
		{ProjectID: "p2", ProposalID: "np1", BaseSeq: 2, ThreadID: "t1"},
		{ProjectID: "p1", ProposalID: "np2", BaseSeq: 2, ThreadID: "t1"},
		{ProjectID: "p1", ProposalID: "np1", BaseSeq: 3, ThreadID: "t1"},
		{ProjectID: "p1", ProposalID: "np1", BaseSeq: 2, ThreadID: "t2"},
	} {
		if _, err := OpenNoteProposal(dek, moved, sealedProposal); !errors.Is(err, domain.ErrTranscriptCorrupt) {
			t.Errorf("proposal moved (%d) opened: %v", index, err)
		}
	}
	if _, err := SealNoteRevision(dek, aad, bytes.Repeat([]byte("a"), domain.MaxNoteBytes+1)); !errors.Is(err, domain.ErrResourceExhausted) {
		t.Fatalf("oversized note sealed: %v", err)
	}
}
