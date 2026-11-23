package capsuleproto

import (
	"bytes"
	"encoding/json"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

// tokenRedactor removes one token from a byte stream. It holds back only a
// suffix that is a prefix of the token, so a token split across reads is
// removed and ordinary output is not delayed.
type tokenRedactor struct {
	token []byte
	hold  []byte
}

func newTokenRedactor(token string) *tokenRedactor {
	if token == "" {
		return &tokenRedactor{}
	}
	return &tokenRedactor{token: []byte(token)}
}

func (r *tokenRedactor) push(chunk []byte) []byte {
	if len(r.token) == 0 {
		return append([]byte(nil), chunk...)
	}
	data := append(append([]byte(nil), r.hold...), chunk...)
	r.hold = nil
	data = bytes.ReplaceAll(data, r.token, []byte("[redacted]"))
	hold := tokenPrefixSuffix(data, r.token)
	if hold == 0 {
		return data
	}
	r.hold = append([]byte(nil), data[len(data)-hold:]...)
	return append([]byte(nil), data[:len(data)-hold]...)
}

func (r *tokenRedactor) flush() []byte {
	out := r.hold
	r.hold = nil
	return out
}

// tokenPrefixSuffix is the longest suffix of data that is a proper prefix of
// token. A full match is not held: the caller has already replaced those.
func tokenPrefixSuffix(data, token []byte) int {
	max := len(token) - 1
	if max > len(data) {
		max = len(data)
	}
	for n := max; n > 0; n-- {
		if bytes.Equal(data[len(data)-n:], token[:n]) {
			return n
		}
	}
	return 0
}

func redactToken(value, token string) string {
	if token == "" || value == "" {
		return value
	}
	return string(bytes.ReplaceAll([]byte(value), []byte(token), []byte("[redacted]")))
}

// redactFrameToken removes token from a structured frame before it is stored
// or sent on. GitHub tokens are ASCII without JSON escapes, so a replacement
// in the encoded frame stays valid JSON.
func redactFrameToken(frame adapterproto.Frame, token string) adapterproto.Frame {
	if token == "" {
		return frame
	}
	raw, err := json.Marshal(frame)
	if err != nil || !bytes.Contains(raw, []byte(token)) {
		return frame
	}
	raw = bytes.ReplaceAll(raw, []byte(token), []byte("[redacted]"))
	var out adapterproto.Frame
	if json.Unmarshal(raw, &out) != nil {
		frame.Content = redactToken(frame.Content, token)
		frame.Summary = redactToken(frame.Summary, token)
		frame.Result = redactToken(frame.Result, token)
		frame.Reason = redactToken(frame.Reason, token)
		frame.Metadata = json.RawMessage(redactToken(string(frame.Metadata), token))
		return frame
	}
	return out
}
