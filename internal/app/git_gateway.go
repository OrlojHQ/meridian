package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/gitgateway"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// GitGateway forwards an approved push from a Capsule to the source host and
// keeps the credential on the Station (ADR 0054).
type GitGateway interface {
	http.Handler
	Issue(gitgateway.Grant) (string, func(), error)
	RemoteRef(context.Context, string, gitgateway.Credential, string) (string, error)
}

// ConfigureGitGateway enables pushes through gateway. origin is the HTTPS
// origin Capsules reach the gateway listener at. trust returns the CA a
// Capsule must trust for the local listener (ADR 0037); it is nil when the
// origin is publicly trusted, and the API listener then serves the routes.
func (s *Service) ConfigureGitGateway(gateway GitGateway, origin string, trust func() (string, error)) error {
	if gateway == nil {
		return fmt.Errorf("git gateway is required")
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("git gateway origin must be an HTTPS origin reachable from Capsules")
	}
	s.gitGateway = gateway
	s.gitGatewayURL = strings.TrimRight(origin, "/") + gitgateway.RepositoryPath
	s.gitGatewayTrust = trust
	return nil
}

// ConfigureDeliveryPushInCapsule keeps the ADR 0020 push, which sends the
// source-host credential into the Capsule, for an installation with no
// gateway. It is off unless --delivery-push=capsule.
func (s *Service) ConfigureDeliveryPushInCapsule(enabled bool) {
	s.deliveryPushInCapsule = enabled
}

// GitGatewayHandler serves /git-gateway/ routes. It is nil without a gateway.
func (s *Service) GitGatewayHandler() http.Handler {
	if s.gitGateway == nil {
		return nil
	}
	return s.gitGateway
}

// GitGatewayOnAPIListener reports whether the API listener serves the
// gateway routes. The local gateway listener serves them otherwise.
func (s *Service) GitGatewayOnAPIListener() bool {
	return s.gitGateway != nil && s.gitGatewayTrust == nil
}

// CloseGitGateway revokes every outstanding push capability.
func (s *Service) CloseGitGateway() {
	if closer, ok := s.gitGateway.(interface{ Close() }); ok {
		closer.Close()
	}
}

func (s *Service) requireDeliveryPush() error {
	if s.gitGateway == nil && !s.deliveryPushInCapsule {
		return fmt.Errorf("%w: pushing needs the Station Git gateway; set --provider-gateway-url, "+
			"or --delivery-push=capsule to push from inside the Capsule", domain.ErrUnsupported)
	}
	return nil
}

// pushDelivery pushes sourceCommit to destination. Through the gateway the
// Station reads the remote branch itself, then the Capsule pushes with a
// one-time capability. The credential stays here.
func (s *Service) pushDelivery(
	ctx context.Context, project domain.Project, capsule domain.Capsule,
	sourceCommit, destination string, credential ports.GitHTTPSCredential,
) (ports.DeliveryPushResult, error) {
	if s.gitGateway == nil {
		if !s.deliveryPushInCapsule {
			return ports.DeliveryPushResult{}, s.requireDeliveryPush()
		}
		expectedOldRef := ""
		return s.delivery.PushDelivery(ctx, ports.DeliveryPushRequest{
			ResourceID: capsule.ProviderResourceID, SourceCommit: sourceCommit,
			DestinationRef: destination, ExpectedOldRef: &expectedOldRef,
			GitCredential: credential,
		})
	}
	upstream := strings.TrimRight(project.RepositoryURL, "/")
	upstreamCredential := gitgateway.Credential{Username: credential.Username, Password: credential.Password}
	defer func() { upstreamCredential = gitgateway.Credential{} }()
	current, err := s.gitGateway.RemoteRef(ctx, upstream, upstreamCredential, destination)
	if err != nil {
		return ports.DeliveryPushResult{}, err
	}
	if current == sourceCommit {
		// A retry after an ambiguous push that did land.
		return ports.DeliveryPushResult{Commit: sourceCommit, DestinationRef: destination}, nil
	}
	if current != "" {
		return ports.DeliveryPushResult{}, fmt.Errorf("%w: the remote branch already exists at another commit", domain.ErrConflict)
	}
	trust := ""
	if s.gitGatewayTrust != nil {
		if trust, err = s.gitGatewayTrust(); err != nil {
			return ports.DeliveryPushResult{}, err
		}
	}
	capability, revoke, err := s.gitGateway.Issue(gitgateway.Grant{
		Upstream: upstream, Credential: upstreamCredential,
		SourceCommit: sourceCommit, DestinationRef: destination,
	})
	if err != nil {
		return ports.DeliveryPushResult{}, err
	}
	defer revoke()
	return s.delivery.PushDelivery(ctx, ports.DeliveryPushRequest{
		ResourceID: capsule.ProviderResourceID, SourceCommit: sourceCommit, DestinationRef: destination,
		Gateway: &ports.GitGatewayPush{
			URL: s.gitGatewayURL, CA: trust, Username: gitgateway.Username, Capability: capability,
		},
	})
}
