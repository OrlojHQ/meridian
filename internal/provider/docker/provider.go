// Package docker implements trusted-local-development Capsules using Docker Engine.
package docker

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const (
	protocolPort = "7777/tcp"
	workspace    = "/workspace"

	labelOwned   = "owned"
	labelCapsule = "capsule-id"
	labelRole    = "role"
	labelImage   = "image-digest"
)

type Engine interface {
	ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImagePull(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error)
	VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeCreate(context.Context, client.VolumeCreateOptions) (client.VolumeCreateResult, error)
	VolumeRemove(context.Context, string, client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
	NetworkInspect(context.Context, string, client.NetworkInspectOptions) (client.NetworkInspectResult, error)
	NetworkCreate(context.Context, string, client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	NetworkRemove(context.Context, string, client.NetworkRemoveOptions) (client.NetworkRemoveResult, error)
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerStop(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRename(context.Context, string, client.ContainerRenameOptions) (client.ContainerRenameResult, error)
	ContainerPause(context.Context, string, client.ContainerPauseOptions) (client.ContainerPauseResult, error)
	ContainerUnpause(context.Context, string, client.ContainerUnpauseOptions) (client.ContainerUnpauseResult, error)
	ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}

type Config struct {
	Host             string
	Image            string
	NamePrefix       string
	LabelPrefix      string
	Labels           map[string]string
	StateDir         string
	MemoryBytes      int64
	NanoCPUs         int64
	PIDs             int64
	OperationTimeout time.Duration
	SetupTimeout     time.Duration
}

func (c Config) withDefaults() (Config, error) {
	if c.Image == "" {
		c.Image = domain.LocalCapsuleImage
	}
	if c.NamePrefix == "" {
		c.NamePrefix = "meridian"
	}
	if c.LabelPrefix == "" {
		c.LabelPrefix = "io.orloj.meridian"
	}
	if c.MemoryBytes == 0 {
		c.MemoryBytes = 2 << 30
	}
	if c.NanoCPUs == 0 {
		c.NanoCPUs = 2_000_000_000
	}
	if c.PIDs == 0 {
		c.PIDs = 512
	}
	if c.OperationTimeout == 0 {
		c.OperationTimeout = 30 * time.Second
	}
	if c.SetupTimeout == 0 {
		c.SetupTimeout = 10 * time.Minute
	}
	if c.StateDir == "" {
		return Config{}, fmt.Errorf("%w: Docker provider state directory is required", domain.ErrInvalid)
	}
	if strings.ContainsAny(c.NamePrefix+c.LabelPrefix, "\x00\r\n") ||
		c.MemoryBytes < 64<<20 || c.NanoCPUs < 10_000_000 || c.PIDs < 16 ||
		c.OperationTimeout <= 0 || c.SetupTimeout <= 0 {
		return Config{}, fmt.Errorf("%w: invalid Docker provider configuration", domain.ErrInvalid)
	}
	return c, nil
}

type Provider struct {
	config Config
	engine Engine
	owned  *client.Client
	tokens *tokenStore
	// gatewayHost is the extra hosts entry naming the local provider
	// gateway, or empty.
	gatewayHost string
}

func New(config Config) (*Provider, error) {
	resolved, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	options := []client.Opt{client.FromEnv}
	if resolved.Host != "" {
		options = append(options, client.WithHost(resolved.Host))
	}
	engine, err := client.New(options...)
	if err != nil {
		return nil, fmt.Errorf("create Docker client: %w", err)
	}
	// Fail at startup, not on the first Capsule, when Docker is unreachable.
	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err = engine.Ping(pingCtx, client.PingOptions{})
	cancel()
	if err != nil {
		host := engine.DaemonHost()
		_ = engine.Close()
		return nil, fmt.Errorf(
			"cannot reach Docker at %s (is Docker running? set --docker-host or DOCKER_HOST to override): %w",
			host, err,
		)
	}
	provider, err := NewWithEngine(resolved, engine)
	if err != nil {
		_ = engine.Close()
		return nil, err
	}
	provider.owned = engine
	return provider, nil
}

func NewWithEngine(config Config, engine Engine) (*Provider, error) {
	resolved, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	if engine == nil {
		return nil, fmt.Errorf("%w: Docker engine is required", domain.ErrInvalid)
	}
	tokens, err := newTokenStore(filepath.Join(resolved.StateDir, "tokens"))
	if err != nil {
		return nil, err
	}
	return &Provider{config: resolved, engine: engine, tokens: tokens}, nil
}

func (p *Provider) Close() error {
	if p.owned != nil {
		return p.owned.Close()
	}
	return nil
}

func (p *Provider) Capabilities(context.Context) (ports.ProviderCapabilities, error) {
	return ports.ProviderCapabilities{
		Version: "docker/v2", Pause: true, Attach: true, Run: true, Git: true,
		Snapshot: true, Clone: true, Preview: true, Structured: true,
		Browse: true, Delivery: true, Coordination: true, UpgradeImage: true,
		Isolation: domain.IsolationContainer,
	}, nil
}

func (p *Provider) Create(
	ctx context.Context,
	request ports.CreateCapsuleRequest,
) (ports.ProviderResource, error) {
	if request.CapsuleID == "" {
		return ports.ProviderResource{}, fmt.Errorf("%w: Capsule ID is required", domain.ErrInvalid)
	}
	names := p.resourceNames(request.CapsuleID)
	operationContext, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	imageReference := request.ImageReference
	if imageReference == "" {
		imageReference = p.config.Image
	}
	plan := &ports.StartupPublisher{Report: request.ReportStartup}
	imageID, err := p.resolveImage(ctx, imageReference, plan)
	if err != nil {
		return ports.ProviderResource{}, err
	}
	plan.Progress(domain.PhaseCreatingCapsule, 0, 0)
	token, err := p.tokens.loadOrCreate(request.CapsuleID)
	if err != nil {
		return ports.ProviderResource{}, err
	}
	labels := p.labels(request.CapsuleID, "capsule", imageID)
	if err := p.ensureVolume(operationContext, names.volume, request.CapsuleID, "workspace"); err != nil {
		return ports.ProviderResource{}, err
	}
	if err := p.ensureVolume(operationContext, names.homeVolume, request.CapsuleID, "home"); err != nil {
		return ports.ProviderResource{}, err
	}
	if err := p.ensureNetwork(operationContext, names.network, request.CapsuleID); err != nil {
		return ports.ProviderResource{}, err
	}
	if err := p.ensureContainer(
		operationContext, names, request.CapsuleID, imageID, token, labels,
	); err != nil {
		return ports.ProviderResource{}, err
	}
	plan.Progress(domain.PhaseStartingHarness, 0, 0)
	if err := p.ensureStarted(operationContext, names.container, request.CapsuleID); err != nil {
		return ports.ProviderResource{}, err
	}

	plan.Progress(domain.PhaseConnecting, 0, 0)
	readinessContext, readinessCancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	supervisor, err := p.waitForSupervisor(
		readinessContext, names.container, request.CapsuleID, token,
	)
	readinessCancel()
	if err != nil {
		return ports.ProviderResource{}, err
	}
	setupContext, setupCancel := context.WithTimeout(ctx, p.config.SetupTimeout)
	defer setupCancel()
	repositoryURL, setup := request.RepositoryURL, append([]string(nil), request.Setup...)
	if request.Restore {
		repositoryURL, setup = "", nil
	}
	prepareRequest := capsuleproto.PrepareRequest{
		Force:         request.ForcePreparation,
		RepositoryURL: repositoryURL,
		Destination:   workspace,
		Setup:         setup,
	}
	if request.GitCredential != nil && !request.Restore {
		prepareRequest.GitCredential = &capsuleproto.GitHTTPSCredential{
			Username: request.GitCredential.Username, Password: request.GitCredential.Password,
		}
	}
	_, err = supervisor.Prepare(setupContext, prepareRequest)
	if prepareRequest.GitCredential != nil {
		prepareRequest.GitCredential.Username = ""
		prepareRequest.GitCredential.Password = ""
	}
	if err != nil {
		if repositoryAccess(err) {
			return ports.ProviderResource{}, domain.RepositoryAccessFromURL(request.RepositoryURL)
		}
		status, statusErr := supervisor.Status(context.Background())
		if statusErr == nil && status.Error != "" {
			return ports.ProviderResource{}, fmt.Errorf("Capsule setup failed: %s", domain.RedactFailureText(status.Error))
		}
		return ports.ProviderResource{}, fmt.Errorf("Capsule setup failed: %s", domain.RedactFailureText(err.Error()))
	}
	return ports.ProviderResource{
		ID: names.container, State: ports.ProviderReady, ImageDigest: imageID,
	}, nil
}

func (p *Provider) Get(ctx context.Context, id string) (ports.ProviderResource, error) {
	if !p.validResourceID(id) {
		return ports.ProviderResource{}, domain.ErrNotFound
	}
	operationContext, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	inspection, err := p.engine.ContainerInspect(
		operationContext, id, client.ContainerInspectOptions{},
	)
	if errdefs.IsNotFound(err) {
		return ports.ProviderResource{ID: id, State: ports.ProviderDeleted}, nil
	}
	if err != nil {
		return ports.ProviderResource{}, fmt.Errorf("inspect Capsule container: %w", err)
	}
	capsuleID, err := p.verifyLabels(inspection.Container.Config.Labels, "", "capsule")
	if err != nil {
		return ports.ProviderResource{}, err
	}
	imageID := inspection.Container.Config.Labels[p.label(labelImage)]
	if inspection.Container.State == nil {
		return ports.ProviderResource{}, errors.New("Docker returned no container state")
	}
	if inspection.Container.State.Paused {
		return ports.ProviderResource{ID: id, State: ports.ProviderPaused, ImageDigest: imageID}, nil
	}
	if stoppedContainer(inspection.Container.State) {
		// The container and its workspace volume remain, so this is a
		// recoverable state for the reconciler rather than a failure.
		return ports.ProviderResource{
			ID: id, State: ports.ProviderStopped, ImageDigest: imageID,
			Reason: stopReason(inspection.Container.State),
		}, nil
	}
	if !inspection.Container.State.Running {
		return ports.ProviderResource{}, fmt.Errorf(
			"Capsule container is not running (%s)", inspection.Container.State.Status,
		)
	}
	token, err := p.tokens.load(domain.CapsuleID(capsuleID))
	if err != nil {
		return ports.ProviderResource{}, err
	}
	supervisor, err := p.supervisorClient(inspection.Container, token)
	if err != nil {
		return ports.ProviderResource{}, err
	}
	status, err := supervisor.Status(operationContext)
	if err != nil {
		return ports.ProviderResource{}, fmt.Errorf("query Capsule supervisor: %w", err)
	}
	switch status.Preparation {
	case capsuleproto.StateReady:
		return ports.ProviderResource{ID: id, State: ports.ProviderReady, ImageDigest: imageID}, nil
	case capsuleproto.StatePreparing, capsuleproto.StateUnprepared:
		return ports.ProviderResource{ID: id, State: ports.ProviderPreparing, ImageDigest: imageID}, nil
	default:
		return ports.ProviderResource{}, fmt.Errorf("Capsule preparation failed: %s", status.Error)
	}
}

func (p *Provider) Pause(ctx context.Context, id string) (ports.ProviderResource, error) {
	return p.changePause(ctx, id, true)
}

func (p *Provider) Resume(ctx context.Context, id string) (ports.ProviderResource, error) {
	return p.changePause(ctx, id, false)
}

func (p *Provider) changePause(
	ctx context.Context,
	id string,
	pause bool,
) (ports.ProviderResource, error) {
	operationContext, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	inspection, err := p.engine.ContainerInspect(
		operationContext, id, client.ContainerInspectOptions{},
	)
	if errdefs.IsNotFound(err) {
		return ports.ProviderResource{}, domain.ErrNotFound
	}
	if err != nil {
		return ports.ProviderResource{}, err
	}
	capsuleID, err := p.verifyLabels(inspection.Container.Config.Labels, "", "capsule")
	if err != nil {
		return ports.ProviderResource{}, err
	}
	if inspection.Container.State == nil {
		return ports.ProviderResource{}, errors.New("Docker returned no container state")
	}
	if !pause && stoppedContainer(inspection.Container.State) {
		return p.restart(ctx, id, domain.CapsuleID(capsuleID))
	}
	if pause && !inspection.Container.State.Paused {
		if _, err := p.engine.ContainerPause(
			operationContext, id, client.ContainerPauseOptions{},
		); err != nil {
			return ports.ProviderResource{}, fmt.Errorf("pause Capsule container: %w", err)
		}
	}
	if !pause && inspection.Container.State.Paused {
		if _, err := p.engine.ContainerUnpause(
			operationContext, id, client.ContainerUnpauseOptions{},
		); err != nil {
			return ports.ProviderResource{}, fmt.Errorf("resume Capsule container: %w", err)
		}
	}
	if pause {
		return ports.ProviderResource{ID: id, State: ports.ProviderPaused}, nil
	}
	return p.Get(ctx, id)
}

// restart starts a stopped Capsule container again with its existing
// configuration, image, workspace volume, and supervisor token, then waits for
// the supervisor. It never recreates the container or reruns setup.
func (p *Provider) restart(
	ctx context.Context,
	id string,
	capsuleID domain.CapsuleID,
) (ports.ProviderResource, error) {
	token, err := p.tokens.load(capsuleID)
	if err != nil {
		return ports.ProviderResource{}, err
	}
	operationContext, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	_, err = p.engine.ContainerStart(operationContext, id, client.ContainerStartOptions{})
	cancel()
	if err != nil {
		return ports.ProviderResource{}, fmt.Errorf("restart Capsule container: %w", err)
	}
	readinessContext, readinessCancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	_, err = p.waitForSupervisor(readinessContext, id, capsuleID, token)
	readinessCancel()
	if err != nil {
		return ports.ProviderResource{}, err
	}
	return p.Get(ctx, id)
}

// stoppedContainer reports a Capsule container that exists but is not running
// and can be started again: exited, or created and never started.
func stoppedContainer(state *container.State) bool {
	if state == nil || state.Running || state.Paused || state.Restarting || state.Dead {
		return false
	}
	return state.Status != container.StateRemoving && state.Status != container.StateDead
}

func stopReason(state *container.State) string {
	if state.OOMKilled {
		return fmt.Sprintf("out of memory, exit code %d", state.ExitCode)
	}
	return fmt.Sprintf("exit code %d", state.ExitCode)
}

func (p *Provider) Delete(ctx context.Context, id string) error {
	if !p.validResourceID(id) {
		return domain.ErrNotFound
	}
	operationContext, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	names := resourceNames{
		container:  id,
		network:    id + "-network",
		volume:     id + "-workspace",
		homeVolume: id + "-home",
	}
	capsuleID, err := p.discoverCapsuleID(operationContext, names)
	if err != nil {
		return err
	}
	if capsuleID == "" {
		return nil
	}
	var cleanupErrors []error
	for _, containerName := range []string{names.container, names.container + upgradeSuffix, names.container + previousSuffix} {
		containerInspection, err := p.engine.ContainerInspect(
			operationContext, containerName, client.ContainerInspectOptions{},
		)
		if err == nil {
			_, verifyErr := p.verifyLabels(containerInspection.Container.Config.Labels, capsuleID, "capsule")
			if verifyErr != nil {
				cleanupErrors = append(cleanupErrors, verifyErr)
			} else {
				if _, removeErr := p.engine.ContainerRemove(
					operationContext, containerName,
					client.ContainerRemoveOptions{Force: true},
				); removeErr != nil && !errdefs.IsNotFound(removeErr) {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("remove Capsule container: %w", removeErr))
				}
			}
		} else if !errdefs.IsNotFound(err) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("inspect Capsule container: %w", err))
		}
	}
	if err := p.removeNetwork(operationContext, names.network, capsuleID); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := p.removeVolume(operationContext, names.volume, capsuleID, "workspace"); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := p.removeVolume(operationContext, names.homeVolume, capsuleID, "home"); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if len(cleanupErrors) > 0 {
		return errors.Join(cleanupErrors...)
	}
	if err := p.tokens.remove(capsuleID); err != nil {
		return err
	}
	return nil
}

// Suffixes of the transient containers an image upgrade uses (ADR 0064). The
// replacement boots as <name>-upgrade; the old container waits as
// <name>-previous until the replacement holds the Capsule's name.
const (
	upgradeSuffix  = "-upgrade"
	previousSuffix = "-previous"
)

// UpgradeImage replaces an idle Capsule container with a new official pack
// image while keeping its workspace volume, persistent home volume, and
// network, skipping preparation entirely (ADR 0064). The replacement is created
// and must pass readiness before the old container gives up its name, so every
// failure up to the swap rolls back to the untouched old container. A Paused
// Capsule's replacement is stopped again after its readiness check.
func (p *Provider) UpgradeImage(
	ctx context.Context,
	request ports.UpgradeCapsuleRequest,
) (ports.ProviderResource, error) {
	if !p.validResourceID(request.ResourceID) {
		return ports.ProviderResource{}, domain.ErrNotFound
	}
	if strings.TrimSpace(request.ImageReference) == "" || strings.ContainsAny(request.ImageReference, "\x00\r\n") {
		return ports.ProviderResource{}, fmt.Errorf("%w: image reference is invalid", domain.ErrInvalid)
	}
	opCtx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	inspection, err := p.engine.ContainerInspect(opCtx, request.ResourceID, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return ports.ProviderResource{}, domain.ErrNotFound
	}
	if err != nil {
		return ports.ProviderResource{}, fmt.Errorf("inspect Capsule container: %w", err)
	}
	ownerCapsuleID, err := p.verifyLabels(inspection.Container.Config.Labels, request.CapsuleID, "capsule")
	if err != nil {
		return ports.ProviderResource{}, err
	}
	capsuleID := domain.CapsuleID(ownerCapsuleID)
	wasRunning := inspection.Container.State != nil && inspection.Container.State.Running && !inspection.Container.State.Paused
	wasPaused := inspection.Container.State != nil && inspection.Container.State.Paused

	// Pull and verify the target image before touching the existing container.
	plan := &ports.StartupPublisher{Report: request.ReportStartup}
	imageID, err := p.resolveImage(ctx, request.ImageReference, plan)
	if err != nil {
		return ports.ProviderResource{}, err
	}

	names := resourceNames{
		container:  request.ResourceID,
		network:    request.ResourceID + "-network",
		volume:     request.ResourceID + "-workspace",
		homeVolume: request.ResourceID + "-home",
	}
	// Ensure the persistent home volume exists (legacy Capsules created before
	// ADR 0064 gain one on their first upgrade).
	if err := p.ensureVolume(opCtx, names.homeVolume, capsuleID, "home"); err != nil {
		return ports.ProviderResource{}, err
	}
	token, err := p.tokens.loadOrCreate(capsuleID)
	if err != nil {
		return ports.ProviderResource{}, err
	}
	upgradeName := names.container + upgradeSuffix
	previousName := names.container + previousSuffix
	// The old container still holds the Capsule's name, so leftovers of an
	// earlier attempt are not the Capsule's runtime.
	for _, stale := range []string{upgradeName, previousName} {
		if err := p.removeOwnedContainer(opCtx, stale, capsuleID); err != nil {
			return ports.ProviderResource{}, fmt.Errorf("remove stale upgrade container: %w", err)
		}
	}

	plan.Progress(domain.PhaseCreatingCapsule, 0, 0)
	upgradeNames := resourceNames{
		container:  upgradeName,
		network:    names.network,
		volume:     names.volume,
		homeVolume: names.homeVolume,
	}
	labels := p.labels(capsuleID, "capsule", imageID)
	options := p.ContainerCreateOptions(upgradeNames, imageID, token, labels)
	if _, err := p.engine.ContainerCreate(opCtx, options); err != nil {
		return ports.ProviderResource{}, fmt.Errorf("create upgraded Capsule container: %w", err)
	}

	// Stop the old container before starting the replacement so two capsuled
	// instances never write to the workspace or home volumes concurrently.
	if wasPaused {
		if _, err := p.engine.ContainerUnpause(opCtx, names.container, client.ContainerUnpauseOptions{}); err != nil && !errdefs.IsNotFound(err) {
			_, _ = p.engine.ContainerRemove(context.WithoutCancel(ctx), upgradeName, client.ContainerRemoveOptions{Force: true})
			return ports.ProviderResource{}, fmt.Errorf("unpause Capsule container before upgrade: %w", err)
		}
	}
	if wasRunning || wasPaused {
		if err := p.stopContainer(opCtx, names.container); err != nil {
			_, _ = p.engine.ContainerRemove(context.WithoutCancel(ctx), upgradeName, client.ContainerRemoveOptions{Force: true})
			return ports.ProviderResource{}, fmt.Errorf("stop Capsule container before upgrade: %w", err)
		}
	}

	// rollback removes the replacement and starts the old container again
	// when it was running. Its own failure joins the upgrade error so the
	// caller can see that the old runtime is not back.
	rollback := func(upgradeErr error) (ports.ProviderResource, error) {
		rbCtx, rbCancel := context.WithTimeout(context.WithoutCancel(ctx), p.config.OperationTimeout)
		defer rbCancel()
		if _, err := p.engine.ContainerRemove(rbCtx, upgradeName, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return ports.ProviderResource{}, errors.Join(upgradeErr, fmt.Errorf("remove upgraded Capsule container: %w", err))
		}
		if wasRunning {
			if _, err := p.restart(rbCtx, names.container, capsuleID); err != nil {
				return ports.ProviderResource{}, errors.Join(upgradeErr, fmt.Errorf("restore previous Capsule container: %w", err))
			}
		}
		return ports.ProviderResource{}, upgradeErr
	}

	plan.Progress(domain.PhaseStartingHarness, 0, 0)
	if err := p.ensureStarted(opCtx, upgradeName, capsuleID); err != nil {
		return rollback(err)
	}
	plan.Progress(domain.PhaseConnecting, 0, 0)
	readinessCtx, readinessCancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	_, err = p.waitForSupervisor(readinessCtx, upgradeName, capsuleID, token)
	readinessCancel()
	if err != nil {
		return rollback(err)
	}

	// Swap names without a moment where no container holds one: the old
	// container moves aside, the replacement takes its name, and only then is
	// the old container removed. RecoverUpgrade settles an interrupted swap.
	swapCtx, swapCancel := context.WithTimeout(context.WithoutCancel(ctx), p.config.OperationTimeout)
	defer swapCancel()
	if _, err := p.engine.ContainerRename(swapCtx, names.container, client.ContainerRenameOptions{NewName: previousName}); err != nil {
		return rollback(fmt.Errorf("rename previous Capsule container: %w", err))
	}
	if _, err := p.engine.ContainerRename(swapCtx, upgradeName, client.ContainerRenameOptions{NewName: names.container}); err != nil {
		renameErr := fmt.Errorf("rename upgraded Capsule container: %w", err)
		if _, restoreErr := p.engine.ContainerRename(swapCtx, previousName, client.ContainerRenameOptions{NewName: names.container}); restoreErr != nil {
			return ports.ProviderResource{}, errors.Join(renameErr, fmt.Errorf("restore previous Capsule container name: %w", restoreErr))
		}
		return rollback(renameErr)
	}
	// The replacement owns the name now; a leftover previous container is
	// removed by the next upgrade, RecoverUpgrade, or Delete.
	_, _ = p.engine.ContainerRemove(swapCtx, previousName, client.ContainerRemoveOptions{Force: true})
	if request.Paused {
		// A stopped runtime runs nothing and keeps its workspace, which is all
		// Paused promises; resuming starts it again (ADR 0044).
		if err := p.stopContainer(swapCtx, names.container); err != nil {
			return ports.ProviderResource{}, fmt.Errorf("stop upgraded Capsule container: %w", err)
		}
		return ports.ProviderResource{ID: names.container, State: ports.ProviderStopped, ImageDigest: imageID}, nil
	}
	return ports.ProviderResource{ID: names.container, State: ports.ProviderReady, ImageDigest: imageID}, nil
}

// RecoverUpgrade settles the containers an UpgradeImage interrupted by a
// daemon restart left behind, so exactly one container holds the Capsule's
// name and no other runs on its volumes (ADR 0064). It reports whether that
// container runs request.ImageReference.
func (p *Provider) RecoverUpgrade(
	ctx context.Context,
	request ports.UpgradeCapsuleRequest,
) (ports.ProviderResource, bool, error) {
	if !p.validResourceID(request.ResourceID) || request.CapsuleID == "" {
		return ports.ProviderResource{}, false, domain.ErrNotFound
	}
	opCtx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	name := request.ResourceID
	upgradeName, previousName := name+upgradeSuffix, name+previousSuffix
	present := map[string]bool{}
	for _, candidate := range []string{name, upgradeName, previousName} {
		ok, err := p.ownedContainerExists(opCtx, candidate, request.CapsuleID)
		if err != nil {
			return ports.ProviderResource{}, false, err
		}
		present[candidate] = ok
	}
	rename := func(from string) error {
		_, err := p.engine.ContainerRename(opCtx, from, client.ContainerRenameOptions{NewName: name})
		if err != nil {
			return fmt.Errorf("restore Capsule container name: %w", err)
		}
		return nil
	}
	switch {
	case present[name]:
		// The swap never started or already finished: anything else is
		// not the Capsule's runtime.
	case present[upgradeName]:
		// Interrupted between the two renames. The replacement passed
		// readiness before the first rename, so it takes the name.
		if err := rename(upgradeName); err != nil {
			return ports.ProviderResource{}, false, err
		}
	case present[previousName]:
		if err := rename(previousName); err != nil {
			return ports.ProviderResource{}, false, err
		}
	default:
		return ports.ProviderResource{ID: name, State: ports.ProviderDeleted}, false, nil
	}
	for _, leftover := range []string{upgradeName, previousName} {
		if err := p.removeOwnedContainer(opCtx, leftover, request.CapsuleID); err != nil {
			return ports.ProviderResource{}, false, fmt.Errorf("remove leftover upgrade container: %w", err)
		}
	}
	inspection, err := p.engine.ContainerInspect(opCtx, name, client.ContainerInspectOptions{})
	if err != nil {
		return ports.ProviderResource{}, false, fmt.Errorf("inspect Capsule container: %w", err)
	}
	imageID := inspection.Container.Config.Labels[p.label(labelImage)]
	resource := ports.ProviderResource{ID: name, ImageDigest: imageID}
	if request.ImageReference == "" {
		return resource, false, nil
	}
	target, err := p.inspectImage(opCtx, request.ImageReference)
	if errdefs.IsNotFound(err) {
		return resource, false, nil
	}
	if err != nil {
		return ports.ProviderResource{}, false, fmt.Errorf("inspect Capsule image: %w", err)
	}
	return resource, imageID != "" && target.ID == imageID, nil
}

func (p *Provider) stopContainer(ctx context.Context, name string) error {
	stopTimeout := 10
	if _, err := p.engine.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &stopTimeout}); err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	return nil
}

func (p *Provider) ownedContainerExists(ctx context.Context, name string, capsuleID domain.CapsuleID) (bool, error) {
	inspection, err := p.engine.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect Capsule container: %w", err)
	}
	if _, err := p.verifyLabels(inspection.Container.Config.Labels, capsuleID, "capsule"); err != nil {
		return false, err
	}
	return true, nil
}

// removeOwnedContainer force-removes a container of this Capsule, refusing one
// that carries another owner's labels.
func (p *Provider) removeOwnedContainer(ctx context.Context, name string, capsuleID domain.CapsuleID) error {
	exists, err := p.ownedContainerExists(ctx, name, capsuleID)
	if err != nil || !exists {
		return err
	}
	if _, err := p.engine.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	return nil
}

// ResourceID returns the container name Create uses for a Capsule.
func (p *Provider) ResourceID(capsuleID domain.CapsuleID) string {
	return p.resourceNames(capsuleID).container
}

func (p *Provider) discoverCapsuleID(
	ctx context.Context,
	names resourceNames,
) (domain.CapsuleID, error) {
	var found domain.CapsuleID
	check := func(labels map[string]string, role string) error {
		value, err := p.verifyLabels(labels, "", role)
		if err != nil {
			return err
		}
		if found != "" && found != domain.CapsuleID(value) {
			return fmt.Errorf("%w: Docker resource ownership labels disagree", domain.ErrConflict)
		}
		found = domain.CapsuleID(value)
		return nil
	}
	containerInspection, err := p.engine.ContainerInspect(
		ctx, names.container, client.ContainerInspectOptions{},
	)
	if err == nil {
		if err := check(containerInspection.Container.Config.Labels, "capsule"); err != nil {
			return "", err
		}
	} else if !errdefs.IsNotFound(err) {
		return "", fmt.Errorf("inspect Capsule container: %w", err)
	}
	networkInspection, err := p.engine.NetworkInspect(ctx, names.network, client.NetworkInspectOptions{})
	if err == nil {
		if err := check(networkInspection.Network.Labels, "network"); err != nil {
			return "", err
		}
	} else if !errdefs.IsNotFound(err) {
		return "", fmt.Errorf("inspect Capsule network: %w", err)
	}
	volumeInspection, err := p.engine.VolumeInspect(ctx, names.volume, client.VolumeInspectOptions{})
	if err == nil {
		if err := check(volumeInspection.Volume.Labels, "workspace"); err != nil {
			return "", err
		}
	} else if !errdefs.IsNotFound(err) {
		return "", fmt.Errorf("inspect Capsule volume: %w", err)
	}
	if names.homeVolume != "" {
		homeInspection, err := p.engine.VolumeInspect(ctx, names.homeVolume, client.VolumeInspectOptions{})
		if err == nil {
			if err := check(homeInspection.Volume.Labels, "home"); err != nil {
				return "", err
			}
		} else if !errdefs.IsNotFound(err) {
			return "", fmt.Errorf("inspect Capsule home volume: %w", err)
		}
	}
	return found, nil
}

type resourceNames struct {
	container  string
	network    string
	volume     string
	homeVolume string
}

func (p *Provider) resourceNames(capsuleID domain.CapsuleID) resourceNames {
	hash := sha256.Sum256([]byte(capsuleID))
	suffix := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:10]))
	base := p.config.NamePrefix + "-" + suffix
	return resourceNames{
		container:  base,
		network:    base + "-network",
		volume:     base + "-workspace",
		homeVolume: base + "-home",
	}
}

func (p *Provider) validResourceID(id string) bool {
	return strings.HasPrefix(id, p.config.NamePrefix+"-") &&
		!strings.ContainsAny(id, "/\\\x00\r\n") &&
		!strings.HasSuffix(id, "-network") && !strings.HasSuffix(id, "-workspace") &&
		!strings.HasSuffix(id, "-home") && !strings.HasSuffix(id, upgradeSuffix) &&
		!strings.HasSuffix(id, previousSuffix)
}

func (p *Provider) label(name string) string {
	return p.config.LabelPrefix + "." + name
}

func (p *Provider) labels(capsuleID domain.CapsuleID, role, imageID string) map[string]string {
	labels := make(map[string]string, len(p.config.Labels)+4)
	for key, value := range p.config.Labels {
		labels[key] = value
	}
	labels[p.label(labelOwned)] = "true"
	labels[p.label(labelCapsule)] = string(capsuleID)
	labels[p.label(labelRole)] = role
	if imageID != "" {
		labels[p.label(labelImage)] = imageID
	}
	return labels
}

func (p *Provider) verifyLabels(labels map[string]string, capsuleID domain.CapsuleID, role string) (string, error) {
	if labels[p.label(labelOwned)] != "true" || labels[p.label(labelRole)] != role {
		return "", fmt.Errorf("%w: Docker resource is not owned by Meridian", domain.ErrConflict)
	}
	actual := labels[p.label(labelCapsule)]
	if actual == "" || capsuleID != "" && actual != string(capsuleID) {
		return "", fmt.Errorf("%w: Docker resource ownership does not match Capsule", domain.ErrConflict)
	}
	return actual, nil
}

func (p *Provider) resolveImage(
	ctx context.Context,
	reference string,
	plan *ports.StartupPublisher,
) (string, error) {
	if strings.TrimSpace(reference) == "" || strings.ContainsAny(reference, "\x00\r\n") {
		return "", fmt.Errorf("%w: image reference is invalid", domain.ErrInvalid)
	}
	inspection, err := p.inspectImage(ctx, reference)
	if err != nil && errdefs.IsNotFound(err) && domain.RegistryQualifiedImage(reference) {
		plan.SetPhases(domain.ImagePullPhases())
		plan.Progress(domain.PhasePullingImage, 0, 0)
		if err := p.pullImage(ctx, reference, plan); err != nil {
			return "", err
		}
		inspection, err = p.inspectImage(ctx, reference)
	} else if err == nil && plan != nil && !plan.HasPhases() {
		plan.SetPhases(domain.LocalImagePhases())
	}
	if err != nil {
		var pull *domain.StartupFailure
		if errors.As(err, &pull) {
			return "", pull
		}
		return "", fmt.Errorf("resolve Capsule image %q: %w", reference, err)
	}
	if !strings.HasPrefix(inspection.ID, "sha256:") || len(inspection.ID) != len("sha256:")+64 {
		return "", fmt.Errorf("Docker did not resolve image %q to an immutable ID", reference)
	}
	return inspection.ID, nil
}

func (p *Provider) inspectImage(ctx context.Context, reference string) (client.ImageInspectResult, error) {
	inspectCtx, cancel := context.WithTimeout(ctx, p.config.OperationTimeout)
	defer cancel()
	return p.engine.ImageInspect(inspectCtx, reference)
}

// ImageAvailable reports whether reference is already present in the local
// Docker image store (ADR 0064).
func (p *Provider) ImageAvailable(ctx context.Context, reference string) (bool, error) {
	if strings.TrimSpace(reference) == "" || strings.ContainsAny(reference, "\x00\r\n") {
		return false, fmt.Errorf("%w: image reference is invalid", domain.ErrInvalid)
	}
	inspection, err := p.inspectImage(ctx, reference)
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect Capsule image %q: %w", reference, err)
	}
	return strings.HasPrefix(inspection.ID, "sha256:") && len(inspection.ID) == len("sha256:")+64, nil
}

// PrepullImage downloads a missing registry-qualified official pack image into
// the local Docker store without starting a container (ADR 0064).
func (p *Provider) PrepullImage(ctx context.Context, reference string) error {
	if strings.TrimSpace(reference) == "" || strings.ContainsAny(reference, "\x00\r\n") {
		return fmt.Errorf("%w: image reference is invalid", domain.ErrInvalid)
	}
	available, err := p.ImageAvailable(ctx, reference)
	if err != nil {
		return err
	}
	if available {
		return nil
	}
	if !domain.RegistryQualifiedImage(reference) {
		return nil
	}
	if err := p.pullImage(ctx, reference, nil); err != nil {
		return err
	}
	_, err = p.resolveImage(ctx, reference, nil)
	return err
}

func (p *Provider) pullImage(
	ctx context.Context,
	reference string,
	plan *ports.StartupPublisher,
) error {
	pullCtx, cancel := context.WithTimeout(ctx, p.config.SetupTimeout)
	defer cancel()
	response, err := p.engine.ImagePull(pullCtx, reference, client.ImagePullOptions{})
	if err != nil {
		return classifyDockerPull(err)
	}
	defer response.Close()
	return readPullProgress(pullCtx, response, plan)
}

// readPullProgress consumes Docker's pull stream. Only numeric byte counts and
// the pulling_image phase leave this function. Registry text is classified
// into a fixed error and then discarded.
func readPullProgress(
	ctx context.Context,
	response client.ImagePullResponse,
	plan *ports.StartupPublisher,
) error {
	reader := bufio.NewReaderSize(response, 32<<10)
	var aggregator pullAggregator
	var current, total int64
	var emittedCurrent, emittedTotal int64
	var lastEmit time.Time
	emitted := false
	read := 0
	lines := 0
	flush := func(force bool) {
		if !force && !shouldEmitPull(pullNow(), lastEmit, emittedCurrent, emittedTotal, current, total, emitted) {
			return
		}
		if emitted && current == emittedCurrent && total == emittedTotal {
			return
		}
		plan.Progress(domain.PhasePullingImage, current, total)
		lastEmit = pullNow()
		emittedCurrent, emittedTotal = current, total
		emitted = true
	}
	for {
		if err := ctx.Err(); err != nil {
			return classifyDockerPull(err)
		}
		line, err := readPullLine(reader, &read)
		if len(line) > 0 {
			lines++
			if lines > maxPullLines {
				return &domain.StartupFailure{}
			}
			var message pullMessage
			if jsonErr := json.Unmarshal(line, &message); jsonErr != nil {
				return &domain.StartupFailure{}
			}
			if pullErr := classifyPullMessage(message); pullErr != nil {
				return pullErr
			}
			if nextCurrent, nextTotal, ok := aggregator.observe(message); ok {
				current, total = nextCurrent, nextTotal
				flush(false)
			}
		}
		if errors.Is(err, errPullOversized) || errors.Is(err, io.ErrUnexpectedEOF) {
			return &domain.StartupFailure{}
		}
		if errors.Is(err, io.EOF) {
			flush(true)
			return nil
		}
		if err != nil {
			return classifyDockerPull(err)
		}
	}
}

func (p *Provider) ensureVolume(
	ctx context.Context,
	name string,
	capsuleID domain.CapsuleID,
	role string,
) error {
	inspection, err := p.engine.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err == nil {
		_, err = p.verifyLabels(inspection.Volume.Labels, capsuleID, role)
		return err
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect Capsule volume: %w", err)
	}
	_, err = p.engine.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name: name, Labels: p.labels(capsuleID, role, ""),
	})
	if err != nil {
		return fmt.Errorf("create Capsule volume: %w", err)
	}
	return nil
}

func (p *Provider) ensureNetwork(
	ctx context.Context,
	name string,
	capsuleID domain.CapsuleID,
) error {
	inspection, err := p.engine.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err == nil {
		_, err = p.verifyLabels(inspection.Network.Labels, capsuleID, "network")
		return err
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect Capsule network: %w", err)
	}
	_, err = p.engine.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge", Labels: p.labels(capsuleID, "network", ""),
	})
	if err != nil {
		return fmt.Errorf("create Capsule network: %w", err)
	}
	return nil
}

func (p *Provider) ensureContainer(
	ctx context.Context,
	names resourceNames,
	capsuleID domain.CapsuleID,
	imageID, token string,
	labels map[string]string,
) error {
	inspection, err := p.engine.ContainerInspect(
		ctx, names.container, client.ContainerInspectOptions{},
	)
	if err == nil {
		_, err = p.verifyLabels(inspection.Container.Config.Labels, capsuleID, "capsule")
		if err != nil {
			return err
		}
		if inspection.Container.Config.Labels[p.label(labelImage)] != imageID {
			return fmt.Errorf("%w: adopted Capsule image identity differs", domain.ErrConflict)
		}
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("inspect Capsule container: %w", err)
	}
	options := p.ContainerCreateOptions(names, imageID, token, labels)
	if _, err := p.engine.ContainerCreate(ctx, options); err != nil {
		return fmt.Errorf("create Capsule container: %w", err)
	}
	return nil
}

// ContainerCreateOptions returns the complete hardened create request for tests and audits.
func (p *Provider) ContainerCreateOptions(
	names resourceNames,
	imageID, token string,
	labels map[string]string,
) client.ContainerCreateOptions {
	port := network.MustParsePort(protocolPort)
	init := true
	pids := p.config.PIDs
	var extraHosts []string
	if p.gatewayHost != "" {
		extraHosts = []string{p.gatewayHost}
	}
	mounts := []mount.Mount{{
		Type: mount.TypeVolume, Source: names.volume, Target: workspace,
	}}
	if names.homeVolume != "" {
		mounts = append(mounts, mount.Mount{
			Type: mount.TypeVolume, Source: names.homeVolume, Target: "/home/capsule",
		})
	}
	return client.ContainerCreateOptions{
		Name: names.container,
		Config: &container.Config{
			User:  "10001:10001",
			Image: imageID,
			Env:   []string{"MERIDIAN_CAPSULE_TOKEN=" + token, "HOME=/home/capsule"},
			Cmd: []string{
				"serve", "--listen=:7777", "--workspace=" + workspace,
				"--session-dir=/run/meridian-session",
				"--setup-timeout=" + p.config.SetupTimeout.String(),
			},
			WorkingDir:   workspace,
			ExposedPorts: network.PortSet{port: {}},
			Labels:       labels,
			Healthcheck: &container.HealthConfig{
				Test:     []string{"CMD", "/usr/local/bin/capsuled", "healthcheck"},
				Interval: 5 * time.Second, Timeout: 2 * time.Second, Retries: 5,
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    container.NetworkMode(names.network),
			ExtraHosts:     extraHosts,
			PortBindings:   network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1")}}},
			ReadonlyRootfs: true,
			Privileged:     false,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			Init:           &init,
			LogConfig: container.LogConfig{
				Type: "json-file", Config: map[string]string{"max-size": "1m", "max-file": "2"},
			},
			Tmpfs: map[string]string{
				"/tmp":                  "rw,noexec,nosuid,nodev,size=64m,mode=1777",
				"/run/meridian-session": "rw,exec,nosuid,nodev,size=16m,uid=10001,gid=10001,mode=0700",
			},
			Resources: container.Resources{
				Memory: p.config.MemoryBytes, MemorySwap: p.config.MemoryBytes,
				NanoCPUs: p.config.NanoCPUs, PidsLimit: &pids,
			},
			Mounts: mounts,
		},
		NetworkingConfig: &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{names.network: {}},
		},
	}
}

func (p *Provider) ensureStarted(
	ctx context.Context,
	name string,
	capsuleID domain.CapsuleID,
) error {
	inspection, err := p.engine.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	if _, err := p.verifyLabels(inspection.Container.Config.Labels, capsuleID, "capsule"); err != nil {
		return err
	}
	if inspection.Container.State != nil && inspection.Container.State.Running {
		return nil
	}
	if _, err := p.engine.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start Capsule container: %w", err)
	}
	return nil
}

func (p *Provider) waitForSupervisor(
	ctx context.Context,
	containerName string,
	capsuleID domain.CapsuleID,
	token string,
) (*capsuleproto.Client, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastError error
	for {
		inspection, err := p.engine.ContainerInspect(
			ctx, containerName, client.ContainerInspectOptions{},
		)
		if err == nil {
			if _, verifyErr := p.verifyLabels(
				inspection.Container.Config.Labels, capsuleID, "capsule",
			); verifyErr != nil {
				return nil, verifyErr
			}
			// Callers start the container first, so a stopped one exited
			// again before its supervisor answered.
			if stoppedContainer(inspection.Container.State) {
				return nil, fmt.Errorf(
					"Capsule container stopped before its supervisor was ready (%s)",
					stopReason(inspection.Container.State),
				)
			}
			supervisor, clientErr := p.supervisorClient(inspection.Container, token)
			if clientErr == nil {
				if _, healthErr := supervisor.Health(ctx); healthErr == nil {
					return supervisor, nil
				} else {
					lastError = healthErr
				}
			} else {
				lastError = clientErr
			}
		} else {
			lastError = err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for Capsule supervisor: %w: %v", ctx.Err(), lastError)
		case <-ticker.C:
		}
	}
}

func (p *Provider) supervisorClient(
	inspection container.InspectResponse,
	token string,
) (*capsuleproto.Client, error) {
	if inspection.NetworkSettings == nil {
		return nil, errors.New("Capsule container has no network settings")
	}
	bindings := inspection.NetworkSettings.Ports[network.MustParsePort(protocolPort)]
	if len(bindings) != 1 || bindings[0].HostIP.String() != "127.0.0.1" ||
		bindings[0].HostPort == "" {
		return nil, errors.New("Capsule supervisor is not bound to one loopback port")
	}
	if _, err := strconv.ParseUint(bindings[0].HostPort, 10, 16); err != nil {
		return nil, errors.New("Capsule supervisor host port is invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: 2 * time.Second}).DialContext
	return capsuleproto.NewClient(
		"http://127.0.0.1:"+bindings[0].HostPort,
		token,
		&http.Client{Transport: transport},
	)
}

func (p *Provider) removeNetwork(
	ctx context.Context,
	name string,
	capsuleID domain.CapsuleID,
) error {
	inspection, err := p.engine.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Capsule network: %w", err)
	}
	if _, err := p.verifyLabels(inspection.Network.Labels, capsuleID, "network"); err != nil {
		return err
	}
	if _, err := p.engine.NetworkRemove(ctx, name, client.NetworkRemoveOptions{}); err != nil &&
		!errdefs.IsNotFound(err) {
		return fmt.Errorf("remove Capsule network: %w", err)
	}
	return nil
}

func (p *Provider) removeVolume(
	ctx context.Context,
	name string,
	capsuleID domain.CapsuleID,
	role string,
) error {
	if name == "" {
		return nil
	}
	inspection, err := p.engine.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Capsule volume: %w", err)
	}
	if _, err := p.verifyLabels(inspection.Volume.Labels, capsuleID, role); err != nil {
		return err
	}
	if _, err := p.engine.VolumeRemove(
		ctx, name, client.VolumeRemoveOptions{Force: false},
	); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("remove Capsule volume: %w", err)
	}
	return nil
}

type tokenStore struct {
	directory string
}

func newTokenStore(directory string) (*tokenStore, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create provider token directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("provider token path must be a real directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, err
	}
	return &tokenStore{directory: directory}, nil
}

func (s *tokenStore) path(capsuleID domain.CapsuleID) string {
	hash := sha256.Sum256([]byte(capsuleID))
	return filepath.Join(s.directory, base64.RawURLEncoding.EncodeToString(hash[:])+".token")
}

func (s *tokenStore) loadOrCreate(capsuleID domain.CapsuleID) (string, error) {
	token, err := s.load(capsuleID)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate Capsule protocol token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(random)
	temp, err := os.CreateTemp(s.directory, ".token-*")
	if err != nil {
		return "", err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return "", err
	}
	if _, err := temp.WriteString(token); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tempName, s.path(capsuleID)); err != nil {
		return "", err
	}
	return token, nil
}

func (s *tokenStore) load(capsuleID domain.CapsuleID) (string, error) {
	path := s.path(capsuleID)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return "", errors.New("Capsule protocol token file is unsafe")
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(value) < 32 || len(value) > 128 {
		return "", errors.New("Capsule protocol token file is invalid")
	}
	return string(value), nil
}

func (s *tokenStore) remove(capsuleID domain.CapsuleID) error {
	if err := os.Remove(s.path(capsuleID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove Capsule protocol token: %w", err)
	}
	return nil
}
