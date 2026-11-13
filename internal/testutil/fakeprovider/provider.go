// Package fakeprovider is an in-memory Capsule lifecycle double for tests.
// It is not a product provider: it has no CLI flag, starts no workload, and
// is imported only from test files.
package fakeprovider

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func (p *Provider) CaptureWorkspace(context.Context, string) (ports.WorkspaceCapture, error) {
	return ports.WorkspaceCapture{}, domain.ErrUnsupported
}

func (p *Provider) RestoreWorkspace(context.Context, string, string, int64, io.Reader) error {
	return domain.ErrUnsupported
}

type Options struct {
	Delay    time.Duration
	Failures map[string]int
	// CreateHook runs after an injected failure check and before the
	// resource is recorded. Tests use it to publish startup progress.
	CreateHook func(context.Context, ports.CreateCapsuleRequest) error
}

type Provider struct {
	mu         sync.Mutex
	resources  map[string]ports.ProviderResource
	delay      time.Duration
	failures   map[string]int
	createHook func(context.Context, ports.CreateCapsuleRequest) error
}

func New(options Options) *Provider {
	failures := make(map[string]int, len(options.Failures))
	for operation, count := range options.Failures {
		failures[operation] = count
	}
	return &Provider{
		resources:  make(map[string]ports.ProviderResource),
		delay:      options.Delay,
		failures:   failures,
		createHook: options.CreateHook,
	}
}

func (p *Provider) Capabilities(ctx context.Context) (ports.ProviderCapabilities, error) {
	if err := p.before(ctx, "capabilities"); err != nil {
		return ports.ProviderCapabilities{}, err
	}
	return ports.ProviderCapabilities{
		Version: "test/v1",
		Pause:   true,
	}, nil
}

func (p *Provider) StartRun(context.Context, ports.RuntimeRunRequest) (ports.RuntimeRun, error) {
	return ports.RuntimeRun{}, domain.ErrUnsupported
}

func (p *Provider) GetRun(context.Context, string, domain.RunID) (ports.RuntimeRun, error) {
	return ports.RuntimeRun{}, domain.ErrUnsupported
}

func (p *Provider) CancelRun(context.Context, string, domain.RunID) (ports.RuntimeRun, error) {
	return ports.RuntimeRun{}, domain.ErrUnsupported
}

func (p *Provider) RunEvents(context.Context, string, domain.RunID, uint64) (ports.RuntimeEvents, error) {
	return ports.RuntimeEvents{}, domain.ErrUnsupported
}

func (p *Provider) AttachRun(context.Context, string, domain.RunID, uint64) (ports.RuntimeAttachment, error) {
	return nil, domain.ErrUnsupported
}

func (p *Provider) StartStructured(
	context.Context,
	ports.RuntimeStructuredStartRequest,
) (ports.RuntimeRun, error) {
	return ports.RuntimeRun{}, domain.ErrUnsupported
}

func (p *Provider) GetStructured(context.Context, string, domain.RunID) (ports.RuntimeRun, error) {
	return ports.RuntimeRun{}, domain.ErrUnsupported
}

func (p *Provider) SendStructured(context.Context, ports.RuntimeStructuredSendRequest) error {
	return domain.ErrUnsupported
}

func (p *Provider) StructuredEvents(
	context.Context,
	string,
	domain.RunID,
	uint64,
) (ports.RuntimeStructuredEvents, error) {
	return ports.RuntimeStructuredEvents{}, domain.ErrUnsupported
}

func (p *Provider) CancelStructured(
	context.Context,
	string,
	domain.RunID,
) (ports.RuntimeRun, error) {
	return ports.RuntimeRun{}, domain.ErrUnsupported
}

func (p *Provider) HarnessProfiles(
	context.Context,
	string,
) ([]ports.RuntimeHarnessProfile, error) {
	return nil, domain.ErrUnsupported
}

func (p *Provider) GitStatus(context.Context, string) (ports.GitResult, error) {
	return ports.GitResult{}, domain.ErrUnsupported
}

func (p *Provider) GitDiff(context.Context, string) (ports.GitResult, error) {
	return ports.GitResult{}, domain.ErrUnsupported
}

func (p *Provider) GitPatch(context.Context, string, ports.GitPatchRequest) (ports.GitPatchResult, error) {
	return ports.GitPatchResult{}, domain.ErrUnsupported
}

func (p *Provider) Create(
	ctx context.Context,
	request ports.CreateCapsuleRequest,
) (ports.ProviderResource, error) {
	if err := p.before(ctx, "create"); err != nil {
		return ports.ProviderResource{}, err
	}
	if p.createHook != nil {
		if err := p.createHook(ctx, request); err != nil {
			return ports.ProviderResource{}, err
		}
	}
	id := "test-" + string(request.CapsuleID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.resources[id]; ok {
		return existing, nil
	}
	resource := ports.ProviderResource{ID: id, State: ports.ProviderReady}
	p.resources[id] = resource
	return resource, nil
}

func (p *Provider) Get(ctx context.Context, id string) (ports.ProviderResource, error) {
	if err := p.before(ctx, "get"); err != nil {
		return ports.ProviderResource{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.resources[id]; ok {
		return existing, nil
	}
	// IDs are deterministic, so a restarted double can reconstruct the
	// ready resource needed by control-plane recovery.
	if strings.HasPrefix(id, "test-") {
		resource := ports.ProviderResource{ID: id, State: ports.ProviderReady}
		p.resources[id] = resource
		return resource, nil
	}
	return ports.ProviderResource{}, domain.ErrNotFound
}

func (p *Provider) Pause(ctx context.Context, id string) (ports.ProviderResource, error) {
	return p.change(ctx, "pause", id, ports.ProviderPaused)
}

func (p *Provider) Resume(ctx context.Context, id string) (ports.ProviderResource, error) {
	return p.change(ctx, "resume", id, ports.ProviderReady)
}

// Stop simulates a runtime stopped outside Meridian, such as by a container
// engine restart. Resume starts it again.
func (p *Provider) Stop(id, reason string) {
	p.Set(id, ports.ProviderStopped, reason)
}

// Set changes a resource's state as the provider's own control plane would.
func (p *Provider) Set(id string, state ports.ProviderState, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	resource := p.resources[id]
	resource.ID, resource.State, resource.Reason = id, state, reason
	p.resources[id] = resource
}

// Fail makes the next count calls of operation fail.
func (p *Provider) Fail(operation string, count int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures[operation] += count
}

// State reports a resource's current state without injected delays.
func (p *Provider) State(id string) ports.ProviderState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resources[id].State
}

func (p *Provider) Delete(ctx context.Context, id string) error {
	if err := p.before(ctx, "delete"); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.resources[id]; ok && existing.State == ports.ProviderDeleted {
		return nil
	}
	p.resources[id] = ports.ProviderResource{ID: id, State: ports.ProviderDeleted}
	return nil
}

func (p *Provider) change(
	ctx context.Context,
	operation, id string,
	state ports.ProviderState,
) (ports.ProviderResource, error) {
	if err := p.before(ctx, operation); err != nil {
		return ports.ProviderResource{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	resource, ok := p.resources[id]
	if !ok || resource.State == ports.ProviderDeleted {
		return ports.ProviderResource{}, domain.ErrNotFound
	}
	resource.State, resource.Reason = state, ""
	p.resources[id] = resource
	return resource, nil
}

func (p *Provider) before(ctx context.Context, operation string) error {
	if p.delay > 0 {
		timer := time.NewTimer(p.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failures[operation] > 0 {
		p.failures[operation]--
		return fmt.Errorf("injected test provider %s failure", operation)
	}
	return nil
}
