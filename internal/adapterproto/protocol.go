// Package adapterproto defines the bounded, bidirectional protocol between
// capsuled and a structured harness adapter.
package adapterproto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	Version = "meridian.adapter.v1"

	MaxFrameBytes       = 256 << 10
	MaxContentBytes     = 128 << 10
	MaxToolNameBytes    = 512
	MaxToolSummaryBytes = 32 << 10
	MaxResultBytes      = 64 << 10
	MaxMetadataBytes    = 32 << 10
	MaxResumeStateBytes = 16 << 10
	MaxIDBytes          = 160
	MaxOptions          = 32
	MaxOptionBytes      = 1024
)

var (
	ErrFrameTooLarge = errors.New("adapter frame exceeds size limit")
	ErrMissingLF     = errors.New("adapter frame is not LF terminated")
	ErrInvalidFrame  = errors.New("invalid adapter frame")
	ErrUnknownType   = errors.New("unknown adapter frame type")
)

type Kind string

const (
	KindHello              Kind = "hello"
	KindStart              Kind = "start"
	KindResume             Kind = "resume"
	KindUserMessage        Kind = "user_message"
	KindAssistantDelta     Kind = "assistant_delta"
	KindAssistantMessage   Kind = "assistant_message"
	KindToolStart          Kind = "tool_start"
	KindToolResult         Kind = "tool_result"
	KindStatus             Kind = "status"
	KindError              Kind = "error"
	KindPermissionRequest  Kind = "permission_request"
	KindPermissionResponse Kind = "permission_response"
	KindInputRequest       Kind = "input_request"
	KindInputResponse      Kind = "input_response"
	KindHeartbeat          Kind = "heartbeat"
	KindCancel             Kind = "cancel"
	KindAck                Kind = "ack"
	KindCursor             Kind = "cursor"
	KindGap                Kind = "gap"
	KindEnd                Kind = "end"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Status string

const (
	StatusStarting   Status = "starting"
	StatusRunning    Status = "running"
	StatusIdle       Status = "idle"
	StatusWaiting    Status = "waiting"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
	StatusRestarting Status = "restarting"
)

type Capabilities struct {
	Resume         bool `json:"resume"`
	ToolEvents     bool `json:"toolEvents"`
	Permissions    bool `json:"permissions"`
	InputRequests  bool `json:"inputRequests"`
	Heartbeat      bool `json:"heartbeat"`
	OpaqueMetadata bool `json:"opaqueMetadata"`
}

type Permission struct {
	Kind    string   `json:"kind"`
	Summary string   `json:"summary,omitempty"`
	Options []string `json:"options,omitempty"`
	Choice  string   `json:"choice,omitempty"`
}

type Input struct {
	Prompt string `json:"prompt,omitempty"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

type Gap struct {
	RequestedAfter uint64 `json:"requestedAfter"`
	AvailableFrom  uint64 `json:"availableFrom"`
}

// Frame is deliberately a closed union. Unknown JSON fields and unknown Type
// values are rejected; forward-compatible presentation data belongs in
// Metadata, which must be a bounded JSON object.
type Frame struct {
	Protocol     string          `json:"protocol"`
	Type         Kind            `json:"type"`
	ID           string          `json:"id,omitempty"`
	ResponseTo   string          `json:"responseTo,omitempty"`
	SessionID    string          `json:"sessionId,omitempty"`
	MessageID    string          `json:"messageId,omitempty"`
	ToolCallID   string          `json:"toolCallId,omitempty"`
	Role         Role            `json:"role,omitempty"`
	Content      string          `json:"content,omitempty"`
	ToolName     string          `json:"toolName,omitempty"`
	Summary      string          `json:"summary,omitempty"`
	Result       string          `json:"result,omitempty"`
	Status       Status          `json:"status,omitempty"`
	Code         string          `json:"code,omitempty"`
	Retryable    bool            `json:"retryable,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	ResumeState  string          `json:"resumeState,omitempty"`
	Capabilities *Capabilities   `json:"capabilities,omitempty"`
	Permission   *Permission     `json:"permission,omitempty"`
	Input        *Input          `json:"input,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	Cursor       uint64          `json:"cursor,omitempty"`
	Gap          *Gap            `json:"gap,omitempty"`
}

func (f Frame) Validate() error {
	if f.Protocol != Version {
		return invalid("protocol")
	}
	if !knownKind(f.Type) {
		return fmt.Errorf("%w: %q", ErrUnknownType, f.Type)
	}
	for name, value := range map[string]string{
		"id": f.ID, "responseTo": f.ResponseTo, "sessionId": f.SessionID,
		"messageId": f.MessageID, "toolCallId": f.ToolCallID, "code": f.Code,
	} {
		if value != "" && !validID(value) {
			return invalid(name)
		}
	}
	if !validText(f.Content, MaxContentBytes) ||
		!validText(f.ToolName, MaxToolNameBytes) ||
		!validText(f.Summary, MaxToolSummaryBytes) ||
		!validText(f.Result, MaxResultBytes) ||
		!validText(f.Reason, MaxToolSummaryBytes) ||
		!validText(f.ResumeState, MaxResumeStateBytes) {
		return invalid("content limit")
	}
	if len(f.Metadata) > 0 {
		if len(f.Metadata) > MaxMetadataBytes || !json.Valid(f.Metadata) ||
			firstNonSpace(f.Metadata) != '{' {
			return invalid("metadata")
		}
	}
	if err := validateShape(f); err != nil {
		return err
	}
	encoded, err := json.Marshal(f)
	if err != nil || len(encoded)+1 > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	return nil
}

func validateShape(f Frame) error {
	requireID := func() error {
		if f.ID == "" {
			return invalid("id")
		}
		return nil
	}
	requireSession := func() error {
		if f.SessionID == "" {
			return invalid("sessionId")
		}
		return nil
	}
	switch f.Type {
	case KindHello:
		if err := requireID(); err != nil || f.Capabilities == nil {
			return invalid("hello")
		}
	case KindStart:
		if err := requireID(); err != nil {
			return err
		}
		if err := requireSession(); err != nil {
			return err
		}
	case KindResume:
		if err := requireID(); err != nil || f.ResumeState == "" {
			return invalid("resume")
		}
		if err := requireSession(); err != nil {
			return err
		}
	case KindUserMessage:
		if err := requireID(); err != nil || f.Role != RoleUser || f.Content == "" {
			return invalid("user message")
		}
		if err := requireSession(); err != nil {
			return err
		}
	case KindAssistantDelta, KindAssistantMessage:
		if f.Role != RoleAssistant || f.Content == "" || f.MessageID == "" {
			return invalid("assistant message")
		}
	case KindToolStart:
		if f.Role != RoleAssistant || f.ToolCallID == "" || f.ToolName == "" {
			return invalid("tool start")
		}
	case KindToolResult:
		if f.Role != RoleTool || f.ToolCallID == "" || f.Result == "" {
			return invalid("tool result")
		}
	case KindStatus:
		if !validStatus(f.Status) {
			return invalid("status")
		}
	case KindError:
		if f.Code == "" || f.Summary == "" {
			return invalid("error")
		}
	case KindPermissionRequest:
		if err := requireID(); err != nil || f.Permission == nil ||
			!validPermission(*f.Permission, false) {
			return invalid("permission request")
		}
	case KindPermissionResponse:
		if err := requireID(); err != nil || f.ResponseTo == "" || f.Permission == nil ||
			!validPermission(*f.Permission, true) {
			return invalid("permission response")
		}
	case KindInputRequest:
		if err := requireID(); err != nil || f.Input == nil ||
			!validText(f.Input.Prompt, MaxToolSummaryBytes) || f.Input.Prompt == "" {
			return invalid("input request")
		}
	case KindInputResponse:
		if err := requireID(); err != nil || f.ResponseTo == "" || f.Input == nil ||
			!validText(f.Input.Value, MaxContentBytes) {
			return invalid("input response")
		}
	case KindHeartbeat:
		// An ID requests an acknowledgement; an empty ID is informational.
	case KindCancel:
		if err := requireID(); err != nil {
			return err
		}
	case KindAck:
		if err := requireID(); err != nil || f.ResponseTo == "" {
			return invalid("ack responseTo")
		}
	case KindCursor:
		if f.Cursor == 0 {
			return invalid("cursor")
		}
	case KindGap:
		if f.Gap == nil || f.Gap.AvailableFrom == 0 ||
			f.Gap.AvailableFrom <= f.Gap.RequestedAfter {
			return invalid("gap")
		}
	case KindEnd:
		if !validStatus(f.Status) || f.Status == StatusStarting ||
			f.Status == StatusRunning || f.Status == StatusWaiting ||
			f.Status == StatusRestarting {
			return invalid("end status")
		}
	}
	return nil
}

func validPermission(value Permission, response bool) bool {
	if !validText(value.Kind, MaxToolNameBytes) ||
		!validText(value.Summary, MaxToolSummaryBytes) ||
		!validText(value.Choice, MaxOptionBytes) ||
		len(value.Options) > MaxOptions || value.Kind == "" {
		return false
	}
	for _, option := range value.Options {
		if option == "" || !validText(option, MaxOptionBytes) {
			return false
		}
	}
	return !response || value.Choice != ""
}

func knownKind(kind Kind) bool {
	switch kind {
	case KindHello, KindStart, KindResume, KindUserMessage, KindAssistantDelta,
		KindAssistantMessage, KindToolStart, KindToolResult, KindStatus, KindError,
		KindPermissionRequest, KindPermissionResponse, KindInputRequest,
		KindInputResponse, KindHeartbeat, KindCancel, KindAck, KindCursor,
		KindGap, KindEnd:
		return true
	default:
		return false
	}
}

func validStatus(status Status) bool {
	switch status {
	case StatusStarting, StatusRunning, StatusIdle, StatusWaiting,
		StatusSucceeded, StatusFailed, StatusCancelled, StatusRestarting:
		return true
	default:
		return false
	}
}

func validID(value string) bool {
	if value == "" || len(value) > MaxIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune("/\\", r) {
			return false
		}
	}
	return true
}

func validText(value string, limit int) bool {
	return len(value) <= limit && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func firstNonSpace(value []byte) byte {
	for _, current := range value {
		switch current {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return current
		}
	}
	return 0
}

func invalid(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidFrame, field)
}

// Decoder consumes exactly one LF-delimited JSON object at a time. It does not
// treat Unicode line/paragraph separators as framing bytes and rejects CRLF.
type Decoder struct {
	reader *bufio.Reader
}

func NewDecoder(reader io.Reader) *Decoder {
	return &Decoder{reader: bufio.NewReaderSize(reader, 4096)}
}

func (d *Decoder) Decode() (Frame, error) {
	line := make([]byte, 0, 4096)
	for {
		fragment, err := d.reader.ReadSlice('\n')
		if len(line)+len(fragment) > MaxFrameBytes {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = d.reader.ReadSlice('\n')
			}
			return Frame{}, ErrFrameTooLarge
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) != 0 {
				return Frame{}, ErrMissingLF
			}
			return Frame{}, err
		}
		break
	}
	if len(line) < 2 || line[len(line)-1] != '\n' || line[len(line)-2] == '\r' {
		return Frame{}, ErrMissingLF
	}
	payload := line[:len(line)-1]
	if bytes.IndexByte(payload, '\n') >= 0 || bytes.IndexByte(payload, '\r') >= 0 {
		return Frame{}, ErrMissingLF
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var frame Frame
	if err := decoder.Decode(&frame); err != nil {
		return Frame{}, fmt.Errorf("%w: json", ErrInvalidFrame)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Frame{}, fmt.Errorf("%w: trailing data", ErrInvalidFrame)
	}
	if err := frame.Validate(); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

type Encoder struct {
	writer io.Writer
}

func NewEncoder(writer io.Writer) *Encoder {
	return &Encoder{writer: writer}
}

func (e *Encoder) Encode(frame Frame) error {
	if err := frame.Validate(); err != nil {
		return err
	}
	value, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	value = append(value, '\n')
	if len(value) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	written, err := e.writer.Write(value)
	if err == nil && written != len(value) {
		return io.ErrShortWrite
	}
	return err
}
