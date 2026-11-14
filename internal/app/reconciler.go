package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"go.opentelemetry.io/otel"
)

type Reconciler struct {
	store         ports.Store
	provider      ports.CapsuleProvider
	clock         ports.Clock
	ids           ports.IDSource
	queue         chan domain.CapsuleID
	done          chan struct{}
	retryDelay    time.Duration
	maxRetries    int
	snapshotter   ports.WorkspaceSnapshotter
	artifacts     ports.ArtifactStore
	observer      ports.Observer
	providerName  string
	secretKey     *secrets.InstallationKey
	sessionReady  func(context.Context, domain.Capsule) error
	launcherReady func(context.Context, domain.Capsule) error
	onSettled     func(context.Context, domain.Capsule)
	// runtimeStopped fails the Runs a stopped runtime lost, before the
	// reconciler starts that runtime again.
	runtimeStopped func(context.Context, domain.Capsule) error
	restartLimit   int
	restartWindow  time.Duration

	mu          sync.Mutex
	pending     map[domain.CapsuleID]struct{}
	idleStarted bool
	wg          sync.WaitGroup

	startupMu   sync.Mutex
	startupSeen map[domain.CapsuleID]startupMark

	restartMu sync.Mutex
	restarts  map[domain.CapsuleID][]time.Time
}

// ConfigureRuntimeRecovery sets the hook that fails a stopped runtime's lost
// Runs before the reconciler restarts it. Set it before Start.
func (r *Reconciler) ConfigureRuntimeRecovery(
	failRuns func(context.Context, domain.Capsule) error,
) {
	r.runtimeStopped = failRuns
}

func (r *Reconciler) ConfigureSecrets(key *secrets.InstallationKey) {
	r.secretKey = key
}

func (r *Reconciler) ConfigureProjectThreads(
	coordinator func(context.Context, domain.Capsule) error,
) {
	r.sessionReady = coordinator
}

func (r *Reconciler) ConfigureLauncher(
	coordinator func(context.Context, domain.Capsule) error,
) {
	r.launcherReady = coordinator
}

// OnCapsuleSettled is invoked after a Capsule reaches Deleted or fails while
// its desired state is Deleted. Set it before Start.
func (r *Reconciler) OnCapsuleSettled(fn func(context.Context, domain.Capsule)) {
	r.onSettled = fn
}

func (r *Reconciler) ConfigureObserver(observer ports.Observer, providerName string) {
	r.observer = observer
	r.providerName = providerName
}

func (r *Reconciler) ConfigureTemporal(
	snapshotter ports.WorkspaceSnapshotter,
	artifacts ports.ArtifactStore,
) {
	r.snapshotter = snapshotter
	r.artifacts = artifacts
}

func NewReconciler(
	store ports.Store,
	provider ports.CapsuleProvider,
	clock ports.Clock,
	ids ports.IDSource,
) *Reconciler {
	return &Reconciler{
		store:      newScopedStore(store, clock),
		provider:   provider,
		clock:      clock,
		ids:        ids,
		queue:      make(chan domain.CapsuleID, 256),
		done:       make(chan struct{}),
		retryDelay: 25 * time.Millisecond,
		maxRetries: 5,
		pending:    make(map[domain.CapsuleID]struct{}),
		// A runtime that needs a fourth restart within ten minutes is
		// crash looping, not recovering from an engine restart.
		restartLimit:  3,
		restartWindow: 10 * time.Minute,
		restarts:      make(map[domain.CapsuleID][]time.Time),
	}
}

func (r *Reconciler) Enqueue(ctx context.Context, id domain.CapsuleID) error {
	r.mu.Lock()
	if _, exists := r.pending[id]; exists {
		r.mu.Unlock()
		return nil
	}
	r.pending[id] = struct{}{}
	r.mu.Unlock()

	select {
	case r.queue <- id:
		return nil
	case <-ctx.Done():
		r.forget(id)
		if r.observer != nil && len(r.queue) == cap(r.queue) {
			r.observer.EventBackpressure("queue_full")
		}
		return ctx.Err()
	case <-r.done:
		r.forget(id)
		return context.Canceled
	}
}

// Start runs the reconcile worker as the system principal: it acts on one
// Capsule at a time for whoever owns it, and no request reaches it directly.
func (r *Reconciler) Start(ctx context.Context) {
	ctx = principal.WithSystem(ctx)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.done:
				return
			case id := <-r.queue:
				r.forget(id)
				r.process(ctx, id)
			}
		}
	}()
}

func (r *Reconciler) Recover(ctx context.Context) error {
	ctx = principal.WithSystem(ctx)
	var capsules []domain.Capsule
	if err := r.store.View(ctx, func(reader ports.Reader) error {
		var err error
		capsules, err = reader.ListRecoverableCapsules(ctx)
		return err
	}); err != nil {
		return err
	}
	for _, capsule := range capsules {
		if skip, err := r.heldForSpawnApproval(ctx, capsule); err != nil {
			return err
		} else if skip {
			continue
		}
		if err := r.Enqueue(ctx, capsule.ID); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) Close() {
	select {
	case <-r.done:
	default:
		close(r.done)
	}
	r.wg.Wait()
}

func (r *Reconciler) process(ctx context.Context, id domain.CapsuleID) {
	ctx, span := otel.Tracer("github.com/OrlojHQ/meridian").Start(ctx, "capsule.reconcile")
	defer span.End()
	started := time.Now()
	var err error
	defer func() {
		if r.observer != nil {
			r.observer.ObserveReconcile(time.Since(started), operationResult(err))
		}
	}()
	for attempt := 0; attempt < r.maxRetries; attempt++ {
		err = r.reconcile(ctx, id)
		if err == nil || errors.Is(err, domain.ErrNotFound) {
			return
		}
		if errors.Is(err, errProjectSetup) {
			break
		}
		var startup *domain.StartupFailure
		if errors.As(err, &startup) {
			break
		}
		var restart *restartFailure
		if errors.As(err, &restart) {
			break
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if r.observer != nil {
			r.observer.Retry("reconcile")
		}
		timer := time.NewTimer(r.retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.done:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	_ = r.recordFailure(ctx, id, err)
}

func (r *Reconciler) heldForSpawnApproval(ctx context.Context, capsule domain.Capsule) (bool, error) {
	if capsule.LinkID == "" || capsule.ProviderResourceID != "" {
		return false, nil
	}
	var link domain.ThreadLink
	err := r.store.View(ctx, func(reader ports.Reader) error {
		var err error
		link, err = reader.GetThreadLink(ctx, capsule.LinkID)
		return err
	})
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return link.State != domain.LinkActive, nil
}

func (r *Reconciler) reconcile(ctx context.Context, id domain.CapsuleID) error {
	capsule, err := r.getCapsule(ctx, id)
	if err != nil {
		return err
	}
	if skip, err := r.heldForSpawnApproval(ctx, capsule); err != nil {
		return err
	} else if skip {
		return nil
	}
	if r.sessionReady != nil {
		switch capsule.State {
		case domain.CapsuleReady, domain.CapsuleFailed, domain.CapsuleSealed, domain.CapsuleDeleted:
			if err := r.sessionReady(ctx, capsule); err != nil {
				return err
			}
		}
	}
	if r.launcherReady != nil && capsule.State == domain.CapsuleReady {
		if err := r.launcherReady(ctx, capsule); err != nil {
			return err
		}
	}
	if capsule.State.Terminal() {
		if capsule.State == domain.CapsuleSealed && capsule.DesiredState == domain.IntentDeleted {
			return r.reconcileDelete(ctx, capsule)
		}
		return nil
	}
	if capsule.State == domain.CapsuleReady &&
		(capsule.Maintenance == "capture" || capsule.Maintenance == "seal") {
		previous := capsule.ResourceVersion
		capsule.Maintenance = ""
		capsule.UpdatedAt = r.clock.Now().UTC()
		capsule.ResourceVersion++
		return r.saveCapsule(ctx, capsule, previous, "capsule.maintenance_recovered")
	}
	if capsule.DesiredState == domain.IntentDeleted {
		return r.reconcileDelete(ctx, capsule)
	}
	if capsule.State == domain.CapsuleCreating || capsule.State == domain.CapsulePreparing {
		var cancel context.CancelFunc
		ctx, cancel = r.preparationContext(ctx, capsule.ID)
		defer cancel()
	}
	if capsule.ProviderResourceID == "" {
		var project domain.Project
		if err := r.store.View(ctx, func(reader ports.Reader) error {
			var err error
			project, err = reader.GetProject(ctx, capsule.ProjectID)
			return err
		}); err != nil {
			return err
		}
		_, staged := r.provider.(ports.WorkspacePreparer)
		if staged && capsule.OriginMomentID == "" {
			spec, err := r.preparationSpec(ctx, capsule, project)
			if err != nil {
				return err
			}
			project.RepositoryURL, project.ImageReference, project.GitSecretName = spec.RepositoryURL, spec.ImageReference, spec.GitSecretName
			capsule.ImageReference = spec.ImageReference
			project.Setup = nil // Checkout first; setup runs after exact identity and cache lookup.
		}
		createRequest := r.startupRequest(ctx, capsule.ID, ports.CreateCapsuleRequest{
			CapsuleID:      capsule.ID,
			RepositoryURL:  project.RepositoryURL,
			Setup:          append([]string(nil), project.Setup...),
			ImageReference: capsule.WorkspaceImage(project),
		})
		if project.GitSecretName != "" {
			var secret domain.Secret
			if err := r.store.View(ctx, func(reader ports.Reader) error {
				var err error
				secret, err = reader.GetSecret(ctx, project.OwnerUserID, project.GitSecretName)
				return err
			}); err != nil {
				return fmt.Errorf("resolve project Git secret: %w", err)
			}
			if secret.Purpose != domain.SecretGitHTTPS || r.secretKey == nil {
				return fmt.Errorf("%w: project Git secret purpose mismatch", domain.ErrInvalid)
			}
			payload, err := r.secretKey.Open(secret)
			if err != nil {
				return fmt.Errorf("resolve project Git secret: %w", err)
			}
			createRequest.GitCredential = &ports.GitHTTPSCredential{
				Username: payload.Username, Password: payload.Password,
			}
		}
		if capsule.OriginMomentID != "" {
			var origin domain.Moment
			if err := r.store.View(ctx, func(reader ports.Reader) error {
				var err error
				origin, err = reader.GetMoment(ctx, capsule.OriginMomentID)
				return err
			}); err != nil {
				return err
			}
			if origin.ProjectID != capsule.ProjectID {
				return fmt.Errorf("%w: origin Moment project mismatch", domain.ErrInvalid)
			}
			createRequest.RepositoryURL = ""
			createRequest.Setup = nil
			createRequest.ImageReference = origin.ImageDigest
			createRequest.Restore = true
		}
		started := time.Now()
		resource, err := r.provider.Create(ctx, createRequest)
		if createRequest.GitCredential != nil {
			createRequest.GitCredential.Username = ""
			createRequest.GitCredential.Password = ""
			createRequest.GitCredential = nil
		}
		r.observeProvider("create", started, err)
		if err != nil {
			return err
		}
		capsule.ProviderResourceID = resource.ID
		if capsule.State != domain.CapsulePreparing {
			if err := capsule.Transition(domain.CapsulePreparing, r.clock.Now()); err != nil {
				return err
			}
		} else {
			capsule.ResourceVersion++
			capsule.UpdatedAt = r.clock.Now().UTC()
		}
		if err := r.saveCapsule(ctx, capsule, capsule.ResourceVersion-1, "capsule.preparing"); err != nil {
			return err
		}
		return r.Enqueue(ctx, id)
	}

	started := time.Now()
	resource, err := r.provider.Get(ctx, capsule.ProviderResourceID)
	r.observeProvider("get", started, err)
	if err != nil {
		return err
	}
	switch capsule.DesiredState {
	case domain.IntentReady:
		if resource.State == ports.ProviderStopped {
			return r.restartStopped(ctx, capsule, resource)
		}
		if resource.State == ports.ProviderPaused {
			started = time.Now()
			resource, err = r.provider.Resume(ctx, resource.ID)
			r.observeProvider("resume", started, err)
			if err != nil {
				return err
			}
		}
		if resource.State == ports.ProviderPreparing && capsule.State == domain.CapsuleReady {
			// A provider whose control plane restarts runtimes itself
			// (agent-sandbox) reports a Ready Capsule's runtime as not ready
			// while it comes back. The workspace is intact, so the Capsule
			// stays Ready; Runs the restart lost fail when they are checked.
			return nil
		}
		if resource.State != ports.ProviderReady {
			return fmt.Errorf("provider resource %s is %s", resource.ID, resource.State)
		}
		if capsule.OriginMomentID != "" && !capsule.RestoreComplete {
			return r.reconcileRestore(ctx, capsule)
		}
		if capsule.State != domain.CapsuleReady {
			if completed, err := r.completePreparation(ctx, capsule, resource); err != nil {
				return err
			} else if completed {
				return r.Enqueue(ctx, id)
			}
			previous := capsule.ResourceVersion
			if err := capsule.Transition(domain.CapsuleReady, r.clock.Now()); err != nil {
				return err
			}
			return r.saveCapsule(ctx, capsule, previous, "capsule.ready")
		}
	case domain.IntentPaused:
		capabilities, err := r.provider.Capabilities(ctx)
		if err != nil {
			return err
		}
		if !capabilities.Pause {
			return domain.ErrUnsupported
		}
		// A stopped runtime runs nothing and keeps its workspace, which is
		// all Paused promises; resuming starts it again.
		if resource.State != ports.ProviderPaused && resource.State != ports.ProviderStopped {
			started = time.Now()
			resource, err = r.provider.Pause(ctx, resource.ID)
			r.observeProvider("pause", started, err)
			if err != nil {
				return err
			}
		}
		if resource.State != ports.ProviderPaused && resource.State != ports.ProviderStopped {
			return fmt.Errorf("provider resource %s did not pause", resource.ID)
		}
		if capsule.State != domain.CapsulePaused {
			previous := capsule.ResourceVersion
			if err := capsule.Transition(domain.CapsulePaused, r.clock.Now()); err != nil {
				return err
			}
			return r.saveCapsule(ctx, capsule, previous, "capsule.paused")
		}
	}
	return nil
}

// restartFailure ends reconcile retries for a stopped runtime that could not be
// started again or keeps stopping. Its message becomes the Capsule failure.
type restartFailure struct {
	message string
	cause   error
}

func (e *restartFailure) Error() string { return e.message }
func (e *restartFailure) Unwrap() error { return e.cause }

// restartStopped starts a stopped runtime of a Capsule that should be Ready,
// such as a container stopped by an engine restart (ADR 0044). Runs the stop
// lost fail first. Restarts are bounded: a runtime that keeps stopping, or
// that cannot start, fails the Capsule instead of being restarted forever.
func (r *Reconciler) restartStopped(
	ctx context.Context,
	capsule domain.Capsule,
	resource ports.ProviderResource,
) error {
	reason := resource.Reason
	if reason == "" {
		reason = "no exit status"
	}
	if !r.reserveRestart(capsule.ID) {
		return &restartFailure{message: fmt.Sprintf(
			"Capsule runtime keeps stopping (%s); it was restarted %d times within %d minutes",
			reason, r.restartLimit, int(r.restartWindow.Minutes()),
		)}
	}
	if r.runtimeStopped != nil {
		if err := r.runtimeStopped(ctx, capsule); err != nil {
			return err
		}
	}
	started := time.Now()
	restarted, err := r.provider.Resume(ctx, resource.ID)
	r.observeProvider("restart", started, err)
	if err != nil && ctx.Err() != nil {
		return err
	}
	if err != nil {
		return &restartFailure{message: "Capsule runtime could not be started again: " + err.Error(), cause: err}
	}
	if restarted.State != ports.ProviderReady {
		return &restartFailure{message: fmt.Sprintf(
			"Capsule runtime is %s after being started again", restarted.State,
		)}
	}
	previous := capsule.ResourceVersion
	capsule.UpdatedAt = r.clock.Now().UTC()
	capsule.ResourceVersion++
	if err := r.saveCapsule(ctx, capsule, previous, "capsule.runtime_restarted"); err != nil {
		return err
	}
	return r.Enqueue(ctx, capsule.ID)
}

// reserveRestart records one restart of a Capsule's runtime unless the
// runtime already used its restarts within the window. Counts live in memory:
// a daemon restart grants a fresh budget, which stays bounded per run.
func (r *Reconciler) reserveRestart(id domain.CapsuleID) bool {
	r.restartMu.Lock()
	defer r.restartMu.Unlock()
	now := r.clock.Now()
	recent := r.restarts[id][:0]
	for _, at := range r.restarts[id] {
		if now.Sub(at) < r.restartWindow {
			recent = append(recent, at)
		}
	}
	if len(recent) >= r.restartLimit {
		r.restarts[id] = recent
		return false
	}
	r.restarts[id] = append(recent, now)
	return true
}

func (r *Reconciler) forgetRestarts(id domain.CapsuleID) {
	r.restartMu.Lock()
	delete(r.restarts, id)
	r.restartMu.Unlock()
}

func (r *Reconciler) reconcileRestore(ctx context.Context, capsule domain.Capsule) error {
	if r.snapshotter == nil || r.artifacts == nil {
		return domain.ErrUnsupported
	}
	var moment domain.Moment
	if err := r.store.View(ctx, func(reader ports.Reader) error {
		var err error
		moment, err = reader.GetMoment(ctx, capsule.OriginMomentID)
		return err
	}); err != nil {
		return err
	}
	if moment.ProjectID != capsule.ProjectID {
		return fmt.Errorf("%w: restore Moment project mismatch", domain.ErrInvalid)
	}
	archive, size, err := r.artifacts.Open(ctx, moment.ArchiveSHA256)
	if err != nil {
		if r.observer != nil {
			kind := "missing"
			if errors.Is(err, domain.ErrCorrupt) {
				kind = "corrupt"
			}
			r.observer.ArtifactFailure(kind)
		}
		return err
	}
	if size != moment.ArchiveSize {
		_ = archive.Close()
		if r.observer != nil {
			r.observer.ArtifactFailure("corrupt")
		}
		return fmt.Errorf("%w: restore archive size mismatch", domain.ErrCorrupt)
	}
	started := time.Now()
	restoreErr := r.snapshotter.RestoreWorkspace(
		ctx, capsule.ProviderResourceID, moment.ArchiveSHA256, size, archive,
	)
	r.observeProvider("restore", started, restoreErr)
	closeErr := archive.Close()
	if restoreErr != nil {
		return restoreErr
	}
	if closeErr != nil {
		return closeErr
	}
	previous := capsule.ResourceVersion
	capsule.RestoreComplete = true
	capsule.Maintenance = ""
	capsule.UpdatedAt = r.clock.Now().UTC()
	capsule.ResourceVersion++
	if err := r.saveCapsule(ctx, capsule, previous, "capsule.restore_completed"); err != nil {
		return err
	}
	return r.Enqueue(ctx, capsule.ID)
}

func (r *Reconciler) reconcileDelete(ctx context.Context, capsule domain.Capsule) error {
	if capsule.State != domain.CapsuleDeleting {
		previous := capsule.ResourceVersion
		if err := capsule.Transition(domain.CapsuleDeleting, r.clock.Now()); err != nil {
			return err
		}
		if err := r.saveCapsule(ctx, capsule, previous, "capsule.deleting"); err != nil {
			return err
		}
		return r.Enqueue(ctx, capsule.ID)
	}
	// A Create that failed during setup leaves its resources running without
	// a recorded ID, so fall back to the name Create derives from the Capsule.
	resourceID := capsule.ProviderResourceID
	if resourceID == "" {
		resourceID = r.provider.ResourceID(capsule.ID)
	}
	started := time.Now()
	if err := r.provider.Delete(ctx, resourceID); err != nil {
		r.observeProvider("delete", started, err)
		if r.observer != nil {
			r.observer.Cleanup(r.providerName, operationResult(err))
		}
		return err
	}
	r.observeProvider("delete", started, nil)
	if r.observer != nil {
		r.observer.Cleanup(r.providerName, "ok")
	}
	previous := capsule.ResourceVersion
	if err := capsule.Transition(domain.CapsuleDeleted, r.clock.Now()); err != nil {
		return err
	}
	if err := r.saveCapsule(ctx, capsule, previous, "capsule.deleted"); err != nil {
		return err
	}
	r.forgetRestarts(capsule.ID)
	r.settle(ctx, capsule)
	return nil
}

func (r *Reconciler) getCapsule(ctx context.Context, id domain.CapsuleID) (domain.Capsule, error) {
	var capsule domain.Capsule
	err := r.store.View(ctx, func(reader ports.Reader) error {
		var err error
		capsule, err = reader.GetCapsule(ctx, id)
		return err
	})
	return capsule, err
}

func (r *Reconciler) saveCapsule(
	ctx context.Context,
	capsule domain.Capsule,
	expected domain.ResourceVersion,
	eventType string,
) error {
	if err := r.store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.UpdateCapsule(ctx, capsule, expected); err != nil {
			return err
		}
		if capsule.State == domain.CapsuleReady || capsule.State == domain.CapsuleDeleted {
			if err := tx.DeleteStartup(ctx, capsule.ID); err != nil {
				return err
			}
		}
		return tx.AppendEvent(ctx, domain.Event{
			ID:              domain.EventID(r.ids.NewID()),
			AggregateType:   "capsule",
			AggregateID:     string(capsule.ID),
			Type:            eventType,
			Timestamp:       r.clock.Now().UTC(),
			ResourceVersion: capsule.ResourceVersion,
		})
	}); err != nil {
		return err
	}
	if capsule.State == domain.CapsuleReady || capsule.State == domain.CapsuleDeleted {
		r.clearStartupMark(capsule.ID)
	}
	if r.sessionReady != nil {
		if err := r.sessionReady(ctx, capsule); err != nil {
			return err
		}
	}
	if r.launcherReady != nil && capsule.State == domain.CapsuleReady {
		return r.launcherReady(ctx, capsule)
	}
	return nil
}

func (r *Reconciler) recordFailure(ctx context.Context, id domain.CapsuleID, cause error) error {
	capsule, err := r.getCapsule(ctx, id)
	if err != nil || capsule.State.Terminal() || capsule.State == domain.CapsuleFailed {
		return err
	}
	previous := capsule.ResourceVersion
	if err := capsule.Transition(domain.CapsuleFailed, r.clock.Now()); err != nil {
		return err
	}
	var pull *domain.StartupFailure
	if errors.As(cause, &pull) {
		capsule.Failure = pull.Error()
		capsule.FailureCode = pull.Code
	} else {
		capsule.Failure = cause.Error()
		capsule.FailureCode = ""
	}
	r.clearStartupMark(id)
	if err := r.saveCapsule(ctx, capsule, previous, "capsule.failed"); err != nil {
		return err
	}
	if capsule.DesiredState == domain.IntentDeleted {
		r.settle(ctx, capsule)
	}
	return nil
}

func (r *Reconciler) settle(ctx context.Context, capsule domain.Capsule) {
	if r.onSettled != nil {
		r.onSettled(ctx, capsule)
	}
}

func (r *Reconciler) forget(id domain.CapsuleID) {
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
}

func (r *Reconciler) observeProvider(operation string, started time.Time, err error) {
	if r.observer != nil {
		r.observer.ProviderOperation(r.providerName, operation, operationResult(err), time.Since(started))
	}
}

func operationResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, domain.ErrNotFound):
		return "not_found"
	case errors.Is(err, domain.ErrUnsupported):
		return "unsupported"
	default:
		return "error"
	}
}
