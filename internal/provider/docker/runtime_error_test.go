package docker

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

func TestMapRuntimeErrorPreservesProtocolCode(t *testing.T) {
	err := mapRuntimeError(&capsuleproto.Error{
		Status: http.StatusUnprocessableEntity, Code: "structured_interaction_unavailable",
	})
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("mapped = %v", err)
	}
	var protocolError *capsuleproto.Error
	if !errors.As(err, &protocolError) || protocolError.Code != "structured_interaction_unavailable" {
		t.Fatalf("protocol code lost: %v", err)
	}
}

func TestMapRuntimeErrorGitFailedIsNotIllegalTransition(t *testing.T) {
	err := mapRuntimeError(&capsuleproto.Error{
		Status: http.StatusUnprocessableEntity, Code: "git_failed",
	})
	if !errors.Is(err, domain.ErrWorkspaceGit) {
		t.Fatalf("git_failed = %v", err)
	}
	if errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatal("git_failed mapped to illegal lifecycle transition")
	}
	if !strings.Contains(err.Error(), "git_failed") {
		t.Fatalf("git_failed message = %q", err.Error())
	}
}

func TestMapRuntimeErrorPassthrough(t *testing.T) {
	original := errors.New("dial supervisor")
	if got := mapRuntimeError(original); got != original {
		t.Fatalf("passthrough = %v", got)
	}
	if got := mapRuntimeError(nil); got != nil {
		t.Fatalf("nil = %v", got)
	}
	timeout := &capsuleproto.Error{Status: http.StatusGatewayTimeout, Code: "git_timeout"}
	if got := mapRuntimeError(timeout); !errors.Is(got, timeout) {
		t.Fatalf("unmapped protocol = %v", got)
	}
}
