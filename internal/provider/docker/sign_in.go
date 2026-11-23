package docker

import (
	"context"
	"errors"
	"fmt"

	"github.com/OrlojHQ/meridian/internal/antigravityauth"
	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// Harness sign-in in a sign-in Capsule (ADR 0062).

func (p *Provider) StartHarnessSignIn(ctx context.Context, resourceID, harness string) (string, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return "", err
	}
	start, err := supervisor.StartHarnessSignIn(ctx, harness)
	if err != nil {
		return "", mapSignInError(err)
	}
	return start.AuthorizationURL, nil
}

func (p *Provider) FinishHarnessSignIn(ctx context.Context, resourceID, code string) ([]antigravityauth.File, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	result, err := supervisor.FinishHarnessSignIn(ctx, code)
	if err != nil {
		return nil, mapSignInError(err)
	}
	return result.Files, nil
}

func (p *Provider) CancelHarnessSignIn(ctx context.Context, resourceID string) error {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	return mapRuntimeError(supervisor.CancelHarnessSignIn(ctx))
}

func mapSignInError(err error) error {
	var protocolError *capsuleproto.Error
	if errors.As(err, &protocolError) && protocolError.Code == "sign_in_rejected" {
		return fmt.Errorf("%w: %w", ports.ErrHarnessSignInRejected, err)
	}
	return mapRuntimeError(err)
}

var _ ports.HarnessSignInRuntime = (*Provider)(nil)
