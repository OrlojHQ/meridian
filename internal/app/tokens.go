package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

const (
	// TokenPrefix marks a personal access token, so secret scanners can
	// find one that leaked (ADR 0046).
	TokenPrefix          = apiauth.PersonalTokenPrefix
	defaultTokenLifetime = 90 * 24 * time.Hour
	maxTokenLifetime     = 365 * 24 * time.Hour
	minTokenLifetime     = 24 * time.Hour
	maxActiveTokens      = 50
)

// MintedToken is a new token. Value is shown once and never stored.
type MintedToken struct {
	Value  string
	Record domain.PersonalAccessToken
}

// MintToken creates a personal access token for the calling member. A zero
// lifetime is 90 days; at most a year is allowed. Tokens exist only in team
// mode: in single-user mode the installation token is the owner's.
func (i *Identity) MintToken(ctx context.Context, name string, readOnly bool, lifetime time.Duration) (MintedToken, error) {
	caller, err := principal.Require(ctx)
	if err != nil {
		return MintedToken{}, err
	}
	if caller.Kind() != principal.KindUser {
		return MintedToken{}, domain.ErrNotFound
	}
	if !i.team.Load() {
		return MintedToken{}, fmt.Errorf("%w: personal access tokens need team mode", domain.ErrUnsupported)
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || strings.IndexFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return MintedToken{}, fmt.Errorf("%w: token name must be 1 to 64 printable characters", domain.ErrInvalid)
	}
	if lifetime == 0 {
		lifetime = defaultTokenLifetime
	}
	if lifetime < minTokenLifetime || lifetime > maxTokenLifetime {
		return MintedToken{}, fmt.Errorf("%w: token lifetime must be between 1 and 365 days", domain.ErrInvalid)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return MintedToken{}, err
	}
	value := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	now := i.clock.Now()
	record := domain.PersonalAccessToken{
		ID: "pat_" + i.ids.NewID(), TokenDigest: sha256.Sum256([]byte(value)), UserID: caller.UserID(),
		Name: name, ReadOnly: readOnly, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(lifetime),
	}
	err = i.store.Transact(ctx, func(tx ports.Transaction) error {
		existing, err := tx.ListPersonalAccessTokens(ctx, caller.UserID())
		if err != nil {
			return err
		}
		active := 0
		for _, token := range existing {
			if token.RevokedAt.IsZero() && now.Before(token.ExpiresAt) {
				active++
			}
		}
		if active >= maxActiveTokens {
			return fmt.Errorf("%w: revoke a token before minting another", domain.ErrResourceExhausted)
		}
		if err := tx.InsertPersonalAccessToken(ctx, record); err != nil {
			return err
		}
		audit, ok := auditRecordFor(ctx, now, domain.AuditTokenCreate, "token", record.ID, domain.AuditAllowed)
		if !ok {
			return principal.ErrMissing
		}
		return tx.AppendAuditRecord(ctx, audit)
	})
	if err != nil {
		return MintedToken{}, err
	}
	return MintedToken{Value: value, Record: record}, nil
}

// Tokens lists the calling member's tokens, newest first.
func (i *Identity) Tokens(ctx context.Context) ([]domain.PersonalAccessToken, error) {
	caller, err := principal.Require(ctx)
	if err != nil {
		return nil, err
	}
	if caller.Kind() != principal.KindUser {
		return nil, domain.ErrNotFound
	}
	var tokens []domain.PersonalAccessToken
	err = i.store.View(ctx, func(reader ports.Reader) error {
		var err error
		tokens, err = reader.ListPersonalAccessTokens(ctx, caller.UserID())
		return err
	})
	return tokens, err
}

// RevokeToken revokes one of the calling member's tokens.
func (i *Identity) RevokeToken(ctx context.Context, id string) error {
	caller, err := principal.Require(ctx)
	if err != nil {
		return err
	}
	if caller.Kind() != principal.KindUser {
		return domain.ErrNotFound
	}
	now := i.clock.Now()
	return i.store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.RevokePersonalAccessToken(ctx, id, now); err != nil {
			return err
		}
		audit, ok := auditRecordFor(ctx, now, domain.AuditTokenRevoke, "token", id, domain.AuditAllowed)
		if !ok {
			return principal.ErrMissing
		}
		return tx.AppendAuditRecord(ctx, audit)
	})
}

// Token resolves a personal access token to its member and whether it is
// read-only. An unknown, revoked, or expired token, or one whose member is
// disabled, reads as not found.
func (i *Identity) Token(ctx context.Context, value string) (principal.Principal, bool, error) {
	if i == nil || !i.team.Load() || !strings.HasPrefix(value, TokenPrefix) || len(value) > 128 {
		return principal.Principal{}, false, domain.ErrNotFound
	}
	digest := sha256.Sum256([]byte(value))
	system := principal.WithSystem(ctx)
	now := i.clock.Now()
	var token domain.PersonalAccessToken
	var user domain.User
	if err := i.store.View(system, func(reader ports.Reader) error {
		var err error
		if token, err = reader.GetPersonalAccessTokenByDigest(system, digest); err != nil {
			return err
		}
		user, err = reader.GetUser(system, token.UserID)
		return err
	}); err != nil {
		return principal.Principal{}, false, domain.ErrNotFound
	}
	if !token.RevokedAt.IsZero() || !now.Before(token.ExpiresAt) || !user.DisabledAt.IsZero() {
		return principal.Principal{}, false, domain.ErrNotFound
	}
	if now.Sub(token.LastUsedAt) >= sessionTouchInterval {
		_ = i.store.Transact(system, func(tx ports.Transaction) error {
			return tx.TouchPersonalAccessToken(system, token.ID, now)
		})
	}
	caller, ok := principal.User(user.ID)
	if !ok {
		return principal.Principal{}, false, domain.ErrNotFound
	}
	return caller, token.ReadOnly, nil
}
