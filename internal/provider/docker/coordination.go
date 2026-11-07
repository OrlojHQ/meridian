package docker

import (
	"context"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

func (p *Provider) ConfigureCoordination(
	ctx context.Context, resourceID string, config coordproto.Config,
) error {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	return mapRuntimeError(supervisor.ConfigureCoordination(ctx, config))
}

func (p *Provider) PollCoordination(
	ctx context.Context, resourceID string, after, consumedGeneration uint64, wait time.Duration,
) (coordproto.Poll, error) {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return coordproto.Poll{}, err
	}
	poll, err := supervisor.PollCoordination(ctx, after, consumedGeneration, wait)
	return poll, mapRuntimeError(err)
}

func (p *Provider) ReplyCoordination(
	ctx context.Context, resourceID string, replies []coordproto.Reply,
) error {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	return mapRuntimeError(supervisor.ReplyCoordination(ctx, replies))
}

func (p *Provider) NotifyCoordination(
	ctx context.Context, resourceID string, notification coordproto.Notification,
) error {
	supervisor, err := p.runtimeClient(ctx, resourceID)
	if err != nil {
		return err
	}
	return mapRuntimeError(supervisor.NotifyCoordination(ctx, notification))
}
