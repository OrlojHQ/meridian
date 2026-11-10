package tui

import (
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/pkg/client"
)

func startupLine(capsule client.Capsule) (string, bool) {
	startup, ok := capsule.Startup.Get()
	if !ok {
		return "", false
	}
	current, total := int64(0), int64(0)
	if pull, ok := startup.Pull.Get(); ok {
		current = pull.CurrentBytes
		total, _ = pull.TotalBytes.Get()
	}
	line := domain.FormatStartup(domain.StartupPhase(startup.Phase), current, total)
	if line == "" {
		return "", false
	}
	return line, true
}

func provisioningStatus(capsule client.Capsule) string {
	if capsule.State == client.CapsuleStatePreparing {
		if preparation, ok := capsule.Preparation.Get(); ok {
			return preparationLabel(string(preparation.Stage))
		}
	}
	if line, ok := startupLine(capsule); ok {
		return line
	}
	return "Provisioning the Capsule…"
}
