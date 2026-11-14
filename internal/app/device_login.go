package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/oidcauth"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

const (
	maxDeviceLogins       = 32
	deviceLoginTokenName  = "meridian login"
	deviceLoginSlowdownBy = 5 * time.Second
)

// DeviceIdentityProvider is an issuer that offers the OAuth device
// authorization grant, which meridian login uses (ADR 0046).
type DeviceIdentityProvider interface {
	BeginDevice(context.Context) (oidcauth.DeviceAttempt, error)
	PollDevice(context.Context, oidcauth.DeviceAttempt) (oidcauth.Identity, error)
}

// DeviceLogin is what a terminal shows the member: a code to enter at the
// issuer. Handle is the poller's own secret; only it can collect the token.
type DeviceLogin struct {
	Handle                  string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	Interval                time.Duration
	ExpiresAt               time.Time
}

type deviceLogin struct {
	attempt  oidcauth.DeviceAttempt
	interval time.Duration
	next     time.Time
}

// ErrDevicePending reports a device login the member has not approved yet.
var ErrDevicePending = errors.New("device login is waiting for approval")

func (i *Identity) deviceProvider() (DeviceIdentityProvider, bool) {
	if i == nil || i.provider == nil {
		return nil, false
	}
	device, ok := i.provider.(DeviceIdentityProvider)
	return device, ok
}

// BeginDeviceLogin starts meridian login in team mode.
func (i *Identity) BeginDeviceLogin(ctx context.Context) (DeviceLogin, error) {
	device, ok := i.deviceProvider()
	if !ok || !i.team.Load() {
		return DeviceLogin{}, domain.ErrUnsupported
	}
	now := i.clock.Now()
	i.mu.Lock()
	i.pruneDevicesLocked(now)
	full := len(i.devices) >= maxDeviceLogins
	i.mu.Unlock()
	if full {
		return DeviceLogin{}, fmt.Errorf("%w: too many device logins are waiting", domain.ErrResourceExhausted)
	}
	attempt, err := device.BeginDevice(ctx)
	if errors.Is(err, oidcauth.ErrDeviceUnsupported) {
		return DeviceLogin{}, fmt.Errorf("%w: the identity provider has no device sign-in; mint a personal access token instead", domain.ErrUnsupported)
	}
	if err != nil {
		return DeviceLogin{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return DeviceLogin{}, err
	}
	handle := base64.RawURLEncoding.EncodeToString(raw)
	i.mu.Lock()
	defer i.mu.Unlock()
	i.devices[sha256.Sum256([]byte(handle))] = &deviceLogin{
		attempt: attempt, interval: attempt.Interval, next: now.Add(attempt.Interval),
	}
	return DeviceLogin{
		Handle: handle, UserCode: attempt.UserCode, VerificationURI: attempt.VerificationURI,
		VerificationURIComplete: attempt.VerificationURIComplete, Interval: attempt.Interval,
		ExpiresAt: attempt.Expires,
	}, nil
}

// PollDeviceLogin asks once whether the member approved. It returns
// ErrDevicePending (with the interval to wait) until they do, and then a new
// personal access token for them, exactly once.
func (i *Identity) PollDeviceLogin(ctx context.Context, handle string) (string, time.Duration, error) {
	device, ok := i.deviceProvider()
	if !ok || !i.team.Load() {
		return "", 0, domain.ErrUnsupported
	}
	key := sha256.Sum256([]byte(handle))
	now := i.clock.Now()
	i.mu.Lock()
	i.pruneDevicesLocked(now)
	login, found := i.devices[key]
	if !found {
		i.mu.Unlock()
		return "", 0, domain.ErrNotFound
	}
	if now.Before(login.next) {
		wait := login.interval
		i.mu.Unlock()
		return "", wait, ErrDevicePending
	}
	login.next = now.Add(login.interval)
	attempt := login.attempt
	i.mu.Unlock()

	verified, err := device.PollDevice(ctx, attempt)
	switch {
	case errors.Is(err, oidcauth.ErrDevicePending):
		return "", login.interval, ErrDevicePending
	case errors.Is(err, oidcauth.ErrDeviceSlowDown):
		i.mu.Lock()
		login.interval += deviceLoginSlowdownBy
		login.next = now.Add(login.interval)
		wait := login.interval
		i.mu.Unlock()
		return "", wait, ErrDevicePending
	case errors.Is(err, oidcauth.ErrDenied):
		i.forgetDevice(key)
		return "", 0, ErrSignInDenied
	case err != nil:
		return "", login.interval, ErrDevicePending
	}
	i.forgetDevice(key)
	token, err := i.mintForDevice(ctx, verified, now)
	if err != nil {
		return "", 0, err
	}
	return token, 0, nil
}

// mintForDevice signs the member in and mints the CLI's token, as one
// transaction.
func (i *Identity) mintForDevice(ctx context.Context, verified oidcauth.Identity, now time.Time) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	value := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	system := principal.WithSystem(ctx)
	err := i.store.Transact(system, func(tx ports.Transaction) error {
		user, err := i.memberTx(system, tx, verified, now)
		if err != nil {
			return err
		}
		token := domain.PersonalAccessToken{
			ID: "pat_" + i.ids.NewID(), TokenDigest: sha256.Sum256([]byte(value)), UserID: user.ID,
			Name: deviceLoginTokenName, CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(defaultTokenLifetime),
		}
		if err := tx.InsertPersonalAccessToken(system, token); err != nil {
			return err
		}
		if err := i.auditAs(system, tx, user.ID, domain.AuditSignIn, "token", token.ID, now); err != nil {
			return err
		}
		return i.auditAs(system, tx, user.ID, domain.AuditTokenCreate, "token", token.ID, now)
	})
	if err != nil {
		return "", err
	}
	return value, nil
}

func (i *Identity) forgetDevice(key [32]byte) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.devices, key)
}

func (i *Identity) pruneDevicesLocked(now time.Time) {
	for key, login := range i.devices {
		if !now.Before(login.attempt.Expires) {
			delete(i.devices, key)
		}
	}
}
