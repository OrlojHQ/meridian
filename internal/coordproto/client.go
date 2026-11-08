package coordproto

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// DefaultSocketPath resolves the mailbox socket for tools in the Capsule.
func DefaultSocketPath() string {
	if path := os.Getenv(SocketEnv); path != "" {
		return path
	}
	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/capsule"
	}
	return filepath.Join(home, ".meridian", "coord.sock")
}

// SocketPathForHome is the mailbox socket capsuled creates under home.
func SocketPathForHome(home string) string {
	return filepath.Join(home, ".meridian", "coord.sock")
}

// Do sends one request and reads its response. Transport failures map to
// mailbox_lost so callers can retry with the same key.
func Do(ctx context.Context, socketPath string, request Request) (Response, error) {
	request.Protocol = Version
	if err := request.Validate(); err != nil {
		return Fail(CodeInvalidRequest, err.Error()), nil
	}
	record, err := Encode(request)
	if err != nil {
		return Fail(CodeInvalidRequest, err.Error()), nil
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Fail(CodeCoordinationDisabled, "coordination mailbox is not available in this Capsule"), nil
		}
		return Fail(CodeMailboxLost, "coordination mailbox is unreachable"), nil
	}
	defer conn.Close()
	deadline := time.Now().Add(MaxReplyLatency + 15*time.Second)
	if request.Type == KindWait {
		timeout := request.TimeoutSeconds
		if timeout == 0 {
			timeout = DefaultWaitSeconds
		}
		deadline = time.Now().Add(time.Duration(timeout)*time.Second + 15*time.Second)
	}
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return Response{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if _, err := conn.Write(record); err != nil {
		return Fail(CodeMailboxLost, "coordination mailbox closed the connection"), nil
	}
	line, err := ReadRecordLimit(bufio.NewReaderSize(conn, 64<<10), MaxResponseBytes)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Fail(CodeMailboxLost, "coordination mailbox closed the connection"), nil
	}
	response, err := DecodeResponse(line)
	if err != nil {
		return Response{}, fmt.Errorf("decode coordination response: %w", err)
	}
	return response, nil
}
