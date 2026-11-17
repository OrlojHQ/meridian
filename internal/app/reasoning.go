package app

import (
	"context"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/providergateway"
)

// resolveConnectionReasoning chooses the effort levels a Capsule is told
// about. Saved levels are kept. Otherwise an explicit yes uses the standard
// levels and defaults the harness to medium, an explicit no declares none,
// and unknown leaves the levels empty so the gateway can try medium itself.
func resolveConnectionReasoning(saved []string, detected string, blocked bool) (efforts []string, mode string) {
	if len(saved) > 0 {
		return append([]string{}, saved...), "yes"
	}
	if blocked || detected == "no" {
		return nil, "no"
	}
	if detected == "yes" {
		return append([]string{}, harnesssetup.EffortLevels...), "yes"
	}
	return nil, ""
}

func (s *Service) connectionReasoning(ctx context.Context, connection domain.ProviderConnection, model string) (efforts []string, mode string) {
	if len(connection.ReasoningEfforts) > 0 {
		return resolveConnectionReasoning(connection.ReasoningEfforts, "", false)
	}
	detected := ""
	if s.gateway != nil && s.gateway.ReasoningBlocked(connection.ID, model) {
		return resolveConnectionReasoning(nil, "", true)
	}
	detected = s.importedModelReasoning(ctx, connection.BaseURL, model)
	if detected == "" {
		detected = s.endpointModelReasoning(ctx, connection, model)
	}
	return resolveConnectionReasoning(connection.ReasoningEfforts, detected, false)
}

func (s *Service) importedModelReasoning(ctx context.Context, base, model string) string {
	if base == "" || model == "" {
		return ""
	}
	setups, err := s.ListHarnessSetups(ctx)
	if err != nil {
		return ""
	}
	for _, setup := range setups {
		if setup.Deleted || setup.Harness != "opencode" {
			continue
		}
		contents, err := s.HarnessSetupContents(ctx, setup.ID)
		if err != nil {
			continue
		}
		_, models := harnesssetup.OpenCodeModelCatalog(contents.Bundle.Files, base)
		if item, ok := models[model]; ok && item.Reasoning != "" {
			return item.Reasoning
		}
	}
	return ""
}

type endpointReasoningCache struct {
	at    time.Time
	flags map[string]string
}

func (s *Service) endpointModelReasoning(ctx context.Context, connection domain.ProviderConnection, model string) string {
	if connection.BaseURL == "" || model == "" || s.secretKey == nil {
		return ""
	}
	base, err := providergateway.NormalizeBaseURL(connection.BaseURL, connection.AllowPrivateHTTP)
	if err != nil {
		return ""
	}
	if flags, ok := s.cachedEndpointReasoning(base); ok {
		return flags[model]
	}
	var raw []byte
	err = s.store.View(ctx, func(ports.Reader) error {
		opened, err := s.secretKey.OpenSetup(connectionEnvelopeID(connection), connection.KeyID, connection.Nonce, connection.Ciphertext)
		if err != nil {
			return err
		}
		raw = append([]byte(nil), opened...)
		wipe(opened)
		return nil
	})
	if err != nil {
		return ""
	}
	key := string(raw)
	wipe(raw)
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	flags := providergateway.EndpointReasoning(probeCtx, providergateway.Upstream{
		Key: key, BaseURL: base, AllowPrivateHTTP: connection.AllowPrivateHTTP && s.privateHTTP,
	})
	wipe([]byte(key))
	s.storeEndpointReasoning(base, flags)
	return flags[model]
}

func (s *Service) cachedEndpointReasoning(base string) (map[string]string, bool) {
	s.endpointReasoningMu.Lock()
	defer s.endpointReasoningMu.Unlock()
	item, ok := s.endpointReasoning[base]
	if !ok || time.Since(item.at) > 10*time.Minute {
		return nil, false
	}
	return item.flags, true
}

func (s *Service) storeEndpointReasoning(base string, flags map[string]string) {
	s.endpointReasoningMu.Lock()
	defer s.endpointReasoningMu.Unlock()
	if s.endpointReasoning == nil {
		s.endpointReasoning = map[string]endpointReasoningCache{}
	}
	s.endpointReasoning[base] = endpointReasoningCache{at: time.Now(), flags: flags}
}
