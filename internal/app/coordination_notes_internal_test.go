package app

import (
	"context"
	"testing"

	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

// The lead Capsule's mailbox resolves contextFiles before relaying, so a
// spawn that still names them did not come from a mailbox that did.
func TestMeridiandRefusesUnresolvedContextFiles(t *testing.T) {
	request := coordproto.Request{
		Protocol: coordproto.Version, Type: coordproto.KindSpawn, Key: "k", Name: "a",
		Harness: "mock", Prompt: "task", ContextFiles: []string{"conventions.md"},
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	response := (&Service{}).handleCoordinationRequest(context.Background(), domain.Capsule{}, request)
	if response.OK || response.Error.Code != coordproto.CodeInvalidRequest {
		t.Fatalf("unresolved contextFiles = %+v", response)
	}
}
