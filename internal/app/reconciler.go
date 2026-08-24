package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"go.opentelemetry.io/otel"
)

type Reconciler struct {
	store        ports.Store
	provider     ports.CapsuleProvider
	clock        ports.Clock
	ids          ports.IDSource
	queue        chan domain.CapsuleID
	done         chan struct{}
	retryDelay   time.Duration
	maxRetries   int
	snapshotter  ports.WorkspaceSnapshotter
	artifacts    ports.ArtifactStore
	observer     ports.Observer
	providerName string
	secretKey    *secrets.InstallationKey
	sessionReady func(context.Context, domain.Capsule) error

	mu          sync.Mutex
	pending     map[domain.CapsuleID]struct{}
	idleStarted bool
	wg          sync.WaitGroup
}

func (r *Reconciler) ConfigureSecrets(key *secrets.InstallationKey) {
	r.secretKey = key
}

func (r *Reconciler) ConfigureProjectThreads(
	coordinator func(context.Context, domain.Capsule) error,
) {
	r.sessionReady = coordinator
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
		store:      store,
		provider:   provider,
		clock:      clock,
		ids:        ids,
		queue:      make(chan domain.CapsuleID, 256),
		done:       make(chan struct{}),
		retryDelay: 25 * time.Millisecond,
		maxRetries: 5,
		pending:    make(map[domain.CapsuleID]struct{}),
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

func (r *Reconciler) Start(ctx context.Context) {
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
	var capsules []domain.Capsule
	if err := r.store.View(ctx, func(reader ports.Reader) error {
		var err error
		capsules, err = reader.ListRecoverableCapsules(ctx)
		return err
	}); err != nil {
		return err
	}
	for _, capsule := range capsules {
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

func (r *Reconciler) reconcile(ctx context.Context, id domain.CapsuleID) error {
	capsule, err := r.getCapsule(ctx, id)
	if err != nil {
		return err
	}
	if r.sessionReady != nil {
		switch capsule.State {
		case domain.CapsuleReady, domain.CapsuleFailed, domain.CapsuleSealed, domain.CapsuleDeleted:
			if err := r.sessionReady(ctx, capsule); err != nil {
				return err
			}
		}
	}
	if capsule.State.Terminal() {
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
	if capsule.ProviderResourceID == "" {
		var project domain.Project
		if err := r.store.View(ctx, func(reader ports.Reader) error {
			var err error
			project, err = reader.GetProject(ctx, capsule.ProjectID)
			return err
		}); err != nil {
			return err
		}
		if selected, err := r.prepareSetupCacheRestore(ctx, capsule, project); err != nil {
			return err
		} else if selected {
			return r.Enqueue(ctx, id)
		}
		createRequest := ports.CreateCapsuleRequest{
			CapsuleID:      capsule.ID,
			RepositoryURL:  project.RepositoryURL,
			Setup:          append([]string(nil), project.Setup...),
			ImageReference: project.ImageReference,
		}
		if project.GitSecretName != "" {
			var secret domain.Secret
			if err := r.store.View(ctx, func(reader ports.Reader) error {
				var err error
				secret, err = reader.GetSecret(ctx, project.GitSecretName)
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
		if resource.State == ports.ProviderPaused {
			started = time.Now()
			resource, err = r.provider.Resume(ctx, resource.ID)
			r.observeProvider("resume", started, err)
			if err != nil {
				return err
			}
		}
		if resource.State != ports.ProviderReady {
			return fmt.Errorf("provider resource %s is %s", resource.ID, resource.State)
		}
		if capsule.OriginMomentID != "" && !capsule.RestoreComplete {
			return r.reconcileRestore(ctx, capsule)
		}
		if capsule.State != domain.CapsuleReady {
			if capsule.State == domain.CapsulePreparing && capsule.OriginMomentID == "" {
				// Setup caching is an optimization. Publication or persistence
				// failure must not prevent an otherwise healthy Capsule from
				// becoming Ready.
				_ = r.captureSetupMomentCache(ctx, capsule)
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
		if resource.State != ports.ProviderPaused {
			started = time.Now()
			resource, err = r.provider.Pause(ctx, resource.ID)
			r.observeProvider("pause", started, err)
			if err != nil {
				return err
			}
		}
		if resource.State != ports.ProviderPaused {
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
	if capsule.ProviderResourceID != "" {
		started := time.Now()
		if err := r.provider.Delete(ctx, capsule.ProviderResourceID); err != nil {
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
	}
	previous := capsule.ResourceVersion
	if err := capsule.Transition(domain.CapsuleDeleted, r.clock.Now()); err != nil {
		return err
	}
	return r.saveCapsule(ctx, capsule, previous, "capsule.deleted")
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
	if r.sessionReady != nil {
		return r.sessionReady(ctx, capsule)
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
	capsule.Failure = cause.Error()
	return r.saveCapsule(ctx, capsule, previous, "capsule.failed")
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
