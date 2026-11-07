// Package coordtool implements the meridian-coord CLI and MCP server that a
// lead Thread's harness uses to reach its Capsule-local coordination mailbox.
package coordtool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/OrlojHQ/meridian/internal/coordproto"
)

const callAttempts = 5

// Call fills a default idempotency key and retries with the same key while
// the mailbox reports the request as still pending or briefly unreachable.
func Call(ctx context.Context, socket string, request coordproto.Request) (coordproto.Response, error) {
	request.Protocol = coordproto.Version
	if request.Mutating() && request.Key == "" {
		request.Key = DefaultKey(request)
	}
	delay := time.Second
	var response coordproto.Response
	for attempt := 0; attempt < callAttempts; attempt++ {
		var err error
		response, err = coordproto.Do(ctx, socket, request)
		if err != nil {
			return response, err
		}
		if response.OK || response.Error == nil ||
			(response.Error.Code != coordproto.CodePending && response.Error.Code != coordproto.CodeMailboxLost) {
			return response, nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return response, ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 15*time.Second)
	}
	return response, nil
}

// DefaultKey makes retries of the same spawn or cancel idempotent. Sends get
// a random key because repeating a message may be intentional.
func DefaultKey(request coordproto.Request) string {
	switch request.Type {
	case coordproto.KindSpawn:
		return "spawn-" + request.Name
	case coordproto.KindCancel:
		return "cancel-" + request.Link
	default:
		raw := make([]byte, 12)
		_, _ = rand.Read(raw)
		return string(request.Type) + "-" + hex.EncodeToString(raw)
	}
}
