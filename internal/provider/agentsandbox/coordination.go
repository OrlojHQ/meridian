package agentsandbox

import (
	"context"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

func (p *Provider) ConfigureCoordination(
	ctx context.Context, resourceID string, config coordproto.Config,
) error {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	defer session.Close()
	return mapRuntimeError(client.ConfigureCoordination(ctx, config))
}

func (p *Provider) PollCoordination(
	ctx context.Context, resourceID string, after, consumedGeneration uint64, wait time.Duration,
) (coordproto.Poll, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout+wait)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return coordproto.Poll{}, err
	}
	defer session.Close()
	poll, err := client.PollCoordination(ctx, after, consumedGeneration, wait)
	return poll, mapRuntimeError(err)
}

func (p *Provider) ReplyCoordination(
	ctx context.Context, resourceID string, replies []coordproto.Reply,
) error {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	defer session.Close()
	return mapRuntimeError(client.ReplyCoordination(ctx, replies))
}

func (p *Provider) NotifyCoordination(
	ctx context.Context, resourceID string, notification coordproto.Notification,
) error {
	ctx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	client, session, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	defer session.Close()
	return mapRuntimeError(client.NotifyCoordination(ctx, notification))
}
