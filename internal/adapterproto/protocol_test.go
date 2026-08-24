package adapterproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCodecSplitCoalescedAndUnicodeSeparators(t *testing.T) {
	first := Frame{
		Protocol: Version, Type: KindAssistantMessage, Role: RoleAssistant,
		MessageID: "message-1", Content: "one\u2028two\u2029three",
	}
	second := Frame{
		Protocol: Version, Type: KindToolResult, Role: RoleTool,
		ToolCallID: "tool-1", Result: "done",
	}
	var encoded bytes.Buffer
	encoder := NewEncoder(&encoded)
	if err := encoder.Encode(first); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(second); err != nil {
		t.Fatal(err)
	}
	reader := &oneByteReader{value: encoded.Bytes()}
	decoder := NewDecoder(reader)
	got, err := decoder.Decode()
	if err != nil || got.Content != first.Content {
		t.Fatalf("first frame = %#v, %v", got, err)
	}
	got, err = decoder.Decode()
	if err != nil || got.ToolCallID != second.ToolCallID {
		t.Fatalf("second frame = %#v, %v", got, err)
	}
	if _, err := decoder.Decode(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal error = %v", err)
	}
}

func TestCodecRejectsMalformedOversizedAndNonLFFrames(t *testing.T) {
	valid, err := json.Marshal(Frame{
		Protocol: Version, Type: KindHeartbeat, ID: "heartbeat-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		value []byte
		want  error
	}{
		"missing LF":    {value: valid, want: ErrMissingLF},
		"CRLF":          {value: append(append(valid, '\r'), '\n'), want: ErrMissingLF},
		"unknown field": {value: []byte(`{"protocol":"meridian.adapter.v1","type":"heartbeat","extra":true}` + "\n"), want: ErrInvalidFrame},
		"unknown type":  {value: []byte(`{"protocol":"meridian.adapter.v1","type":"future"}` + "\n"), want: ErrUnknownType},
		"oversized":     {value: append(bytes.Repeat([]byte("x"), MaxFrameBytes), '\n'), want: ErrFrameTooLarge},
		"deep JSON": {
			value: []byte(`{"protocol":"meridian.adapter.v1","type":"heartbeat","metadata":` +
				strings.Repeat(`{"v":`, 10_001) + `null` +
				strings.Repeat(`}`, 10_001) + "}\n"),
			want: ErrInvalidFrame,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := NewDecoder(bytes.NewReader(test.value)).Decode()
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestEncoderRejectsShortWrite(t *testing.T) {
	err := NewEncoder(shortWriter{}).Encode(Frame{
		Protocol: Version, Type: KindHeartbeat,
	})
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v", err)
	}
}

func TestFrameRolesCorrelationAndBounds(t *testing.T) {
	valid := []Frame{
		{Protocol: Version, Type: KindStart, ID: "start-1", SessionID: "session-1"},
		{
			Protocol: Version, Type: KindPermissionResponse, ID: "response-1",
			ResponseTo: "request-1", Permission: &Permission{Kind: "edit", Choice: "allow"},
		},
		{Protocol: Version, Type: KindAck, ID: "ack-1", ResponseTo: "message-1"},
	}
	for _, frame := range valid {
		if err := frame.Validate(); err != nil {
			t.Fatalf("%s rejected: %v", frame.Type, err)
		}
	}
	invalid := []Frame{
		{Protocol: Version, Type: KindUserMessage, ID: "message-1", SessionID: "session-1", Role: RoleAssistant, Content: "wrong role"},
		{Protocol: Version, Type: KindAck},
		{Protocol: Version, Type: KindResume, ID: "resume-1", SessionID: "session-1"},
		{Protocol: Version, Type: KindAssistantDelta, Role: RoleAssistant, MessageID: "message-1", Content: strings.Repeat("x", MaxContentBytes+1)},
	}
	for _, frame := range invalid {
		if err := frame.Validate(); err == nil {
			t.Fatalf("invalid %s frame accepted", frame.Type)
		}
	}
}

func FuzzDecoderNeverAcceptsMalformedFraming(f *testing.F) {
	f.Add([]byte(`{"protocol":"meridian.adapter.v1","type":"heartbeat"}` + "\n"))
	f.Add([]byte("{"))
	f.Add([]byte("\r\n"))
	f.Fuzz(func(t *testing.T, value []byte) {
		if len(value) > MaxFrameBytes+1024 {
			value = value[:MaxFrameBytes+1024]
		}
		frame, err := NewDecoder(bytes.NewReader(value)).Decode()
		if err == nil {
			if len(value) == 0 || value[len(value)-1] != '\n' ||
				(len(value) > 1 && value[len(value)-2] == '\r') {
				t.Fatal("accepted non-LF framing")
			}
			if err := frame.Validate(); err != nil {
				t.Fatalf("accepted invalid frame: %v", err)
			}
		}
	})
}

type oneByteReader struct {
	value []byte
}

func (r *oneByteReader) Read(output []byte) (int, error) {
	if len(r.value) == 0 {
		return 0, io.EOF
	}
	output[0] = r.value[0]
	r.value = r.value[1:]
	return 1, nil
}

type shortWriter struct{}

func (shortWriter) Write(value []byte) (int, error) {
	return len(value) - 1, nil
}
