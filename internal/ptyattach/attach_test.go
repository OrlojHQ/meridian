package ptyattach

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type fakeTerminal struct {
	mu       sync.Mutex
	terminal bool
	rawErr   error
	raw      int
	restore  int
	columns  int
	rows     int
	sizeErr  error
}

func (t *fakeTerminal) IsTerminal(int) bool { return t.terminal }
func (t *fakeTerminal) MakeRaw(int) (any, error) {
	t.mu.Lock()
	t.raw++
	t.mu.Unlock()
	if t.rawErr != nil {
		return nil, t.rawErr
	}
	return "state", nil
}
func (t *fakeTerminal) Restore(int, any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.restore++
	return nil
}
func (t *fakeTerminal) GetSize(int) (int, int, error) {
	return t.columns, t.rows, t.sizeErr
}
func (t *fakeTerminal) restores() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.restore
}
func (t *fakeTerminal) enteredRaw() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.raw > 0
}

type socketRead struct {
	messageType websocket.MessageType
	value       []byte
	err         error
}

type socketWrite struct {
	messageType websocket.MessageType
	value       []byte
}

type fakeSocket struct {
	reads     chan socketRead
	mu        sync.Mutex
	writes    []socketWrite
	writeErr  error
	closeNow  int
	closeOnce sync.Once
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{reads: make(chan socketRead, 16)}
}

func (s *fakeSocket) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case value := <-s.reads:
		return value.messageType, value.value, value.err
	}
}
func (s *fakeSocket) Write(
	_ context.Context,
	messageType websocket.MessageType,
	value []byte,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	s.writes = append(s.writes, socketWrite{messageType: messageType, value: append([]byte(nil), value...)})
	return nil
}
func (s *fakeSocket) Close(websocket.StatusCode, string) error { return nil }
func (s *fakeSocket) CloseNow() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeNow++
	return nil
}
func (s *fakeSocket) written() []socketWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]socketWrite(nil), s.writes...)
}

type fakeSignals struct {
	mu       sync.Mutex
	channels map[os.Signal][]chan<- os.Signal
}

func (s *fakeSignals) Notify(channel chan<- os.Signal, values ...os.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channels == nil {
		s.channels = make(map[os.Signal][]chan<- os.Signal)
	}
	for _, value := range values {
		s.channels[value] = append(s.channels[value], channel)
	}
}
func (*fakeSignals) Stop(chan<- os.Signal) {}
func (s *fakeSignals) emit(value os.Signal) {
	s.mu.Lock()
	channels := append([]chan<- os.Signal(nil), s.channels[value]...)
	s.mu.Unlock()
	for _, channel := range channels {
		channel <- value
	}
}

type attachmentHarness struct {
	options Options
	input   *os.File
	write   *os.File
	output  *os.File
	stderr  *os.File
	socket  *fakeSocket
	term    *fakeTerminal
	signals *fakeSignals
}

func newHarness(t *testing.T) *attachmentHarness {
	t.Helper()
	input, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	socket := newFakeSocket()
	terminal := &fakeTerminal{terminal: true, columns: 120, rows: 40}
	signals := &fakeSignals{}
	harness := &attachmentHarness{
		input: input, write: write, output: output, stderr: stderr,
		socket: socket, term: terminal, signals: signals,
	}
	harness.options = Options{
		Server: "http://127.0.0.1:8080", RunID: "run-1",
		Stdin: input, Stdout: output, Stderr: stderr,
		Terminal: terminal, Signals: signals,
		Dial: func(context.Context, string, string, uint64) (Socket, error) {
			return socket, nil
		},
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = write.Close()
		_ = output.Close()
		_ = stderr.Close()
	})
	return harness
}

func TestRunRestoresTerminalOnExitPaths(t *testing.T) {
	tests := []struct {
		name  string
		start func(*attachmentHarness, context.CancelFunc)
	}{
		{
			name: "normal close",
			start: func(h *attachmentHarness, _ context.CancelFunc) {
				h.socket.reads <- socketRead{err: websocket.CloseError{Code: websocket.StatusNormalClosure}}
			},
		},
		{
			name: "detach",
			start: func(h *attachmentHarness, _ context.CancelFunc) {
				_, _ = h.write.Write([]byte{detachByte})
			},
		},
		{
			name: "alternate detach",
			start: func(h *attachmentHarness, _ context.CancelFunc) {
				_, _ = h.write.Write([]byte{alternateDetachByte})
			},
		},
		{
			name: "server failure",
			start: func(h *attachmentHarness, _ context.CancelFunc) {
				h.socket.reads <- socketRead{err: errors.New("server failed")}
			},
		},
		{
			name: "context cancellation",
			start: func(h *attachmentHarness, cancel context.CancelFunc) {
				waitForRaw(t, h.term)
				cancel()
			},
		},
		{
			name: "SIGTERM",
			start: func(h *attachmentHarness, _ context.CancelFunc) {
				waitForRaw(t, h.term)
				h.signals.emit(syscall.SIGTERM)
			},
		},
		{
			name: "SIGHUP",
			start: func(h *attachmentHarness, _ context.CancelFunc) {
				waitForRaw(t, h.term)
				h.signals.emit(syscall.SIGHUP)
			},
		},
		{
			name: "SIGQUIT",
			start: func(h *attachmentHarness, _ context.CancelFunc) {
				waitForRaw(t, h.term)
				h.signals.emit(syscall.SIGQUIT)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newHarness(t)
			harness.options.AttachTitle = "Meridian · attached"
			harness.options.RestoreTitle = "Meridian · dashboard"
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- Run(ctx, harness.options) }()
			test.start(harness, cancel)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("attachment did not stop")
			}
			if got := harness.term.restores(); got != 1 {
				t.Fatalf("restore calls = %d, want 1", got)
			}
			output := readTemp(t, harness.output)
			if !contains(output, string(terminalTitleSequence(harness.options.AttachTitle))) ||
				!contains(output, string(terminalTitleSequence(harness.options.RestoreTitle))) {
				t.Fatalf("title lifecycle output = %q", output)
			}
		})
	}
}

func TestRunRestoresAfterSetupFailure(t *testing.T) {
	harness := newHarness(t)
	harness.socket.writeErr = errors.New("resize failed")
	if err := Run(context.Background(), harness.options); err == nil {
		t.Fatal("expected setup failure")
	}
	if got := harness.term.restores(); got != 1 {
		t.Fatalf("restore calls = %d, want 1", got)
	}
}

func TestRunRejectsNonTTYBeforeDialOrRaw(t *testing.T) {
	harness := newHarness(t)
	harness.term.terminal = false
	dialed := false
	harness.options.Dial = func(context.Context, string, string, uint64) (Socket, error) {
		dialed = true
		return harness.socket, nil
	}
	if err := Run(context.Background(), harness.options); err == nil {
		t.Fatal("expected non-TTY error")
	}
	if dialed || harness.term.restores() != 0 {
		t.Fatalf("dialed=%t restore=%d", dialed, harness.term.restores())
	}
}

func TestRunResizeDetachAndInputSerialization(t *testing.T) {
	harness := newHarness(t)
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), harness.options) }()
	waitForSignals(t, harness.signals)
	harness.signals.emit(syscall.SIGWINCH)
	_, _ = harness.write.Write([]byte("abc"))
	_, _ = harness.write.Write([]byte("\x1b[112;5u"))
	_, _ = harness.write.Write([]byte("x"))
	_, _ = harness.write.Write([]byte("\x1b[27;5;112"))
	_, _ = harness.write.Write([]byte("~\x1b[27;5;113~"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("attachment did not detach")
	}
	writes := harness.socket.written()
	var resizeCount int
	var input string
	for _, write := range writes {
		if write.messageType == websocket.MessageText {
			var size struct {
				Columns int `json:"columns"`
				Rows    int `json:"rows"`
			}
			if json.Unmarshal(write.value, &size) == nil {
				if size.Columns != 120 || size.Rows != 40 {
					t.Fatalf("resize = %dx%d", size.Columns, size.Rows)
				}
				resizeCount++
			}
		} else {
			input += string(write.value)
		}
	}
	if resizeCount < 1 || input != "abc"+string(detachPrefixByte)+"x" {
		t.Fatalf("resize count=%d input=%q", resizeCount, input)
	}
}

func TestNormalizeEnhancedControlKeysAcrossReads(t *testing.T) {
	var pending []byte
	if output := normalizeEnhancedControlKeys(&pending, []byte("abc\x1b[92;")); string(output) != "abc" {
		t.Fatalf("first output = %q", output)
	}
	if string(pending) != "\x1b[92;" {
		t.Fatalf("pending = %q", pending)
	}
	output := normalizeEnhancedControlKeys(&pending, []byte("5u\x1b[27;5;93~z"))
	want := []byte{alternateDetachByte, detachByte, 'z'}
	if !bytes.Equal(output, want) || len(pending) != 0 {
		t.Fatalf("normalized = %q, pending = %q", output, pending)
	}
}

func TestRunCursorAndReplayGap(t *testing.T) {
	harness := newHarness(t)
	harness.options.After = 4
	output := base64.StdEncoding.EncodeToString([]byte("hello"))
	gap, _ := json.Marshal(outputEvent{Sequence: 5, Type: "gap"})
	frame, _ := json.Marshal(outputEvent{Sequence: 7, Type: "output", Data: output})
	harness.socket.reads <- socketRead{messageType: websocket.MessageText, value: gap}
	harness.socket.reads <- socketRead{messageType: websocket.MessageText, value: frame}
	harness.socket.reads <- socketRead{err: errors.New("network lost")}
	err := Run(context.Background(), harness.options)
	var disconnected *DisconnectError
	if !errors.As(err, &disconnected) || disconnected.After != 7 {
		t.Fatalf("error = %#v, want cursor 7", err)
	}
	if got := readTemp(t, harness.output); got != "hello" {
		t.Fatalf("stdout = %q", got)
	}
	if got := readTemp(t, harness.stderr); !contains(got, "replay gap") {
		t.Fatalf("stderr = %q", got)
	}
}

func TestSendSizeFallsBackForInvalidDimensions(t *testing.T) {
	harness := newHarness(t)
	harness.term.columns, harness.term.rows = 0, -1
	harness.socket.reads <- socketRead{err: websocket.CloseError{Code: websocket.StatusNormalClosure}}
	if err := Run(context.Background(), harness.options); err != nil {
		t.Fatal(err)
	}
	var size struct {
		Columns int `json:"columns"`
		Rows    int `json:"rows"`
	}
	if err := json.Unmarshal(harness.socket.written()[0].value, &size); err != nil {
		t.Fatal(err)
	}
	if size.Columns != 80 || size.Rows != 24 {
		t.Fatalf("fallback size = %dx%d", size.Columns, size.Rows)
	}
}

func waitForSignals(t *testing.T, signals *fakeSignals) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		signals.mu.Lock()
		ready := len(signals.channels[syscall.SIGTERM]) > 0
		signals.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("signal handlers were not registered")
}

func waitForRaw(t *testing.T, terminal *fakeTerminal) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if terminal.enteredRaw() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("terminal did not enter raw mode")
}

func readTemp(t *testing.T, file *os.File) string {
	t.Helper()
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	value, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func contains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
