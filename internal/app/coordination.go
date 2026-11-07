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
		CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
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
		policy.SpawnApproval != stored.SpawnApproval ||
		!slices.Equal(policy.AllowedHarnesses, stored.AllowedHarnesses) ||
		grant.DeadlineAt.Sub(grant.CreatedAt) != policy.Deadline {
		return fmt.Errorf("%w: Idempotency-Key was used with a different request", domain.ErrConflict)
	}
	return nil
}

func (s *Service) GetCoordinationGrant(
	ctx context.Context, id domain.CoordinationGrantID,
) (domain.CoordinationGrant, error) {
	var grant domain.CoordinationGrant
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		grant, err = reader.GetCoordinationGrant(ctx, id)
		return err
	})
	return grant, err
}

func (s *Service) GetThreadLink(ctx context.Context, id domain.ThreadLinkID) (domain.ThreadLink, error) {
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
			if link.State != domain.LinkActive {
				continue
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
	case coordproto.KindStatus:
		link, ok := s.ownedLink(ctx, thread.ID, request.Link)
		if !ok {
			return coordproto.Fail(coordproto.CodeNotFound, "no such link")
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
		link := domain.ThreadLink{
			ID: linkID, GrantID: current.ID, ParentThreadID: parent.ID, ParentCapsuleID: capsule.ID,
			ChildIntentID: intent.ID, ChildThreadID: intent.ThreadID, ChildCapsuleID: intent.CapsuleID,
			Depth: 1, Name: request.Name, Harness: request.Harness, State: domain.LinkActive,
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
			LinkID: linkID, CreatedAt: now,
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
	_ = s.queue.Enqueue(ctx, intent.CapsuleID)
	s.invalidateCoordinationPush(parent.ID)
	return s.linkResponse(ctx, linkID)
}

var errCoordinationRejected = errors.New("coordination request rejected")

// liveTreeCapsules counts the grant's children whose Capsules hold or are
// about to hold compute.
func (s *Service) liveTreeCapsules(
	ctx context.Context, reader ports.Reader, grantID domain.CoordinationGrantID,
) (int, error) {
	links, err := reader.ListThreadLinksByGrant(ctx, grantID)
	if err != nil {
		return 0, err
	}
	live := 0
	for _, link := range links {
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
		if link.State != domain.LinkActive {
			return coordproto.Fail(coordproto.CodeLinkClosed, "link is closed")
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
	if record.ID == "" && link.State == domain.LinkActive {
		err := s.store.Transact(ctx, func(tx ports.Transaction) error {
			current, err := tx.GetThreadLink(ctx, link.ID)
			if err != nil {
				return err
			}
			now := s.clock.Now().UTC()
			if current.State == domain.LinkActive {
				if err := s.closeLinkTx(ctx, tx, current, domain.LinkCancelled,
					domain.ReportFailed, domain.ReportCancelled, 0, now); err != nil {
					return err
				}
			}
			return tx.InsertCoordinationRequest(ctx, domain.CoordinationRequest{
				ID: domain.CoordinationRequestID(s.ids.NewID()), GrantID: grant.ID, CapsuleID: capsule.ID,
				Key: request.Key, CanonicalDigest: request.Digest(), Kind: domain.CoordinationCancel,
				LinkID: link.ID, CreatedAt: now,
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
// helper directly so the lead's next wake can say so. Closed links are left
// untouched: the lead can no longer address the helper, and a closed link's
// update time bounds how long the sweep may stop the helper.
func (s *Service) countOperatorMessage(ctx context.Context) appendTxHook {
	return func(tx ports.Transaction, thread domain.Thread, _ domain.ThreadMessageID, now time.Time) error {
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
	}
	view.Phase = s.linkPhase(ctx, link)
	return view
}

// ThreadLinkPhase is the content-free phase of a link's child.
func (s *Service) ThreadLinkPhase(ctx context.Context, link domain.ThreadLink) string {
	return s.linkPhase(ctx, link)
}

func (s *Service) linkPhase(ctx context.Context, link domain.ThreadLink) string {
	if link.State == domain.LinkClosed {
		if link.CloseReason == domain.LinkFailed {
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

// configureLeadMailbox enables the mailbox before a lead's harness starts so
// its coordination tools are listed. Failure leaves the mailbox disabled.
func (s *Service) configureLeadMailbox(ctx context.Context, capsuleID domain.CapsuleID, threadID domain.ThreadID) {
	if s.coordination == nil {
		return
	}
	var grant domain.CoordinationGrant
	var capsule domain.Capsule
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		grant, err = reader.GetCoordinationGrantByRootThread(ctx, threadID)
		if err != nil {
			return err
		}
		capsule, err = reader.GetCapsule(ctx, capsuleID)
		return err
	}); err != nil || grant.State != domain.GrantActive {
		return
	}
	_ = s.coordination.ConfigureCoordination(ctx, capsule.ProviderResourceID,
		coordproto.Config{Enabled: true, Self: coordinationSelf(grant)})
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
