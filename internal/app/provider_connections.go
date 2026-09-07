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
	ExpectedResourceVersion domain.ResourceVersion `json:"expectedResourceVersion"`
	Revoked                 bool                   `json:"revoked,omitempty"`
}

func connectionEnvelopeID(item domain.ProviderConnection) string {
	return fmt.Sprintf("connection:%s:%s:%d:%t", item.ID, item.Provider, item.ResourceVersion, item.Revoked)
}

func (s *Service) ConfigureProviderGateway(baseURL string) error {
	if baseURL == "" {
		return nil
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Scheme != "https" {
		return fmt.Errorf("provider gateway URL must be an HTTPS origin reachable from Capsules")
	}
	s.gatewayURL = strings.TrimRight(baseURL, "/") + "/provider-gateway"
	s.gateway = providergateway.New(s.authorizeProviderLease)
	return nil
}
func (s *Service) ProviderGateway() http.Handler { return s.gateway }
func (s *Service) ProviderGatewayEnabled() bool  { return s.gateway != nil }
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
	if input.Provider != "openai" && input.Provider != "anthropic" {
		return domain.ProviderConnection{}, domain.ErrInvalid
	}
	name, err := requireName(input.Name)
	if err != nil {
		return domain.ProviderConnection{}, err
	}
	if !input.Revoked && (len(input.APIKey) < 8 || len(input.APIKey) > 4096 || strings.ContainsAny(input.APIKey, "\r\n\x00")) {
		return domain.ProviderConnection{}, fmt.Errorf("%w: invalid API key", domain.ErrInvalid)
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
func (s *Service) GrantProviderConnection(ctx context.Context, project domain.ProjectID, harness, connection string) error {
	if !harnesssetup.Supported(harness) {
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
			if harness == "claude" && item.Provider != "anthropic" || harness == "codex" && item.Provider != "openai" {
				return domain.ErrInvalid
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
func (s *Service) authorizeProviderLease(ctx context.Context, lease providergateway.Lease) (string, error) {
	var key string
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
		connection, err := r.GetProviderConnection(ctx, grant)
		if err != nil {
			return err
		}
		if connection.Revoked || connection.Provider != lease.Provider {
			return domain.ErrConflict
		}
		raw, err := s.secretKey.OpenSetup(connectionEnvelopeID(connection), connection.KeyID, connection.Nonce, connection.Ciphertext)
		if err != nil {
			return err
		}
		defer wipe(raw)
		key = string(raw)
		return nil
	})
	return key, err
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
	token, err := s.gateway.Issue(providergateway.Lease{Connection: id, Project: string(capsule.ProjectID), Capsule: string(capsule.ID), Harness: harness, Provider: connection.Provider})
	if err != nil {
		return nil, err
	}
	return &harnesssetup.Connection{Provider: connection.Provider, URL: s.gatewayURL + "/" + connection.Provider, Token: token}, nil
}
