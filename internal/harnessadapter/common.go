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
	decoder *adapterproto.Decoder
	encoder *adapterproto.Encoder
	output  io.Writer
	mu      sync.Mutex
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
	return s.Write(adapterproto.Frame{
		Protocol:     adapterproto.Version,
		Type:         adapterproto.KindHello,
		ID:           "adapter-hello",
		Capabilities: &capabilities,
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
	mu     sync.Mutex
}

func NewJSONLines(reader io.Reader, writer io.Writer) *JSONLines {
	return &JSONLines{reader: bufio.NewReaderSize(reader, 4096), writer: writer}
}

func (j *JSONLines) Read(output any) error {
	line := make([]byte, 0, 4096)
	for {
		fragment, err := j.reader.ReadSlice('\n')
		if len(line)+len(fragment) > maxUpstreamLine {
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
	if len(encoded) > maxUpstreamLine {
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
	Dir     string
}

func startProcess(config processConfig) (*process, error) {
	if len(config.Command) == 0 || strings.TrimSpace(config.Command[0]) == "" {
		return nil, errors.New("child executable is required")
	}
	command := exec.Command(config.Command[0], append([]string(nil), config.Command[1:]...)...)
	command.Dir = config.Dir
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

func contextFromSignal(parent context.Context) context.Context {
	if parent != nil {
		return parent
	}
	return context.Background()
}

func currentEnv() []string {
	return os.Environ()
}
