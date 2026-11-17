package app_test

import (
	"errors"
	"testing"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
)

// Without a gateway, and without opting into the in-Capsule push, a push is
// refused before approval reaches the Capsule.
func TestPushWithoutAGatewayFailsClosed(t *testing.T) {
	fixture := newDeliveryFixtureWith(t, true, func(*app.Service) *fakeGitGateway { return nil })
	_, err := fixture.service.CreateDelivery(operatorContext(), deliveryInput(fixture, "no-gateway"))
	if !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("CreateDelivery without a gateway = %v", err)
	}
	fixture.runtime.mu.Lock()
	commits, pushes := fixture.runtime.commits, fixture.runtime.pushes
	fixture.runtime.mu.Unlock()
	if commits != 0 || pushes != 0 {
		t.Fatalf("commits=%d pushes=%d", commits, pushes)
	}
}

// --delivery-push=capsule keeps the ADR 0020 push for an installation with
// no gateway: the credential goes to the Capsule.
func TestInCapsulePushIsAnExplicitFallback(t *testing.T) {
	fixture := newDeliveryFixtureWith(t, true, func(service *app.Service) *fakeGitGateway {
		service.ConfigureDeliveryPushInCapsule(true)
		return nil
	})
	if _, err := fixture.service.CreateDelivery(operatorContext(), deliveryInput(fixture, "fallback")); err != nil {
		t.Fatal(err)
	}
	fixture.runtime.mu.Lock()
	push := fixture.runtime.push
	fixture.runtime.mu.Unlock()
	if push.Gateway != nil || push.GitCredential.Password != "push-token-secret" ||
		push.ExpectedOldRef == nil || *push.ExpectedOldRef != "" {
		t.Fatalf("fallback push = %#v", push)
	}
}

// The Station reads the remote branch before it issues a capability.
func TestGatewayPushChecksTheRemoteBranchOnTheStation(t *testing.T) {
	t.Run("already pushed", func(t *testing.T) {
		fixture := newDeliveryFixture(t, true)
		fixture.gateway.refs["refs/heads/feature/reviewed"] = deliveryCommit
		result, err := fixture.service.CreateDelivery(operatorContext(), deliveryInput(fixture, "retry"))
		if err != nil || result.State != domain.DeliverySucceeded {
			t.Fatalf("Delivery = %#v, %v", result, err)
		}
		fixture.runtime.mu.Lock()
		pushes := fixture.runtime.pushes
		fixture.runtime.mu.Unlock()
		if _, grants, _ := fixture.gateway.lastGrant(); pushes != 0 || grants != 0 {
			t.Fatalf("pushes=%d grants=%d after the branch already matched", pushes, grants)
		}
	})
	t.Run("another commit", func(t *testing.T) {
		fixture := newDeliveryFixture(t, true)
		fixture.gateway.refs["refs/heads/feature/reviewed"] = deliveryHead
		result, _ := fixture.service.CreateDelivery(operatorContext(), deliveryInput(fixture, "taken"))
		if result.State != domain.DeliveryFailed {
			t.Fatalf("Delivery = %#v", result)
		}
		fixture.runtime.mu.Lock()
		pushes := fixture.runtime.pushes
		fixture.runtime.mu.Unlock()
		if _, grants, _ := fixture.gateway.lastGrant(); pushes != 0 || grants != 0 {
			t.Fatalf("pushes=%d grants=%d over an existing branch", pushes, grants)
		}
	})
	t.Run("capability is revoked after a failed push", func(t *testing.T) {
		fixture := newDeliveryFixture(t, true)
		fixture.runtime.pushErr = errors.New("push failed")
		result, _ := fixture.service.CreateDelivery(operatorContext(), deliveryInput(fixture, "fails"))
		if result.State != domain.DeliveryFailed {
			t.Fatalf("Delivery = %#v", result)
		}
		if _, grants, revoked := fixture.gateway.lastGrant(); grants != 1 || revoked != 1 {
			t.Fatalf("grants=%d revoked=%d", grants, revoked)
		}
	})
}
