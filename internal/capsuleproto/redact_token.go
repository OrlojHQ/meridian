package capsuleproto

import (
	"bytes"
	"encoding/json"
	"io"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

const githubRedactIdle = 50 * time.Millisecond

type streamRead struct {
	chunk []byte
	err   error
}

// streamRedacted copies from reader through redactor to emit, flushing any
// held token prefix after githubRedactIdle of quiet so interactive terminal
// echoes do not stall, and again on EOF.
func streamRedacted(reader io.Reader, bufSize int, redactor *tokenRedactor, emit func([]byte)) {
	if bufSize <= 0 {
		bufSize = 4096
	}
	reads := make(chan streamRead, 1)
	go func() {
		buffer := make([]byte, bufSize)
		for {
			n, err := reader.Read(buffer)
			chunk := append([]byte(nil), buffer[:n]...)
			reads <- streamRead{chunk: chunk, err: err}
			if err != nil {
				return
			}
		}
	}()
	var timer *time.Timer
	var timerC <-chan time.Time
	stopTimer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timerC = nil
	}
	defer stopTimer()
	for {
		select {
		case read := <-reads:
			chunk := read.chunk
			if len(chunk) > 0 && redactor != nil {
				chunk = redactor.push(chunk)
			}
			if len(chunk) > 0 {
				emit(chunk)
			}
			if redactor != nil && redactor.holding() {
				if timer == nil {
					timer = time.NewTimer(githubRedactIdle)
				} else {
					stopTimer()
					timer.Reset(githubRedactIdle)
				}
				timerC = timer.C
			} else {
				stopTimer()
			}
			if read.err != nil {
				if redactor != nil {
					if tail := redactor.flush(); len(tail) > 0 {
						emit(tail)
					}
				}
				return
			}
		case <-timerC:
			timerC = nil
			if redactor != nil {
				if tail := redactor.flush(); len(tail) > 0 {
					emit(tail)
				}
			}
		}
	}
}

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

func (r *tokenRedactor) holding() bool {
	return r != nil && len(r.hold) > 0
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
