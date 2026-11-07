package capsuleproto

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

const coordinationPollLimit = 2*coordproto.MaxPendingBytes + (1 << 20)

func (c *Client) ConfigureCoordination(ctx context.Context, config coordproto.Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	return c.call(ctx, http.MethodPut, CoordinationConfigPath, config, nil, true)
}

func (c *Client) PollCoordination(
	ctx context.Context,
	after, consumedGeneration uint64,
	wait time.Duration,
) (coordproto.Poll, error) {
	if wait < 0 || wait > coordproto.MaxPollWait {
		return coordproto.Poll{}, errors.New("coordination poll wait is invalid")
	}
	path := CoordinationRequestsPath + "?after=" + strconv.FormatUint(after, 10) +
		"&consumedGeneration=" + strconv.FormatUint(consumedGeneration, 10) +
		"&waitMs=" + strconv.FormatInt(wait.Milliseconds(), 10)
	var poll coordproto.Poll
	if err := c.callLimit(ctx, http.MethodGet, path, nil, &poll, true, coordinationPollLimit); err != nil {
		return coordproto.Poll{}, err
	}
	if poll.Epoch == "" || len(poll.Requests) > coordproto.MaxBatch || len(poll.Consumed) > coordproto.MaxLinks {
		return coordproto.Poll{}, errors.New("invalid coordination poll response")
	}
	for _, pending := range poll.Requests {
		if pending.Cursor == 0 || pending.Request.Validate() != nil {
			return coordproto.Poll{}, errors.New("invalid coordination request in poll response")
		}
	}
	return poll, nil
}

func (c *Client) ReplyCoordination(ctx context.Context, replies []coordproto.Reply) error {
	if len(replies) == 0 {
		return nil
	}
	return c.call(ctx, http.MethodPost, CoordinationRepliesPath, coordproto.Replies{Items: replies}, nil, true)
}

func (c *Client) NotifyCoordination(ctx context.Context, notification coordproto.Notification) error {
	if err := notification.Validate(); err != nil {
		return err
	}
	return c.call(ctx, http.MethodPost, CoordinationNotificationsPath, notification, nil, true)
}
