package app

import (
	"context"
	"errors"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

// ownerScope is the set of rows a call may see (ADR 0046).
//
//   - A user sees only its own rows.
//   - A Capsule sees its owner's rows; the Coordination Grant and gateway
//     lease checks narrow that further where they apply.
//   - The system sees every row, one object at a time.
//   - The break-glass admin sees no object at all.
type ownerScope struct {
	owner domain.UserID
	all   bool
}

func scopeOf(ctx context.Context) (ownerScope, error) {
	caller, err := principal.Require(ctx)
	if err != nil {
		return ownerScope{}, err
	}
	switch caller.Kind() {
	case principal.KindSystem:
		return ownerScope{all: true}, nil
	case principal.KindUser, principal.KindCapsule:
		return ownerScope{owner: caller.UserID()}, nil
	default:
		return ownerScope{}, nil
	}
}

// allows reports whether the scope may see a row that owner owns. A row
// never has the empty owner, so the break-glass scope sees nothing.
func (scope ownerScope) allows(owner domain.UserID) bool {
	return scope.all || (owner != "" && owner == scope.owner)
}

func (scope ownerScope) check(owner domain.UserID) error {
	if !scope.allows(owner) {
		return domain.ErrNotFound
	}
	return nil
}

// keyOwner is the owner a scope lists roots and keys idempotency under; the
// system's is empty, which lists every owner's roots and keys the system's
// own replays.
func (scope ownerScope) keyOwner() domain.UserID {
	if scope.all {
		return ""
	}
	return scope.owner
}

// scopedStore authorizes every store call by the caller's principal. It is
// the one place internal/app turns a principal into the rows a call can
// reach. A row outside the scope reads as not found, the same content-free
// answer as a row that does not exist, so ids do not become a directory.
//
// A child row is checked through its parent: the store guarantees a child's
// owner equals its parent's. The reader and transaction below implement
// every port method explicitly, so a new store method does not compile
// until it is given a check.
//
// An id-addressed read or write of a row another owner owns is a denied
// cross-owner access. It is answered as not found and, once the call
// returns, written to the audit log (ADR 0046). Rows a list leaves out are
// not denials: nobody named them.
type scopedStore struct {
	inner ports.Store
	clock ports.Clock
}

func newScopedStore(inner ports.Store, clock ports.Clock) ports.Store {
	if inner == nil {
		return nil
	}
	if _, ok := inner.(scopedStore); ok {
		return inner
	}
	return scopedStore{inner: inner, clock: clock}
}

// idempotencyOwner keys a call's replays: one user's key never replays or
// suppresses another's mutation (ADR 0046).
func idempotencyOwner(ctx context.Context) (domain.UserID, error) {
	scope, err := scopeOf(ctx)
	if err != nil {
		return "", err
	}
	return scope.keyOwner(), nil
}

func (s scopedStore) View(ctx context.Context, fn func(ports.Reader) error) error {
	scope, err := scopeOf(ctx)
	if err != nil {
		return err
	}
	var scoped *scopedReader
	err = s.inner.View(ctx, func(reader ports.Reader) error {
		if scope.all {
			return fn(reader)
		}
		scoped = newScopedReader(scope, reader)
		return fn(scoped)
	})
	s.recordDenials(ctx, scoped)
	return err
}

func (s scopedStore) Transact(ctx context.Context, fn func(ports.Transaction) error) error {
	scope, err := scopeOf(ctx)
	if err != nil {
		return err
	}
	var scoped *scopedReader
	err = s.inner.Transact(ctx, func(tx ports.Transaction) error {
		if scope.all {
			return fn(tx)
		}
		scoped = newScopedReader(scope, tx)
		return fn(&scopedTx{scopedReader: scoped, tx: tx})
	})
	s.recordDenials(ctx, scoped)
	return err
}

// recordDenials writes the call's denied cross-owner accesses in their own
// transaction, so a refused call that rolled back still leaves its record.
// Recording is best effort: a failure here never changes the call's answer.
func (s scopedStore) recordDenials(ctx context.Context, scoped *scopedReader) {
	if scoped == nil || len(scoped.denials) == 0 || s.clock == nil {
		return
	}
	_ = s.inner.Transact(context.WithoutCancel(ctx), func(tx ports.Transaction) error {
		for _, denied := range scoped.denials {
			record, ok := auditRecordFor(ctx, s.clock.Now(), domain.AuditCrossOwnerAccess,
				denied.objectType, denied.id, domain.AuditDenied)
			if !ok {
				continue
			}
			if err := tx.AppendAuditRecord(ctx, record); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s scopedStore) Close() error { return s.inner.Close() }

type scopedReader struct {
	scope    ownerScope
	inner    ports.Reader
	projects map[domain.ProjectID]ownerLookup
	capsules map[domain.CapsuleID]ownerLookup
	// quiet is set while a list filters its rows; denials holds the
	// id-addressed refusals to audit.
	quiet   int
	denials []denial
}

type ownerLookup struct {
	owner domain.UserID
	err   error
}

type denial struct {
	objectType string
	id         string
}

func newScopedReader(scope ownerScope, inner ports.Reader) *scopedReader {
	return &scopedReader{
		scope: scope, inner: inner,
		projects: map[domain.ProjectID]ownerLookup{}, capsules: map[domain.CapsuleID]ownerLookup{},
	}
}

// owned checks that the scope may see a row owner owns. A refusal outside
// a list is a denied cross-owner access and is recorded once per row.
func (r *scopedReader) owned(objectType, id string, owner domain.UserID) error {
	if err := r.scope.check(owner); err != nil {
		if r.quiet == 0 {
			seen := false
			for _, item := range r.denials {
				seen = seen || (item.objectType == objectType && item.id == id)
			}
			if !seen && len(r.denials) < 64 {
				r.denials = append(r.denials, denial{objectType: objectType, id: id})
			}
		}
		return err
	}
	return nil
}

// project and capsule cache the row's owner for the call: owners never
// change, and a row deleted during the call fails at its own write.
func (r *scopedReader) project(ctx context.Context, id domain.ProjectID) error {
	lookup, ok := r.projects[id]
	if !ok {
		project, err := r.inner.GetProject(ctx, id)
		lookup = ownerLookup{owner: project.OwnerUserID, err: err}
		r.projects[id] = lookup
	}
	if lookup.err != nil {
		return lookup.err
	}
	return r.owned("project", string(id), lookup.owner)
}

func (r *scopedReader) capsule(ctx context.Context, id domain.CapsuleID) error {
	lookup, ok := r.capsules[id]
	if !ok {
		capsule, err := r.inner.GetCapsule(ctx, id)
		lookup = ownerLookup{owner: capsule.OwnerUserID, err: err}
		r.capsules[id] = lookup
	}
	if lookup.err != nil {
		return lookup.err
	}
	return r.owned("capsule", string(id), lookup.owner)
}

func (r *scopedReader) thread(ctx context.Context, id domain.ThreadID) error {
	thread, err := r.inner.GetThread(ctx, id)
	if err != nil {
		return err
	}
	return r.capsule(ctx, thread.CapsuleID)
}

func (r *scopedReader) run(ctx context.Context, id domain.RunID) error {
	run, err := r.inner.GetRun(ctx, id)
	if err != nil {
		return err
	}
	return r.capsule(ctx, run.CapsuleID)
}

func (r *scopedReader) timeline(ctx context.Context, id domain.TimelineID) error {
	timeline, err := r.inner.GetTimeline(ctx, id)
	if err != nil {
		return err
	}
	return r.project(ctx, timeline.ProjectID)
}

func (r *scopedReader) grant(ctx context.Context, id domain.CoordinationGrantID) error {
	grant, err := r.inner.GetCoordinationGrant(ctx, id)
	if err != nil {
		return err
	}
	return r.project(ctx, grant.ProjectID)
}

func (r *scopedReader) setup(ctx context.Context, id string) error {
	setups, err := r.inner.ListHarnessSetups(ctx)
	if err != nil {
		return err
	}
	for _, setup := range setups {
		if setup.ID == id {
			return r.owned("harness_setup", id, setup.OwnerUserID)
		}
	}
	return domain.ErrNotFound
}

func (r *scopedReader) connection(ctx context.Context, id string) error {
	connection, err := r.inner.GetProviderConnection(ctx, id)
	if err != nil {
		return err
	}
	return r.owned("provider_connection", id, connection.OwnerUserID)
}

// keyOwner refuses an owner-keyed lookup for anyone but the scope's owner.
func (r *scopedReader) keyOwner(owner domain.UserID) error {
	if owner != r.scope.keyOwner() || owner == "" {
		return domain.ErrNotFound
	}
	return nil
}

// filter keeps the items whose parent check passes. A missing parent drops
// the item, as a row of another owner would be. Dropped rows are not
// denials.
func filter[T any](r *scopedReader, items []T, allowed func(T) error) ([]T, error) {
	r.quiet++
	defer func() { r.quiet-- }()
	kept := items[:0]
	for _, item := range items {
		switch err := allowed(item); {
		case err == nil:
			kept = append(kept, item)
		case !errors.Is(err, domain.ErrNotFound):
			return nil, err
		}
	}
	return kept, nil
}

var (
	_ ports.Reader      = (*scopedReader)(nil)
	_ ports.Transaction = (*scopedTx)(nil)
)

func (r *scopedReader) GetPreparationPolicy(ctx context.Context, id domain.ProjectID) (domain.PreparationPolicy, error) {
	if err := r.project(ctx, id); err != nil {
		return domain.PreparationPolicy{}, err
	}
	return r.inner.GetPreparationPolicy(ctx, id)
}

func (r *scopedReader) GetProjectThreadIntentByCapsule(ctx context.Context, id domain.CapsuleID) (domain.ProjectThreadIntent, error) {
	if err := r.capsule(ctx, id); err != nil {
		return domain.ProjectThreadIntent{}, err
	}
	return r.inner.GetProjectThreadIntentByCapsule(ctx, id)
}

func (r *scopedReader) GetPreparation(ctx context.Context, id domain.CapsuleID) (domain.Preparation, error) {
	if err := r.capsule(ctx, id); err != nil {
		return domain.Preparation{}, err
	}
	return r.inner.GetPreparation(ctx, id)
}

func (r *scopedReader) GetStartup(ctx context.Context, id domain.CapsuleID) (domain.StartupProgress, error) {
	if err := r.capsule(ctx, id); err != nil {
		return domain.StartupProgress{}, err
	}
	return r.inner.GetStartup(ctx, id)
}

func (r *scopedReader) ProjectHarnessSetups(ctx context.Context, id domain.ProjectID) (map[string]string, error) {
	if err := r.project(ctx, id); err != nil {
		return nil, err
	}
	return r.inner.ProjectHarnessSetups(ctx, id)
}

func (r *scopedReader) ListProviderConnections(ctx context.Context) ([]domain.ProviderConnection, error) {
	items, err := r.inner.ListProviderConnections(ctx)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.ProviderConnection) error { return r.scope.check(item.OwnerUserID) })
}

func (r *scopedReader) GetProviderConnection(ctx context.Context, id string) (domain.ProviderConnection, error) {
	item, err := r.inner.GetProviderConnection(ctx, id)
	if err != nil {
		return domain.ProviderConnection{}, err
	}
	if err := r.owned("provider_connection", id, item.OwnerUserID); err != nil {
		return domain.ProviderConnection{}, err
	}
	return item, nil
}

func (r *scopedReader) GetProjectProviderConnection(ctx context.Context, id domain.ProjectID, harness string) (string, error) {
	if err := r.project(ctx, id); err != nil {
		return "", err
	}
	return r.inner.GetProjectProviderConnection(ctx, id, harness)
}

func (r *scopedReader) ListHarnessSetups(ctx context.Context) ([]domain.HarnessSetup, error) {
	items, err := r.inner.ListHarnessSetups(ctx)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.HarnessSetup) error { return r.scope.check(item.OwnerUserID) })
}

func (r *scopedReader) GetHarnessSetupRevision(ctx context.Context, id string) (domain.HarnessSetupRevision, error) {
	item, err := r.inner.GetHarnessSetupRevision(ctx, id)
	if err != nil {
		return domain.HarnessSetupRevision{}, err
	}
	if err := r.setup(ctx, item.SetupID); err != nil {
		return domain.HarnessSetupRevision{}, err
	}
	return item, nil
}

func (r *scopedReader) ListHarnessSetupRevisions(ctx context.Context, id string) ([]domain.HarnessSetupRevision, error) {
	if err := r.setup(ctx, id); err != nil {
		return nil, err
	}
	return r.inner.ListHarnessSetupRevisions(ctx, id)
}

func (r *scopedReader) GetPinnedHarnessSetup(ctx context.Context, id domain.CapsuleID, harness string) (string, error) {
	if err := r.capsule(ctx, id); err != nil {
		return "", err
	}
	return r.inner.GetPinnedHarnessSetup(ctx, id, harness)
}

func (r *scopedReader) ListPinnedHarnessSetups(ctx context.Context, ids []domain.CapsuleID) (map[domain.CapsuleID][]domain.PinnedHarnessSetup, error) {
	allowed, err := filter(r, append([]domain.CapsuleID(nil), ids...), func(id domain.CapsuleID) error { return r.capsule(ctx, id) })
	if err != nil {
		return nil, err
	}
	return r.inner.ListPinnedHarnessSetups(ctx, allowed)
}

func (r *scopedReader) GetProject(ctx context.Context, id domain.ProjectID) (domain.Project, error) {
	if err := r.project(ctx, id); err != nil {
		return domain.Project{}, err
	}
	return r.inner.GetProject(ctx, id)
}

func (r *scopedReader) ListProjects(ctx context.Context, owner domain.UserID, page ports.Page) ([]domain.Project, bool, error) {
	if err := r.keyOwner(owner); err != nil {
		return nil, false, nil
	}
	return r.inner.ListProjects(ctx, owner, page)
}

func (r *scopedReader) ListProjectsInDeletion(ctx context.Context) ([]domain.Project, error) {
	items, err := r.inner.ListProjectsInDeletion(ctx)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.Project) error { return r.scope.check(item.OwnerUserID) })
}

// ArtifactReferenced asks whether any Moment still names a content-addressed
// archive, across owners, so a shared archive is never collected while
// another owner needs it. It returns no row.
func (r *scopedReader) ArtifactReferenced(ctx context.Context, digest string) (bool, error) {
	return r.inner.ArtifactReferenced(ctx, digest)
}

func (r *scopedReader) GetSecret(ctx context.Context, owner domain.UserID, name string) (domain.Secret, error) {
	if err := r.keyOwner(owner); err != nil {
		return domain.Secret{}, err
	}
	return r.inner.GetSecret(ctx, owner, name)
}

func (r *scopedReader) ListSecrets(ctx context.Context, owner domain.UserID, page ports.Page) ([]domain.Secret, bool, error) {
	if err := r.keyOwner(owner); err != nil {
		return nil, false, nil
	}
	return r.inner.ListSecrets(ctx, owner, page)
}

func (r *scopedReader) GetCapsule(ctx context.Context, id domain.CapsuleID) (domain.Capsule, error) {
	if err := r.capsule(ctx, id); err != nil {
		return domain.Capsule{}, err
	}
	return r.inner.GetCapsule(ctx, id)
}

func (r *scopedReader) ListCapsules(ctx context.Context, id domain.ProjectID, role domain.CapsuleRole, page ports.Page) ([]domain.Capsule, bool, error) {
	if err := r.project(ctx, id); err != nil {
		return nil, false, err
	}
	return r.inner.ListCapsules(ctx, id, role, page)
}

// CountLiveCapsules is the installation's live Capsule count, the capacity
// every owner shares. It returns no row.
func (r *scopedReader) CountLiveCapsules(ctx context.Context) (int, error) {
	return r.inner.CountLiveCapsules(ctx)
}

func (r *scopedReader) ListIdleCapsules(ctx context.Context, before time.Time, limit int) ([]domain.Capsule, error) {
	items, err := r.inner.ListIdleCapsules(ctx, before, limit)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.Capsule) error { return r.scope.check(item.OwnerUserID) })
}

func (r *scopedReader) ListRecoverableCapsules(ctx context.Context) ([]domain.Capsule, error) {
	items, err := r.inner.ListRecoverableCapsules(ctx)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.Capsule) error { return r.scope.check(item.OwnerUserID) })
}

func (r *scopedReader) GetRun(ctx context.Context, id domain.RunID) (domain.Run, error) {
	item, err := r.inner.GetRun(ctx, id)
	if err != nil {
		return domain.Run{}, err
	}
	if err := r.capsule(ctx, item.CapsuleID); err != nil {
		return domain.Run{}, err
	}
	return item, nil
}

func (r *scopedReader) ListRuns(ctx context.Context, id domain.CapsuleID, page ports.Page) ([]domain.Run, bool, error) {
	if err := r.capsule(ctx, id); err != nil {
		return nil, false, err
	}
	return r.inner.ListRuns(ctx, id, page)
}

func (r *scopedReader) LatestRun(ctx context.Context, id domain.CapsuleID) (domain.Run, error) {
	if err := r.capsule(ctx, id); err != nil {
		return domain.Run{}, err
	}
	return r.inner.LatestRun(ctx, id)
}

func (r *scopedReader) ListRecoverableRuns(ctx context.Context) ([]domain.Run, error) {
	items, err := r.inner.ListRecoverableRuns(ctx)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.Run) error { return r.capsule(ctx, item.CapsuleID) })
}

func (r *scopedReader) ListRunEvents(ctx context.Context, id domain.RunID, after int64, limit int) ([]domain.Event, bool, error) {
	if err := r.run(ctx, id); err != nil {
		return nil, false, err
	}
	return r.inner.ListRunEvents(ctx, id, after, limit)
}

// ListEvents reads an aggregate's events by type and id, which carry no
// owner; only daemon-internal work may read them unscoped.
func (r *scopedReader) ListEvents(context.Context, string, string) ([]domain.Event, error) {
	return nil, domain.ErrNotFound
}

func (r *scopedReader) GetIdempotency(ctx context.Context, owner domain.UserID, scope, key string) (ports.IdempotencyRecord, error) {
	if err := r.keyOwner(owner); err != nil {
		return ports.IdempotencyRecord{}, err
	}
	return r.inner.GetIdempotency(ctx, owner, scope, key)
}

func (r *scopedReader) GetTimeline(ctx context.Context, id domain.TimelineID) (domain.Timeline, error) {
	item, err := r.inner.GetTimeline(ctx, id)
	if err != nil {
		return domain.Timeline{}, err
	}
	if err := r.project(ctx, item.ProjectID); err != nil {
		return domain.Timeline{}, err
	}
	return item, nil
}

func (r *scopedReader) ListTimelineAncestry(ctx context.Context, id domain.TimelineID) ([]domain.Timeline, error) {
	if err := r.timeline(ctx, id); err != nil {
		return nil, err
	}
	items, err := r.inner.ListTimelineAncestry(ctx, id)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.Timeline) error { return r.project(ctx, item.ProjectID) })
}

func (r *scopedReader) GetMoment(ctx context.Context, id domain.MomentID) (domain.Moment, error) {
	item, err := r.inner.GetMoment(ctx, id)
	if err != nil {
		return domain.Moment{}, err
	}
	if err := r.project(ctx, item.ProjectID); err != nil {
		return domain.Moment{}, err
	}
	return item, nil
}

func (r *scopedReader) ListMoments(ctx context.Context, id domain.TimelineID, page ports.Page) ([]domain.Moment, bool, error) {
	if err := r.timeline(ctx, id); err != nil {
		return nil, false, err
	}
	return r.inner.ListMoments(ctx, id, page)
}

func (r *scopedReader) LatestMoment(ctx context.Context, id domain.TimelineID) (domain.Moment, error) {
	if err := r.timeline(ctx, id); err != nil {
		return domain.Moment{}, err
	}
	return r.inner.LatestMoment(ctx, id)
}

func (r *scopedReader) GetSetupMomentCache(ctx context.Context, id domain.ProjectID, hash string) (domain.SetupMomentCache, error) {
	if err := r.project(ctx, id); err != nil {
		return domain.SetupMomentCache{}, err
	}
	return r.inner.GetSetupMomentCache(ctx, id, hash)
}

func (r *scopedReader) HasActiveRun(ctx context.Context, id domain.CapsuleID) (bool, error) {
	if err := r.capsule(ctx, id); err != nil {
		return false, err
	}
	return r.inner.HasActiveRun(ctx, id)
}

func (r *scopedReader) GetThread(ctx context.Context, id domain.ThreadID) (domain.Thread, error) {
	item, err := r.inner.GetThread(ctx, id)
	if err != nil {
		return domain.Thread{}, err
	}
	if err := r.capsule(ctx, item.CapsuleID); err != nil {
		return domain.Thread{}, err
	}
	return item, nil
}

func (r *scopedReader) GetActiveThread(ctx context.Context, id domain.CapsuleID) (domain.Thread, error) {
	if err := r.capsule(ctx, id); err != nil {
		return domain.Thread{}, err
	}
	return r.inner.GetActiveThread(ctx, id)
}

func (r *scopedReader) ListThreads(ctx context.Context, id domain.CapsuleID, page ports.Page) ([]domain.Thread, bool, error) {
	if err := r.capsule(ctx, id); err != nil {
		return nil, false, err
	}
	return r.inner.ListThreads(ctx, id, page)
}

func (r *scopedReader) ListRecoverableThreads(ctx context.Context) ([]domain.Thread, error) {
	items, err := r.inner.ListRecoverableThreads(ctx)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.Thread) error { return r.capsule(ctx, item.CapsuleID) })
}

func (r *scopedReader) ListThreadMessages(ctx context.Context, id domain.ThreadID, after int64, limit int) ([]domain.ThreadMessage, bool, error) {
	if err := r.thread(ctx, id); err != nil {
		return nil, false, err
	}
	return r.inner.ListThreadMessages(ctx, id, after, limit)
}

func (r *scopedReader) GetThreadDelivery(ctx context.Context, id domain.ThreadMessageID) (domain.ThreadDelivery, error) {
	item, err := r.inner.GetThreadDelivery(ctx, id)
	if err != nil {
		return domain.ThreadDelivery{}, err
	}
	if err := r.thread(ctx, item.ThreadID); err != nil {
		return domain.ThreadDelivery{}, err
	}
	return item, nil
}

func (r *scopedReader) FindUndeliveredUserMessage(ctx context.Context, id domain.ThreadID) (domain.ThreadMessage, error) {
	if err := r.thread(ctx, id); err != nil {
		return domain.ThreadMessage{}, err
	}
	return r.inner.FindUndeliveredUserMessage(ctx, id)
}

func (r *scopedReader) GetProjectThreadIntent(ctx context.Context, id domain.ProjectThreadIntentID) (domain.ProjectThreadIntent, error) {
	item, err := r.inner.GetProjectThreadIntent(ctx, id)
	if err != nil {
		return domain.ProjectThreadIntent{}, err
	}
	if err := r.project(ctx, item.ProjectID); err != nil {
		return domain.ProjectThreadIntent{}, err
	}
	return item, nil
}

func (r *scopedReader) GetProjectThreadIntentByKey(ctx context.Context, id domain.ProjectID, key string) (domain.ProjectThreadIntent, error) {
	if err := r.project(ctx, id); err != nil {
		return domain.ProjectThreadIntent{}, err
	}
	return r.inner.GetProjectThreadIntentByKey(ctx, id, key)
}

func (r *scopedReader) ListProvisioningProjectThreadIntents(ctx context.Context) ([]domain.ProjectThreadIntent, error) {
	items, err := r.inner.ListProvisioningProjectThreadIntents(ctx)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.ProjectThreadIntent) error { return r.project(ctx, item.ProjectID) })
}

func (r *scopedReader) GetDelivery(ctx context.Context, id domain.DeliveryID) (domain.Delivery, error) {
	item, err := r.inner.GetDelivery(ctx, id)
	if err != nil {
		return domain.Delivery{}, err
	}
	if err := r.capsule(ctx, item.CapsuleID); err != nil {
		return domain.Delivery{}, err
	}
	return item, nil
}

func (r *scopedReader) GetDeliveryByIdempotency(ctx context.Context, id domain.CapsuleID, key string) (domain.Delivery, error) {
	if err := r.capsule(ctx, id); err != nil {
		return domain.Delivery{}, err
	}
	return r.inner.GetDeliveryByIdempotency(ctx, id, key)
}

func (r *scopedReader) ListDeliveries(ctx context.Context, id domain.CapsuleID, page ports.Page) ([]domain.Delivery, bool, error) {
	if err := r.capsule(ctx, id); err != nil {
		return nil, false, err
	}
	return r.inner.ListDeliveries(ctx, id, page)
}

func (r *scopedReader) ListRecoverableDeliveries(ctx context.Context) ([]domain.Delivery, error) {
	items, err := r.inner.ListRecoverableDeliveries(ctx)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.Delivery) error { return r.capsule(ctx, item.CapsuleID) })
}

func (r *scopedReader) GetCoordinationGrant(ctx context.Context, id domain.CoordinationGrantID) (domain.CoordinationGrant, error) {
	item, err := r.inner.GetCoordinationGrant(ctx, id)
	if err != nil {
		return domain.CoordinationGrant{}, err
	}
	if err := r.project(ctx, item.ProjectID); err != nil {
		return domain.CoordinationGrant{}, err
	}
	return item, nil
}

func (r *scopedReader) GetCoordinationGrantByRootThread(ctx context.Context, id domain.ThreadID) (domain.CoordinationGrant, error) {
	item, err := r.inner.GetCoordinationGrantByRootThread(ctx, id)
	if err != nil {
		return domain.CoordinationGrant{}, err
	}
	if err := r.project(ctx, item.ProjectID); err != nil {
		return domain.CoordinationGrant{}, err
	}
	return item, nil
}

func (r *scopedReader) GetCoordinationGrantByRootCapsule(ctx context.Context, id domain.CapsuleID) (domain.CoordinationGrant, error) {
	item, err := r.inner.GetCoordinationGrantByRootCapsule(ctx, id)
	if err != nil {
		return domain.CoordinationGrant{}, err
	}
	if err := r.project(ctx, item.ProjectID); err != nil {
		return domain.CoordinationGrant{}, err
	}
	return item, nil
}

func (r *scopedReader) ListActiveCoordinationGrants(ctx context.Context, stoppedSince time.Time) ([]domain.CoordinationGrant, error) {
	items, err := r.inner.ListActiveCoordinationGrants(ctx, stoppedSince)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.CoordinationGrant) error { return r.project(ctx, item.ProjectID) })
}

func (r *scopedReader) GetThreadLink(ctx context.Context, id domain.ThreadLinkID) (domain.ThreadLink, error) {
	item, err := r.inner.GetThreadLink(ctx, id)
	if err != nil {
		return domain.ThreadLink{}, err
	}
	if err := r.grant(ctx, item.GrantID); err != nil {
		return domain.ThreadLink{}, err
	}
	return item, nil
}

func (r *scopedReader) GetThreadLinkByChildThread(ctx context.Context, id domain.ThreadID) (domain.ThreadLink, error) {
	item, err := r.inner.GetThreadLinkByChildThread(ctx, id)
	if err != nil {
		return domain.ThreadLink{}, err
	}
	if err := r.grant(ctx, item.GrantID); err != nil {
		return domain.ThreadLink{}, err
	}
	return item, nil
}

func (r *scopedReader) ListThreadLinksByParent(ctx context.Context, id domain.ThreadID) ([]domain.ThreadLink, error) {
	if err := r.thread(ctx, id); err != nil {
		return nil, err
	}
	return r.inner.ListThreadLinksByParent(ctx, id)
}

func (r *scopedReader) ListThreadLinksByGrant(ctx context.Context, id domain.CoordinationGrantID) ([]domain.ThreadLink, error) {
	if err := r.grant(ctx, id); err != nil {
		return nil, err
	}
	return r.inner.ListThreadLinksByGrant(ctx, id)
}

func (r *scopedReader) ListArchivableThreadLinks(ctx context.Context, now time.Time, limit int) ([]domain.ThreadLink, error) {
	items, err := r.inner.ListArchivableThreadLinks(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.ThreadLink) error { return r.grant(ctx, item.GrantID) })
}

func (r *scopedReader) ListArchivedLiveThreadLinks(ctx context.Context, limit int) ([]domain.ThreadLink, error) {
	items, err := r.inner.ListArchivedLiveThreadLinks(ctx, limit)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.ThreadLink) error { return r.grant(ctx, item.GrantID) })
}

func (r *scopedReader) GetCoordinationRequest(ctx context.Context, id domain.CapsuleID, key string) (domain.CoordinationRequest, error) {
	if err := r.capsule(ctx, id); err != nil {
		return domain.CoordinationRequest{}, err
	}
	return r.inner.GetCoordinationRequest(ctx, id, key)
}

func (r *scopedReader) GetCoordinationRequestByID(ctx context.Context, id domain.CoordinationRequestID) (domain.CoordinationRequest, error) {
	item, err := r.inner.GetCoordinationRequestByID(ctx, id)
	if err != nil {
		return domain.CoordinationRequest{}, err
	}
	if err := r.grant(ctx, item.GrantID); err != nil {
		return domain.CoordinationRequest{}, err
	}
	return item, nil
}

func (r *scopedReader) ListCoordinationRequests(ctx context.Context, state domain.CoordinationRequestState) ([]domain.CoordinationRequest, error) {
	items, err := r.inner.ListCoordinationRequests(ctx, state)
	if err != nil {
		return nil, err
	}
	return filter(r, items, func(item domain.CoordinationRequest) error { return r.grant(ctx, item.GrantID) })
}

// GetUser reads only the caller's own membership row.
func (r *scopedReader) GetUser(ctx context.Context, id domain.UserID) (domain.User, error) {
	if err := r.keyOwner(id); err != nil {
		return domain.User{}, err
	}
	return r.inner.GetUser(ctx, id)
}

// ListAuditRecords lists the caller's own records. Listing every user's
// records needs the admin role (ADR 0046).
func (r *scopedReader) ListAuditRecords(ctx context.Context, user domain.UserID, after int64, limit int) ([]domain.AuditRecord, bool, error) {
	if user == "" {
		member, err := r.inner.GetUser(ctx, r.scope.owner)
		if err != nil || member.Role != domain.UserAdmin || !member.DisabledAt.IsZero() {
			return nil, false, domain.ErrNotFound
		}
	} else if err := r.keyOwner(user); err != nil {
		return nil, false, err
	}
	return r.inner.ListAuditRecords(ctx, user, after, limit)
}

// scopedTx checks each write against the same scope. A root row must be
// the scope owner's; any other row must belong to a parent the scope can
// see. The store's triggers then refuse a child whose owner differs from
// its parent's, as a second line.
type scopedTx struct {
	*scopedReader
	tx ports.Transaction
}

func (t *scopedTx) delivery(ctx context.Context, id domain.DeliveryID) error {
	_, err := t.GetDelivery(ctx, id)
	return err
}

func (t *scopedTx) link(ctx context.Context, id domain.ThreadLinkID) error {
	_, err := t.GetThreadLink(ctx, id)
	return err
}

func (t *scopedTx) PutPreparationPolicy(ctx context.Context, id domain.ProjectID, policy domain.PreparationPolicy) error {
	if err := t.project(ctx, id); err != nil {
		return err
	}
	return t.tx.PutPreparationPolicy(ctx, id, policy)
}

func (t *scopedTx) PutPreparation(ctx context.Context, item domain.Preparation) error {
	if err := t.capsule(ctx, item.CapsuleID); err != nil {
		return err
	}
	return t.tx.PutPreparation(ctx, item)
}

func (t *scopedTx) PutStartup(ctx context.Context, item domain.StartupProgress) error {
	if err := t.capsule(ctx, item.CapsuleID); err != nil {
		return err
	}
	return t.tx.PutStartup(ctx, item)
}

func (t *scopedTx) DeleteStartup(ctx context.Context, id domain.CapsuleID) error {
	if err := t.capsule(ctx, id); err != nil {
		return err
	}
	return t.tx.DeleteStartup(ctx, id)
}

// CollectDeletedHarnessSetups removes revisions no Capsule pins of setups
// their owners deleted. It touches no live row.
func (t *scopedTx) CollectDeletedHarnessSetups(ctx context.Context) error {
	return t.tx.CollectDeletedHarnessSetups(ctx)
}

func (t *scopedTx) SetProjectHarnessSetup(ctx context.Context, id domain.ProjectID, harness, setup string) error {
	if err := t.project(ctx, id); err != nil {
		return err
	}
	if setup != "" {
		if err := t.setup(ctx, setup); err != nil {
			return err
		}
	}
	return t.tx.SetProjectHarnessSetup(ctx, id, harness, setup)
}

func (t *scopedTx) PutProviderConnection(ctx context.Context, item domain.ProviderConnection) error {
	if err := t.scope.check(item.OwnerUserID); err != nil {
		return err
	}
	return t.tx.PutProviderConnection(ctx, item)
}

func (t *scopedTx) GrantProviderConnection(ctx context.Context, id domain.ProjectID, harness, connection string) error {
	if err := t.project(ctx, id); err != nil {
		return err
	}
	if connection != "" {
		if err := t.connection(ctx, connection); err != nil {
			return err
		}
	}
	return t.tx.GrantProviderConnection(ctx, id, harness, connection)
}

func (t *scopedTx) PutHarnessSetup(ctx context.Context, item domain.HarnessSetup) error {
	if err := t.scope.check(item.OwnerUserID); err != nil {
		return err
	}
	return t.tx.PutHarnessSetup(ctx, item)
}

func (t *scopedTx) InsertHarnessSetupRevision(ctx context.Context, item domain.HarnessSetupRevision) error {
	if err := t.setup(ctx, item.SetupID); err != nil {
		return err
	}
	return t.tx.InsertHarnessSetupRevision(ctx, item)
}

func (t *scopedTx) PinHarnessSetup(ctx context.Context, id domain.CapsuleID, harness, revision string) error {
	if err := t.capsule(ctx, id); err != nil {
		return err
	}
	return t.tx.PinHarnessSetup(ctx, id, harness, revision)
}

func (t *scopedTx) InsertProject(ctx context.Context, item domain.Project) error {
	if err := t.scope.check(item.OwnerUserID); err != nil {
		return err
	}
	return t.tx.InsertProject(ctx, item)
}

func (t *scopedTx) UpdateProject(ctx context.Context, item domain.Project, expected domain.ResourceVersion) error {
	if err := t.project(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.UpdateProject(ctx, item, expected)
}

func (t *scopedTx) UpdateProjectState(ctx context.Context, item domain.Project, expected domain.ResourceVersion) error {
	if err := t.project(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.UpdateProjectState(ctx, item, expected)
}

func (t *scopedTx) PurgeProjectContents(ctx context.Context, item domain.Project, now time.Time) (domain.Project, []string, error) {
	if err := t.project(ctx, item.ID); err != nil {
		return domain.Project{}, nil, err
	}
	return t.tx.PurgeProjectContents(ctx, item, now)
}

func (t *scopedTx) InsertSecret(ctx context.Context, item domain.Secret) error {
	if err := t.scope.check(item.OwnerUserID); err != nil {
		return err
	}
	return t.tx.InsertSecret(ctx, item)
}

func (t *scopedTx) UpdateSecret(ctx context.Context, item domain.Secret, expected domain.ResourceVersion) error {
	if err := t.scope.check(item.OwnerUserID); err != nil {
		return err
	}
	return t.tx.UpdateSecret(ctx, item, expected)
}

func (t *scopedTx) DeleteSecret(ctx context.Context, owner domain.UserID, name string, expected domain.ResourceVersion) error {
	if err := t.keyOwner(owner); err != nil {
		return err
	}
	return t.tx.DeleteSecret(ctx, owner, name, expected)
}

func (t *scopedTx) InsertCapsule(ctx context.Context, item domain.Capsule) error {
	if err := t.project(ctx, item.ProjectID); err != nil {
		return err
	}
	return t.tx.InsertCapsule(ctx, item)
}

func (t *scopedTx) UpdateCapsule(ctx context.Context, item domain.Capsule, expected domain.ResourceVersion) error {
	if err := t.capsule(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.UpdateCapsule(ctx, item, expected)
}

func (t *scopedTx) TouchCapsuleActivity(ctx context.Context, id domain.CapsuleID, at time.Time) error {
	if err := t.capsule(ctx, id); err != nil {
		return err
	}
	return t.tx.TouchCapsuleActivity(ctx, id, at)
}

func (t *scopedTx) InsertTimeline(ctx context.Context, item domain.Timeline) error {
	if err := t.project(ctx, item.ProjectID); err != nil {
		return err
	}
	return t.tx.InsertTimeline(ctx, item)
}

func (t *scopedTx) InsertMoment(ctx context.Context, item domain.Moment) error {
	if err := t.project(ctx, item.ProjectID); err != nil {
		return err
	}
	return t.tx.InsertMoment(ctx, item)
}

func (t *scopedTx) PutSetupMomentCache(ctx context.Context, item domain.SetupMomentCache) error {
	if err := t.project(ctx, item.ProjectID); err != nil {
		return err
	}
	return t.tx.PutSetupMomentCache(ctx, item)
}

func (t *scopedTx) InsertRun(ctx context.Context, item domain.Run) error {
	if err := t.capsule(ctx, item.CapsuleID); err != nil {
		return err
	}
	return t.tx.InsertRun(ctx, item)
}

func (t *scopedTx) UpdateRun(ctx context.Context, item domain.Run, expected domain.ResourceVersion) error {
	if err := t.run(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.UpdateRun(ctx, item, expected)
}

// AppendEvent records a content-free event about an object the call
// already reached through a checked read or write.
func (t *scopedTx) AppendEvent(ctx context.Context, item domain.Event) error {
	return t.tx.AppendEvent(ctx, item)
}

func (t *scopedTx) PutIdempotency(ctx context.Context, record ports.IdempotencyRecord) error {
	if err := t.keyOwner(record.Owner); err != nil {
		return err
	}
	return t.tx.PutIdempotency(ctx, record)
}

func (t *scopedTx) InsertThread(ctx context.Context, item domain.Thread) error {
	if err := t.capsule(ctx, item.CapsuleID); err != nil {
		return err
	}
	return t.tx.InsertThread(ctx, item)
}

func (t *scopedTx) UpdateThread(ctx context.Context, item domain.Thread, expected domain.ResourceVersion) error {
	if err := t.thread(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.UpdateThread(ctx, item, expected)
}

func (t *scopedTx) AppendThreadMessage(ctx context.Context, item domain.ThreadMessage) error {
	if err := t.thread(ctx, item.ThreadID); err != nil {
		return err
	}
	return t.tx.AppendThreadMessage(ctx, item)
}

func (t *scopedTx) InsertThreadDelivery(ctx context.Context, item domain.ThreadDelivery) error {
	if err := t.thread(ctx, item.ThreadID); err != nil {
		return err
	}
	return t.tx.InsertThreadDelivery(ctx, item)
}

func (t *scopedTx) AcknowledgeThreadDelivery(ctx context.Context, id domain.ThreadMessageID, at time.Time) error {
	if _, err := t.GetThreadDelivery(ctx, id); err != nil {
		return err
	}
	return t.tx.AcknowledgeThreadDelivery(ctx, id, at)
}

func (t *scopedTx) RewrapThreadKey(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion,
	wrapped []byte, kekID string, kekVersion uint32, at time.Time,
) error {
	if err := t.thread(ctx, id); err != nil {
		return err
	}
	return t.tx.RewrapThreadKey(ctx, id, expected, wrapped, kekID, kekVersion, at)
}

func (t *scopedTx) CryptoShredThread(ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, at time.Time) error {
	if err := t.thread(ctx, id); err != nil {
		return err
	}
	return t.tx.CryptoShredThread(ctx, id, expected, at)
}

func (t *scopedTx) InsertProjectThreadIntent(ctx context.Context, item domain.ProjectThreadIntent) error {
	if err := t.project(ctx, item.ProjectID); err != nil {
		return err
	}
	return t.tx.InsertProjectThreadIntent(ctx, item)
}

func (t *scopedTx) UpdateProjectThreadIntent(ctx context.Context, item domain.ProjectThreadIntent, expected domain.ResourceVersion) error {
	if _, err := t.GetProjectThreadIntent(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.UpdateProjectThreadIntent(ctx, item, expected)
}

func (t *scopedTx) InsertDelivery(ctx context.Context, item domain.Delivery) error {
	if err := t.capsule(ctx, item.CapsuleID); err != nil {
		return err
	}
	return t.tx.InsertDelivery(ctx, item)
}

func (t *scopedTx) UpdateDelivery(ctx context.Context, item domain.Delivery, expected domain.ResourceVersion) error {
	if err := t.delivery(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.UpdateDelivery(ctx, item, expected)
}

func (t *scopedTx) ApplyDeliveryApproval(ctx context.Context, item domain.Delivery, expected domain.ResourceVersion) error {
	if err := t.delivery(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.ApplyDeliveryApproval(ctx, item, expected)
}

func (t *scopedTx) InsertCoordinationGrant(ctx context.Context, item domain.CoordinationGrant) error {
	if err := t.project(ctx, item.ProjectID); err != nil {
		return err
	}
	return t.tx.InsertCoordinationGrant(ctx, item)
}

func (t *scopedTx) UpdateCoordinationGrant(ctx context.Context, item domain.CoordinationGrant, expected domain.ResourceVersion) error {
	if err := t.grant(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.UpdateCoordinationGrant(ctx, item, expected)
}

func (t *scopedTx) InsertThreadLink(ctx context.Context, item domain.ThreadLink) error {
	if err := t.grant(ctx, item.GrantID); err != nil {
		return err
	}
	return t.tx.InsertThreadLink(ctx, item)
}

func (t *scopedTx) UpdateThreadLink(ctx context.Context, item domain.ThreadLink, expected domain.ResourceVersion) error {
	if err := t.link(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.UpdateThreadLink(ctx, item, expected)
}

func (t *scopedTx) PromoteCapsule(ctx context.Context, item domain.Capsule, expected domain.ResourceVersion) error {
	if err := t.capsule(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.PromoteCapsule(ctx, item, expected)
}

func (t *scopedTx) InsertCoordinationRequest(ctx context.Context, item domain.CoordinationRequest) error {
	if err := t.grant(ctx, item.GrantID); err != nil {
		return err
	}
	return t.tx.InsertCoordinationRequest(ctx, item)
}

func (t *scopedTx) UpdateCoordinationRequest(ctx context.Context, item domain.CoordinationRequest, expected domain.ResourceVersion) error {
	if _, err := t.GetCoordinationRequestByID(ctx, item.ID); err != nil {
		return err
	}
	return t.tx.UpdateCoordinationRequest(ctx, item, expected)
}

// AppendAuditRecord appends a record internal/app built from the caller's
// own principal.
func (t *scopedTx) AppendAuditRecord(ctx context.Context, record domain.AuditRecord) error {
	return t.tx.AppendAuditRecord(ctx, record)
}

// PruneAuditRecords is retention, daemon-internal work only.
func (t *scopedTx) PruneAuditRecords(context.Context, time.Time) (int, error) {
	return 0, domain.ErrNotFound
}
