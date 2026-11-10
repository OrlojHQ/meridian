package app

import (
	"context"
	"fmt"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/providergateway"
	"net/http"
	"net/url"
	"strings"
)

type ConnectionInput struct {
	Name                    string                 `json:"name"`
	Provider                string                 `json:"provider"`
	APIKey                  string                 `json:"apiKey"`
	BaseURL                 string                 `json:"baseUrl,omitempty"`
	DefaultModel            string                 `json:"defaultModel,omitempty"`
	AllowPrivateHTTP        bool                   `json:"allowPrivateHttp,omitempty"`
	ReasoningEfforts        []string               `json:"reasoningEfforts,omitempty"`
	CredentialKind          string                 `json:"credentialKind,omitempty"`
	ExpectedResourceVersion domain.ResourceVersion `json:"expectedResourceVersion"`
	Revoked                 bool                   `json:"revoked,omitempty"`
}

// The custom upstream is bound into the envelope context, so a stored key
// cannot be redirected to another endpoint by editing connection metadata.
func connectionEnvelopeID(item domain.ProviderConnection) string {
	id := fmt.Sprintf("connection:%s:%s:%d:%t", item.ID, item.Provider, item.ResourceVersion, item.Revoked)
	if providergateway.Custom(item.Provider) {
		id += fmt.Sprintf(":%q:%t", item.BaseURL, item.AllowPrivateHTTP)
	}
	// API-key rows keep the historical envelope id so existing ciphertext
	// still opens. A setup token is bound to oauth so editing the stored
	// kind cannot change how the gateway attaches it.
	if item.CredentialKind == providergateway.CredentialOAuth {
		id += ":oauth"
	}
	return id
}

// ConfigureProviderGateway enables the gateway. allowPrivateHTTP is the
// installation-level opt-in that custom connections need before they may use
// plaintext HTTP to a private-network upstream.
func (s *Service) ConfigureProviderGateway(baseURL string, allowPrivateHTTP bool) error {
	if baseURL == "" {
		if allowPrivateHTTP {
			return fmt.Errorf("private-network HTTP provider upstreams require the provider gateway (--provider-gateway-url, or the local gateway on Docker)")
		}
		return nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Scheme != "https" {
		return fmt.Errorf("provider gateway URL must be an HTTPS origin reachable from Capsules")
	}
	s.gatewayURL = strings.TrimRight(baseURL, "/") + "/provider-gateway"
	s.privateHTTP = allowPrivateHTTP
	s.gateway = providergateway.New(s.authorizeProviderLease, providergateway.Options{AllowPrivateHTTP: allowPrivateHTTP})
	return nil
}

// ConfigureLocalProviderGateway enables the gateway behind meridiand's own
// TLS listener (ADR 0037). trust returns the CA certificates a Capsule given
// a lease now must trust; they travel with the lease. The API listener then
// does not serve gateway routes.
func (s *Service) ConfigureLocalProviderGateway(origin string, trust func() (string, error), allowPrivateHTTP bool) error {
	if trust == nil {
		return fmt.Errorf("local provider gateway requires a trust source")
	}
	if err := s.ConfigureProviderGateway(origin, allowPrivateHTTP); err != nil {
		return err
	}
	s.gatewayTrust = trust
	return nil
}
func (s *Service) ProviderGateway() http.Handler { return s.gateway }
func (s *Service) ProviderGatewayEnabled() bool  { return s.gateway != nil }

// ProviderGatewayOnAPIListener reports whether the API listener serves
// gateway routes; the local gateway has a listener of its own.
func (s *Service) ProviderGatewayOnAPIListener() bool {
	return s.gateway != nil && s.gatewayTrust == nil
}
func (s *Service) ProviderGatewayAllowsPrivateHTTP() bool { return s.gateway != nil && s.privateHTTP }
func (s *Service) CloseProviderGateway() {
	if s.gateway != nil {
		s.gateway.Close()
	}
}
func (s *Service) ListProviderConnections(ctx context.Context) ([]domain.ProviderConnection, error) {
	var items []domain.ProviderConnection
	err := s.store.View(ctx, func(r ports.Reader) error { var err error; items, err = r.ListProviderConnections(ctx); return err })
	return items, err
}
func (s *Service) PutProviderConnection(ctx context.Context, id string, input ConnectionInput) (domain.ProviderConnection, error) {
	if s.secretKey == nil || s.gateway == nil {
		return domain.ProviderConnection{}, domain.ErrUnsupported
	}
	if providergateway.API(input.Provider) == "" {
		return domain.ProviderConnection{}, fmt.Errorf("%w: unsupported provider", domain.ErrInvalid)
	}
	name, err := requireName(input.Name)
	if err != nil {
		return domain.ProviderConnection{}, err
	}
	if !input.Revoked {
		if input, err = s.validateConnectionInput(input); err != nil {
			return domain.ProviderConnection{}, err
		}
	}
	var result domain.ProviderConnection
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if id == "" {
			items, err := tx.ListProviderConnections(ctx)
			if err != nil {
				return err
			}
			if len(items) >= 128 {
				return domain.ErrConflict
			}
			result = domain.ProviderConnection{ID: s.ids.NewID(), Provider: input.Provider, CreatedAt: s.clock.Now().UTC()}
		} else {
			var err error
			result, err = tx.GetProviderConnection(ctx, id)
			if err != nil {
				return err
			}
		}
		if result.ResourceVersion != input.ExpectedResourceVersion || result.Provider != input.Provider {
			return domain.ErrConflict
		}
		result.Name = name
		result.ResourceVersion++
		result.Revoked = input.Revoked
		if !input.Revoked {
			result.BaseURL, result.DefaultModel, result.AllowPrivateHTTP = input.BaseURL, input.DefaultModel, input.AllowPrivateHTTP
			result.ReasoningEfforts = input.ReasoningEfforts
			result.CredentialKind = input.CredentialKind
		} else {
			result.CredentialKind = ""
		}
		result.KeyID = s.secretKey.ID
		// Keep a valid encrypted empty envelope on revocation, never old key material.
		value := input.APIKey
		if input.Revoked {
			value = ""
		}
		result.Nonce, result.Ciphertext, err = s.secretKey.SealSetup(connectionEnvelopeID(result), []byte(value))
		if err != nil {
			return err
		}
		if err := tx.PutProviderConnection(ctx, result); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "provider_connection", result.ID, "provider_connection.updated", result.ResourceVersion, nil)
	})
	if err == nil && result.Revoked {
		s.gateway.Revoke(result.ID, "", "")
	}
	return result, err
}

// Custom endpoints may omit the key; official providers require one and use
// fixed upstreams.
func (s *Service) validateConnectionInput(input ConnectionInput) (ConnectionInput, error) {
	switch input.CredentialKind {
	case "", providergateway.CredentialAPIKey:
		input.CredentialKind = ""
	case providergateway.CredentialOAuth:
		if input.Provider != providergateway.Anthropic {
			return input, fmt.Errorf("%w: a Claude setup token is only valid for the official Anthropic connection", domain.ErrInvalid)
		}
	default:
		return input, fmt.Errorf("%w: invalid Claude login type", domain.ErrInvalid)
	}
	if len(input.APIKey) > 4096 || strings.ContainsAny(input.APIKey, "\r\n\x00") {
		return input, fmt.Errorf("%w: invalid API key", domain.ErrInvalid)
	}
	if !providergateway.Custom(input.Provider) {
		if len(input.APIKey) < 8 {
			return input, fmt.Errorf("%w: invalid API key", domain.ErrInvalid)
		}
		if input.BaseURL != "" || input.DefaultModel != "" || input.AllowPrivateHTTP || len(input.ReasoningEfforts) != 0 {
			return input, fmt.Errorf("%w: official providers use fixed upstreams; choose a custom endpoint type", domain.ErrInvalid)
		}
		return input, nil
	}
	base, err := providergateway.NormalizeBaseURL(input.BaseURL, input.AllowPrivateHTTP)
	if err != nil {
		return input, fmt.Errorf("%w: %v", domain.ErrInvalid, err)
	}
	if input.AllowPrivateHTTP && !s.privateHTTP {
		return input, fmt.Errorf("%w: private-network HTTP upstreams are disabled on this installation", domain.ErrInvalid)
	}
	if input.DefaultModel != "" && !harnesssetup.ValidModel(input.DefaultModel) {
		return input, fmt.Errorf("%w: invalid default model name", domain.ErrInvalid)
	}
	if len(input.ReasoningEfforts) != 0 {
		if input.DefaultModel == "" {
			return input, fmt.Errorf("%w: reasoning effort levels describe the default model; set one", domain.ErrInvalid)
		}
		levels, ok := harnesssetup.ValidEfforts(input.ReasoningEfforts)
		if !ok {
			return input, fmt.Errorf("%w: reasoning effort levels must be distinct values of %s",
				domain.ErrInvalid, strings.Join(harnesssetup.EffortLevels, ", "))
		}
		input.ReasoningEfforts = levels
	}
	input.BaseURL = base
	return input, nil
}
func (s *Service) GrantProviderConnection(ctx context.Context, project domain.ProjectID, harness, connection string) error {
	if !harnesssetup.ConnectionHarness(harness) {
		return domain.ErrInvalid
	}
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, err := tx.GetProject(ctx, project); err != nil {
			return err
		}
		if connection != "" {
			item, err := tx.GetProviderConnection(ctx, connection)
			if err != nil {
				return err
			}
			if item.Revoked {
				return domain.ErrConflict
			}
			if !harnesssetup.ConnectionSupported(harness, item.Provider, item.DefaultModel) {
				return fmt.Errorf("%w: this connection type cannot be used with %s", domain.ErrInvalid, harness)
			}
		}
		if err := tx.GrantProviderConnection(ctx, project, harness, connection); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "project", string(project), "provider_connection.grant_changed", 1, nil)
	})
	if err == nil && s.gateway != nil {
		s.gateway.Revoke("", string(project), harness)
	}
	return err
}
func (s *Service) ProjectProviderConnection(ctx context.Context, project domain.ProjectID, harness string) (string, error) {
	var id string
	err := s.store.View(ctx, func(r ports.Reader) error {
		if _, err := r.GetProject(ctx, project); err != nil {
			return err
		}
		var err error
		id, err = r.GetProjectProviderConnection(ctx, project, harness)
		return err
	})
	return id, err
}
func (s *Service) authorizeProviderLease(ctx context.Context, lease providergateway.Lease) (providergateway.Upstream, error) {
	var upstream providergateway.Upstream
	var connection domain.ProviderConnection
	var raw []byte
	err := s.store.View(ctx, func(r ports.Reader) error {
		capsule, err := r.GetCapsule(ctx, domain.CapsuleID(lease.Capsule))
		if err != nil {
			return err
		}
		if string(capsule.ProjectID) != lease.Project || capsule.State != domain.CapsuleReady {
			return domain.ErrConflict
		}
		grant, err := r.GetProjectProviderConnection(ctx, capsule.ProjectID, lease.Harness)
		if err != nil {
			return err
		}
		if grant != lease.Connection {
			return domain.ErrConflict
		}
		connection, err = r.GetProviderConnection(ctx, grant)
		if err != nil {
			return err
		}
		if connection.Revoked || connection.Provider != lease.Provider {
			return domain.ErrConflict
		}
		if connection.CredentialKind == providergateway.CredentialOAuth && connection.Provider != providergateway.Anthropic {
			return domain.ErrConflict
		}
		opened, err := s.secretKey.OpenSetup(connectionEnvelopeID(connection), connection.KeyID, connection.Nonce, connection.Ciphertext)
		if err != nil {
			return err
		}
		raw = append([]byte(nil), opened...)
		wipe(opened)
		upstream = providergateway.Upstream{BaseURL: connection.BaseURL, AllowPrivateHTTP: connection.AllowPrivateHTTP, CredentialKind: connection.CredentialKind}
		return nil
	})
	if err != nil {
		wipe(raw)
		return providergateway.Upstream{}, err
	}
	defer wipe(raw)
	key, err := s.claudeAccess(ctx, connection, raw)
	if err != nil {
		return providergateway.Upstream{}, err
	}
	upstream.Key = key
	return upstream, nil
}
func (s *Service) runtimeProviderConnection(ctx context.Context, capsule domain.Capsule, harness string) (*harnesssetup.Connection, error) {
	harness = strings.TrimSuffix(harness, "-structured")
	id, err := s.ProjectProviderConnection(ctx, capsule.ProjectID, harness)
	if err != nil || id == "" {
		return nil, err
	}
	if s.gateway == nil {
		return nil, domain.ErrUnsupported
	}
	var connection domain.ProviderConnection
	err = s.store.View(ctx, func(r ports.Reader) error {
		var err error
		connection, err = r.GetProviderConnection(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if connection.Revoked {
		return nil, domain.ErrConflict
	}
	if !harnesssetup.ConnectionSupported(harness, connection.Provider, connection.DefaultModel) {
		return nil, fmt.Errorf("%w: this connection type cannot be used with %s", domain.ErrInvalid, harness)
	}
	// Take the trust bundle before the lease, so the bundle covers the
	// lease's whole life (ADR 0037).
	var ca string
	if s.gatewayTrust != nil {
		if ca, err = s.gatewayTrust(); err != nil {
			return nil, err
		}
	}
	token, err := s.gateway.Issue(providergateway.Lease{Connection: id, Project: string(capsule.ProjectID), Capsule: string(capsule.ID), Harness: harness, Provider: connection.Provider})
	if err != nil {
		return nil, err
	}
	return &harnesssetup.Connection{Provider: connection.Provider, URL: s.gatewayURL + "/" + connection.Provider, Token: token, Model: connection.DefaultModel, Efforts: connection.ReasoningEfforts, CA: ca}, nil
}
