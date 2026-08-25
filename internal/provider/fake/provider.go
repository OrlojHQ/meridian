// Package fake provides a deterministic lifecycle provider for local development.
// It does not provide workload execution, security isolation, or persistence.
package fake

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
}

type Provider struct {
	mu        sync.Mutex
	resources map[string]ports.ProviderResource
	delay     time.Duration
	failures  map[string]int
}

func New(options Options) *Provider {
	failures := make(map[string]int, len(options.Failures))
	for operation, count := range options.Failures {
		failures[operation] = count
	}
	return &Provider{
		resources: make(map[string]ports.ProviderResource),
		delay:     options.Delay,
		failures:  failures,
	}
}

func (p *Provider) Capabilities(ctx context.Context) (ports.ProviderCapabilities, error) {
	if err := p.before(ctx, "capabilities"); err != nil {
		return ports.ProviderCapabilities{}, err
	}
	return ports.ProviderCapabilities{
		Version: "fake/v1",
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

func (p *Provider) Create(
	ctx context.Context,
	request ports.CreateCapsuleRequest,
) (ports.ProviderResource, error) {
	if err := p.before(ctx, "create"); err != nil {
		return ports.ProviderResource{}, err
	}
	id := "fake-" + string(request.CapsuleID)
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
	// IDs are deterministic, so a restarted fake provider can reconstruct the
	// ready resource needed by control-plane recovery.
	if strings.HasPrefix(id, "fake-") {
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
	resource.State = state
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
		return fmt.Errorf("injected fake provider %s failure", operation)
	}
	return nil
}
