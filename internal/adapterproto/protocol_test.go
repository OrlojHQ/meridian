package adapterproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

func TestCommandsFrameIsBoundedAndValidated(t *testing.T) {
	command := func(name string) Command {
		return Command{Name: name, Kind: CommandPrompt}
	}
	full := make([]Command, 0, MaxCommands)
	for index := range MaxCommands {
		full = append(full, Command{
			Name:         strings.Repeat("n", MaxCommandNameBytes-3) + fmt.Sprintf("%03d", index),
			Description:  strings.Repeat("d", MaxCommandDescriptionBytes),
			ArgumentHint: strings.Repeat("h", MaxCommandHintBytes),
			Kind:         CommandAction,
			Aliases: []string{
				fmt.Sprintf("a%03d", index), fmt.Sprintf("b%03d", index),
				fmt.Sprintf("c%03d", index), fmt.Sprintf("d%03d", index),
			},
		})
	}
	valid := [][]Command{
		nil,
		{command("compact"), command("skill:ship"), command("pr-comments"), command("v1.2")},
		full,
	}
	for _, commands := range valid {
		frame := Frame{Protocol: Version, Type: KindCommands, Commands: commands}
		if err := frame.Validate(); err != nil {
			t.Fatalf("valid commands rejected: %v", err)
		}
	}
	invalid := map[string][]Command{
		"too many":         append(append([]Command(nil), full...), command("extra")),
		"leading symbol":   {command("__remote-workflow")},
		"slash":            {command("a/b")},
		"space":            {command("a b")},
		"long name":        {command(strings.Repeat("x", MaxCommandNameBytes+1))},
		"duplicate":        {command("compact"), command("compact")},
		"alias collision":  {command("usage"), {Name: "stats", Kind: CommandPrompt, Aliases: []string{"usage"}}},
		"unknown kind":     {{Name: "compact", Kind: "shell"}},
		"control in text":  {{Name: "compact", Kind: CommandPrompt, Description: "one\ntwo"}},
		"C1 control":       {{Name: "compact", Kind: CommandPrompt, ArgumentHint: "\u009b31m"}},
		"long description": {{Name: "compact", Kind: CommandPrompt, Description: strings.Repeat("d", MaxCommandDescriptionBytes+1)}},
		"too many aliases": {{Name: "compact", Kind: CommandPrompt, Aliases: []string{"a", "b", "c", "d", "e"}}},
	}
	for name, commands := range invalid {
		t.Run(name, func(t *testing.T) {
			frame := Frame{Protocol: Version, Type: KindCommands, Commands: commands}
			if err := frame.Validate(); err == nil {
				t.Fatal("invalid commands accepted")
			}
		})
	}
	stray := Frame{Protocol: Version, Type: KindStatus, Status: StatusIdle, Commands: []Command{command("compact")}}
	if err := stray.Validate(); err == nil {
		t.Fatal("commands accepted outside a commands frame")
	}
}

func TestModelsFramesAreBoundedAndValidated(t *testing.T) {
	option := func(id string) ModelOption { return ModelOption{ID: id, Label: "Model " + id} }
	full := make([]ModelOption, 0, MaxModels)
	for index := range MaxModels {
		full = append(full, ModelOption{
			ID:          strings.Repeat("m", MaxModelIDBytes-4) + fmt.Sprintf("%04d", index),
			Label:       strings.Repeat("l", MaxModelLabelBytes),
			Description: strings.Repeat("d", MaxModelDescriptionBytes),
		})
	}
	valid := []ModelSet{
		{Options: []ModelOption{}},
		{Current: "default", Options: []ModelOption{option("default"), option("sonnet")}},
		{Current: "claude-opus-5-5", Options: []ModelOption{option("opus")}},
		{Current: `["deepseek-official","deepseek-v4-flash"]`, Options: []ModelOption{option("anthropic/claude-sonnet-5")}},
		{Options: full},
	}
	for _, models := range valid {
		frame := Frame{Protocol: Version, Type: KindModels, Models: &models}
		if err := frame.Validate(); err != nil {
			t.Fatalf("valid models rejected: %v", err)
		}
	}
	invalid := map[string]*ModelSet{
		"missing":          nil,
		"too many":         {Options: append(append([]ModelOption(nil), full...), option("extra"))},
		"empty id":         {Options: []ModelOption{option("")}},
		"duplicate":        {Options: []ModelOption{option("a"), option("a")}},
		"empty label":      {Options: []ModelOption{{ID: "a"}}},
		"long id":          {Options: []ModelOption{option(strings.Repeat("x", MaxModelIDBytes+1))}},
		"long current":     {Current: strings.Repeat("x", MaxModelIDBytes+1), Options: []ModelOption{}},
		"control current":  {Current: "a\nb", Options: []ModelOption{}},
		"control in label": {Options: []ModelOption{{ID: "a", Label: "one\ntwo"}}},
		"C1 description":   {Options: []ModelOption{{ID: "a", Label: "A", Description: "\u009b31m"}}},
		"long label":       {Options: []ModelOption{{ID: "a", Label: strings.Repeat("l", MaxModelLabelBytes+1)}}},
	}
	for name, models := range invalid {
		t.Run(name, func(t *testing.T) {
			frame := Frame{Protocol: Version, Type: KindModels, Models: models}
			if err := frame.Validate(); err == nil {
				t.Fatal("invalid models accepted")
			}
		})
	}
	set := Frame{Protocol: Version, Type: KindSetModel, ID: "switch-1", Model: "sonnet"}
	if err := set.Validate(); err != nil {
		t.Fatalf("valid set_model rejected: %v", err)
	}
	for name, frame := range map[string]Frame{
		"set without id":    {Protocol: Version, Type: KindSetModel, Model: "sonnet"},
		"set without model": {Protocol: Version, Type: KindSetModel, ID: "switch-1"},
		"set control model": {Protocol: Version, Type: KindSetModel, ID: "switch-1", Model: "a\tb"},
		"stray models":      {Protocol: Version, Type: KindStatus, Status: StatusIdle, Models: &ModelSet{Options: []ModelOption{}}},
		"stray model":       {Protocol: Version, Type: KindStatus, Status: StatusIdle, Model: "sonnet"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := frame.Validate(); err == nil {
				t.Fatal("invalid frame accepted")
			}
		})
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
