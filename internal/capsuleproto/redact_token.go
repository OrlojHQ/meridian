package capsuleproto

import (
	"bytes"
	"encoding/json"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

// tokenRedactor removes installation tokens from a byte stream. It holds
// back only a suffix that is a prefix of a token, so a token split across
// reads is removed and ordinary output is not delayed. Tokens added to the
// vault later are removed from later output.
type tokenRedactor struct {
	vault *tokenVault
	hold  []byte
}

func newTokenRedactor(vault *tokenVault) *tokenRedactor {
	return &tokenRedactor{vault: vault}
}

func (r *tokenRedactor) push(chunk []byte) []byte {
	tokens := r.vault.snapshot()
	if len(tokens) == 0 {
		return append([]byte(nil), chunk...)
	}
	data := append(append([]byte(nil), r.hold...), chunk...)
	r.hold = nil
	for _, token := range tokens {
		data = bytes.ReplaceAll(data, token, []byte("[redacted]"))
	}
	hold := 0
	for _, token := range tokens {
		if n := tokenPrefixSuffix(data, token); n > hold {
			hold = n
		}
	}
	if hold == 0 {
		return data
	}
	r.hold = append([]byte(nil), data[len(data)-hold:]...)
	return append([]byte(nil), data[:len(data)-hold]...)
}

func (r *tokenRedactor) flush() []byte {
	out := r.hold
	r.hold = nil
	if len(out) == 0 {
		return nil
	}
	for _, token := range r.vault.snapshot() {
		out = bytes.ReplaceAll(out, token, []byte("[redacted]"))
	}
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

func redactFrameTokens(frame adapterproto.Frame, tokens [][]byte) adapterproto.Frame {
	if len(tokens) == 0 {
		return frame
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return frame
	}
	changed := false
	for _, token := range tokens {
		if len(token) == 0 || !bytes.Contains(raw, token) {
			continue
		}
		raw = bytes.ReplaceAll(raw, token, []byte("[redacted]"))
		changed = true
	}
	if !changed {
		return frame
	}
	var out adapterproto.Frame
	if json.Unmarshal(raw, &out) != nil {
		return frame
	}
	return out
}
