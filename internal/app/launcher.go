package app

import (
	"context"
	"fmt"

	"github.com/OrlojHQ/meridian/internal/domain"
)

// ReconcileCapsuleLauncher starts the one initial native PTY Run requested at
// Capsule creation. Run history is the durable completion marker: once any Run
// exists, later reconciliation never restarts the harness.
func (s *Service) ReconcileCapsuleLauncher(
	ctx context.Context,
	capsule domain.Capsule,
) error {
	if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady ||
		capsule.Maintenance != "" || capsule.LauncherHarness == "" {
		return nil
	}
	runs, err := s.ListRuns(ctx, capsule.ID, 0, 1)
	if err != nil {
		return err
	}
	if len(runs.Items) != 0 {
		return nil
	}
	profiles, err := s.ListHarnessProfiles(ctx, capsule.ID)
	if err != nil {
		return fmt.Errorf("resolve launcher harness: %w", err)
	}
	for _, profile := range profiles {
		if profile.Name != capsule.LauncherHarness {
			continue
		}
		if profile.Structured || !profile.PTY {
			return fmt.Errorf(
				"%w: launcher harness %q must be a native PTY profile",
				domain.ErrInvalid, capsule.LauncherHarness,
			)
		}
		_, err = s.StartRun(
			ctx,
			capsule.ID,
			capsule.LauncherHarness,
			"",
			"capsule-launcher:"+string(capsule.ID),
			0,
			0,
		)
		return err
	}
	return fmt.Errorf(
		"%w: launcher harness %q is unavailable in the Capsule",
		domain.ErrInvalid, capsule.LauncherHarness,
	)
}
