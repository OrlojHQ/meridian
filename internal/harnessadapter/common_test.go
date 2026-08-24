package harnessadapter

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestJSONLinesRejectsMalformedOversizeAndNonLF(t *testing.T) {
	tests := map[string]string{
		"malformed":    "{bad}\n",
		"CRLF":         "{\"type\":\"ok\"}\r\n",
		"unterminated": "{\"type\":\"ok\"}",
		"oversize":     strings.Repeat("x", maxUpstreamLine) + "\n",
		"deep JSON": strings.Repeat(`{"v":`, 10_001) + `null` +
			strings.Repeat(`}`, 10_001) + "\n",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			var output map[string]any
			if err := NewJSONLines(strings.NewReader(value), io.Discard).Read(&output); err == nil {
				t.Fatal("invalid upstream record was accepted")
			}
		})
	}
}

func TestJSONLinesRejectsShortWrite(t *testing.T) {
	err := NewJSONLines(strings.NewReader(""), shortUpstreamWriter{}).Write(
		map[string]string{"type": "command"},
	)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v", err)
	}
}

func TestJSONLinesPreservesUnicodeSeparatorsAndCorrelatedFields(t *testing.T) {
	input := "{\"type\":\"response\",\"id\":\"request-1\",\"value\":\"one\u2028two\u2029three\"}\n"
	var output map[string]any
	if err := NewJSONLines(strings.NewReader(input), io.Discard).Read(&output); err != nil {
		t.Fatal(err)
	}
	if output["id"] != "request-1" || output["value"] != "one\u2028two\u2029three" {
		t.Fatalf("output = %#v", output)
	}
	var encoded bytes.Buffer
	codec := NewJSONLines(strings.NewReader(""), &encoded)
	if err := codec.Write(output); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(encoded.Bytes(), []byte{'\n'}) ||
		bytes.HasSuffix(encoded.Bytes(), []byte{'\r', '\n'}) {
		t.Fatalf("encoded framing = %q", encoded.Bytes())
	}
}

func TestJSONLinesReturnsEOFOnlyAtRecordBoundary(t *testing.T) {
	var output map[string]any
	err := NewJSONLines(strings.NewReader(""), io.Discard).Read(&output)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("error = %v", err)
	}
}

type shortUpstreamWriter struct{}

func (shortUpstreamWriter) Write(value []byte) (int, error) {
	return len(value) - 1, nil
}
