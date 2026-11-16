package daemon

import (
	"errors"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
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
