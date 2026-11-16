package domain

import "fmt"

// IsolationClass is the isolation boundary a provider actually verified, in
// increasing strength (ADR 0050): container shares the host kernel with no
// added user-kernel boundary, sandbox adds a user-kernel boundary inside the
// host kernel's process tree (gVisor), microvm is a hardware-virtualized
// guest with its own kernel (Kata Containers).
type IsolationClass string

const (
	IsolationContainer IsolationClass = "container"
	IsolationSandbox   IsolationClass = "sandbox"
	IsolationMicroVM   IsolationClass = "microvm"
)

var isolationRank = map[IsolationClass]int{
	IsolationContainer: 0,
	IsolationSandbox:   1,
	IsolationMicroVM:   2,
}

// Valid reports whether the class is one of the three defined classes.
func (c IsolationClass) Valid() bool {
	_, ok := isolationRank[c]
	return ok
}

// Label renders the class for the API and logs. Unknown classes return an
// empty string.
func (c IsolationClass) Label() string {
	switch c {
	case IsolationContainer:
		return "container"
	case IsolationSandbox:
		return "sandbox"
	case IsolationMicroVM:
		return "microvm"
	default:
		return ""
	}
}

// ParseIsolationClass parses an operator- or API-supplied class name. Empty
// input is valid and means "no requirement".
func ParseIsolationClass(value string) (IsolationClass, error) {
	if value == "" {
		return "", nil
	}
	class := IsolationClass(value)
	if !class.Valid() {
		return "", fmt.Errorf("%w: unknown isolation class %q", ErrInvalid, value)
	}
	return class, nil
}

// Satisfies reports whether the verified class meets the required class. An
// empty required class needs nothing. An empty verified class satisfies only
// an empty requirement: an installation that verified nothing may not claim
// to meet anything.
func (required IsolationClass) Satisfies(verified IsolationClass) bool {
	if required == "" {
		return true
	}
	if !required.Valid() || !verified.Valid() {
		return false
	}
	return isolationRank[verified] >= isolationRank[required]
}

// RequireIsolation fails with ErrUnsupported when verified does not meet
// required. Empty input on either side behaves like Satisfies.
func RequireIsolation(required, verified IsolationClass) error {
	if required.Satisfies(verified) {
		return nil
	}
	provided := verified.Label()
	if provided == "" {
		provided = "none"
	}
	return fmt.Errorf(
		"%w: %s isolation is required; this installation provides %s",
		ErrUnsupported, required.Label(), provided,
	)
}
