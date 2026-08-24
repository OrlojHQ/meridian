package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/provider/contract"
)

func TestProviderContract(t *testing.T) {
	contract.Test(t, func() ports.CapsuleProvider {
		return New(Options{})
	})
}

func TestCancellationAndFailures(t *testing.T) {
	provider := New(Options{
		Delay:    time.Second,
		Failures: map[string]int{"get": 1},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Get(ctx, "fake-any"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get error = %v", err)
	}

	provider.delay = 0
	if _, err := provider.Get(context.Background(), "fake-any"); err == nil {
		t.Fatal("expected injected failure")
	}
	if _, err := provider.Get(context.Background(), "fake-any"); err != nil {
		t.Fatalf("failure was not bounded: %v", err)
	}
}

func TestStructuredRuntimeIsExplicitlyUnsupported(t *testing.T) {
	provider := New(Options{})
	capabilities, err := provider.Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Structured {
		t.Fatal("fake provider advertised structured runtime")
	}
	_, err = provider.StartStructured(context.Background(), ports.RuntimeStructuredStartRequest{
		RunID: "run-1", Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindStart,
			ID: "start-1", SessionID: "session-1",
		},
	})
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("structured start error = %v", err)
	}
}
