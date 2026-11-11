package daemon

import (
	"context"

	"github.com/OrlojHQ/meridian/internal/ports"
)

// UseTestProvider installs a Capsule provider for tests in this package.
// The production daemon leaves the hook unset and constructs Docker or Agent
// Sandbox from configuration.
func UseTestProvider(provider ports.CapsuleProvider) {
	testProvider = func(context.Context, Config) (ports.CapsuleProvider, func() error, error) {
		return provider, func() error { return nil }, nil
	}
}

// ClearTestProvider removes the test provider hook.
func ClearTestProvider() {
	testProvider = nil
}
