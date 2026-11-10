package tui

import (
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/pkg/client"
)

func TestStartupLinePrintsPhaseAndPullProgress(t *testing.T) {
	capsule := client.Capsule{State: client.CapsuleStateCreating}
	capsule.SetStartup(client.NewOptCapsuleStartup(client.CapsuleStartup{
		Phase: client.CapsuleStartupPhasePullingImage,
		Phases: []client.CapsuleStartupPhase{
			client.CapsuleStartupPhasePullingImage,
			client.CapsuleStartupPhaseCreatingCapsule,
		},
		Pull: client.NewOptImagePullProgress(client.ImagePullProgress{
			CurrentBytes: 312_000_000,
			TotalBytes:   client.NewOptInt64(540_000_000),
		}),
		UpdatedAt: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
	}))
	line, ok := startupLine(capsule)
	if !ok || line != "Pulling image · 312 MB / 540 MB · 57%" {
		t.Fatalf("line = %q ok=%v", line, ok)
	}
	if provisioningStatus(capsule) != line {
		t.Fatalf("status = %q", provisioningStatus(capsule))
	}
	capsule.State = client.CapsuleStatePreparing
	capsule.SetPreparation(client.NewOptPreparationProgress(client.PreparationProgress{
		Stage: client.PreparationProgressStageCheckout, UpdatedAt: time.Now().UTC(),
	}))
	if provisioningStatus(capsule) != "Checking out" {
		t.Fatalf("preparing status = %q", provisioningStatus(capsule))
	}
}
