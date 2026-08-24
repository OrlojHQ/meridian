// Package contract contains reusable CapsuleProvider conformance tests.
package contract

import (
	"context"
	"testing"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

type Factory func() ports.CapsuleProvider

func Test(t *testing.T, factory Factory) {
	t.Helper()
	ctx := context.Background()
	provider := factory()

	capabilities, err := provider.Capabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if capabilities.Version == "" {
		t.Fatal("provider capability version is empty")
	}

	request := ports.CreateCapsuleRequest{CapsuleID: domain.CapsuleID("contract-capsule")}
	first, err := provider.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Create(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("create is not stable: %#v then %#v", first, second)
	}

	got, err := provider.Get(ctx, first.ID)
	if err != nil || got.ID != first.ID {
		t.Fatalf("get = %#v, %v", got, err)
	}
	if capabilities.Pause {
		paused, err := provider.Pause(ctx, first.ID)
		if err != nil || paused.State != ports.ProviderPaused {
			t.Fatalf("pause = %#v, %v", paused, err)
		}
		again, err := provider.Pause(ctx, first.ID)
		if err != nil || again != paused {
			t.Fatalf("repeated pause = %#v, %v", again, err)
		}
		resumed, err := provider.Resume(ctx, first.ID)
		if err != nil || resumed.State != ports.ProviderReady {
			t.Fatalf("resume = %#v, %v", resumed, err)
		}
	}
	if err := provider.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := provider.Delete(ctx, first.ID); err != nil {
		t.Fatalf("repeated delete: %v", err)
	}
	deleted, err := provider.Get(ctx, first.ID)
	if err != nil || deleted.State != ports.ProviderDeleted {
		t.Fatalf("deleted get = %#v, %v", deleted, err)
	}
}
