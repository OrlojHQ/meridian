package harnessadapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

// RunGeneric hosts an adapter-speaking child and validates both protocol
// directions. Capsuled may execute a trusted adapter directly; this host is
// useful when a child also needs stderr suppression and descendant cleanup.
func RunGeneric(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	command []string,
) error {
	child, err := startProcess(processConfig{Command: command})
	if err != nil {
		return err
	}
	defer child.Close()

	parent := NewStream(input, output)
	childDecoder := adapterproto.NewDecoder(child.stdout)
	childEncoder := adapterproto.NewEncoder(child.stdin)

	controllerHello, err := parent.Read()
	if err != nil || controllerHello.Type != adapterproto.KindHello ||
		controllerHello.Capabilities == nil {
		return errors.New("controller negotiation is incompatible")
	}
	if err := childEncoder.Encode(controllerHello); err != nil {
		return errors.New("write child negotiation")
	}

	type decoded struct {
		frame adapterproto.Frame
		err   error
	}
	helloResult := make(chan decoded, 1)
	go func() {
		frame, decodeErr := childDecoder.Decode()
		helloResult <- decoded{frame: frame, err: decodeErr}
	}()
	timer := time.NewTimer(startupTimeout)
	defer timer.Stop()
	var childHello adapterproto.Frame
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("child negotiation timed out")
	case result := <-helloResult:
		if result.err != nil {
			return errors.New("child negotiation failed")
		}
		childHello = result.frame
	}
	if childHello.Type != adapterproto.KindHello || childHello.Capabilities == nil {
		return errors.New("child negotiation is incompatible")
	}
	if err := parent.Write(childHello); err != nil {
		return err
	}

	parentErrors := make(chan error, 1)
	go func() {
		started := false
		for {
			frame, readErr := parent.Read()
			if readErr != nil {
				parentErrors <- readErr
				return
			}
			if !started {
				if frame.Type != adapterproto.KindStart && frame.Type != adapterproto.KindResume {
					parentErrors <- errors.New("child start frame is missing")
					return
				}
				started = true
			} else if !controllerKind(frame.Type) {
				parentErrors <- errors.New("invalid controller frame")
				return
			}
			if writeErr := childEncoder.Encode(frame); writeErr != nil {
				parentErrors <- writeErr
				return
			}
		}
	}()

	childErrors := make(chan error, 1)
	go func() {
		for {
			frame, readErr := childDecoder.Decode()
			if readErr != nil {
				childErrors <- readErr
				return
			}
			if !adapterKind(frame.Type) {
				childErrors <- errors.New("invalid child adapter frame")
				return
			}
			if writeErr := parent.Write(frame); writeErr != nil {
				childErrors <- writeErr
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-parentErrors:
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("controller stream failed: %w", err)
	case err := <-childErrors:
		if errors.Is(err, io.EOF) {
			return errors.New("child adapter exited unexpectedly")
		}
		return fmt.Errorf("child stream failed: %w", err)
	}
}

func controllerKind(kind adapterproto.Kind) bool {
	switch kind {
	case adapterproto.KindUserMessage, adapterproto.KindPermissionResponse,
		adapterproto.KindInputResponse, adapterproto.KindCancel,
		adapterproto.KindHeartbeat, adapterproto.KindAck:
		return true
	default:
		return false
	}
}

func adapterKind(kind adapterproto.Kind) bool {
	switch kind {
	case adapterproto.KindAssistantDelta, adapterproto.KindAssistantMessage,
		adapterproto.KindToolStart, adapterproto.KindToolResult,
		adapterproto.KindStatus, adapterproto.KindError,
		adapterproto.KindPermissionRequest, adapterproto.KindInputRequest,
		adapterproto.KindHeartbeat, adapterproto.KindAck, adapterproto.KindEnd:
		return true
	default:
		return false
	}
}
