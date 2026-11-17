package app

import (
	"context"
	"fmt"
	"github.com/OrlojHQ/meridian/internal/chatgptauth"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/providergateway"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

type ConnectionInput struct {
	// signIn marks a write that completes a provider sign-in ceremony, for
	// the audit record. Only internal/app sets it.
	signIn                  bool
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

// auditAction is the audit action a connection write records.
func (input ConnectionInput) auditAction() domain.AuditAction {
	switch {
	case input.signIn:
		return domain.AuditConnectionSignIn
	case input.Revoked:
		return domain.AuditConnectionDisconnect
	default:
		return domain.AuditConnectionPut
	}
}

// connectionEnvelopeID is a connection's authenticated envelope context. It
// binds the owner (ADR 0046), so a ciphertext moved to another user's row
// fails to open. The rest is the legacy context below.
func connectionEnvelopeID(item domain.ProviderConnection) string {
	return "owner:" + string(item.OwnerUserID) + ":" + legacyConnectionEnvelopeID(item)
}

// legacyConnectionEnvelopeID is the context connections were sealed under
// before owners were bound. Only the startup re-wrap opens with it.
//
// The custom upstream is bound into the envelope context, so a stored key
// cannot be redirected to another endpoint by editing connection metadata.
func legacyConnectionEnvelopeID(item domain.ProviderConnection) string {
	if item.Provider == providergateway.ChatGPT {
		// Token sets rotate without a resource version change, so they are
		// bound to their credential generation instead (ADR 0038).
		return fmt.Sprintf("connection:%s:%s:generation:%d:%t", item.ID, item.Provider, item.CredentialGeneration, item.Revoked)
	}
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
	if err := requirePrincipal(ctx); err != nil {
		return nil, err
	}
	var items []domain.ProviderConnection
	err := s.store.View(ctx, func(r ports.Reader) error { var err error; items, err = r.ListProviderConnections(ctx); return err })
	return items, err
}
func (s *Service) PutProviderConnection(ctx context.Context, id string, input ConnectionInput) (domain.ProviderConnection, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.ProviderConnection{}, err
	}
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
	if input.Provider == providergateway.ChatGPT {
		return s.putChatGPTConnection(ctx, id, name, input)
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
			owner, err := ownerOf(ctx)
			if err != nil {
				return err
			}
			result = domain.ProviderConnection{OwnerUserID: owner, ID: s.ids.NewID(), Provider: input.Provider, CreatedAt: s.clock.Now().UTC()}
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
			// An update that omits levels keeps a declaration the operator
			// already saved. A new connection, or an explicit list, uses
			// what was sent.
			if id == "" || input.ReasoningEfforts != nil {
				result.ReasoningEfforts = input.ReasoningEfforts
			}
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
		if err := s.audit(ctx, tx, input.auditAction(), "provider_connection", result.ID); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "provider_connection", result.ID, "provider_connection.updated", result.ResourceVersion, nil)
	})
	s.auditFailure(ctx, input.auditAction(), "provider_connection", id, err)
	if err == nil && result.Revoked {
		s.gateway.Revoke(result.ID, "", "")
	}
	return result, err
}

// DeleteProviderConnection removes a revoked connection. Revoke keeps the
// row, including a ChatGPT sign-out whose upstream revocation was not
// confirmed. Remove is the step after that. It also drops project grants
// and a workstation-profile default that names the connection. A connection
// that is still signed in, or that a starting, ready, or paused Capsule
// still uses, is left in place. Another member's connection is not found.
func (s *Service) DeleteProviderConnection(ctx context.Context, id string) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	owner, err := ownerOf(ctx)
	if err != nil {
		return err
	}
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		item, err := tx.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		if !item.Revoked {
			return fmt.Errorf("%w: revoke the connection before removing it", domain.ErrConflict)
		}
		live, err := tx.ProviderConnectionLive(ctx, id)
		if err != nil {
			return err
		}
		if live {
			return fmt.Errorf("%w: a Capsule is still using this connection; stop it before removing the connection", domain.ErrConflict)
		}
		if err := tx.DeleteProviderConnection(ctx, id, owner); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditConnectionDelete, "provider_connection", id); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "provider_connection", id, "provider_connection.deleted", item.ResourceVersion, nil)
	})
	s.auditFailure(ctx, domain.AuditConnectionDelete, "provider_connection", id, err)
	if err == nil && s.gateway != nil {
		s.gateway.Revoke(id, "", "")
	}
	return err
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
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	if !harnesssetup.ConnectionHarness(harness) {
		return domain.ErrInvalid
	}
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetProject(ctx, project)
		if err != nil {
			return err
		}
		if err := projectAcceptsWork(current); err != nil {
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
		if err := s.audit(ctx, tx, domain.AuditProjectConnectionGrant, "project", string(project)); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "project", string(project), "provider_connection.grant_changed", 1, nil)
	})
	s.auditFailure(ctx, domain.AuditProjectConnectionGrant, "project", string(project), err)
	if err == nil && s.gateway != nil {
		s.gateway.Revoke("", string(project), harness)
	}
	return err
}
func (s *Service) ProjectProviderConnection(ctx context.Context, project domain.ProjectID, harness string) (string, error) {
	if err := requirePrincipal(ctx); err != nil {
		return "", err
	}
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

// RevokeOwnerLeases ends every gateway lease issued from owner's provider
// connections. It runs when the owner is disabled (ADR 0046); leases are
// only ever issued from the Capsule owner's own connections.
func (s *Service) RevokeOwnerLeases(ctx context.Context, owner domain.UserID) error {
	if err := requireSystem(ctx); err != nil {
		return err
	}
	if s.gateway == nil {
		return nil
	}
	var connections []domain.ProviderConnection
	if err := s.store.View(ctx, func(r ports.Reader) error {
		var err error
		connections, err = r.ListProviderConnections(ctx)
		return err
	}); err != nil {
		return err
	}
	for _, connection := range connections {
		if connection.OwnerUserID == owner {
			s.gateway.Revoke(connection.ID, "", "")
		}
	}
	return nil
}

// authorizeProviderLease runs for each gateway request. A lease is a Capsule
// credential, so the request acts as that Capsule, never as a user.
func (s *Service) authorizeProviderLease(ctx context.Context, lease providergateway.Lease) (providergateway.Upstream, error) {
	var caller principal.Principal
	var upstream providergateway.Upstream
	var connection domain.ProviderConnection
	var raw []byte
	// The gateway issued this lease and has authenticated its token; finding
	// the Capsule it names is daemon work. Everything after acts as that
	// Capsule, within its owner's rows.
	lookup := principal.WithSystem(ctx)
	err := s.store.View(lookup, func(r ports.Reader) error {
		capsule, err := r.GetCapsule(ctx, domain.CapsuleID(lease.Capsule))
		if err != nil {
			return err
		}
		if string(capsule.ProjectID) != lease.Project || capsule.State != domain.CapsuleReady {
			return domain.ErrConflict
		}
		var ok bool
		if caller, ok = capsulePrincipal(capsule); !ok {
			return domain.ErrNotFound
		}
		// A disabled member's Capsules lose model traffic at once.
		owner, err := r.GetUser(lookup, capsule.OwnerUserID)
		if err != nil || !owner.DisabledAt.IsZero() {
			return domain.ErrNotFound
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
		// The Capsule's owner must still own the connection it was leased.
		if connection.OwnerUserID != caller.UserID() {
			return domain.ErrNotFound
		}
		if connection.Provider == providergateway.ChatGPT {
			// Resolved below: a refresh may need the network and a write.
			return nil
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
	ctx = principal.With(ctx, caller)
	if lease.Provider == providergateway.ChatGPT {
		wipe(raw)
		return s.chatGPTUpstream(ctx, lease.Connection)
	}
	defer wipe(raw)
	key, err := s.claudeAccess(ctx, connection, raw)
	if err != nil {
		return providergateway.Upstream{}, err
	}
	upstream.Key = key
	return upstream, nil
}

// putChatGPTConnection handles the only edits a ChatGPT connection accepts
// through the connection API: rename and revoke. Its token set comes only
// from a completed sign-in, so it is never replaced here.
func (s *Service) putChatGPTConnection(ctx context.Context, id, name string, input ConnectionInput) (domain.ProviderConnection, error) {
	if id == "" {
		return domain.ProviderConnection{}, fmt.Errorf("%w: sign in with ChatGPT to create this connection", domain.ErrInvalid)
	}
	if input.APIKey != "" || input.BaseURL != "" || input.DefaultModel != "" || input.AllowPrivateHTTP || len(input.ReasoningEfforts) != 0 || input.CredentialKind != "" {
		return domain.ProviderConnection{}, fmt.Errorf("%w: a ChatGPT connection takes no key, endpoint, or Claude login type; sign in again to change its account", domain.ErrInvalid)
	}
	var result domain.ProviderConnection
	var tokens *chatgptauth.TokenSet
	unchanged := false
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		result, err = tx.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		if result.ResourceVersion != input.ExpectedResourceVersion || result.Provider != input.Provider {
			return domain.ErrConflict
		}
		if result.Revoked {
			if input.Revoked && name == result.Name {
				unchanged = true
				return nil
			}
			return fmt.Errorf("%w: this ChatGPT connection is revoked; sign in again to create a new one", domain.ErrConflict)
		}
		result.Name = name
		result.ResourceVersion++
		if input.Revoked {
			if set, openErr := s.openChatGPT(result); openErr == nil {
				tokens = &set
			}
			result.Revoked, result.NeedsSignIn = true, false
			result.CredentialGeneration++
			result.KeyID = s.secretKey.ID
			// Keep a valid encrypted empty envelope, never old token material.
			result.Nonce, result.Ciphertext, err = s.secretKey.SealSetup(connectionEnvelopeID(result), nil)
			if err != nil {
				return err
			}
		}
		if err := tx.PutProviderConnection(ctx, result); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, input.auditAction(), "provider_connection", result.ID); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "provider_connection", result.ID, "provider_connection.updated", result.ResourceVersion, nil)
	})
	s.auditFailure(ctx, input.auditAction(), "provider_connection", id, err)
	if err != nil || !input.Revoked || unchanged {
		return result, err
	}
	s.gateway.Revoke(result.ID, "", "")
	if tokens != nil && s.revokeChatGPTUpstream(ctx, *tokens) {
		return result, nil
	}
	// Local tokens are gone either way; record that OpenAI did not confirm.
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetProviderConnection(ctx, id)
		if err != nil {
			return err
		}
		if current.RevocationUnconfirmed {
			result = current
			return nil
		}
		current.RevocationUnconfirmed = true
		current.ResourceVersion++
		if err := tx.PutProviderConnection(ctx, current); err != nil {
			return err
		}
		result = current
		return s.appendEvent(ctx, tx, "provider_connection", current.ID, "provider_connection.updated", current.ResourceVersion, nil)
	})
	return result, err
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
	efforts, reasoning := s.connectionReasoning(ctx, connection, connection.DefaultModel)
	token, err := s.gateway.Issue(providergateway.Lease{Connection: id, Project: string(capsule.ProjectID), Capsule: string(capsule.ID), Harness: harness, Provider: connection.Provider, Reasoning: reasoning})
	if err != nil {
		return nil, err
	}
	runtime := &harnesssetup.Connection{Provider: connection.Provider, URL: s.gatewayURL + "/" + connection.Provider, Token: token, Model: connection.DefaultModel, Efforts: efforts, CA: ca, Name: connection.Name}
	if connection.BaseURL != "" && strings.TrimSuffix(harness, "-structured") == "opencode" {
		if setup, err := s.runtimeHarnessSetup(ctx, capsule.ID, harness); err == nil && setup != nil {
			runtime.Fold = harnesssetup.OpenCodeFoldIDs(setup.Bundle.Files, connection.BaseURL)
		}
	}
	return runtime, nil
}

// EndpointModel is one model a custom endpoint serves. Name is the friendly
// label from an imported setup when one matches the id.
type EndpointModel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Context int    `json:"context,omitempty"`
	Output  int    `json:"output,omitempty"`
}

// EndpointModelList is the model dropdown for a custom endpoint.
type EndpointModelList struct {
	Models    []EndpointModel `json:"models"`
	Suggested string          `json:"suggested,omitempty"`
}

// ListEndpointModelsInput asks for a model list. ConnectionID uses the stored
// endpoint and key. Otherwise the caller supplies them. The key is not returned.
type ListEndpointModelsInput struct {
	ConnectionID     string `json:"connectionId,omitempty"`
	Provider         string `json:"provider,omitempty"`
	BaseURL          string `json:"baseUrl,omitempty"`
	APIKey           string `json:"apiKey,omitempty"`
	AllowPrivateHTTP bool   `json:"allowPrivateHttp,omitempty"`
}

// ListEndpointModels fetches GET /v1/models and overlays names from the
// caller's imported OpenCode setup when the endpoint matches.
func (s *Service) ListEndpointModels(ctx context.Context, input ListEndpointModelsInput) (EndpointModelList, error) {
	if err := requirePrincipal(ctx); err != nil {
		return EndpointModelList{}, err
	}
	base, key, private, stored, err := s.endpointModelSource(ctx, input)
	if err != nil {
		return EndpointModelList{}, err
	}
	defer wipe([]byte(key))
	ids, err := providergateway.ListModels(ctx, providergateway.Upstream{BaseURL: base, Key: key, AllowPrivateHTTP: private})
	if err != nil {
		return EndpointModelList{}, fmt.Errorf("%w: %s", domain.ErrInvalid, err.Error())
	}
	imported, catalog := s.openCodeModelCatalog(ctx, base)
	models := make([]EndpointModel, 0, len(ids)+1)
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] || !harnesssetup.ValidModel(id) {
			return
		}
		seen[id] = true
		item := EndpointModel{ID: id, Name: id}
		if known, ok := catalog[id]; ok {
			if known.Name != "" {
				item.Name = known.Name
			}
			item.Context, item.Output = known.Context, known.Output
		}
		models = append(models, item)
	}
	for _, id := range ids {
		add(id)
	}
	add(stored)
	sort.Slice(models, func(i, j int) bool {
		if models[i].Name == models[j].Name {
			return models[i].ID < models[j].ID
		}
		return models[i].Name < models[j].Name
	})
	suggested := ""
	if stored != "" && seen[stored] {
		suggested = stored
	} else if imported != "" && seen[imported] {
		suggested = imported
	}
	return EndpointModelList{Models: models, Suggested: suggested}, nil
}

func (s *Service) endpointModelSource(ctx context.Context, input ListEndpointModelsInput) (base, key string, private bool, stored string, err error) {
	if input.ConnectionID != "" {
		var connection domain.ProviderConnection
		var raw []byte
		err = s.store.View(ctx, func(r ports.Reader) error {
			connection, err = r.GetProviderConnection(ctx, input.ConnectionID)
			if err != nil {
				return err
			}
			if connection.Revoked || connection.Provider != providergateway.OpenAICompatible {
				return fmt.Errorf("%w: model list is available for an OpenAI-compatible endpoint", domain.ErrInvalid)
			}
			if s.secretKey == nil {
				return domain.ErrUnsupported
			}
			opened, err := s.secretKey.OpenSetup(connectionEnvelopeID(connection), connection.KeyID, connection.Nonce, connection.Ciphertext)
			if err != nil {
				return err
			}
			raw = append([]byte(nil), opened...)
			wipe(opened)
			return nil
		})
		if err != nil {
			wipe(raw)
			return "", "", false, "", err
		}
		base, private, stored = connection.BaseURL, connection.AllowPrivateHTTP, connection.DefaultModel
		key = string(raw)
		wipe(raw)
		if input.APIKey != "" {
			key = input.APIKey
		}
		if input.BaseURL != "" {
			normalized, err := providergateway.NormalizeBaseURL(input.BaseURL, input.AllowPrivateHTTP || private)
			if err != nil {
				return "", "", false, "", fmt.Errorf("%w: %s", domain.ErrInvalid, err.Error())
			}
			base = normalized
			private = input.AllowPrivateHTTP
		}
		return base, key, private, stored, nil
	}
	if input.Provider != providergateway.OpenAICompatible {
		return "", "", false, "", fmt.Errorf("%w: model list is available for an OpenAI-compatible endpoint", domain.ErrInvalid)
	}
	normalized, err := providergateway.NormalizeBaseURL(input.BaseURL, input.AllowPrivateHTTP)
	if err != nil {
		return "", "", false, "", fmt.Errorf("%w: %s", domain.ErrInvalid, err.Error())
	}
	if input.AllowPrivateHTTP && !s.privateHTTP {
		return "", "", false, "", fmt.Errorf("%w: private-network HTTP upstreams are disabled on this installation", domain.ErrInvalid)
	}
	return normalized, input.APIKey, input.AllowPrivateHTTP, "", nil
}

func (s *Service) openCodeModelCatalog(ctx context.Context, base string) (string, map[string]harnesssetup.OpenCodeModel) {
	setups, err := s.ListHarnessSetups(ctx)
	if err != nil {
		return "", nil
	}
	var suggested string
	merged := map[string]harnesssetup.OpenCodeModel{}
	for _, setup := range setups {
		if setup.Deleted || setup.Harness != "opencode" {
			continue
		}
		contents, err := s.HarnessSetupContents(ctx, setup.ID)
		if err != nil {
			continue
		}
		hint, models := harnesssetup.OpenCodeModelCatalog(contents.Bundle.Files, base)
		if suggested == "" && hint != "" {
			suggested = hint
		}
		for id, model := range models {
			if _, ok := merged[id]; !ok {
				merged[id] = model
			}
		}
	}
	return suggested, merged
}
