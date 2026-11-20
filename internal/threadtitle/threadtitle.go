// Package threadtitle names a Capsule from the operator's first message on
// the client side, matching the web UI (frontend/src/shell/threadTitle.ts).
// The daemon never calls it: a Capsule name is plaintext metadata, and only a
// client may submit the operator's own words as one.
package threadtitle

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// titleLimit is the length, in characters, of a title cut on a word.
	titleLimit = 48
	// minWordCut keeps a title whole rather than cutting it on a space this
	// early.
	minWordCut = 16
	// maxNameBytes is the Capsule name limit the API enforces.
	maxNameBytes = 128
)

// CapsuleName returns a short Capsule name from the operator's first
// message, or "" when the message has no usable text.
func CapsuleName(message string) string {
	name := title(message)
	for len(name) > maxNameBytes {
		_, size := utf8.DecodeLastRuneInString(name)
		name = strings.TrimRightFunc(name[:len(name)-size], unicode.IsSpace)
	}
	return name
}

func title(message string) string {
	text := strings.Join(strings.Fields(clean(message)), " ")
	text = strings.TrimLeft(text, "#")
	text = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if strings.ContainsRune("`*_~>", r) {
			return ' '
		}
		return r
	}, text)), " ")
	if text == "" {
		return ""
	}
	source := text
	if sentence := firstSentence(text); utf8.RuneCountInString(sentence) <= titleLimit {
		source = sentence
	}
	runes := []rune(source)
	if len(runes) > titleLimit {
		clipped := string(runes[:titleLimit])
		if space := strings.LastIndex(clipped, " "); utf8.RuneCountInString(clipped[:max(space, 0)]) > minWordCut {
			clipped = clipped[:space]
		}
		source = clipped
	}
	return strings.TrimSpace(strings.TrimRight(source, ".,:;!-"))
}

// clean drops control and format characters, which the API refuses and
// which can spoof layout, and turns line breaks and tabs into spaces.
func clean(message string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, message)
}

// firstSentence is the text up to and including the first '.', '!', or '?'
// that a space follows.
func firstSentence(text string) string {
	for index, r := range text {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		if next := index + 1; next < len(text) && text[next] == ' ' {
			return text[:next]
		}
	}
	return text
}
