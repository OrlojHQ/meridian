package agentsandbox

import (
	"context"
	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func (p *Provider) PreparationIdentity(ctx context.Context, id string) (ports.PreparationIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	var result ports.PreparationIdentity
	supervisor, session, err := p.runtimeClient(ctx, id)
	if err != nil {
		return result, err
	}
	defer session.Close()
	value, err := supervisor.PreparationIdentity(ctx)
	return ports.PreparationIdentity{SourceRevision: value.SourceRevision, Platform: value.Platform}, mapRuntimeError(err)
}
func (p *Provider) FinishPreparation(ctx context.Context, id string, setup []string, revision, key string) error {
	ctx, cancel := context.WithTimeout(ctx, p.config.SetupTimeout)
	defer cancel()
	supervisor, session, err := p.runtimeClient(ctx, id)
	if err != nil {
		return err
	}
	defer session.Close()
	return mapRuntimeError(supervisor.FinishPreparation(ctx, capsuleproto.FinishPreparationRequest{Setup: setup, SourceRevision: revision, Key: key}))
}
