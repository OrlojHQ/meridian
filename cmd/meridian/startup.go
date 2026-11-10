package main

import (
	"context"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/pkg/client"
)

func capsuleStartupLine(ctx context.Context, api *client.Client, capsuleID string) string {
	if api == nil || capsuleID == "" {
		return ""
	}
	result, err := api.GetCapsule(ctx, client.GetCapsuleParams{CapsuleId: capsuleID})
	if err != nil {
		return ""
	}
	headers, ok := result.(*client.CapsuleHeaders)
	if !ok {
		return ""
	}
	capsule := headers.Response
	if capsule.State == client.CapsuleStatePreparing {
		if _, ok := capsule.Preparation.Get(); ok {
			return ""
		}
	}
	startup, ok := capsule.Startup.Get()
	if !ok {
		return ""
	}
	current, total := int64(0), int64(0)
	if pull, ok := startup.Pull.Get(); ok {
		current = pull.CurrentBytes
		total, _ = pull.TotalBytes.Get()
	}
	return domain.FormatStartup(domain.StartupPhase(startup.Phase), current, total)
}
