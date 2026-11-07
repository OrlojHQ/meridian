package providergateway

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	reasonBodyLimit = 16 << 10
	reasonRuneLimit = 300
)

var (
	reasonURL        = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]*`)
	reasonCredential = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+\S+|\b(?:api[_-]?key|access[_-]?token|token|secret|password|authorization|x-api-key)\s*[:=]\s*(?:(?:bearer|basic)\s+)?\S+|\b(?:sk|pk|rk|ghp|gho|ghs|xox[abprs])[-_][A-Za-z0-9_*.-]{6,}`)
	reasonLongToken  = regexp.MustCompile(`[A-Za-z0-9_-]{32,}`)
)

// upstreamReason extracts a short, display-safe explanation from a hostile
// upstream error response. Only the body is read, never headers; the result
// carries no control characters, URLs, or credential-shaped strings, and every
// value in redact is removed case-insensitively.
func upstreamReason(response *http.Response, redact ...string) string {
	raw, err := io.ReadAll(io.LimitReader(response.Body, reasonBodyLimit))
	if err != nil && len(raw) == 0 {
		return ""
	}
	var text string
	var parsed any
	if json.Unmarshal(raw, &parsed) == nil {
		text = errorText(parsed, 0)
	} else if media, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type")); media == "text/plain" {
		text = string(raw)
	}
	return sanitizeReason(text, redact)
}

func errorText(value any, depth int) string {
	if depth > 4 {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case []any:
		for _, item := range v {
			if text := errorText(item, depth+1); text != "" {
				return text
			}
		}
	case map[string]any:
		for _, key := range []string{"message", "detail", "error", "msg", "reason"} {
			if text := errorText(v[key], depth+1); text != "" {
				return text
			}
		}
	}
	return ""
}

func sanitizeReason(text string, redact []string) string {
	text = strings.Map(func(r rune) rune {
		switch {
		case r == utf8.RuneError || unicode.Is(unicode.Cf, r):
			return -1
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, ""))
	text = strings.Join(strings.Fields(text), " ")
	for _, value := range redact {
		if len(value) >= 4 {
			text = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(value)).ReplaceAllString(text, "[redacted]")
		}
	}
	text = reasonURL.ReplaceAllString(text, "[url]")
	text = reasonCredential.ReplaceAllString(text, "[redacted]")
	text = reasonLongToken.ReplaceAllString(text, "[redacted]")
	if utf8.RuneCountInString(text) > reasonRuneLimit {
		text = string([]rune(text)[:reasonRuneLimit]) + "…"
	}
	return text
}
