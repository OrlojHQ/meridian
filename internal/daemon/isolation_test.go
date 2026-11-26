package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
	agentsandboxprovider "github.com/OrlojHQ/meridian/internal/provider/agentsandbox"
)

func TestCheckIsolationFloor(t *testing.T) {
	tests := []struct {
		name     string
		required domain.IsolationClass
		verified domain.IsolationClass
		wantErr  bool
		unsup    bool
	}{
		{"no floor", "", domain.IsolationContainer, false, false},
		{"no floor, no verified class", "", "", false, false},
		{"container floor met", domain.IsolationContainer, domain.IsolationContainer, false, false},
		{"microvm floor met", domain.IsolationMicroVM, domain.IsolationMicroVM, false, false},
		{"stronger verified class", domain.IsolationContainer, domain.IsolationMicroVM, false, false},
		{"container on no verified class", domain.IsolationContainer, "", true, true},
		{"sandbox on container", domain.IsolationSandbox, domain.IsolationContainer, true, true},
		{"microvm on sandbox", domain.IsolationMicroVM, domain.IsolationSandbox, true, true},
		{"microvm on nothing", domain.IsolationMicroVM, "", true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := checkIsolationFloor(test.required, test.verified)
			if test.wantErr && err == nil {
				t.Fatalf("checkIsolationFloor(%q, %q) = nil, want error", test.required, test.verified)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("checkIsolationFloor(%q, %q) = %v, want nil", test.required, test.verified, err)
			}
			if test.unsup && !errors.Is(err, domain.ErrUnsupported) {
				t.Fatalf("error = %v, want ErrUnsupported", err)
			}
		})
	}
	if err := checkIsolationFloor("vm", domain.IsolationContainer); !errors.Is(err, domain.ErrUnsupported) {
		t.Errorf("invalid floor class = %v, want ErrUnsupported", err)
	}
}

// ADR 0065: a microvm installation whose control plane runs outside the
// cluster could only reach Capsules through port-forward, which cannot reach a
// Kata guest. meridiand refuses to start before touching its data directory.
func TestRunRefusesOutOfClusterMicroVM(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	err := run(context.Background(), Config{
		Provider: "agentsandbox", Listen: "127.0.0.1:0", DataDir: dataDir,
		AgentSandbox: agentsandboxprovider.Config{
			IsolationClass: domain.IsolationMicroVM, RuntimeClass: "kata",
		},
	})
	if !errors.Is(err, domain.ErrUnsupported) || !strings.Contains(err.Error(), "ADR 0065") {
		t.Fatalf("out-of-cluster microvm start = %v", err)
	}
	if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
		t.Fatalf("refused start created the data directory: %v", statErr)
	}
}
