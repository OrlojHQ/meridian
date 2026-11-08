package coordproto

import (
	"bufio"
	"errors"
	"strings"
	"testing"
)

func record(body string) []byte { return []byte(body + "\n") }

func TestDecodeRequestAcceptsClosedFieldSets(t *testing.T) {
	valid := []string{
		`{"protocol":"meridian.coord.v1","type":"spawn","key":"spawn-a","name":"subagent-1","harness":"claude","prompt":"do it"}`,
		`{"protocol":"meridian.coord.v1","type":"send","key":"k","link":"link-1","prompt":"more"}`,
		`{"protocol":"meridian.coord.v1","type":"cancel","key":"k","link":"link-1"}`,
		`{"protocol":"meridian.coord.v1","type":"wait","links":["a","b"],"all":true,"timeoutSeconds":30}`,
		`{"protocol":"meridian.coord.v1","type":"status","link":"link-1"}`,
		`{"protocol":"meridian.coord.v1","type":"result","link":"link-1"}`,
		`{"protocol":"meridian.coord.v1","type":"patch","link":"link-1","maxBytes":1048576}`,
		`{"protocol":"meridian.coord.v1","type":"report","key":"r","status":"done","summary":"finished the task"}`,
		`{"protocol":"meridian.coord.v1","type":"propose-delivery","key":"p","title":"Ship the parser","body":"notes","branch":"feature/parser"}`,
		`{"protocol":"meridian.coord.v1","type":"list"}`,
		`{"protocol":"meridian.coord.v1","type":"whoami"}`,
	}
	for _, body := range valid {
		if _, err := DecodeRequest(record(body)); err != nil {
			t.Errorf("%s: %v", body, err)
		}
	}
}

func TestDecodeRequestRejectsHostileRecords(t *testing.T) {
	cases := map[string][]byte{
		"unknown field":            record(`{"protocol":"meridian.coord.v1","type":"list","capsule":"x"}`),
		"wrong protocol":           record(`{"protocol":"meridian.coord.v2","type":"list"}`),
		"unknown type":             record(`{"protocol":"meridian.coord.v1","type":"promote"}`),
		"lead cannot report extra": record(`{"protocol":"meridian.coord.v1","type":"report","key":"k","status":"done","summary":"x","link":"l"}`),
		"bad report status":        record(`{"protocol":"meridian.coord.v1","type":"report","key":"k","status":"turn_ended","summary":"x"}`),
		"propose missing title":    record(`{"protocol":"meridian.coord.v1","type":"propose-delivery","key":"k","branch":"feature/x"}`),
		"propose newline title":    record(`{"protocol":"meridian.coord.v1","type":"propose-delivery","key":"k","title":"a\nb","branch":"feature/x"}`),
		"patch too large":          record(`{"protocol":"meridian.coord.v1","type":"patch","link":"l","maxBytes":4194305}`),
		"field not allowed":        record(`{"protocol":"meridian.coord.v1","type":"list","link":"x"}`),
		"spawn without key":        record(`{"protocol":"meridian.coord.v1","type":"spawn","name":"a","harness":"h","prompt":"p"}`),
		"bad name":                 record(`{"protocol":"meridian.coord.v1","type":"spawn","key":"k","name":"Subagent","harness":"h","prompt":"p"}`),
		"long name":                record(`{"protocol":"meridian.coord.v1","type":"spawn","key":"k","name":"` + strings.Repeat("a", 49) + `","harness":"h","prompt":"p"}`),
		"control in key":           record(`{"protocol":"meridian.coord.v1","type":"cancel","key":"a b","link":"l"}`),
		"NUL in prompt":            record(`{"protocol":"meridian.coord.v1","type":"send","key":"k","link":"l","prompt":"a\u0000b"}`),
		"all without links":        record(`{"protocol":"meridian.coord.v1","type":"wait","all":true}`),
		"timeout too long":         record(`{"protocol":"meridian.coord.v1","type":"wait","timeoutSeconds":3601}`),
		"trailing data":            record(`{"protocol":"meridian.coord.v1","type":"list"} {}`),
		"carriage return":          []byte("{\"protocol\":\"meridian.coord.v1\",\"type\":\"list\"}\r\n"),
		"missing LF":               []byte(`{"protocol":"meridian.coord.v1","type":"list"}`),
		"invalid UTF-8":            record("{\"protocol\":\"meridian.coord.v1\",\"type\":\"list\",\"x\":\"\xff\"}"),
		"oversized prompt": record(`{"protocol":"meridian.coord.v1","type":"send","key":"k","link":"l","prompt":"` +
			strings.Repeat("a", MaxPromptBytes+1) + `"}`),
	}
	for name, body := range cases {
		if _, err := DecodeRequest(body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	tooMany := `{"protocol":"meridian.coord.v1","type":"wait","links":[` +
		strings.TrimSuffix(strings.Repeat(`"l",`, MaxWaitLinks+1), ",") + `]}`
	if _, err := DecodeRequest(record(tooMany)); err == nil {
		t.Error("accepted too many wait links")
	}
}

func TestReadRecordBoundsLines(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("a", MaxRecordBytes+1)+"\n"), 4096)
	if _, err := ReadRecord(reader); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("oversized record: %v", err)
	}
	reader = bufio.NewReader(strings.NewReader("{}"))
	if _, err := ReadRecord(reader); !errors.Is(err, ErrMissingLF) {
		t.Fatalf("unterminated record: %v", err)
	}
}

func TestDigestBindsTheWholeRequest(t *testing.T) {
	base := Request{Protocol: Version, Type: KindSpawn, Key: "k", Name: "a", Harness: "h", Prompt: "p"}
	changed := base
	changed.Prompt = "q"
	if base.Digest() == changed.Digest() {
		t.Fatal("digest ignores the prompt")
	}
	unversioned := base
	unversioned.Protocol = ""
	if base.Digest() != unversioned.Digest() {
		t.Fatal("digest depends on the protocol field")
	}
}

func TestFailMarksRetryableCodesAndBoundsMessages(t *testing.T) {
	for _, code := range []string{CodeBusy, CodeRateLimited, CodePending, CodeMailboxLost, CodeCapacityExhausted, CodeUnavailable} {
		if !Fail(code, "x").Error.Retryable {
			t.Errorf("%s is not retryable", code)
		}
	}
	for _, code := range []string{CodeInvalidRequest, CodeNotFound, CodeLimitExceeded, CodeHarnessNotAllowed, CodeGrantInactive} {
		if Fail(code, "x").Error.Retryable {
			t.Errorf("%s is retryable", code)
		}
	}
	if got := Fail(CodeInternal, strings.Repeat("x", 2*MaxMessageBytes)).Error.Message; len(got) != MaxMessageBytes {
		t.Fatalf("message length %d", len(got))
	}
}

func TestDecodeResponseRequiresExactlyOneOutcome(t *testing.T) {
	for _, body := range []string{
		`{"protocol":"meridian.coord.v1","ok":false}`,
		`{"protocol":"meridian.coord.v1","ok":true,"error":{"code":"busy","message":"x"}}`,
		`{"protocol":"meridian.coord.v1","ok":true,"extra":1}`,
	} {
		if _, err := DecodeResponse(record(body)); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
	if _, err := DecodeResponse(record(`{"protocol":"meridian.coord.v1","ok":true}`)); err != nil {
		t.Fatal(err)
	}
}

func TestConfigAndNotificationValidation(t *testing.T) {
	if err := (Config{Enabled: false, Self: &Self{}}).Validate(); err == nil {
		t.Error("disabled config with self accepted")
	}
	if err := (Config{Enabled: true, Self: &Self{Role: "subagent", GrantID: "g"}}).Validate(); err == nil {
		t.Error("non-lead config accepted")
	}
	if err := (Config{Enabled: true, Self: &Self{Role: "lead", GrantID: "g"}}).Validate(); err != nil {
		t.Error(err)
	}
	if err := (Config{Enabled: true, Self: &Self{Role: "sub", Depth: 1, GrantID: "g"}}).Validate(); err != nil {
		t.Error(err)
	}
	if err := (Config{Enabled: true, Self: &Self{Role: "sub", Depth: 0, GrantID: "g"}}).Validate(); err == nil {
		t.Error("sub config with lead depth accepted")
	}
	big := Notification{Reports: []Report{{Link: "l", Seq: 1, Summary: strings.Repeat("x", MaxReportSummaryBytes+1)}}}
	if err := big.Validate(); err == nil {
		t.Error("oversized report accepted")
	}
	if err := (Notification{Reports: []Report{{Link: "l", Seq: 0}}}).Validate(); err == nil {
		t.Error("zero report sequence accepted")
	}
}

func TestTruncateUTF8KeepsRuneBoundaries(t *testing.T) {
	value, truncated := TruncateUTF8("aé", 2)
	if value != "a" || !truncated {
		t.Fatalf("got %q %v", value, truncated)
	}
	if value, truncated := TruncateUTF8("abc", 3); value != "abc" || truncated {
		t.Fatalf("got %q %v", value, truncated)
	}
}

func FuzzDecodeRequest(f *testing.F) {
	f.Add(record(`{"protocol":"meridian.coord.v1","type":"spawn","key":"k","name":"a","harness":"h","prompt":"p"}`))
	f.Add(record(`{"protocol":"meridian.coord.v1","type":"wait","links":["a"],"all":true}`))
	f.Add([]byte("{\n"))
	f.Fuzz(func(t *testing.T, body []byte) {
		request, err := DecodeRequest(body)
		if err != nil {
			return
		}
		if request.Validate() != nil {
			t.Fatal("decoded request does not validate")
		}
		encoded, err := Encode(request)
		if err != nil {
			t.Fatal(err)
		}
		again, err := DecodeRequest(encoded)
		if err != nil || again.Digest() != request.Digest() {
			t.Fatalf("round trip changed the request: %v", err)
		}
	})
}
