// Package harnessadapter implements bounded drivers for structured coding harnesses.
package harnessadapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

const (
	startupTimeout  = 15 * time.Second
	shutdownTimeout = 2 * time.Second
	maxUpstreamLine = adapterproto.MaxFrameBytes
)

// Stream is a synchronized Meridian adapter stream.
type Stream struct {
	decoder  *adapterproto.Decoder
	encoder  *adapterproto.Encoder
	output   io.Writer
	mu       sync.Mutex
	commands bool
}

func NewStream(input io.Reader, output io.Writer) *Stream {
	return &Stream{
		decoder: adapterproto.NewDecoder(input),
		encoder: adapterproto.NewEncoder(output),
		output:  output,
	}
}

func (s *Stream) Read() (adapterproto.Frame, error) {
	return s.decoder.Decode()
}

func (s *Stream) Write(frame adapterproto.Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.encoder.Encode(frame)
}

func (s *Stream) writeRaw(value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	written, err := s.output.Write(value)
	if err == nil && written != len(value) {
		return io.ErrShortWrite
	}
	return err
}

func (s *Stream) Handshake(capabilities adapterproto.Capabilities) error {
	frame, err := s.Read()
	if err != nil {
		return fmt.Errorf("read controller negotiation: %w", err)
	}
	if frame.Type != adapterproto.KindHello || frame.Capabilities == nil {
		return errors.New("controller negotiation is incompatible")
	}
	s.commands = capabilities.Commands && frame.Capabilities.Commands
	capabilities.Commands = s.commands
	return s.Write(adapterproto.Frame{
		Protocol:     adapterproto.Version,
		Type:         adapterproto.KindHello,
		ID:           "adapter-hello",
		Capabilities: &capabilities,
	})
}

// ReportCommands sends the harness's command list when the controller
// negotiated commands frames, and drops it otherwise.
func (s *Stream) ReportCommands(commands *commandSet) error {
	if !s.commands {
		return nil
	}
	return s.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindCommands,
		Commands: commands.list(),
	})
}

func (s *Stream) Fail(code string) {
	_ = s.Write(adapterproto.Frame{
		Protocol: adapterproto.Version,
		Type:     adapterproto.KindError,
		Code:     safeID(code, "adapter_error"),
		Summary:  "structured harness adapter failed",
	})
	_ = s.Write(adapterproto.Frame{
		Protocol: adapterproto.Version,
		Type:     adapterproto.KindEnd,
		Status:   adapterproto.StatusFailed,
	})
}

func safeID(value, fallback string) string {
	if value == "" || len(value) > adapterproto.MaxIDBytes ||
		strings.ContainsAny(value, " \t\r\n/\\\x00") {
		return fallback
	}
	return value
}

func bounded(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func boundedJSON(value any, limit int) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return bounded(string(encoded), limit)
}

func metadata(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > adapterproto.MaxMetadataBytes {
		return nil
	}
	return encoded
}

// JSONLines is a strict, bounded LF-only JSON object codec for upstream protocols.
type JSONLines struct {
	reader *bufio.Reader
	writer io.Writer
	limit  int
	mu     sync.Mutex
}

func NewJSONLines(reader io.Reader, writer io.Writer) *JSONLines {
	return NewJSONLinesLimit(reader, writer, maxUpstreamLine)
}

// NewJSONLinesLimit bounds each upstream record at limit bytes. Harnesses that
// inline whole tool outputs in one record need more than a Meridian frame.
func NewJSONLinesLimit(reader io.Reader, writer io.Writer, limit int) *JSONLines {
	return &JSONLines{reader: bufio.NewReaderSize(reader, 4096), writer: writer, limit: limit}
}

func (j *JSONLines) Read(output any) error {
	line := make([]byte, 0, 4096)
	for {
		fragment, err := j.reader.ReadSlice('\n')
		if len(line)+len(fragment) > j.limit {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = j.reader.ReadSlice('\n')
			}
			return errors.New("upstream record exceeds size limit")
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) != 0 {
				return errors.New("upstream record is not LF terminated")
			}
			return err
		}
		break
	}
	if len(line) < 2 || line[len(line)-2] == '\r' ||
		bytes.IndexByte(line[:len(line)-1], '\n') >= 0 ||
		bytes.IndexByte(line[:len(line)-1], '\r') >= 0 {
		return errors.New("upstream record has invalid framing")
	}
	decoder := json.NewDecoder(bytes.NewReader(line[:len(line)-1]))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return errors.New("upstream record is malformed")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("upstream record has trailing data")
	}
	return nil
}

func (j *JSONLines) Write(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > j.limit {
		return errors.New("upstream command exceeds size limit")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	written, err := j.writer.Write(encoded)
	if err == nil && written != len(encoded) {
		return io.ErrShortWrite
	}
	return err
}

type process struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	done    chan struct{}
	mu      sync.Mutex
	err     error
	once    sync.Once
}

type processConfig struct {
	Command []string
	Env     []string
}

func startProcess(config processConfig) (*process, error) {
	if len(config.Command) == 0 || strings.TrimSpace(config.Command[0]) == "" {
		return nil, errors.New("child executable is required")
	}
	directory, err := sessionWorkingDirectory()
	if err != nil {
		return nil, err
	}
	command := exec.Command(config.Command[0], append([]string(nil), config.Command[1:]...)...)
	command.Dir = directory
	if config.Env != nil {
		command.Env = config.Env
	}
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, errors.New("configure child input")
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, errors.New("configure child output")
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, errors.New("start child process")
	}
	result := &process{
		command: command, stdin: stdin, stdout: stdout, done: make(chan struct{}),
	}
	go func() {
		err := command.Wait()
		result.mu.Lock()
		result.err = err
		result.mu.Unlock()
		close(result.done)
	}()
	return result, nil
}

func (p *process) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *process) Close() {
	p.once.Do(func() {
		_ = p.stdin.Close()
		if p.command.Process != nil {
			_ = syscall.Kill(-p.command.Process.Pid, syscall.SIGTERM)
		}
		timer := time.NewTimer(shutdownTimeout)
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
			if p.command.Process != nil {
				_ = syscall.Kill(-p.command.Process.Pid, syscall.SIGKILL)
			}
			<-p.done
		}
		_ = p.stdout.Close()
	})
}

func replaceEnv(values []string, replacements map[string]string) []string {
	result := make([]string, 0, len(values)+len(replacements))
	for _, value := range values {
		key, _, ok := strings.Cut(value, "=")
		if ok {
			if _, replace := replacements[key]; replace {
				continue
			}
		}
		result = append(result, value)
	}
	for key, value := range replacements {
		result = append(result, key+"="+value)
	}
	return result
}

// readUpstream decodes upstream records as JSON objects until an error.
func readUpstream(ctx context.Context, rpc *JSONLines) (<-chan map[string]json.RawMessage, <-chan error) {
	records := make(chan map[string]json.RawMessage)
	failures := make(chan error, 1)
	go func() {
		defer close(records)
		for {
			var record map[string]json.RawMessage
			if err := rpc.Read(&record); err != nil {
				failures <- err
				return
			}
			if record == nil {
				failures <- errors.New("upstream record is not an object")
				return
			}
			select {
			case records <- record:
			case <-ctx.Done():
				return
			}
		}
	}()
	return records, failures
}

// readController decodes controller frames until an error.
func readController(ctx context.Context, stream *Stream) (<-chan adapterproto.Frame, <-chan error) {
	frames := make(chan adapterproto.Frame)
	failures := make(chan error, 1)
	go func() {
		defer close(frames)
		for {
			frame, err := stream.Read()
			if err != nil {
				failures <- err
				return
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	return frames, failures
}

// field decodes one optional upstream field. Absent or null fields leave
// output unchanged.
func field(record map[string]json.RawMessage, name string, output any) error {
	raw, exists := record[name]
	if !exists || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("upstream field %q is malformed", name)
	}
	return nil
}

func stringField(record map[string]json.RawMessage, name string) string {
	var value string
	if field(record, name, &value) != nil {
		return ""
	}
	return value
}

// permissionAllowed interprets a controller permission choice.
func permissionAllowed(choice string) bool {
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "allow", "yes", "true", "confirm", "once", "accept":
		return true
	default:
		return false
	}
}

// textBlocks joins the text of a string or an array of content blocks.
func textBlocks(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var result strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			result.WriteString(block.Text)
		}
	}
	return result.String()
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

const maxRetiredPrompts = 256

// retiredPrompts remembers prompt frames the harness withdrew, so a
// controller answer that crossed the withdrawal is dropped instead of fatal.
type retiredPrompts map[string]bool

func (r retiredPrompts) add(ids []string) {
	if len(r) >= maxRetiredPrompts {
		clear(r)
	}
	for _, id := range ids {
		r[id] = true
	}
}

func (r retiredPrompts) take(id string) bool {
	if !r[id] {
		return false
	}
	delete(r, id)
	return true
}

// MCPServer is a trusted stdio MCP server a profile registers in a harness.
type MCPServer struct {
	Name      string
	Command   string
	Arguments []string
}

// ParseMCPServer parses NAME=EXECUTABLE[,ARGUMENT...].
func ParseMCPServer(value string) (MCPServer, error) {
	name, rest, ok := strings.Cut(value, "=")
	parts := strings.Split(rest, ",")
	server := MCPServer{Name: name, Command: parts[0], Arguments: parts[1:]}
	if !ok || !validMCPName(name) || !strings.HasPrefix(server.Command, "/") ||
		strings.ContainsAny(value, "\x00\r\n\"'\\") {
		return MCPServer{}, errors.New("MCP server must be NAME=/absolute/executable[,argument...]")
	}
	return server, nil
}

func validMCPName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func contextFromSignal(parent context.Context) context.Context {
	if parent != nil {
		return parent
	}
	return context.Background()
}

func currentEnv() []string {
	return os.Environ()
}
