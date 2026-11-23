package agentsandbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/internal/antigravityauth"
	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// Harness sign-in in a sign-in Capsule (ADR 0061).

// signInExchange covers agy's code exchange, which capsuled bounds at two
// minutes.
const signInExchange = 150 * time.Second

func (p *Provider) StartHarnessSignIn(ctx context.Context, resourceID, harness string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout+time.Minute)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return "", err
	}
	defer session.Close()
	start, err := client.StartHarnessSignIn(ctx, harness)
	if err != nil {
		return "", mapSignInError(err)
	}
	return start.AuthorizationURL, nil
}

func (p *Provider) FinishHarnessSignIn(ctx context.Context, resourceID, code string) ([]antigravityauth.File, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout+signInExchange)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	result, err := client.FinishHarnessSignIn(ctx, code)
	if err != nil {
		return nil, mapSignInError(err)
	}
	return result.Files, nil
}

func (p *Provider) CancelHarnessSignIn(ctx context.Context, resourceID string) error {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	defer session.Close()
	return mapRuntimeError(client.CancelHarnessSignIn(ctx))
}

func mapSignInError(err error) error {
	var protocolError *capsuleproto.Error
	if errors.As(err, &protocolError) && protocolError.Code == "sign_in_rejected" {
		return fmt.Errorf("%w: %w", ports.ErrHarnessSignInRejected, err)
	}
	return mapRuntimeError(err)
}

var _ ports.HarnessSignInRuntime = (*Provider)(nil)
