package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

type ProposeDeliveryInput struct {
	CapsuleID      domain.CapsuleID
	Title          string
	Body           string
	Branch         string
	IdempotencyKey string
}

type ApproveDeliveryInput struct {
	ExpectedCapsuleVersion domain.ResourceVersion
	ExpectedHEAD           string
	ExpectedTree           string
	RemoteBranch           string
	CommitMessage          string
	PullRequestTitle       string
	PullRequestBody        string
	BaseBranch             string
	IdempotencyKey         string
}

func (s *Service) ProposeDelivery(
	ctx context.Context, input ProposeDeliveryInput,
) (domain.Delivery, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.Delivery{}, err
	}
	if s.delivery == nil {
		return domain.Delivery{}, domain.ErrUnsupported
	}
	if err := requireIdempotency(input.IdempotencyKey); err != nil {
		return domain.Delivery{}, err
	}
	if input.Title == "" || len(input.Title) > 512 || !utf8.ValidString(input.Title) ||
		strings.ContainsAny(input.Title, "\x00\r\n") ||
		!coordproto.ValidBranch(input.Branch) ||
		len(input.Body) > 64<<10 || !utf8.ValidString(input.Body) ||
		strings.ContainsRune(input.Body, 0) {
		return domain.Delivery{}, fmt.Errorf("%w: invalid Delivery proposal", domain.ErrInvalid)
	}
	capsule, err := s.readyCapsule(ctx, input.CapsuleID)
	if err != nil {
		return domain.Delivery{}, err
	}
	project, err := s.GetProject(ctx, capsule.ProjectID)
	if err != nil {
		return domain.Delivery{}, err
	}
	inspection, err := s.delivery.InspectDelivery(ctx, capsule.ProviderResourceID)
	if err != nil {
		return domain.Delivery{}, safeDeliveryError(err)
	}
	if refusedDeliveryBranch(input.Branch, project.DefaultBaseBranch, inspection.DefaultBranch) {
		return domain.Delivery{}, fmt.Errorf("%w: delivery to a protected/default branch is refused", domain.ErrIllegalTransition)
	}
	commitMessage := ""
	if inspection.Dirty {
		commitMessage = input.Title
	}
	now := s.clock.Now().UTC()
	proposed := domain.Delivery{
		ID: domain.DeliveryID(s.ids.NewID()), CapsuleID: capsule.ID, ProjectID: project.ID,
		State: domain.DeliveryProposed, Action: domain.DeliveryOpenPullRequest,
		ProposedBy: domain.ProposedByAgent, RemoteBranch: input.Branch,
		DestinationRef: "refs/heads/" + input.Branch, CommitMessage: commitMessage,
		PullRequestTitle: input.Title, PullRequestBody: input.Body,
		IdempotencyKey: input.IdempotencyKey, CreatedAt: now, UpdatedAt: now,
		ResourceVersion: 1,
	}
	if err := proposed.Validate(); err != nil {
		return domain.Delivery{}, err
	}
	var result domain.Delivery
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if existing, found, err := lookupDeliveryProposal(ctx, tx, capsule.ID, input.IdempotencyKey); err != nil {
			return err
		} else if found {
			if existing.PullRequestTitle != input.Title || existing.PullRequestBody != input.Body ||
				existing.RemoteBranch != input.Branch {
				return fmt.Errorf("%w: key was already used for a different proposal", domain.ErrConflict)
			}
			result = existing
			return nil
		}
		open, err := openDeliveryForCapsule(ctx, tx, capsule.ID)
		if err != nil {
			return err
		}
		if open.ID != "" {
			return fmt.Errorf("%w: a Delivery is already proposed or in flight", domain.ErrConflict)
		}
		if err := tx.InsertDelivery(ctx, proposed); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "delivery", string(proposed.ID),
			"delivery.proposed", proposed.ResourceVersion, nil); err != nil {
			return err
		}
		result = proposed
		return nil
	})
	return result, err
}

func (s *Service) ApproveDelivery(
	ctx context.Context, id domain.DeliveryID, expected domain.ResourceVersion, input ApproveDeliveryInput,
) (domain.Delivery, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.Delivery{}, err
	}
	if s.delivery == nil {
		return domain.Delivery{}, domain.ErrUnsupported
	}
	if err := requireIdempotency(input.IdempotencyKey); err != nil {
		return domain.Delivery{}, err
	}
	scope := "delivery:approve:" + string(id)
	var replay domain.Delivery
	var replayed bool
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		replay, replayed, err = getReplay[domain.Delivery](ctx, reader, scope, input.IdempotencyKey)
		if err == nil && replayed {
			replay, err = reader.GetDelivery(ctx, replay.ID)
		}
		return err
	}); err != nil {
		return domain.Delivery{}, err
	}
	if replayed {
		return replay, nil
	}

	current, err := s.GetDelivery(ctx, id)
	if err != nil {
		return domain.Delivery{}, err
	}
	lock := &s.deliverySync[deliveryLockIndex(current.CapsuleID)]
	lock.Lock()
	defer lock.Unlock()
	current, err = s.GetDelivery(ctx, id)
	if err != nil {
		return domain.Delivery{}, err
	}
	if current.ResourceVersion != expected {
		return domain.Delivery{}, domain.ErrConflict
	}
	if current.State != domain.DeliveryProposed {
		return domain.Delivery{}, fmt.Errorf("%w: Delivery is not proposed", domain.ErrIllegalTransition)
	}
	if input.RemoteBranch != "" {
		current.RemoteBranch = input.RemoteBranch
		current.DestinationRef = "refs/heads/" + input.RemoteBranch
	}
	if input.CommitMessage != "" {
		current.CommitMessage = input.CommitMessage
	}
	if input.PullRequestTitle != "" {
		current.PullRequestTitle = input.PullRequestTitle
	}
	if input.PullRequestBody != "" {
		current.PullRequestBody = input.PullRequestBody
	}
	if input.BaseBranch != "" {
		current.BaseBranch = input.BaseBranch
	}
	create := CreateDeliveryInput{
		CapsuleID: current.CapsuleID, Action: current.Action, Approved: true,
		ExpectedCapsuleVersion: input.ExpectedCapsuleVersion,
		ExpectedHEAD:           input.ExpectedHEAD, ExpectedTree: input.ExpectedTree,
		RemoteBranch: current.RemoteBranch, CommitMessage: current.CommitMessage,
		PullRequestTitle: current.PullRequestTitle, PullRequestBody: current.PullRequestBody,
		BaseBranch: current.BaseBranch, IdempotencyKey: current.IdempotencyKey,
	}
	if err := validateCreateDelivery(create); err != nil {
		return domain.Delivery{}, err
	}

	capsule, project, err := s.acquireMaintenance(
		ctx, current.CapsuleID, input.ExpectedCapsuleVersion, "capture",
	)
	if err != nil {
		return domain.Delivery{}, err
	}
	keepMaintenance := false
	defer func() {
		if !keepMaintenance {
			_ = s.clearMaintenance(context.WithoutCancel(ctx), current.CapsuleID, "capture")
		}
	}()
	if err := validateDeliveryProject(project, current.Action); err != nil {
		return domain.Delivery{}, err
	}
	if err := s.validateDeliverySecrets(ctx, project, current.Action); err != nil {
		return domain.Delivery{}, err
	}
	inspection, err := s.delivery.InspectDelivery(ctx, capsule.ProviderResourceID)
	if err != nil {
		return domain.Delivery{}, safeDeliveryError(err)
	}
	if inspection.HEAD != input.ExpectedHEAD || inspection.Tree != input.ExpectedTree {
		return domain.Delivery{}, fmt.Errorf("%w: reviewed Git objects changed", domain.ErrConflict)
	}
	if !sameRepository(project.RepositoryURL, inspection.OriginURL) {
		return domain.Delivery{}, fmt.Errorf("%w: Capsule origin does not match Project repository", domain.ErrConflict)
	}
	if inspection.Dirty && current.CommitMessage == "" {
		return domain.Delivery{}, fmt.Errorf("%w: a dirty tree requires a commit message", domain.ErrIllegalTransition)
	}
	if inspection.Dirty && (project.CommitAuthorName == "" || project.CommitAuthorEmail == "") {
		return domain.Delivery{}, fmt.Errorf("%w: Project commit author is required", domain.ErrIllegalTransition)
	}
	if !inspection.Dirty && current.CommitMessage != "" {
		return domain.Delivery{}, fmt.Errorf("%w: a clean tree cannot be committed", domain.ErrIllegalTransition)
	}
	base := current.BaseBranch
	if base == "" {
		base = project.DefaultBaseBranch
	}
	if base == "" {
		base = inspection.DefaultBranch
	}
	if current.Action == domain.DeliveryOpenPullRequest {
		if base == "" || !validBranch(base) || base == current.RemoteBranch {
			return domain.Delivery{}, fmt.Errorf("%w: a distinct valid PR base branch is required", domain.ErrInvalid)
		}
		if _, _, err := parseGitHubRepository(project.RepositoryURL); err != nil {
			return domain.Delivery{}, fmt.Errorf("%w: open_pull_request requires a github.com repository", domain.ErrIllegalTransition)
		}
	}
	if refusedDeliveryBranch(current.RemoteBranch, project.DefaultBaseBranch, inspection.DefaultBranch) {
		return domain.Delivery{}, fmt.Errorf("%w: delivery to a protected/default branch is refused", domain.ErrIllegalTransition)
	}
	now := s.clock.Now().UTC()
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		fresh, err := tx.GetDelivery(ctx, current.ID)
		if err != nil {
			return err
		}
		if fresh.ResourceVersion != expected || fresh.State != domain.DeliveryProposed {
			return domain.ErrConflict
		}
		if existing, ok, err := getReplay[domain.Delivery](ctx, tx, scope, input.IdempotencyKey); err != nil {
			return err
		} else if ok {
			current, err = tx.GetDelivery(ctx, existing.ID)
			return err
		}
		previous := fresh.ResourceVersion
		fresh.Approved = true
		fresh.ApprovedAt = now
		fresh.ExpectedCapsuleVersion = input.ExpectedCapsuleVersion
		fresh.ExpectedHEAD = input.ExpectedHEAD
		fresh.ExpectedTree = input.ExpectedTree
		fresh.RemoteBranch = current.RemoteBranch
		fresh.DestinationRef = "refs/heads/" + current.RemoteBranch
		fresh.BaseBranch = base
		fresh.CommitMessage = current.CommitMessage
		fresh.PullRequestTitle = current.PullRequestTitle
		fresh.PullRequestBody = current.PullRequestBody
		if err := fresh.Transition(domain.DeliveryQueued, now); err != nil {
			return err
		}
		if err := tx.ApplyDeliveryApproval(ctx, fresh, previous); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "delivery", string(fresh.ID),
			"delivery.queued", fresh.ResourceVersion, nil); err != nil {
			return err
		}
		current = fresh
		return putReplay(ctx, tx, scope, input.IdempotencyKey, current, now)
	})
	if err != nil {
		return domain.Delivery{}, err
	}
	keepMaintenance = true
	return s.executeDelivery(context.WithoutCancel(ctx), current.ID)
}

func (s *Service) RejectDelivery(
	ctx context.Context, id domain.DeliveryID, expected domain.ResourceVersion, idempotencyKey string,
) (domain.Delivery, error) {
	if err := requirePrincipal(ctx); err != nil {
		return domain.Delivery{}, err
	}
	if err := requireIdempotency(idempotencyKey); err != nil {
		return domain.Delivery{}, err
	}
	scope := "delivery:reject:" + string(id)
	var replay domain.Delivery
	var replayed bool
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		replay, replayed, err = getReplay[domain.Delivery](ctx, reader, scope, idempotencyKey)
		if err == nil && replayed {
			replay, err = reader.GetDelivery(ctx, replay.ID)
		}
		return err
	}); err != nil {
		return domain.Delivery{}, err
	}
	if replayed {
		return replay, nil
	}
	now := s.clock.Now().UTC()
	var result domain.Delivery
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetDelivery(ctx, id)
		if err != nil {
			return err
		}
		if current.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if existing, ok, err := getReplay[domain.Delivery](ctx, tx, scope, idempotencyKey); err != nil {
			return err
		} else if ok {
			result, err = tx.GetDelivery(ctx, existing.ID)
			return err
		}
		if current.State != domain.DeliveryProposed {
			return fmt.Errorf("%w: Delivery is not proposed", domain.ErrIllegalTransition)
		}
		previous := current.ResourceVersion
		if err := current.Transition(domain.DeliveryRejected, now); err != nil {
			return err
		}
		if err := tx.ApplyDeliveryApproval(ctx, current, previous); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "delivery", string(current.ID),
			"delivery.rejected", current.ResourceVersion, nil); err != nil {
			return err
		}
		result = current
		return putReplay(ctx, tx, scope, idempotencyKey, current, now)
	})
	return result, err
}

func (s *Service) coordProposeDelivery(
	ctx context.Context, capsule domain.Capsule, request coordproto.Request,
) coordproto.Response {
	if s.delivery == nil {
		return coordproto.Fail(coordproto.CodeUnavailable, "delivery is not available")
	}
	var grant domain.CoordinationGrant
	if capsule.LinkID != "" {
		var failure *coordproto.Response
		grant, _, _, failure = s.childCoordinationAuthority(ctx, capsule)
		if failure != nil {
			return *failure
		}
	} else {
		var failure *coordproto.Response
		grant, _, failure = s.coordinationAuthority(ctx, capsule, true)
		if failure != nil {
			return *failure
		}
	}
	if !grant.Usable(s.clock.Now()) {
		return coordproto.Fail(coordproto.CodeGrantInactive, "coordination grant is not active")
	}
	result, err := s.ProposeDelivery(ctx, ProposeDeliveryInput{
		CapsuleID: capsule.ID, Title: request.Title, Body: request.Body,
		Branch: request.Branch, IdempotencyKey: request.Key,
	})
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrUnsupported):
			return coordproto.Fail(coordproto.CodeUnavailable, "delivery is not available")
		case errors.Is(err, domain.ErrConflict):
			return coordproto.Fail(coordproto.CodeConflict, "a Delivery is already proposed or in flight")
		case errors.Is(err, domain.ErrIllegalTransition):
			return coordproto.Fail(coordproto.CodeInvalidRequest, "delivery to a protected/default branch is refused")
		case errors.Is(err, domain.ErrInvalid):
			return coordproto.Fail(coordproto.CodeInvalidRequest, "invalid Delivery proposal")
		default:
			return coordproto.Fail(coordproto.CodeUnavailable, "could not inspect Capsule git state")
		}
	}
	response := coordproto.OK()
	response.Delivery = &coordproto.DeliveryView{
		ID: string(result.ID), State: string(result.State), Branch: result.RemoteBranch,
	}
	return response
}

func lookupDeliveryProposal(
	ctx context.Context, reader ports.Reader, capsuleID domain.CapsuleID, key string,
) (domain.Delivery, bool, error) {
	existing, err := reader.GetDeliveryByIdempotency(ctx, capsuleID, key)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Delivery{}, false, nil
	}
	if err != nil {
		return domain.Delivery{}, false, err
	}
	return existing, true, nil
}

func openDeliveryForCapsule(
	ctx context.Context, reader ports.Reader, capsuleID domain.CapsuleID,
) (domain.Delivery, error) {
	items, _, err := reader.ListDeliveries(ctx, capsuleID, ports.Page{Limit: 20})
	if err != nil {
		return domain.Delivery{}, err
	}
	for _, item := range items {
		if item.State == domain.DeliveryProposed || item.State.Executable() {
			return item, nil
		}
	}
	return domain.Delivery{}, nil
}
