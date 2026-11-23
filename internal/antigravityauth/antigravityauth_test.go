package antigravityauth_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/OrlojHQ/meridian/internal/antigravityauth"
)

const (
	tokenJSON = `{"token":{"access_token":"ya29.synthetic","token_type":"Bearer","refresh_token":"1//synthetic","expiry":"2026-11-23T03:31:22Z","expires_in":3599},"auth_method":"consumer","id_token_email":"dev@example.com"}`
	stateJSON = `{"methodId":"oauth-personal"}`
)

// Profile is a synthetic profile with the shape agy 1.5.14 writes.
func Profile() []antigravityauth.File {
	return []antigravityauth.File{
		{Path: antigravityauth.TokenFile, Content: []byte(tokenJSON)},
		{Path: antigravityauth.StateFile, Content: []byte(stateJSON)},
	}
}

func TestCheckAcceptsAgyProfile(t *testing.T) {
	account, err := antigravityauth.Check(Profile())
	if err != nil || account != "dev@example.com" {
		t.Fatalf("Check = %q, %v", account, err)
	}
}

func TestCheckRejectsOtherShapes(t *testing.T) {
	replace := func(path, content string) []antigravityauth.File {
		files := Profile()
		for i := range files {
			if files[i].Path == path {
				files[i].Content = []byte(content)
			}
		}
		return files
	}
	cases := map[string][]antigravityauth.File{
		"missing file":     Profile()[:1],
		"extra file":       append(Profile(), antigravityauth.File{Path: "cache/onboarding.json", Content: []byte("{}")}),
		"duplicate file":   {Profile()[0], Profile()[0]},
		"other path":       {Profile()[0], {Path: "../cache/acp-auth-state.json", Content: []byte(stateJSON)}},
		"enterprise":       replace(antigravityauth.TokenFile, strings.Replace(tokenJSON, `"consumer"`, `"enterprise"`, 1)),
		"no refresh token": replace(antigravityauth.TokenFile, strings.Replace(tokenJSON, `"1//synthetic"`, `""`, 1)),
		"refresh number":   replace(antigravityauth.TokenFile, strings.Replace(tokenJSON, `"1//synthetic"`, `7`, 1)),
		"bad email":        replace(antigravityauth.TokenFile, strings.Replace(tokenJSON, `dev@example.com`, `Dev <dev@example.com>`, 1)),
		"trailing data":    replace(antigravityauth.TokenFile, tokenJSON+` {}`),
		"array":            replace(antigravityauth.StateFile, `["oauth-personal"]`),
		"bad method":       replace(antigravityauth.StateFile, `{"methodId":"a b"}`),
		"too large":        replace(antigravityauth.StateFile, `{"methodId":"x","pad":"`+strings.Repeat("x", antigravityauth.MaxFileSize)+`"}`),
		"empty":            replace(antigravityauth.StateFile, ``),
	}
	for name, files := range cases {
		if _, err := antigravityauth.Check(files); !errors.Is(err, antigravityauth.ErrInvalid) {
			t.Errorf("%s: Check error = %v", name, err)
		}
	}
}

func TestCheckErrorsNeverQuoteContents(t *testing.T) {
	files := Profile()
	files[0].Content = []byte(strings.Replace(tokenJSON, "consumer", "secret-marker", 1))
	_, err := antigravityauth.Check(files)
	if err == nil || strings.Contains(err.Error(), "secret-marker") || strings.Contains(err.Error(), "synthetic") {
		t.Fatalf("error = %v", err)
	}
}

func authorizationURL(change func(url.Values)) string {
	query := url.Values{
		"access_type": {"offline"}, "client_id": {antigravityauth.ClientID}, "code_challenge": {"c"},
		"code_challenge_method": {"S256"}, "prompt": {"consent"}, "redirect_uri": {antigravityauth.RedirectURI},
		"response_type": {"code"}, "scope": {"openid"}, "state": {"s"},
	}
	if change != nil {
		change(query)
	}
	return "https://accounts.google.com/o/oauth2/auth?" + query.Encode()
}

func TestCheckAuthorizationURL(t *testing.T) {
	if err := antigravityauth.CheckAuthorizationURL(authorizationURL(nil)); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		strings.Replace(authorizationURL(nil), "https://accounts.google.com", "https://accounts.google.com.evil.test", 1),
		strings.Replace(authorizationURL(nil), "https://", "http://", 1),
		strings.Replace(authorizationURL(nil), "/o/oauth2/auth", "/signin", 1),
		authorizationURL(func(q url.Values) { q.Set("client_id", "other.apps.googleusercontent.com") }),
		authorizationURL(func(q url.Values) { q.Set("redirect_uri", "https://evil.test/callback") }),
		authorizationURL(func(q url.Values) { q.Set("response_type", "token") }),
		"https://user@accounts.google.com/o/oauth2/auth?" + strings.SplitN(authorizationURL(nil), "?", 2)[1],
	}
	for _, raw := range bad {
		if err := antigravityauth.CheckAuthorizationURL(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestCleanCode(t *testing.T) {
	if code, err := antigravityauth.CleanCode("  4/0code \n"); err != nil || code != "4/0code" {
		t.Fatalf("CleanCode = %q, %v", code, err)
	}
	for _, raw := range []string{"", "   ", "4/0 code", "4/0code\nsecond", "4/0\x00", strings.Repeat("x", antigravityauth.MaxCodeLength+1)} {
		if _, err := antigravityauth.CleanCode(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}
