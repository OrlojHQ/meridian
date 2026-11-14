package daemon

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/oidcauth"
)

// OIDCConfig is the operator's one issuer (ADR 0046).
type OIDCConfig struct {
	Issuer   string
	ClientID string
	// ClientSecretFile holds the client secret outside SQLite, like the
	// other key files. It may be empty for a public client.
	ClientSecretFile string
	// RequiredGroup, when set, must be in the ID token's GroupsClaim.
	RequiredGroup string
	GroupsClaim   string
	// PublicURL is the daemon's external origin. The callback is
	// PublicURL + /auth/oidc/callback; the daemon chooses it, never a
	// request.
	PublicURL string
}

func (c OIDCConfig) relyingParty() (app.IdentityProvider, error) {
	if c.Issuer == "" {
		if c.ClientID != "" || c.ClientSecretFile != "" || c.RequiredGroup != "" {
			return nil, errors.New("OIDC settings need --oidc-issuer")
		}
		return nil, nil
	}
	public, err := url.Parse(c.PublicURL)
	if c.PublicURL == "" || err != nil || public.Host == "" || public.RawQuery != "" || public.Fragment != "" {
		return nil, errors.New("--public-url is required with --oidc-issuer and must be an absolute URL")
	}
	secret := ""
	if c.ClientSecretFile != "" {
		raw, err := os.ReadFile(c.ClientSecretFile)
		if err != nil {
			return nil, fmt.Errorf("read OIDC client secret: %w", err)
		}
		secret = strings.TrimSpace(string(raw))
		if secret == "" {
			return nil, errors.New("OIDC client secret file is empty")
		}
	}
	party, err := oidcauth.New(oidcauth.Config{
		Issuer: c.Issuer, ClientID: c.ClientID, ClientSecret: secret,
		RedirectURL:   strings.TrimSuffix(public.String(), "/") + "/auth/oidc/callback",
		RequiredGroup: c.RequiredGroup, GroupsClaim: c.GroupsClaim,
	})
	if err != nil {
		return nil, err
	}
	return party, nil
}
