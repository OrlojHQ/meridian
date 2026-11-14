package oidcauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrDeviceUnsupported reports an issuer without a device authorization
	// endpoint; members mint a personal access token instead.
	ErrDeviceUnsupported = errors.New("the identity provider does not offer device sign-in")
	// ErrDevicePending reports a device sign-in the member has not yet
	// approved; ErrDeviceSlowDown asks the poller to wait longer.
	ErrDevicePending  = errors.New("device sign-in is waiting for approval")
	ErrDeviceSlowDown = errors.New("device sign-in polled too fast")
)

// DeviceAttempt is one device authorization grant (RFC 8628). DeviceCode
// stays in daemon memory; UserCode and the verification URIs are shown to
// the member.
type DeviceAttempt struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	Interval                time.Duration
	Expires                 time.Time
}

// BeginDevice starts a device sign-in for a terminal client.
func (r *RelyingParty) BeginDevice(ctx context.Context) (DeviceAttempt, error) {
	provider, err := r.discover(ctx)
	if err != nil {
		return DeviceAttempt{}, err
	}
	if provider.Endpoint().DeviceAuthURL == "" {
		return DeviceAttempt{}, ErrDeviceUnsupported
	}
	config := r.oauth(provider)
	response, err := config.DeviceAuth(r.context(ctx))
	if err != nil {
		return DeviceAttempt{}, fmt.Errorf("start device sign-in: %w", err)
	}
	interval := time.Duration(response.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	expires := response.Expiry
	if expires.IsZero() || time.Until(expires) > 15*time.Minute {
		expires = time.Now().Add(15 * time.Minute)
	}
	return DeviceAttempt{
		DeviceCode: response.DeviceCode, UserCode: response.UserCode,
		VerificationURI: response.VerificationURI, VerificationURIComplete: response.VerificationURIComplete,
		Interval: interval, Expires: expires,
	}, nil
}

// PollDevice makes one token request for attempt. It returns
// ErrDevicePending or ErrDeviceSlowDown while the member has not approved,
// ErrDenied when they refused or the code expired, and the verified
// identity once they approve.
func (r *RelyingParty) PollDevice(ctx context.Context, attempt DeviceAttempt) (Identity, error) {
	provider, err := r.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	form := url.Values{
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {attempt.DeviceCode},
		"client_id":   {r.config.ClientID},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.Endpoint().TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	if r.config.ClientSecret != "" {
		request.SetBasicAuth(url.QueryEscape(r.config.ClientID), url.QueryEscape(r.config.ClientSecret))
	}
	client := r.config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return Identity{}, fmt.Errorf("poll device sign-in: %w", err)
	}
	defer response.Body.Close()
	var body struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body); err != nil {
		return Identity{}, fmt.Errorf("decode device token response: %w", err)
	}
	switch body.Error {
	case "":
	case "authorization_pending":
		return Identity{}, ErrDevicePending
	case "slow_down":
		return Identity{}, ErrDeviceSlowDown
	case "access_denied", "expired_token":
		return Identity{}, ErrDenied
	default:
		return Identity{}, fmt.Errorf("device sign-in failed: %s", body.Error)
	}
	if response.StatusCode != http.StatusOK || body.IDToken == "" {
		return Identity{}, errors.New("device token response has no ID token")
	}
	return r.identity(r.context(ctx), provider, body.IDToken, "")
}
