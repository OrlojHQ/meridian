// Package ptyattach owns the native terminal lifecycle for Run PTY attachment.
package ptyattach

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/OrlojHQ/meridian/pkg/client"
	"github.com/coder/websocket"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"
)

const (
	maxFrameSize        = 64 << 10
	detachByte          = byte(0x1d) // Ctrl-]
	alternateDetachByte = byte(0x1c) // Ctrl-\
	detachPrefixByte    = byte(0x10) // Ctrl-P
	detachSuffixByte    = byte(0x11) // Ctrl-Q
)

var errDetached = errors.New("detached")

var enhancedControlKeys = []struct {
	sequence   []byte
	normalized byte
}{
	{sequence: []byte("\x1b[92;5u"), normalized: alternateDetachByte},
	{sequence: []byte("\x1b[93;5u"), normalized: detachByte},
	{sequence: []byte("\x1b[112;5u"), normalized: detachPrefixByte},
	{sequence: []byte("\x1b[113;5u"), normalized: detachSuffixByte},
	{sequence: []byte("\x1b[27;5;92~"), normalized: alternateDetachByte},
	{sequence: []byte("\x1b[27;5;93~"), normalized: detachByte},
	{sequence: []byte("\x1b[27;5;112~"), normalized: detachPrefixByte},
	{sequence: []byte("\x1b[27;5;113~"), normalized: detachSuffixByte},
}

// DisconnectError reports the last output cursor that was rendered.
type DisconnectError struct {
	After uint64
	Err   error
}

func (e *DisconnectError) Error() string {
	return fmt.Sprintf("PTY disconnected after cursor %d; rerun with --after=%d: %v", e.After, e.After, e.Err)
}

func (e *DisconnectError) Unwrap() error { return e.Err }

// Terminal abstracts native terminal operations for deterministic tests.
type Terminal interface {
	IsTerminal(fd int) bool
	MakeRaw(fd int) (any, error)
	Restore(fd int, state any) error
	GetSize(fd int) (columns, rows int, err error)
}

// Signals abstracts process signal registration for deterministic tests.
type Signals interface {
	Notify(channel chan<- os.Signal, signals ...os.Signal)
	Stop(channel chan<- os.Signal)
}

// Socket is the bounded WebSocket surface used by the attachment.
type Socket interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	Close(websocket.StatusCode, string) error
	CloseNow() error
}

// DialFunc creates a scoped PTY WebSocket.
type DialFunc func(context.Context, string, string, uint64) (Socket, error)

// Options configures one native PTY attachment.
type Options struct {
	Server string
	RunID  string
	After  uint64
	// AttachTitle and RestoreTitle are local terminal metadata. They are
	// written outside the PTY relay and never sent to the remote Run.
	AttachTitle  string
	RestoreTitle string
	Stdin        *os.File
	Stdout       *os.File
	Stderr       io.Writer

	Terminal Terminal
	Signals  Signals
	Dial     DialFunc
	Security client.SecuritySource
}

type nativeTerminal struct{}

func (nativeTerminal) IsTerminal(fd int) bool { return term.IsTerminal(fd) }
func (nativeTerminal) MakeRaw(fd int) (any, error) {
	return term.MakeRaw(fd)
}
func (nativeTerminal) Restore(fd int, state any) error {
	value, ok := state.(*term.State)
	if !ok {
		return errors.New("invalid terminal state")
	}
	return term.Restore(fd, value)
}
func (nativeTerminal) GetSize(fd int) (int, int, error) { return term.GetSize(fd) }

type nativeSignals struct{}

func (nativeSignals) Notify(channel chan<- os.Signal, values ...os.Signal) {
	signal.Notify(channel, values...)
}
func (nativeSignals) Stop(channel chan<- os.Signal) { signal.Stop(channel) }

type lockedSocket struct {
	Socket
	mu sync.Mutex
}

func (s *lockedSocket) write(ctx context.Context, messageType websocket.MessageType, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Write(ctx, messageType, value)
}

func (s *lockedSocket) close(code websocket.StatusCode, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Close(code, reason)
}

type outputEvent struct {
	Sequence uint64 `json:"sequence"`
	Type     string `json:"type"`
	Data     string `json:"data"`
}

type loopResult struct {
	err        error
	disconnect bool
}

// Run attaches the current native terminal and restores it before returning.
func Run(parent context.Context, options Options) (resultErr error) {
	if options.Stdin == nil {
		options.Stdin = os.Stdin
	}
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	if options.Terminal == nil {
		options.Terminal = nativeTerminal{}
	}
	if options.Signals == nil {
		options.Signals = nativeSignals{}
	}
	if options.Dial == nil {
		options.Dial = func(ctx context.Context, server, runID string, after uint64) (Socket, error) {
			return dial(ctx, server, runID, after, options.Security)
		}
	}
	if options.RunID == "" {
		return errors.New("Run ID is required")
	}
	if options.After > math.MaxInt64 {
		return errors.New("PTY cursor exceeds API range")
	}
	if !options.Terminal.IsTerminal(int(options.Stdin.Fd())) ||
		!options.Terminal.IsTerminal(int(options.Stdout.Fd())) {
		return errors.New("PTY attachment requires terminal stdin and stdout")
	}

	ctx, cancel := context.WithCancel(parent)
	terminationSignals := make(chan os.Signal, 1)
	options.Signals.Notify(terminationSignals, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	var signalMu sync.Mutex
	var terminating os.Signal
	signalDone := make(chan struct{})
	go func() {
		defer close(signalDone)
		select {
		case value := <-terminationSignals:
			signalMu.Lock()
			terminating = value
			signalMu.Unlock()
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() {
		cancel()
		options.Signals.Stop(terminationSignals)
		<-signalDone
	}()

	rawSocket, err := options.Dial(ctx, options.Server, options.RunID, options.After)
	if err != nil {
		return err
	}
	socket := &lockedSocket{Socket: rawSocket}
	defer socket.CloseNow()

	if err := ctx.Err(); err != nil {
		return err
	}
	titleApplied := false
	defer func() {
		if titleApplied {
			restore := options.RestoreTitle
			if restore == "" {
				restore = "Meridian"
			}
			writeTerminalTitle(options.Stdout, restore)
		}
	}()
	state, err := options.Terminal.MakeRaw(int(options.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("enter raw terminal mode: %w", err)
	}
	defer func() {
		if restoreErr := options.Terminal.Restore(int(options.Stdin.Fd()), state); restoreErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("restore terminal: %w", restoreErr))
		}
	}()
	titleApplied = writeTerminalTitle(options.Stdout, options.AttachTitle)
	outputWriter := io.Writer(options.Stdout)
	if titleApplied {
		outputWriter = keepTerminalTitle(options.Stdout, options.AttachTitle)
	}

	reader, err := cancelreader.NewReader(options.Stdin)
	if err != nil {
		return fmt.Errorf("create cancellable terminal reader: %w", err)
	}
	defer reader.Close()

	if err := sendSize(ctx, socket, options.Terminal, int(options.Stdin.Fd())); err != nil {
		return fmt.Errorf("send initial terminal size: %w", err)
	}

	resizeSignals := make(chan os.Signal, 1)
	options.Signals.Notify(resizeSignals, syscall.SIGWINCH)
	defer options.Signals.Stop(resizeSignals)

	results := make(chan loopResult, 3)
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		results <- readInput(ctx, reader, socket)
	}()

	var cursorMu sync.Mutex
	cursor := options.After
	go func() {
		defer wait.Done()
		results <- readOutput(ctx, socket, outputWriter, options.Stderr, &cursorMu, &cursor)
	}()

	go func() {
		defer wait.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-resizeSignals:
				if sizeErr := sendSize(ctx, socket, options.Terminal, int(options.Stdin.Fd())); sizeErr != nil {
					select {
					case results <- loopResult{err: fmt.Errorf("resize terminal: %w", sizeErr), disconnect: true}:
					case <-ctx.Done():
					}
					return
				}
			}
		}
	}()

	var outcome loopResult
	select {
	case outcome = <-results:
	case <-ctx.Done():
		signalMu.Lock()
		value := terminating
		signalMu.Unlock()
		if value != nil {
			outcome.err = fmt.Errorf("received %s", value)
		} else {
			outcome.err = ctx.Err()
		}
	}

	cancel()
	_ = reader.Cancel()
	_ = socket.close(websocket.StatusNormalClosure, "terminal detached")
	_ = socket.CloseNow()
	wait.Wait()

	if errors.Is(outcome.err, errDetached) || outcome.err == nil ||
		websocket.CloseStatus(outcome.err) == websocket.StatusNormalClosure {
		return nil
	}
	if outcome.disconnect {
		cursorMu.Lock()
		after := cursor
		cursorMu.Unlock()
		return &DisconnectError{After: after, Err: outcome.err}
	}
	return outcome.err
}

func readInput(ctx context.Context, input io.Reader, socket *lockedSocket) loopResult {
	buffer := make([]byte, 16<<10)
	pendingDetachPrefix := false
	var pendingEnhancedKey []byte
	for {
		count, err := input.Read(buffer)
		if count > 0 {
			normalized := normalizeEnhancedControlKeys(&pendingEnhancedKey, buffer[:count])
			output := make([]byte, 0, len(normalized)+1)
			for _, current := range normalized {
				if pendingDetachPrefix {
					if current == detachSuffixByte {
						if len(output) > 0 {
							if writeErr := socket.write(ctx, websocket.MessageBinary, output); writeErr != nil {
								return loopResult{err: writeErr, disconnect: true}
							}
						}
						return loopResult{err: errDetached}
					}
					output = append(output, detachPrefixByte)
					pendingDetachPrefix = false
				}
				if current == detachByte || current == alternateDetachByte {
					if len(output) > 0 {
						if writeErr := socket.write(ctx, websocket.MessageBinary, output); writeErr != nil {
							return loopResult{err: writeErr, disconnect: true}
						}
					}
					return loopResult{err: errDetached}
				}
				if current == detachPrefixByte {
					pendingDetachPrefix = true
					continue
				}
				output = append(output, current)
			}
			if len(output) > 0 {
				if writeErr := socket.write(ctx, websocket.MessageBinary, output); writeErr != nil {
					return loopResult{err: writeErr, disconnect: true}
				}
			}
		}
		if err != nil {
			output := make([]byte, 0, len(pendingEnhancedKey)+1)
			if pendingDetachPrefix {
				output = append(output, detachPrefixByte)
			}
			output = append(output, pendingEnhancedKey...)
			if len(output) > 0 {
				if writeErr := socket.write(
					ctx, websocket.MessageBinary, output,
				); writeErr != nil && !errors.Is(writeErr, context.Canceled) {
					return loopResult{err: writeErr, disconnect: true}
				}
			}
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return loopResult{}
			}
			return loopResult{err: err}
		}
	}
}

func normalizeEnhancedControlKeys(pending *[]byte, input []byte) []byte {
	data := make([]byte, 0, len(*pending)+len(input))
	data = append(data, *pending...)
	data = append(data, input...)
	*pending = (*pending)[:0]
	output := make([]byte, 0, len(data))
	for len(data) > 0 {
		matched := false
		for _, key := range enhancedControlKeys {
			if bytes.HasPrefix(data, key.sequence) {
				output = append(output, key.normalized)
				data = data[len(key.sequence):]
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		partial := false
		for _, key := range enhancedControlKeys {
			if bytes.HasPrefix(key.sequence, data) {
				partial = true
				break
			}
		}
		if partial {
			*pending = append(*pending, data...)
			break
		}
		output = append(output, data[0])
		data = data[1:]
	}
	return output
}

func readOutput(
	ctx context.Context,
	socket *lockedSocket,
	stdout io.Writer,
	stderr io.Writer,
	cursorMu *sync.Mutex,
	cursor *uint64,
) loopResult {
	for {
		messageType, value, err := socket.Read(ctx)
		if err != nil {
			return loopResult{err: err, disconnect: true}
		}
		if len(value) > maxFrameSize {
			return loopResult{err: errors.New("PTY frame exceeds size limit"), disconnect: true}
		}
		if messageType == websocket.MessageBinary {
			if _, err := stdout.Write(value); err != nil {
				return loopResult{err: err}
			}
			continue
		}
		var event outputEvent
		if err := json.Unmarshal(value, &event); err != nil {
			return loopResult{err: errors.New("invalid PTY control frame"), disconnect: true}
		}
		if event.Type == "gap" {
			if _, err := fmt.Fprintf(stderr, "\r\n[meridian: PTY replay gap before cursor %d]\r\n", event.Sequence); err != nil {
				return loopResult{err: err}
			}
			continue
		}
		if event.Sequence > 0 {
			cursorMu.Lock()
			if event.Sequence > *cursor {
				*cursor = event.Sequence
			}
			cursorMu.Unlock()
		}
		if event.Data != "" {
			decoded, err := base64.StdEncoding.DecodeString(event.Data)
			if err != nil {
				return loopResult{err: errors.New("invalid PTY output encoding"), disconnect: true}
			}
			if _, err := stdout.Write(decoded); err != nil {
				return loopResult{err: err}
			}
		}
		if terminalRunEvent(event.Type) {
			return loopResult{}
		}
	}
}

func terminalRunEvent(eventType string) bool {
	switch eventType {
	case "run.succeeded", "run.failed", "run.cancelled":
		return true
	default:
		return false
	}
}

func sendSize(ctx context.Context, socket *lockedSocket, terminal Terminal, fd int) error {
	columns, rows, err := terminal.GetSize(fd)
	if err != nil || columns <= 0 || rows <= 0 || columns > 65535 || rows > 65535 {
		columns, rows = 80, 24
	}
	value, err := json.Marshal(struct {
		Type    string `json:"type"`
		Columns int    `json:"columns"`
		Rows    int    `json:"rows"`
	}{Type: "resize", Columns: columns, Rows: rows})
	if err != nil {
		return err
	}
	return socket.write(ctx, websocket.MessageText, value)
}

func dial(
	ctx context.Context,
	server, runID string,
	after uint64,
	security client.SecuritySource,
) (Socket, error) {
	api, err := client.NewClient(server, security)
	if err != nil {
		return nil, fmt.Errorf("create API client: %w", err)
	}
	ticketResult, err := api.CreateRunAttachTicket(ctx, client.CreateRunAttachTicketParams{
		RunId: runID,
		After: client.NewOptInt64(int64(after)),
	})
	if err != nil {
		return nil, fmt.Errorf("create attach ticket: %w", err)
	}
	ticket, ok := ticketResult.(*client.AttachTicket)
	if !ok {
		return nil, fmt.Errorf("create attach ticket: API returned %T", ticketResult)
	}
	base, err := url.Parse(server)
	if err != nil {
		return nil, fmt.Errorf("parse API URL: %w", err)
	}
	switch base.Scheme {
	case "http":
		base.Scheme = "ws"
	case "https":
		base.Scheme = "wss"
	default:
		return nil, fmt.Errorf("unsupported API URL scheme %q", base.Scheme)
	}
	base.Path = ticket.WebSocketPath
	query := base.Query()
	query.Set("ticket", ticket.Ticket)
	base.RawQuery = query.Encode()
	connection, response, err := websocket.Dial(ctx, base.String(), nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("attach WebSocket: %w", err)
	}
	connection.SetReadLimit(maxFrameSize)
	return connection, nil
}
