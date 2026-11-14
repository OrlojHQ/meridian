package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

// ConfigureCoordination enables lead/child Thread coordination. maxLive is
// the installation-wide live Capsule cap applied to coordinated spawns; zero
// disables the installation cap.
func (s *Service) ConfigureCoordination(runtime ports.CoordinationRuntime, maxLive int) {
	s.coordination = runtime
	s.coordMaxLive = maxLive
}

func (s *Service) CoordinationSupported() bool {
	return s.coordination != nil && s.structured != nil && s.transcriptKey != nil
}

func (s *Service) validateCoordinationPolicy(
	ctx context.Context,
	reader ports.Reader,
	projectID domain.ProjectID,
	leadHarness string,
	requested domain.CoordinationPolicy,
) (domain.CoordinationPolicy, error) {
	policy, err := requested.Normalize()
	if err != nil {
		return policy, err
	}
	if len(policy.AllowedHarnesses) == 0 {
		policy.AllowedHarnesses = []string{leadHarness}
	}
	project, err := reader.GetProject(ctx, projectID)
	if err != nil {
		return policy, err
	}
	for _, harness := range policy.AllowedHarnesses {
		if _, err := s.structuredHarnessImage(project, harness); err != nil {
			return policy, fmt.Errorf("%w: coordination harness %q is not an allowlisted structured harness",
				domain.ErrInvalid, harness)
		}
	}
	return policy, nil
}

func (s *Service) insertCoordinationGrantTx(
	ctx context.Context,
	tx ports.Transaction,
	intent domain.ProjectThreadIntent,
	policy domain.CoordinationPolicy,
) error {
	now := s.clock.Now().UTC()
	grant := domain.CoordinationGrant{
		ID: domain.CoordinationGrantID(s.ids.NewID()), ProjectID: intent.ProjectID,
		RootThreadID: intent.ThreadID, RootCapsuleID: intent.CapsuleID,
		State: domain.GrantActive, SpawnApproval: policy.SpawnApproval,
		MaxDepth: policy.MaxDepth, MaxChildren: policy.MaxChildren,
		MaxDescendants: policy.MaxDescendants, MaxLiveCapsules: policy.MaxLiveCapsules,
		MaxMessagesPerLink: policy.MaxMessagesPerLink, AllowedHarnesses: policy.AllowedHarnesses,
		DeadlineAt: now.Add(policy.Deadline), OnDeadline: policy.OnDeadline,
		ArchiveAfter: *policy.ArchiveAfter, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	if err := tx.InsertCoordinationGrant(ctx, grant); err != nil {
		return err
	}
	return s.appendEvent(ctx, tx, "coordination_grant", string(grant.ID), "coordination_grant.created", 1, nil)
}

func (s *Service) matchCoordinationReplay(
	ctx context.Context,
	reader ports.Reader,
	intent domain.ProjectThreadIntent,
	requested *domain.CoordinationPolicy,
) error {
	grant, err := reader.GetCoordinationGrantByRootThread(ctx, intent.ThreadID)
	if errors.Is(err, domain.ErrNotFound) {
		if requested != nil {
			return fmt.Errorf("%w: Idempotency-Key was used with a different request", domain.ErrConflict)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if requested == nil {
		return fmt.Errorf("%w: Idempotency-Key was used with a different request", domain.ErrConflict)
	}
	policy, err := s.validateCoordinationPolicy(ctx, reader, intent.ProjectID, intent.Harness, *requested)
	if err != nil {
		return err
	}
	stored := grant.Policy()
	if policy.MaxDepth != stored.MaxDepth || policy.MaxChildren != stored.MaxChildren ||
		policy.MaxDescendants != stored.MaxDescendants || policy.MaxLiveCapsules != stored.MaxLiveCapsules ||
		policy.MaxMessagesPerLink != stored.MaxMessagesPerLink || policy.OnDeadline != stored.OnDeadline ||
		policy.SpawnApproval != stored.SpawnApproval || *policy.ArchiveAfter != *stored.ArchiveAfter ||
		!slices.Equal(policy.AllowedHarnesses, stored.AllowedHarnesses) ||
		grant.DeadlineAt.Sub(grant.CreatedAt) != policy.Deadline {
		return fmt.Errorf("%w: Idempotency-Key was used with a different request", domain.ErrConflict)
	}
	return nil
}

func (s *Service) GetCoordinationGrant(
	ctx context.Context, id domain.CoordinationGrantID,
) (domain.CoordinationGrant, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.CoordinationGrant{}, err
	}
	var grant domain.CoordinationGrant
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		grant, err = reader.GetCoordinationGrant(ctx, id)
		return err
	})
	return grant, err
}

func (s *Service) GetThreadLink(ctx context.Context, id domain.ThreadLinkID) (domain.ThreadLink, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.ThreadLink{}, err
	}
	var link domain.ThreadLink
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		link, err = reader.GetThreadLink(ctx, id)
		return err
	})
	return link, err
}

// ListThreadLinks lists the children a Thread spawned.
func (s *Service) ListThreadLinks(ctx context.Context, id domain.ThreadID) ([]domain.ThreadLink, error) {
	if err := requirePrincipal(ctx); err != nil {
		return nil, err
	}
	var links []domain.ThreadLink
	err := s.store.View(ctx, func(reader ports.Reader) error {
		if _, err := reader.GetThread(ctx, id); err != nil {
			return err
		}
		var err error
		links, err = reader.ListThreadLinksByParent(ctx, id)
		return err
	})
	return links, err
}

// ThreadCoordination is the read-only coordination position of a Thread.
type ThreadCoordination struct {
	GrantID        domain.CoordinationGrantID
	ParentThreadID domain.ThreadID
	RootThreadID   domain.ThreadID
	LinkID         domain.ThreadLinkID
	Depth          int
}

func (s *Service) ThreadCoordinationOf(ctx context.Context, id domain.ThreadID) (ThreadCoordination, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ThreadCoordination{}, err
	}
	var result ThreadCoordination
	err := s.store.View(ctx, func(reader ports.Reader) error {
		if grant, err := reader.GetCoordinationGrantByRootThread(ctx, id); err == nil {
			result.GrantID, result.RootThreadID = grant.ID, id
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		link, err := reader.GetThreadLinkByChildThread(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		grant, err := reader.GetCoordinationGrant(ctx, link.GrantID)
		if err != nil {
			return err
		}
		result.ParentThreadID, result.LinkID, result.Depth = link.ParentThreadID, link.ID, link.Depth
		result.GrantID, result.RootThreadID = grant.ID, grant.RootThreadID
		return nil
	})
	return result, err
}

// RevokeCoordinationGrant ends a grant. Active links close and their children
// are cancelled and paused when cancelDescendants is set.
func (s *Service) RevokeCoordinationGrant(
	ctx context.Context,
	id domain.CoordinationGrantID,
	expected domain.ResourceVersion,
	cancelDescendants bool,
	key string,
) (domain.CoordinationGrant, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.CoordinationGrant{}, err
	}
	if expected <= 0 {
		return domain.CoordinationGrant{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.CoordinationGrant{}, err
	}
	scope := "coordination_grant:" + string(id) + ":revoke"
	var grant domain.CoordinationGrant
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, ok, err := getReplay[struct{}](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			grant, err = tx.GetCoordinationGrant(ctx, id)
			return err
		}
		current, err := tx.GetCoordinationGrant(ctx, id)
		if err != nil {
			return err
		}
		if current.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if current.State != domain.GrantActive {
			return fmt.Errorf("%w: coordination grant is not active", domain.ErrIllegalTransition)
		}
		grant, err = s.endGrantTx(ctx, tx, current, domain.GrantRevoked, domain.RevokeOperator, cancelDescendants)
		if err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, struct{}{}, s.clock.Now().UTC())
	})
	if err != nil {
		return domain.CoordinationGrant{}, err
	}
	s.afterGrantEnded(ctx, grant)
	return grant, nil
}

// endGrantTx moves an active grant to revoked or expired. Closing links
// records a failed report so the lead learns why.
func (s *Service) endGrantTx(
	ctx context.Context,
	tx ports.Transaction,
	grant domain.CoordinationGrant,
	state domain.CoordinationGrantState,
	reason domain.GrantRevokeReason,
	closeLinks bool,
) (domain.CoordinationGrant, error) {
	now := s.clock.Now().UTC()
	previous := grant.ResourceVersion
	grant.State, grant.RevokeReason, grant.RevokedAt = state, reason, now
	grant.UpdatedAt = now
	grant.ResourceVersion++
	if err := tx.UpdateCoordinationGrant(ctx, grant, previous); err != nil {
		return grant, err
	}
	if closeLinks {
		links, err := tx.ListThreadLinksByGrant(ctx, grant.ID)
		if err != nil {
			return grant, err
		}
		for _, link := range links {
			if link.State != domain.LinkActive && link.State != domain.LinkPendingApproval {
				continue
			}
			if link.State == domain.LinkPendingApproval {
				if err := s.rejectPendingSpawnTx(ctx, tx, link, now); err != nil {
					return grant, err
				}
			}
			if err := s.closeLinkTx(ctx, tx, link, domain.LinkRevoked,
				domain.ReportFailed, domain.ReportRevoked, 0, now); err != nil {
				return grant, err
			}
		}
	} else {
		// Pending spawns never started; leaving them queued after revoke would
		// strand them, so they close even when running children are kept.
		links, err := tx.ListThreadLinksByGrant(ctx, grant.ID)
		if err != nil {
			return grant, err
		}
		for _, link := range links {
			if link.State != domain.LinkPendingApproval {
				continue
			}
			if err := s.rejectPendingSpawnTx(ctx, tx, link, now); err != nil {
				return grant, err
			}
			if err := s.closeLinkTx(ctx, tx, link, domain.LinkRevoked,
				domain.ReportFailed, domain.ReportRevoked, 0, now); err != nil {
				return grant, err
			}
		}
	}
	return grant, s.appendEvent(ctx, tx, "coordination_grant", string(grant.ID),
		"coordination_grant."+string(state), grant.ResourceVersion, []byte(`{"reason":"`+string(reason)+`"}`))
}

func (s *Service) closeLinkTx(
	ctx context.Context,
	tx ports.Transaction,
	link domain.ThreadLink,
	reason domain.LinkCloseReason,
	status domain.ReportStatus,
	reportReason domain.ReportReason,
	messageSeq int,
	now time.Time,
) error {
	previous := link.ResourceVersion
	link.State, link.CloseReason = domain.LinkClosed, reason
	link.ReportSeq++
	link.LastReportStatus, link.LastReportReason, link.LastReportAt = status, reportReason, now
	if messageSeq > link.ReportMessageSeq {
		link.ReportMessageSeq = messageSeq
	}
	link.UpdatedAt = now
	link.ResourceVersion++
	if err := tx.UpdateThreadLink(ctx, link, previous); err != nil {
		return err
	}
	return s.appendEvent(ctx, tx, "thread_link", string(link.ID), "thread_link.closed",
		link.ResourceVersion, []byte(`{"reason":"`+string(reason)+`"}`))
}

// coordinationAuthority resolves what the Capsule's active Thread may do.
// Only grant roots hold authority while depth is limited to one.
func (s *Service) coordinationAuthority(
	ctx context.Context, capsule domain.Capsule, mutating bool,
) (domain.CoordinationGrant, domain.Thread, *coordproto.Response) {
	var grant domain.CoordinationGrant
	var thread domain.Thread
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		grant, err = reader.GetCoordinationGrantByRootCapsule(ctx, capsule.ID)
		if err != nil {
			return err
		}
		thread, err = reader.GetThread(ctx, grant.RootThreadID)
		return err
	})
	if err != nil {
		response := coordproto.Fail(coordproto.CodeCoordinationDisabled, "this Thread has no coordination grant")
		return grant, thread, &response
	}
	now := s.clock.Now()
	if grant.State != domain.GrantActive && !(!mutating && grant.OnDeadline == domain.DeadlineStopNewWork &&
		grant.State == domain.GrantExpired) {
		response := coordproto.Fail(coordproto.CodeGrantInactive, "coordination grant is "+string(grant.State))
		return grant, thread, &response
	}
	if mutating && !grant.Usable(now) {
		response := coordproto.Fail(coordproto.CodeGrantInactive, "coordination grant deadline has passed")
		return grant, thread, &response
	}
	if thread.State != domain.ThreadActive || thread.CapsuleID != capsule.ID {
		response := coordproto.Fail(coordproto.CodeGrantInactive, "lead Thread is not active")
		return grant, thread, &response
	}
	if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady || capsule.Maintenance != "" {
		response := coordproto.Fail(coordproto.CodeUnavailable, "lead Capsule is not Ready")
		return grant, thread, &response
	}
	return grant, thread, nil
}

func (s *Service) handleCoordinationRequest(
	ctx context.Context, capsule domain.Capsule, request coordproto.Request,
) coordproto.Response {
	if err := request.Validate(); err != nil || !request.Remote() {
		return coordproto.Fail(coordproto.CodeInvalidRequest, "invalid coordination request")
	}
	// The poller that pulled this entry is the system; the entry itself is
	// the Capsule's request and acts as that Capsule (ADR 0046).
	caller, ok := capsulePrincipal(capsule.ID)
	if !ok {
		return coordproto.Fail(coordproto.CodeInvalidRequest, "invalid coordination request")
	}
	ctx = principal.With(ctx, caller)
	if request.Type == coordproto.KindReport {
		response := s.coordReport(ctx, capsule, request)
		s.auditRejection(ctx, domain.CoordinationGrant{}, request.Type, response)
		return response
	}
	if request.Type == coordproto.KindProposeDelivery {
		response := s.coordProposeDelivery(ctx, capsule, request)
		s.auditRejection(ctx, domain.CoordinationGrant{}, request.Type, response)
		return response
	}
	grant, thread, failure := s.coordinationAuthority(ctx, capsule, request.Mutating())
	if failure != nil {
		s.auditRejection(ctx, grant, request.Type, *failure)
		return *failure
	}
	response := s.dispatchCoordinationRequest(ctx, grant, thread, capsule, request)
	s.auditRejection(ctx, grant, request.Type, response)
	return response
}

// auditRejection records a content-free event for a refused request. Transient
// retry codes are not recorded.
func (s *Service) auditRejection(
	ctx context.Context, grant domain.CoordinationGrant, kind coordproto.Kind, response coordproto.Response,
) {
	if response.OK || response.Error == nil || grant.ID == "" {
		return
	}
	switch response.Error.Code {
	case coordproto.CodePending, coordproto.CodeBusy, coordproto.CodeUnavailable:
		return
	}
	data, err := json.Marshal(map[string]string{"kind": string(kind), "code": response.Error.Code})
	if err != nil {
		return
	}
	_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
		return s.appendEvent(ctx, tx, "coordination_grant", string(grant.ID),
			"coordination_grant.request_rejected", grant.ResourceVersion, data)
	})
}

func (s *Service) dispatchCoordinationRequest(
	ctx context.Context,
	grant domain.CoordinationGrant,
	thread domain.Thread,
	capsule domain.Capsule,
	request coordproto.Request,
) coordproto.Response {
	var response coordproto.Response
	switch request.Type {
	case coordproto.KindSpawn:
		response = s.coordSpawn(ctx, grant, thread, capsule, request)
	case coordproto.KindSend:
		response = s.coordSend(ctx, grant, thread, capsule, request)
	case coordproto.KindCancel:
		response = s.coordCancel(ctx, grant, thread, capsule, request)
	case coordproto.KindPatch:
		response = s.coordPatch(ctx, thread, request)
	case coordproto.KindStatus:
		link, ok := s.ownedLink(ctx, thread.ID, request.Link)
		if !ok {
			return coordproto.Fail(coordproto.CodeNotFound, "no such link")
		}
		s.refreshChildGit(ctx, link)
		if fresh, err := s.GetThreadLink(ctx, link.ID); err == nil {
			link = fresh
		}
		response = coordproto.OK()
		view := s.linkView(ctx, link)
		response.Link = &view
	case coordproto.KindList:
		links, err := s.ListThreadLinks(ctx, thread.ID)
		if err != nil {
			return coordproto.Fail(coordproto.CodeInternal, "list failed")
		}
		response = coordproto.OK()
		response.Links = make([]coordproto.Link, 0, len(links))
		for _, link := range links {
			s.refreshChildGit(ctx, link)
			if fresh, err := s.GetThreadLink(ctx, link.ID); err == nil {
				link = fresh
			}
			response.Links = append(response.Links, s.linkView(ctx, link))
		}
	}
	return response
}

func (s *Service) ownedLink(ctx context.Context, parent domain.ThreadID, id string) (domain.ThreadLink, bool) {
	link, err := s.GetThreadLink(ctx, domain.ThreadLinkID(id))
	if err != nil || link.ParentThreadID != parent {
		return domain.ThreadLink{}, false
	}
	return link, true
}

func (s *Service) existingCoordinationRequest(
	ctx context.Context, capsule domain.Capsule, request coordproto.Request,
) (domain.CoordinationRequest, *coordproto.Response) {
	var record domain.CoordinationRequest
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		record, err = reader.GetCoordinationRequest(ctx, capsule.ID, request.Key)
		return err
	})
	if errors.Is(err, domain.ErrNotFound) {
		return domain.CoordinationRequest{}, nil
	}
	if err != nil {
		response := coordproto.Fail(coordproto.CodeInternal, "request lookup failed")
		return record, &response
	}
	if record.CanonicalDigest != request.Digest() || string(record.Kind) != string(request.Type) {
		response := coordproto.Fail(coordproto.CodeConflict, "key was already used for a different request")
		return record, &response
	}
	return record, nil
}

func (s *Service) coordSpawn(
	ctx context.Context,
	grant domain.CoordinationGrant,
	parent domain.Thread,
	capsule domain.Capsule,
	request coordproto.Request,
) coordproto.Response {
	if record, failure := s.existingCoordinationRequest(ctx, capsule, request); failure != nil {
		return *failure
	} else if record.ID != "" {
		return s.linkResponse(ctx, record.LinkID)
	}
	if !slices.Contains(grant.AllowedHarnesses, request.Harness) {
		return coordproto.Fail(coordproto.CodeHarnessNotAllowed, "harness is not allowed by the coordination grant")
	}
	linkID := domain.ThreadLinkID(s.ids.NewID())
	var failure *coordproto.Response
	var intent domain.ProjectThreadIntent
	var held bool
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, err := tx.GetCoordinationRequest(ctx, capsule.ID, request.Key); err == nil {
			return domain.ErrConflict
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		current, err := tx.GetCoordinationGrant(ctx, grant.ID)
		if err != nil {
			return err
		}
		reject := func(code, message string) error {
			response := coordproto.Fail(code, message)
			failure = &response
			return errCoordinationRejected
		}
		if !current.Usable(s.clock.Now()) {
			return reject(coordproto.CodeGrantInactive, "coordination grant is not active")
		}
		if current.MaxDepth < 1 {
			return reject(coordproto.CodeLimitExceeded, "depth limit reached")
		}
		siblings, err := tx.ListThreadLinksByParent(ctx, parent.ID)
		if err != nil {
			return err
		}
		if len(siblings) >= current.MaxChildren {
			return reject(coordproto.CodeLimitExceeded, fmt.Sprintf("fan-out limit of %d children reached", current.MaxChildren))
		}
		for _, sibling := range siblings {
			if sibling.Name == request.Name {
				return reject(coordproto.CodeConflict, "a child with this name already exists")
			}
		}
		if current.DescendantsCreated >= current.MaxDescendants {
			return reject(coordproto.CodeLimitExceeded, "descendant limit reached")
		}
		live, err := s.liveTreeCapsules(ctx, tx, current.ID)
		if err != nil {
			return err
		}
		if live >= current.MaxLiveCapsules {
			return reject(coordproto.CodeCapacityExhausted, "live child Capsule limit reached; cancel a child or retry later")
		}
		if s.coordMaxLive > 0 {
			total, err := tx.CountLiveCapsules(ctx)
			if err != nil {
				return err
			}
			if total >= s.coordMaxLive {
				return reject(coordproto.CodeCapacityExhausted, "installation live Capsule limit reached; retry later")
			}
		}
		now := s.clock.Now().UTC()
		intent, err = s.insertProjectThreadTx(ctx, tx, projectThreadSpec{
			ProjectID: current.ProjectID, Name: request.Name, Harness: request.Harness,
			Prompt: request.Prompt, IdempotencyKey: "coord:" + string(linkID),
			Role: domain.CapsuleSub, LeadThreadID: parent.ID, LinkID: linkID,
			Origin: &ThreadOrigin{Source: OriginCoordination, Kind: OriginFromLead, LinkIDs: []domain.ThreadLinkID{linkID}},
		})
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrUnsupported) || errors.Is(err, domain.ErrInvalid) {
				return reject(coordproto.CodeHarnessNotAllowed, "harness is not an allowlisted structured harness")
			}
			return err
		}
		linkState := domain.LinkActive
		requestState := domain.RequestCompleted
		if current.SpawnApproval == domain.SpawnOperator {
			held = true
			linkState = domain.LinkPendingApproval
			requestState = domain.RequestPendingApproval
		}
		link := domain.ThreadLink{
			ID: linkID, GrantID: current.ID, ParentThreadID: parent.ID, ParentCapsuleID: capsule.ID,
			ChildIntentID: intent.ID, ChildThreadID: intent.ThreadID, ChildCapsuleID: intent.CapsuleID,
			Depth: 1, Name: request.Name, Harness: request.Harness, State: linkState,
			CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}
		if err := tx.InsertThreadLink(ctx, link); err != nil {
			return err
		}
		previous := current.ResourceVersion
		current.DescendantsCreated++
		current.UpdatedAt = now
		current.ResourceVersion++
		if err := tx.UpdateCoordinationGrant(ctx, current, previous); err != nil {
			return err
		}
		if err := tx.InsertCoordinationRequest(ctx, domain.CoordinationRequest{
			ID: domain.CoordinationRequestID(s.ids.NewID()), GrantID: current.ID, CapsuleID: capsule.ID,
			Key: request.Key, CanonicalDigest: request.Digest(), Kind: domain.CoordinationSpawn,
			LinkID: linkID, State: requestState, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
		}); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "thread_link", string(linkID), "thread_link.created", 1, nil)
	})
	if failure != nil {
		return *failure
	}
	if errors.Is(err, domain.ErrConflict) {
		return coordproto.Fail(coordproto.CodePending, "request is being processed; retry with the same key")
	}
	if err != nil {
		return coordproto.Fail(coordproto.CodeInternal, "spawn failed")
	}
	if !held {
		_ = s.queue.Enqueue(ctx, intent.CapsuleID)
	}
	s.invalidateCoordinationPush(parent.ID)
	return s.linkResponse(ctx, linkID)
}

var errCoordinationRejected = errors.New("coordination request rejected")

// liveTreeCapsules counts the grant's children whose Capsules hold or are
// about to hold compute. Detached subagents count only toward installation
// limits.
func (s *Service) liveTreeCapsules(
	ctx context.Context, reader ports.Reader, grantID domain.CoordinationGrantID,
) (int, error) {
	links, err := reader.ListThreadLinksByGrant(ctx, grantID)
	if err != nil {
		return 0, err
	}
	live := 0
	for _, link := range links {
		if link.State == domain.LinkPendingApproval ||
			link.CloseReason == domain.LinkDetached || link.CloseReason == domain.LinkDenied {
			continue
		}
		capsule, err := reader.GetCapsule(ctx, link.ChildCapsuleID)
		if err != nil {
			return 0, err
		}
		switch capsule.State {
		case domain.CapsulePaused, domain.CapsuleSealed, domain.CapsuleFailed, domain.CapsuleDeleted:
		default:
			if capsule.DesiredState != domain.IntentDeleted {
				live++
			}
		}
	}
	return live, nil
}

func (s *Service) linkResponse(ctx context.Context, id domain.ThreadLinkID) coordproto.Response {
	link, err := s.GetThreadLink(ctx, id)
	if err != nil {
		return coordproto.Fail(coordproto.CodeInternal, "link lookup failed")
	}
	response := coordproto.OK()
	view := s.linkView(ctx, link)
	response.Link = &view
	return response
}

func (s *Service) coordSend(
	ctx context.Context,
	grant domain.CoordinationGrant,
	parent domain.Thread,
	capsule domain.Capsule,
	request coordproto.Request,
) coordproto.Response {
	record, failure := s.existingCoordinationRequest(ctx, capsule, request)
	if failure != nil {
		return *failure
	}
	link, ok := s.ownedLink(ctx, parent.ID, request.Link)
	if !ok {
		return coordproto.Fail(coordproto.CodeNotFound, "no such link")
	}
	if record.ID == "" {
		if link.State == domain.LinkPendingApproval {
			return coordproto.Fail(coordproto.CodeUnavailable, "spawn is waiting for operator approval")
		}
		if link.State != domain.LinkActive {
			return coordproto.Fail(coordproto.CodeLinkClosed, "link is closed")
		}
		if !link.ArchivedAt.IsZero() {
			return coordproto.Fail(coordproto.CodeUnavailable, "subagent is archived; the operator can resume it")
		}
		if link.MessagesForwarded >= grant.MaxMessagesPerLink {
			return coordproto.Fail(coordproto.CodeLimitExceeded, "message limit for this link reached")
		}
	}
	key := "coord-" + digestKey(string(capsule.ID), request.Key)
	frame := func(messageID domain.ThreadMessageID, thread domain.Thread, saved persistedThreadBlock) adapterproto.Frame {
		return adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
			ID: string(messageID), SessionID: string(thread.ID), Role: adapterproto.RoleUser,
			Content: saved.Content,
		}
	}
	payload := persistedThreadBlock{Content: request.Prompt, Origin: &ThreadOrigin{
		Source: OriginCoordination, Kind: OriginFromLead, LinkIDs: []domain.ThreadLinkID{link.ID},
	}}
	var rejected *coordproto.Response
	result, err := s.appendAndSendHook(ctx, link.ChildThreadID, 0, key, "coordination",
		domain.ThreadRoleUser, domain.ThreadMessagePrompt, domain.ThreadBlockText, payload, frame,
		func(tx ports.Transaction, _ domain.Thread, messageID domain.ThreadMessageID, now time.Time) error {
			current, err := tx.GetThreadLink(ctx, link.ID)
			if err != nil {
				return err
			}
			if current.State != domain.LinkActive {
				response := coordproto.Fail(coordproto.CodeLinkClosed, "link is closed")
				rejected = &response
				return errCoordinationRejected
			}
			if current.MessagesForwarded >= grant.MaxMessagesPerLink {
				response := coordproto.Fail(coordproto.CodeLimitExceeded, "message limit for this link reached")
				rejected = &response
				return errCoordinationRejected
			}
			previous := current.ResourceVersion
			current.MessagesForwarded++
			current.UpdatedAt = now
			current.ResourceVersion++
			if err := tx.UpdateThreadLink(ctx, current, previous); err != nil {
				return err
			}
			if err := tx.InsertCoordinationRequest(ctx, domain.CoordinationRequest{
				ID: domain.CoordinationRequestID(s.ids.NewID()), GrantID: grant.ID, CapsuleID: capsule.ID,
				Key: request.Key, CanonicalDigest: request.Digest(), Kind: domain.CoordinationSend,
				LinkID: link.ID, ResultMessageID: messageID, CreatedAt: now,
			}); err != nil {
				return err
			}
			return s.appendEvent(ctx, tx, "thread_link", string(link.ID), "thread_link.message_forwarded",
				current.ResourceVersion, []byte(fmt.Sprintf(`{"bytes":%d}`, len(request.Prompt))))
		})
	if rejected != nil {
		return *rejected
	}
	if err != nil && result.MessageID == "" {
		switch {
		case errors.Is(err, domain.ErrNotFound):
			return coordproto.Fail(coordproto.CodeUnavailable, "child is still starting; retry later")
		case errors.Is(err, domain.ErrIllegalTransition):
			return coordproto.Fail(coordproto.CodeUnavailable, "child session is not accepting input")
		case errors.Is(err, domain.ErrConflict):
			return coordproto.Fail(coordproto.CodePending, "request is being processed; retry with the same key")
		default:
			return coordproto.Fail(coordproto.CodeInternal, "send failed")
		}
	}
	s.invalidateCoordinationPush(parent.ID)
	response := coordproto.OK()
	response.MessageID = string(result.MessageID)
	return response
}

func (s *Service) coordReport(
	ctx context.Context, capsule domain.Capsule, request coordproto.Request,
) coordproto.Response {
	grant, link, thread, failure := s.childCoordinationAuthority(ctx, capsule)
	if failure != nil {
		s.auditRejection(ctx, grant, request.Type, *failure)
		return *failure
	}
	if record, existing := s.existingCoordinationRequest(ctx, capsule, request); existing != nil {
		return *existing
	} else if record.ID != "" {
		return s.reportResponse(ctx, record.LinkID)
	}
	status := domain.ReportStatus(request.Status)
	payload := persistedThreadBlock{Content: request.Summary}
	result, err := s.appendAndSendHook(ctx, thread.ID, 0, request.Key, "coord-report",
		domain.ThreadRoleAssistant, domain.ThreadMessageResponse, domain.ThreadBlockText,
		payload, nil,
		func(tx ports.Transaction, saved domain.Thread, messageID domain.ThreadMessageID, now time.Time) error {
			current, err := tx.GetThreadLink(ctx, link.ID)
			if err != nil {
				return err
			}
			if current.State != domain.LinkActive || !current.ArchivedAt.IsZero() {
				return errCoordinationRejected
			}
			previous := current.ResourceVersion
			current.ReportSeq++
			current.ReportMessageSeq = int(saved.MessageCount)
			current.LastReportStatus, current.LastReportReason, current.LastReportAt = status, "", now
			current.UpdatedAt = now
			current.ResourceVersion++
			if err := tx.UpdateThreadLink(ctx, current, previous); err != nil {
				return err
			}
			if err := tx.InsertCoordinationRequest(ctx, domain.CoordinationRequest{
				ID: domain.CoordinationRequestID(s.ids.NewID()), GrantID: grant.ID, CapsuleID: capsule.ID,
				Key: request.Key, CanonicalDigest: request.Digest(), Kind: domain.CoordinationReport,
				LinkID: link.ID, ResultMessageID: messageID, State: domain.RequestCompleted,
				CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
			}); err != nil {
				return err
			}
			return s.appendEvent(ctx, tx, "thread_link", string(link.ID), "thread_link.reported",
				current.ResourceVersion, []byte(`{"status":"`+string(status)+`"}`))
		})
	if errors.Is(err, errCoordinationRejected) {
		return coordproto.Fail(coordproto.CodeLinkClosed, "this subagent is no longer reporting")
	}
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrNotFound):
			return coordproto.Fail(coordproto.CodeUnavailable, "subagent session is still starting; retry later")
		case errors.Is(err, domain.ErrIllegalTransition):
			return coordproto.Fail(coordproto.CodeUnavailable, "subagent session is not accepting a report")
		case errors.Is(err, domain.ErrConflict):
			return coordproto.Fail(coordproto.CodePending, "request is being processed; retry with the same key")
		default:
			return coordproto.Fail(coordproto.CodeInternal, "report failed")
		}
	}
	_ = result
	s.refreshChildGit(ctx, link)
	s.invalidateCoordinationPush(link.ParentThreadID)
	return s.reportResponse(ctx, link.ID)
}

func (s *Service) reportResponse(ctx context.Context, id domain.ThreadLinkID) coordproto.Response {
	link, err := s.GetThreadLink(ctx, id)
	if err != nil {
		return coordproto.Fail(coordproto.CodeInternal, "report lookup failed")
	}
	response := coordproto.OK()
	view := s.linkView(ctx, link)
	response.Link = &view
	if link.ReportSeq > 0 {
		response.Reports = []coordproto.Report{s.report(ctx, link)}
	}
	return response
}

func (s *Service) childCoordinationAuthority(
	ctx context.Context, capsule domain.Capsule,
) (domain.CoordinationGrant, domain.ThreadLink, domain.Thread, *coordproto.Response) {
	fail := func(code, message string) (domain.CoordinationGrant, domain.ThreadLink, domain.Thread, *coordproto.Response) {
		response := coordproto.Fail(code, message)
		return domain.CoordinationGrant{}, domain.ThreadLink{}, domain.Thread{}, &response
	}
	if capsule.LinkID == "" {
		return fail(coordproto.CodeInvalidRequest, "only a subagent Thread can report")
	}
	var grant domain.CoordinationGrant
	var link domain.ThreadLink
	var thread domain.Thread
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		link, err = reader.GetThreadLink(ctx, capsule.LinkID)
		if err != nil {
			return err
		}
		if link.ChildCapsuleID != capsule.ID {
			return domain.ErrNotFound
		}
		if grant, err = reader.GetCoordinationGrant(ctx, link.GrantID); err != nil {
			return err
		}
		thread, err = reader.GetThread(ctx, link.ChildThreadID)
		return err
	})
	if err != nil {
		return fail(coordproto.CodeCoordinationDisabled, "this Thread has no coordination grant")
	}
	if link.State != domain.LinkActive || !link.ArchivedAt.IsZero() {
		return fail(coordproto.CodeLinkClosed, "this subagent is no longer reporting")
	}
	if !grant.Usable(s.clock.Now()) {
		return fail(coordproto.CodeGrantInactive, "coordination grant is not active")
	}
	if thread.State != domain.ThreadActive || thread.CapsuleID != capsule.ID {
		return fail(coordproto.CodeGrantInactive, "subagent Thread is not active")
	}
	if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady || capsule.Maintenance != "" {
		return fail(coordproto.CodeUnavailable, "subagent Capsule is not Ready")
	}
	return grant, link, thread, nil
}

func (s *Service) coordPatch(
	ctx context.Context, parent domain.Thread, request coordproto.Request,
) coordproto.Response {
	link, ok := s.ownedLink(ctx, parent.ID, request.Link)
	if !ok {
		return coordproto.Fail(coordproto.CodeNotFound, "no such link")
	}
	if s.runtime == nil {
		return coordproto.Fail(coordproto.CodeUnavailable, "git patch is not available")
	}
	capsule, err := s.GetCapsule(ctx, link.ChildCapsuleID)
	if err != nil {
		return coordproto.Fail(coordproto.CodeNotFound, "no such subagent")
	}
	if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady || capsule.Maintenance != "" {
		return coordproto.Fail(coordproto.CodeUnavailable, "subagent Capsule is not Ready")
	}
	maxBytes := request.MaxBytes
	if maxBytes == 0 {
		maxBytes = coordproto.DefaultPatchBytes
	}
	result, err := s.runtime.GitPatch(ctx, capsule.ProviderResourceID, ports.GitPatchRequest{
		BaseCommit: link.BaseCommit, MaxBytes: maxBytes, IncludePatch: true,
	})
	if err != nil {
		if errors.Is(err, domain.ErrUnsupported) {
			return coordproto.Fail(coordproto.CodeUnavailable, "git patch is not available")
		}
		return coordproto.Fail(coordproto.CodeUnavailable, "git patch failed")
	}
	s.storeGitMetadata(ctx, link, result)
	if fresh, err := s.GetThreadLink(ctx, link.ID); err == nil {
		link = fresh
	}
	response := coordproto.OK()
	view := s.linkView(ctx, link)
	response.Link = &view
	response.Patch = result.Patch
	response.Truncated = result.Truncated
	response.Head = result.Head
	response.BaseCommit = link.BaseCommit
	if response.BaseCommit == "" {
		response.BaseCommit = result.BaseCommit
	}
	response.FilesChanged = result.FilesChanged
	response.Insertions = result.Insertions
	response.Deletions = result.Deletions
	s.invalidateCoordinationPush(parent.ID)
	return response
}

func (s *Service) refreshChildGit(ctx context.Context, link domain.ThreadLink) {
	if s.runtime == nil || link.State != domain.LinkActive {
		return
	}
	capsule, err := s.GetCapsule(ctx, link.ChildCapsuleID)
	if err != nil || capsule.State != domain.CapsuleReady || capsule.ProviderResourceID == "" {
		return
	}
	result, err := s.runtime.GitPatch(ctx, capsule.ProviderResourceID, ports.GitPatchRequest{
		BaseCommit: link.BaseCommit,
	})
	if err != nil {
		return
	}
	s.storeGitMetadata(ctx, link, result)
}

func (s *Service) storeGitMetadata(ctx context.Context, link domain.ThreadLink, result ports.GitPatchResult) {
	head := strings.ToLower(strings.TrimSpace(result.Head))
	base := strings.ToLower(strings.TrimSpace(result.BaseCommit))
	if link.BaseCommit == "" {
		if base == "" {
			base = head
		}
	} else {
		base = link.BaseCommit
	}
	if (head != "" && !validStoredGitSHA(head)) || (base != "" && !validStoredGitSHA(base)) {
		return
	}
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetThreadLink(ctx, link.ID)
		if err != nil {
			return err
		}
		previous := current.ResourceVersion
		if current.BaseCommit == "" {
			current.BaseCommit = base
		}
		current.Head = head
		current.FilesChanged = max(result.FilesChanged, 0)
		current.Insertions = max(result.Insertions, 0)
		current.Deletions = max(result.Deletions, 0)
		if current.BaseCommit == link.BaseCommit && current.Head == link.Head &&
			current.FilesChanged == link.FilesChanged && current.Insertions == link.Insertions &&
			current.Deletions == link.Deletions {
			return nil
		}
		current.UpdatedAt = s.clock.Now().UTC()
		current.ResourceVersion++
		return tx.UpdateThreadLink(ctx, current, previous)
	})
	if err == nil {
		s.invalidateCoordinationPush(link.ParentThreadID)
	}
}

func validStoredGitSHA(value string) bool {
	if len(value) < 7 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (s *Service) coordCancel(
	ctx context.Context,
	grant domain.CoordinationGrant,
	parent domain.Thread,
	capsule domain.Capsule,
	request coordproto.Request,
) coordproto.Response {
	record, failure := s.existingCoordinationRequest(ctx, capsule, request)
	if failure != nil {
		return *failure
	}
	link, ok := s.ownedLink(ctx, parent.ID, request.Link)
	if !ok {
		return coordproto.Fail(coordproto.CodeNotFound, "no such link")
	}
	if record.ID == "" && (link.State == domain.LinkActive || link.State == domain.LinkPendingApproval) {
		err := s.store.Transact(ctx, func(tx ports.Transaction) error {
			current, err := tx.GetThreadLink(ctx, link.ID)
			if err != nil {
				return err
			}
			now := s.clock.Now().UTC()
			if current.State == domain.LinkPendingApproval {
				if err := s.rejectPendingSpawnTx(ctx, tx, current, now); err != nil {
					return err
				}
			}
			if current.State == domain.LinkActive || current.State == domain.LinkPendingApproval {
				if err := s.closeLinkTx(ctx, tx, current, domain.LinkCancelled,
					domain.ReportFailed, domain.ReportCancelled, 0, now); err != nil {
					return err
				}
			}
			return tx.InsertCoordinationRequest(ctx, domain.CoordinationRequest{
				ID: domain.CoordinationRequestID(s.ids.NewID()), GrantID: grant.ID, CapsuleID: capsule.ID,
				Key: request.Key, CanonicalDigest: request.Digest(), Kind: domain.CoordinationCancel,
				LinkID: link.ID, State: domain.RequestCompleted, CreatedAt: now, UpdatedAt: now,
				ResourceVersion: 1,
			})
		})
		if errors.Is(err, domain.ErrConflict) {
			return coordproto.Fail(coordproto.CodePending, "request is being processed; retry with the same key")
		}
		if err != nil {
			return coordproto.Fail(coordproto.CodeInternal, "cancel failed")
		}
	}
	if fresh, err := s.GetThreadLink(ctx, link.ID); err == nil {
		s.stopClosedChild(ctx, fresh)
	}
	s.invalidateCoordinationPush(parent.ID)
	return s.linkResponse(ctx, link.ID)
}

// countOperatorMessage records, without content, that the operator messaged a
// subagent directly so the lead's next wake can say so. Closed links are left
// untouched: the lead can no longer address the subagent, and a closed link's
// update time bounds how long the sweep may stop the subagent.
func (s *Service) countOperatorMessage(ctx context.Context) appendTxHook {
	return func(tx ports.Transaction, thread domain.Thread, _ domain.ThreadMessageID, now time.Time) error {
		if err := s.unarchiveTx(ctx, tx, thread.ID, now); err != nil {
			return err
		}
		link, err := tx.GetThreadLinkByChildThread(ctx, thread.ID)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if link.State != domain.LinkActive {
			return nil
		}
		previous := link.ResourceVersion
		link.OperatorMessages++
		link.UpdatedAt = now
		link.ResourceVersion++
		if err := tx.UpdateThreadLink(ctx, link, previous); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "thread_link", string(link.ID), "thread_link.operator_messaged",
			link.ResourceVersion, nil)
	}
}

func digestKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// linkView derives a content-free status for one child.
func (s *Service) linkView(ctx context.Context, link domain.ThreadLink) coordproto.Link {
	view := coordproto.Link{
		ID: string(link.ID), Name: link.Name, Harness: link.Harness, State: string(link.State),
		CloseReason: string(link.CloseReason), ReportSeq: link.ReportSeq,
		LastReportStatus: string(link.LastReportStatus), MessagesForwarded: link.MessagesForwarded,
		Head: link.Head, BaseCommit: link.BaseCommit, FilesChanged: link.FilesChanged,
		Insertions: link.Insertions, Deletions: link.Deletions,
	}
	view.Phase = s.linkPhase(ctx, link)
	return view
}

// ThreadLinkPhase is the content-free phase of a link's child.
func (s *Service) ThreadLinkPhase(ctx context.Context, link domain.ThreadLink) string {
	if requirePrincipal(ctx) != nil {
		return ""
	}
	return s.linkPhase(ctx, link)
}

// linkPhase is the one derivation behind a lead's subagent panel and its
// board rollup, so the two never disagree.
func (s *Service) linkPhase(ctx context.Context, link domain.ThreadLink) string {
	if link.State == domain.LinkPendingApproval {
		return coordproto.PhaseAwaiting
	}
	if link.State == domain.LinkClosed {
		if link.CloseReason == domain.LinkFailed || s.childRunFailed(ctx, link) {
			return coordproto.PhaseFailed
		}
		return coordproto.PhaseEnded
	}
	intent, err := s.GetProjectThreadIntent(ctx, link.ChildIntentID)
	if err != nil {
		return coordproto.PhaseProvisioning
	}
	switch intent.Intent.State {
	case domain.ProjectThreadFailed:
		return coordproto.PhaseFailed
	case domain.ProjectThreadProvisioning:
		return coordproto.PhaseProvisioning
	}
	thread, err := s.GetThread(ctx, link.ChildThreadID)
	if err != nil || thread.State != domain.ThreadActive || thread.CurrentRunID == "" {
		return coordproto.PhaseEnded
	}
	if run, err := s.GetRunStored(ctx, thread.CurrentRunID); err == nil && run.State.Terminal() {
		if run.State == domain.RunFailed {
			return coordproto.PhaseFailed
		}
		return coordproto.PhaseEnded
	}
	attention, err := s.ThreadAttention(ctx, thread)
	if err != nil {
		return coordproto.PhaseWorking
	}
	if attention.Awaiting != nil {
		return coordproto.PhaseAwaiting
	}
	if attention.Turn == ThreadTurnIdle {
		return coordproto.PhaseIdle
	}
	return coordproto.PhaseWorking
}

// childRunFailed reports whether a closed link's child session ended because
// its Run failed, as when a harness times out.
func (s *Service) childRunFailed(ctx context.Context, link domain.ThreadLink) bool {
	thread, err := s.GetThread(ctx, link.ChildThreadID)
	if err != nil || thread.CurrentRunID == "" {
		return false
	}
	run, err := s.GetRunStored(ctx, thread.CurrentRunID)
	return err == nil && run.State == domain.RunFailed
}

// reportSummary re-derives a report's text from the child's encrypted
// transcript. Nothing outside the transcript stores it.
func (s *Service) reportSummary(ctx context.Context, link domain.ThreadLink) (string, bool) {
	if link.ReportMessageSeq < 1 {
		return "", false
	}
	thread, err := s.GetThread(ctx, link.ChildThreadID)
	if err != nil || thread.State == domain.ThreadDeleted {
		return "", false
	}
	var messages []domain.ThreadMessage
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		messages, _, err = reader.ListThreadMessages(ctx, thread.ID, int64(link.ReportMessageSeq-1), 1)
		return err
	}); err != nil || len(messages) != 1 {
		return "", false
	}
	payload, err := s.decryptPersistedMessage(thread, messages[0])
	if err != nil {
		return "", false
	}
	return coordproto.TruncateUTF8(payload.Content, coordproto.MaxReportSummaryBytes)
}

func (s *Service) report(ctx context.Context, link domain.ThreadLink) coordproto.Report {
	summary, truncated := s.reportSummary(ctx, link)
	return coordproto.Report{
		Link: string(link.ID), Name: link.Name, Seq: link.ReportSeq,
		Status: string(link.LastReportStatus), Reason: string(link.LastReportReason),
		Summary: summary, Truncated: truncated,
	}
}

func coordinationSelf(grant domain.CoordinationGrant) *coordproto.Self {
	return &coordproto.Self{
		Role: "lead", Depth: 0, GrantID: string(grant.ID), MaxChildren: grant.MaxChildren,
		MaxDescendants: grant.MaxDescendants, MaxLiveCapsules: grant.MaxLiveCapsules,
		MaxMessagesPerLink: grant.MaxMessagesPerLink, AllowedHarnesses: slices.Clone(grant.AllowedHarnesses),
		DeadlineAt: grant.DeadlineAt,
	}
}

func coordinationSelfSub(grant domain.CoordinationGrant) *coordproto.Self {
	self := coordinationSelf(grant)
	self.Role = "sub"
	self.Depth = 1
	return self
}

func coordinationConfigFor(grant domain.CoordinationGrant, role domain.CapsuleRole) coordproto.Config {
	self := coordinationSelf(grant)
	if role == domain.CapsuleSub {
		self = coordinationSelfSub(grant)
	}
	return coordproto.Config{Enabled: true, Self: self}
}

// configureCoordinationMailbox enables the mailbox before a harness starts so
// coordination tools are listed. Leads get spawn/wait/patch; subagents get
// report and whoami. Failure leaves the mailbox disabled.
func (s *Service) configureLeadMailbox(ctx context.Context, capsuleID domain.CapsuleID, threadID domain.ThreadID) {
	s.configureCoordinationMailbox(ctx, capsuleID, threadID)
}

func (s *Service) configureCoordinationMailbox(ctx context.Context, capsuleID domain.CapsuleID, threadID domain.ThreadID) {
	if s.coordination == nil {
		return
	}
	var grant domain.CoordinationGrant
	var capsule domain.Capsule
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		capsule, err = reader.GetCapsule(ctx, capsuleID)
		if err != nil {
			return err
		}
		grant, err = reader.GetCoordinationGrantByRootThread(ctx, threadID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) || capsule.LinkID == "" {
			return err
		}
		link, err := reader.GetThreadLink(ctx, capsule.LinkID)
		if err != nil {
			return err
		}
		if link.ChildCapsuleID != capsule.ID {
			return domain.ErrNotFound
		}
		grant, err = reader.GetCoordinationGrant(ctx, link.GrantID)
		return err
	}); err != nil || grant.State != domain.GrantActive {
		return
	}
	role := domain.CapsuleMain
	if grant.RootCapsuleID != capsule.ID {
		role = domain.CapsuleSub
	}
	_ = s.coordination.ConfigureCoordination(ctx, capsule.ProviderResourceID, coordinationConfigFor(grant, role))
}

// lastAssistantMessage returns the sequence of the newest assistant response
// in the attention window, or zero.
func (s *Service) lastAssistantMessage(ctx context.Context, thread domain.Thread) (int, error) {
	start := thread.MessageCount - threadAttentionWindow
	if start < 0 {
		start = 0
	}
	var messages []domain.ThreadMessage
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		messages, _, err = reader.ListThreadMessages(ctx, thread.ID, start, threadAttentionWindow)
		return err
	}); err != nil {
		return 0, err
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == domain.ThreadRoleAssistant && message.Kind == domain.ThreadMessageResponse {
			return int(message.Sequence), nil
		}
	}
	return 0, nil
}
