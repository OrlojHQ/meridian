// Package antigravityauth checks the Antigravity CLI sign-in profile that an
// antigravity provider connection holds (ADR 0062). Meridian never uses the
// token inside it: only agy reads the profile, in a Capsule. This package
// checks shape only, so capsuled and meridiand accept the same two files and
// nothing else.
package antigravityauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/mail"
	"net/url"
	"strings"
	"unicode"
)

const (
	// Provider is the provider connection type that holds a profile.
	Provider = "antigravity"
	// Dir is agy's application data directory under the runtime's home.
	Dir = ".gemini/antigravity-cli"
	// TokenFile and StateFile are the profile, relative to Dir.
	TokenFile = "antigravity-oauth-token"
	StateFile = "cache/acp-auth-state.json"
	// MaxFileSize bounds each profile file.
	MaxFileSize = 16 << 10
	// MaxCodeLength bounds the code, or landing-page address, a user pastes.
	MaxCodeLength = 2048

	// ClientID and RedirectURI are the pinned agy release's own OAuth client.
	// Meridian has no OAuth client of its own for Antigravity.
	ClientID    = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	RedirectURI = "https://antigravity.google/oauth-callback"
)

// Paths lists the profile files, relative to Dir, in a fixed order.
var Paths = []string{TokenFile, StateFile}

// File is one profile file. Content is opaque to Meridian.
type File struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

// ErrInvalid is returned for any profile, URL, or code that fails a check.
// Its text never includes file contents.
var ErrInvalid = errors.New("invalid Antigravity sign-in")

// Check accepts exactly the two profile files and returns the signed-in
// account's email. It reads the token file's keys and types; it does not
// interpret or keep the token.
func Check(files []File) (string, error) {
	if len(files) != len(Paths) {
		return "", ErrInvalid
	}
	byPath := map[string][]byte{}
	for _, file := range files {
		if _, seen := byPath[file.Path]; seen || !knownPath(file.Path) || len(file.Content) == 0 || len(file.Content) > MaxFileSize {
			return "", ErrInvalid
		}
		byPath[file.Path] = file.Content
	}
	var token struct {
		Token struct {
			AccessToken  *string `json:"access_token"`
			TokenType    *string `json:"token_type"`
			RefreshToken *string `json:"refresh_token"`
		} `json:"token"`
		AuthMethod *string `json:"auth_method"`
		Email      *string `json:"id_token_email"`
	}
	if !decodeObject(byPath[TokenFile], &token) {
		return "", ErrInvalid
	}
	if token.Token.AccessToken == nil || token.Token.TokenType == nil || token.Token.RefreshToken == nil ||
		*token.Token.RefreshToken == "" || token.AuthMethod == nil || *token.AuthMethod != "consumer" || token.Email == nil {
		return "", ErrInvalid
	}
	email, ok := Account(*token.Email)
	if !ok {
		return "", ErrInvalid
	}
	var state struct {
		MethodID *string `json:"methodId"`
	}
	if !decodeObject(byPath[StateFile], &state) || state.MethodID == nil || !methodID(*state.MethodID) {
		return "", ErrInvalid
	}
	return email, nil
}

// Account accepts a plain email address for display.
func Account(value string) (string, bool) {
	if len(value) < 3 || len(value) > 254 || strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", false
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || address.Name != "" {
		return "", false
	}
	return value, true
}

// CheckAuthorizationURL accepts only Google's OAuth page for agy's own
// client and redirect, so a sign-in Capsule cannot send the user elsewhere.
func CheckAuthorizationURL(raw string) error {
	if len(raw) > 8192 {
		return ErrInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "accounts.google.com" || u.User != nil || u.Fragment != "" {
		return ErrInvalid
	}
	if u.Path != "/o/oauth2/auth" && u.Path != "/o/oauth2/v2/auth" {
		return ErrInvalid
	}
	query := u.Query()
	if query.Get("client_id") != ClientID || query.Get("redirect_uri") != RedirectURI || query.Get("response_type") != "code" {
		return ErrInvalid
	}
	return nil
}

// CleanCode trims a pasted code or landing-page address and rejects one
// agy could misread as more than one line of input.
func CleanCode(raw string) (string, error) {
	code := strings.TrimSpace(raw)
	if code == "" || len(code) > MaxCodeLength || strings.IndexFunc(code, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", ErrInvalid
	}
	return code, nil
}

func knownPath(p string) bool {
	for _, known := range Paths {
		if p == known {
			return true
		}
	}
	return false
}

func decodeObject(raw []byte, output any) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(output) != nil {
		return false
	}
	var rest json.RawMessage
	return errors.Is(decoder.Decode(&rest), io.EOF) && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{"))
}

func methodID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
