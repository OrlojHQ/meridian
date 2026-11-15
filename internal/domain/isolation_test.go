package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestIsolationClassValid(t *testing.T) {
	tests := []struct {
		class IsolationClass
		want  bool
	}{
		{IsolationContainer, true},
		{IsolationSandbox, true},
		{IsolationMicroVM, true},
		{"", false},
		{"vm", false},
		{"gvisor", false},
		{"Container", false},
	}
	for _, test := range tests {
		if got := test.class.Valid(); got != test.want {
			t.Errorf("%q Valid() = %v, want %v", test.class, got, test.want)
		}
	}
	if label := IsolationClass("vm").Label(); label != "" {
		t.Errorf("unknown class Label() = %q, want empty", label)
	}
	for _, class := range []IsolationClass{IsolationContainer, IsolationSandbox, IsolationMicroVM} {
		if class.Label() != string(class) {
			t.Errorf("%q Label() = %q", class, class.Label())
		}
	}
}

func TestParseIsolationClass(t *testing.T) {
	class, err := ParseIsolationClass("")
	if err != nil || class != "" {
		t.Fatalf("ParseIsolationClass(\"\") = %q, %v", class, err)
	}
	for _, value := range []string{"container", "sandbox", "microvm"} {
		class, err := ParseIsolationClass(value)
		if err != nil || string(class) != value {
			t.Fatalf("ParseIsolationClass(%q) = %q, %v", value, class, err)
		}
	}
	for _, value := range []string{"vm", "gvisor", "Container", "microvm "} {
		class, err := ParseIsolationClass(value)
		if err == nil {
			t.Errorf("ParseIsolationClass(%q) = %q, want error", value, class)
		}
		if class != "" {
			t.Errorf("ParseIsolationClass(%q) class = %q, want empty", value, class)
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseIsolationClass(%q) error = %v, want ErrInvalid", value, err)
		}
	}
}

func TestIsolationSatisfies(t *testing.T) {
	classes := []IsolationClass{"", IsolationContainer, IsolationSandbox, IsolationMicroVM}
	rank := map[IsolationClass]int{"": -1, IsolationContainer: 0, IsolationSandbox: 1, IsolationMicroVM: 2}
	for _, required := range classes {
		for _, verified := range classes {
			want := required == "" ||
				(required.Valid() && verified.Valid() && rank[verified] >= rank[required])
			if got := required.Satisfies(verified); got != want {
				t.Errorf("%q.Satisfies(%q) = %v, want %v", required, verified, got, want)
			}
		}
	}
	// Invalid classes never satisfy a real requirement, in either position.
	if IsolationClass("vm").Satisfies(IsolationMicroVM) {
		t.Error("invalid required class satisfied a microvm installation")
	}
	if IsolationMicroVM.Satisfies("vm") {
		t.Error("microvm requirement satisfied by an invalid verified class")
	}
}

func TestRequireIsolation(t *testing.T) {
	if err := RequireIsolation("", IsolationContainer); err != nil {
		t.Fatalf("empty requirement = %v", err)
	}
	if err := RequireIsolation(IsolationSandbox, IsolationMicroVM); err != nil {
		t.Fatalf("microvm meets sandbox = %v", err)
	}
	err := RequireIsolation(IsolationMicroVM, IsolationContainer)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("container meets microvm = %v, want ErrUnsupported", err)
	}
	if got, want := err.Error(),
		"microvm isolation is required; this installation provides container"; !strings.Contains(got, want) {
		t.Errorf("error = %q, want it to contain %q", got, want)
	}
	err = RequireIsolation(IsolationSandbox, "")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("no verified class = %v, want ErrUnsupported", err)
	}
	if got := err.Error(); !strings.Contains(got, "this installation provides none") {
		t.Errorf("error = %q, want it to name the missing class", got)
	}
}
