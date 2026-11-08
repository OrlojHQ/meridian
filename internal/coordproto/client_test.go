package coordproto

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoReportsMissingMailboxAsDisabled(t *testing.T) {
	// Exceed sun_path on every platform so connect fails with EINVAL, not
	// ENOENT, and the client must still recognise the socket is absent.
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(t.TempDir(), "absent.sock"),
		filepath.Join(long, "absent.sock"),
	} {
		response, err := Do(context.Background(), path, Request{Type: KindWhoAmI})
		if err != nil {
			t.Fatalf("Do(%d-byte path) error = %v", len(path), err)
		}
		if response.OK || response.Error == nil || response.Error.Code != CodeCoordinationDisabled {
			t.Fatalf("Do(%d-byte path) = %+v, want %s", len(path), response, CodeCoordinationDisabled)
		}
	}
}

func TestDoReportsUnreachableExistingPathAsLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coord.sock")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	response, err := Do(context.Background(), path, Request{Type: KindWhoAmI})
	if err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || response.Error.Code != CodeMailboxLost {
		t.Fatalf("Do(non-socket file) = %+v, want %s", response, CodeMailboxLost)
	}
}
