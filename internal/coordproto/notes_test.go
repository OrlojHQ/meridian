package coordproto

import (
	"strings"
	"testing"
)

func TestNoteRequestsHaveAClosedShape(t *testing.T) {
	valid := []Request{
		{Type: KindWriteNote, Key: "k", Name: "findings", Content: "x"},
		{Type: KindWriteNote, Key: "k", Name: "findings", BaseRevision: 3, FromFile: "scratch.md"},
		{Type: KindWriteNote, Key: "k", Name: "findings", BaseRevision: 3, Delete: true},
		{Type: KindSyncNotes},
		{Type: KindProposeNote, Key: "k", Name: "tests", FromWorkingNote: "findings"},
		{Type: KindProposeNote, Key: "k", Name: "tests", BaseRevision: 2, Delete: true},
	}
	for _, request := range valid {
		request.Protocol = Version
		if err := request.Validate(); err != nil {
			t.Errorf("%+v refused: %v", request, err)
		}
	}
	invalid := []Request{
		{Type: KindWriteNote, Key: "k", Name: "findings"},
		{Type: KindWriteNote, Key: "k", Name: "findings", Content: "x", Delete: true},
		{Type: KindWriteNote, Key: "k", Name: "-findings", Content: "x"},
		{Type: KindWriteNote, Key: "k", Name: "f.md", Content: "x"},
		{Type: KindWriteNote, Name: "findings", Content: "x"},
		{Type: KindWriteNote, Key: "k", Name: "findings", BaseRevision: -1, Content: "x"},
		{Type: KindWriteNote, Key: "k", Name: "findings", FromWorkingNote: "other"},
		{Type: KindWriteNote, Key: "k", Name: "findings", FromFile: "../etc/passwd"},
		{Type: KindWriteNote, Key: "k", Name: "findings", Content: strings.Repeat("a", MaxNoteBytes+1)},
		{Type: KindWriteNote, Key: "k", Name: "findings", Content: "a\x00b"},
		{Type: KindWriteNote, Key: "k", Name: "findings", Content: "x", Prompt: "smuggled"},
		{Type: KindSyncNotes, Name: "findings"},
		{Type: KindProposeNote, Key: "k", Name: "tests", Content: "x", FromWorkingNote: "findings"},
		{Type: KindSpawn, Key: "k", Name: "one", Harness: "mock", Prompt: "p", Content: "x"},
	}
	for _, request := range invalid {
		request.Protocol = Version
		if err := request.Validate(); err == nil {
			t.Errorf("%+v accepted", request)
		}
	}
	if !(Request{Type: KindWriteNote}).Mutating() || (Request{Type: KindSyncNotes}).Mutating() ||
		!(Request{Type: KindSyncNotes}).Remote() || !(Request{Type: KindProposeNote}).Mutating() {
		t.Fatal("note request classes are wrong")
	}
}

func TestConfigBoundsNoteProposalStates(t *testing.T) {
	self := &Self{Role: "lead", GrantID: "g"}
	for range MaxNoteProposals + 1 {
		self.NoteProposals = append(self.NoteProposals, NoteProposalRef{ID: "p", Name: "n", State: "pending"})
	}
	if err := (Config{Enabled: true, Self: self}).Validate(); err == nil {
		t.Fatal("too many proposal states accepted")
	}
	self.NoteProposals = []NoteProposalRef{{ID: "p", Name: "-bad", State: "pending"}}
	if err := (Config{Enabled: true, Self: self}).Validate(); err == nil {
		t.Fatal("invalid proposal name accepted")
	}
}
