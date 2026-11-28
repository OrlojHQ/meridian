package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
)

// A single-user browser session is a signed expiry, not a stored record
// (ADR 0066). The signing key is derived from the installation token, so a
// session survives a daemon restart and ends when the token is rotated.
const (
	// browserSessionLifetime is how long a session lasts without use.
	browserSessionLifetime = 7 * 24 * time.Hour
	// browserSessionRenewAfter is the age after which a request that the
	// session authenticates carries a fresh cookie.
	browserSessionRenewAfter = 24 * time.Hour
	browserSessionKeyLabel   = "meridian browser session v1"
	maxBrowserSessionValue   = 128
)

type browserSessions struct {
	key []byte
	now func() time.Time
}

// newBrowserSessions derives the signing key from the installation token.
// A server built without one (tests) signs with a random key that lasts
// only as long as the process.
func newBrowserSessions(token *apiauth.Token) browserSessions {
	sessions := browserSessions{now: time.Now}
	if token != nil {
		sessions.key = token.DeriveKey(browserSessionKeyLabel)
		return sessions
	}
	key := make([]byte, sha256.Size)
	if _, err := rand.Read(key); err != nil {
		return sessions
	}
	sessions.key = key
	return sessions
}

func (b browserSessions) enabled() bool {
	return len(b.key) == sha256.Size
}

// mint returns a session value that expires browserSessionLifetime from now.
func (b browserSessions) mint() string {
	expires := strconv.FormatInt(b.now().Add(browserSessionLifetime).Unix(), 10)
	return expires + "." + base64.RawURLEncoding.EncodeToString(b.sign(expires))
}

// verify reports whether value is an unexpired session this key signed,
// and whether it is old enough to renew.
func (b browserSessions) verify(value string) (ok, renew bool) {
	if !b.enabled() || len(value) > maxBrowserSessionValue {
		return false, false
	}
	expiresText, signature, found := strings.Cut(value, ".")
	if !found || expiresText == "" || len(expiresText) > 12 ||
		strings.Trim(expiresText, "0123456789") != "" {
		return false, false
	}
	presented, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(presented, b.sign(expiresText)) {
		return false, false
	}
	seconds, err := strconv.ParseInt(expiresText, 10, 64)
	if err != nil {
		return false, false
	}
	now := b.now()
	expires := time.Unix(seconds, 0)
	if !expires.After(now) || expires.After(now.Add(browserSessionLifetime+time.Minute)) {
		return false, false
	}
	return true, expires.Sub(now) < browserSessionLifetime-browserSessionRenewAfter
}

func (b browserSessions) sign(expires string) []byte {
	mac := hmac.New(sha256.New, b.key)
	mac.Write([]byte(browserSessionKeyLabel))
	mac.Write([]byte{0})
	mac.Write([]byte(expires))
	return mac.Sum(nil)
}
