package tui

import (
	"fmt"

	"github.com/OrlojHQ/meridian/pkg/client"
)

func checkSummary(capsule client.Capsule) string {
	checks, ok := capsule.Checks.Get()
	if !ok || !checks.Configured {
		return ""
	}
	phase, hasPhase := checks.Phase.Get()
	if !hasPhase {
		return "checks not run"
	}
	attempt, maxAttempts := 1, 1
	if value, ok := checks.Attempt.Get(); ok {
		attempt = value
	}
	if value, ok := checks.MaxAttempts.Get(); ok {
		maxAttempts = value
	}
	switch phase {
	case client.ChecksSummaryPhasePassed:
		return "checks passed"
	case client.ChecksSummaryPhaseFailing:
		if attempt >= maxAttempts {
			return fmt.Sprintf("checks failing (%d/%d)", maxAttempts, maxAttempts)
		}
		return "checks failed"
	case client.ChecksSummaryPhaseReworking:
		return fmt.Sprintf("reworking %d/%d", attempt, maxAttempts)
	case client.ChecksSummaryPhaseRunning, client.ChecksSummaryPhaseQueued:
		return fmt.Sprintf("checks %d/%d", attempt, maxAttempts)
	case client.ChecksSummaryPhaseStale:
		return "checks out of date"
	case client.ChecksSummaryPhaseError:
		return "checks could not finish"
	default:
		return ""
	}
}
