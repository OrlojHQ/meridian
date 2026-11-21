package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
)

// MakeThreadLeadInput asks for a Coordination Grant on an existing plain
// Thread (ADR 0059 section 2).
type MakeThreadLeadInput struct {
	ThreadID       domain.ThreadID
	Expected       domain.ResourceVersion
	IdempotencyKey string
	Policy         domain.CoordinationPolicy
}

// MakeThreadLeadResult is the new grant and the Thread after it.
type MakeThreadLeadResult struct {
	Grant  domain.CoordinationGrant
	Thread domain.Thread
	// Restarted is true when the daemon stopped and resumed the session so
	// the harness learns its role now. Otherwise the role reaches the
	// harness at the next renewal point: before the next prompt, or when
	// the session next resumes.
	Restarted bool
}

type threadLeadOutcome struct {
	GrantID domain.CoordinationGrantID `json:"grantId"`
	Digest  string                     `json:"digest"`
}

// MakeThreadLead gives a plain Thread that has built up useful context a
// Coordination Grant, so it can start subagents. It is the operator's choice
// alone: only a user principal may call it, no meridian.coord.v1 verb reaches
// it, and it never happens on its own. The grant has the same authority as
// one created with a new lead Thread.
//
// A harness learns its role only from Start and Resume metadata, so the
// daemon then restarts the session the way ADR 0058 renews a stale one: if
// the Thread is idle, nothing waits on the operator, and the transcript holds
// resume state, the Run is stopped and resumed as a lead now. Otherwise the
// grant is recorded and the next prompt, or the next resume, delivers the
// role. A running turn is never interrupted and no prompt is invented.
func (s *Service) MakeThreadLead(ctx context.Context, input MakeThreadLeadInput) (MakeThreadLeadResult, error) {
	if err := requirePrincipal(ctx); err != nil {
		return MakeThreadLeadResult{}, err
	}
	// The installation bearer in team mode, a Capsule, and the system never
	// grant coordination on anyone's behalf, so for them the Thread reads as
	// not found, like any row outside scope.
	if caller, _ := principal.From(ctx); caller.Kind() != principal.KindUser {
		return MakeThreadLeadResult{}, domain.ErrNotFound
	}
	if s.structured == nil || s.transcriptKey == nil {
		return MakeThreadLeadResult{}, domain.ErrUnsupported
	}
	if s.coordination == nil {
		return MakeThreadLeadResult{}, fmt.Errorf("%w: provider does not support Thread coordination", domain.ErrUnsupported)
	}
	if input.Expected <= 0 {
		return MakeThreadLeadResult{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(input.IdempotencyKey); err != nil {
		return MakeThreadLeadResult{}, err
	}
	scope := "thread:" + string(input.ThreadID) + ":coordination-grant"
	var result MakeThreadLeadResult
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		replay, replayed, err := getReplay[threadLeadOutcome](ctx, tx, scope, input.IdempotencyKey)
		if err != nil {
			return err
		}
		thread, err := tx.GetThread(ctx, input.ThreadID)
		if err != nil {
			return err
		}
		capsule, err := tx.GetCapsule(ctx, thread.CapsuleID)
		if err != nil {
			return err
		}
		// The digest covers the request as sent, so a retry matches even if
		// the Project's allowlist has changed since.
		digest := threadLeadDigest(input)
		if replayed {
			if replay.Digest != digest {
				return fmt.Errorf("%w: Idempotency-Key was used with a different request", domain.ErrConflict)
			}
			result.Grant, err = tx.GetCoordinationGrant(ctx, replay.GrantID)
			result.Thread = thread
			return err
		}
		if thread.ResourceVersion != input.Expected {
			return domain.ErrConflict
		}
		if err := s.threadCanLead(ctx, tx, thread, capsule); err != nil {
			return err
		}
		policy, err := s.validateCoordinationPolicy(ctx, tx, capsule.ProjectID, thread.AdapterID, input.Policy)
		if err != nil {
			return err
		}
		// The event carries identifiers only.
		data, err := json.Marshal(struct {
			RootThreadID   domain.ThreadID `json:"rootThreadId"`
			ExistingThread bool            `json:"existingThread"`
		}{thread.ID, true})
		if err != nil {
			return err
		}
		result.Grant, err = s.insertCoordinationGrantTx(ctx, tx, capsule.ProjectID, thread.ID, capsule.ID,
			policy, data)
		if err != nil {
			return err
		}
		// The Thread's coordination position changed, so its version moves
		// and a concurrent request on the same version conflicts. A grant is
		// not activity: updated_at stays, so recency and idle pause do not
		// change.
		previous := thread.ResourceVersion
		thread.ResourceVersion++
		if err := tx.UpdateThread(ctx, thread, previous); err != nil {
			return err
		}
		result.Thread = thread
		return putReplay(ctx, tx, scope, input.IdempotencyKey,
			threadLeadOutcome{GrantID: result.Grant.ID, Digest: digest}, s.clock.Now().UTC())
	})
	if err != nil {
		return MakeThreadLeadResult{}, err
	}
	// Configure the lead mailbox before the harness resumes, so its
	// coordination tools are listed. Both steps below derive what to do
	// from stored state, so a replay finishes what a crash interrupted.
	s.configureCoordinationMailbox(ctx, result.Thread.CapsuleID, result.Thread.ID)
	result.Restarted = s.deliverLeadRole(ctx, result.Thread.ID)
	if thread, err := s.GetThread(ctx, result.Thread.ID); err == nil {
		result.Thread = thread
	}
	return result, nil
}

// threadCanLead refuses every Thread that a new grant would not make an
// ordinary lead: one that is not live with a structured session, a
// subagent, a promoted subagent still linked to its lead, a harness the
// Project does not allow as structured, and a Capsule that already holds a
// grant. A revoked grant is not replaced: revocation is the operator's
// terminal stop, and a Capsule has at most one grant (ADR 0059 section 2).
func (s *Service) threadCanLead(
	ctx context.Context, tx ports.Transaction, thread domain.Thread, capsule domain.Capsule,
) error {
	if thread.State != domain.ThreadActive {
		return fmt.Errorf("%w: only an active Thread can become a lead; resume it first", domain.ErrIllegalTransition)
	}
	if capsule.Role != domain.CapsuleMain {
		return fmt.Errorf("%w: a subagent cannot lead; promote and detach it first", domain.ErrIllegalTransition)
	}
	if capsule.LinkID != "" || capsule.LeadThreadID != "" {
		return fmt.Errorf("%w: a promoted subagent still linked to its lead cannot lead; detach it first",
			domain.ErrIllegalTransition)
	}
	project, err := tx.GetProject(ctx, capsule.ProjectID)
	if err != nil {
		return err
	}
	if err := projectAcceptsWork(project); err != nil {
		return err
	}
	if _, err := s.structuredHarnessImage(project, thread.AdapterID); err != nil {
		return fmt.Errorf("%w: harness %q is not an allowlisted structured harness of this Project",
			domain.ErrIllegalTransition, thread.AdapterID)
	}
	for _, lookup := range []func() (domain.CoordinationGrant, error){
		func() (domain.CoordinationGrant, error) { return tx.GetCoordinationGrantByRootThread(ctx, thread.ID) },
		func() (domain.CoordinationGrant, error) { return tx.GetCoordinationGrantByRootCapsule(ctx, capsule.ID) },
	} {
		grant, err := lookup()
		switch {
		case errors.Is(err, domain.ErrNotFound):
			continue
		case err != nil:
			return err
		case grant.State == domain.GrantActive && grant.RootThreadID == thread.ID:
			return fmt.Errorf("%w: Thread is already a lead", domain.ErrIllegalTransition)
		case grant.RootThreadID == thread.ID:
			return fmt.Errorf("%w: Thread's Coordination Grant was %s and cannot be replaced; start a new lead Thread",
				domain.ErrIllegalTransition, grant.State)
		default:
			return fmt.Errorf("%w: Capsule already holds a Coordination Grant from another Thread",
				domain.ErrIllegalTransition)
		}
	}
	return nil
}

func threadLeadDigest(input MakeThreadLeadInput) string {
	encoded, _ := json.Marshal(struct {
		Expected domain.ResourceVersion
		Policy   domain.CoordinationPolicy
	}{input.Expected, input.Policy})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// deliverLeadRole restarts the Thread's session now if that loses nothing,
// and reports whether it did. When it cannot, or the restart fails, the
// stored grant still makes the next renewal point deliver the role: the
// next prompt renews a live session whose role is stale, and a stopped
// session resumes as a lead.
func (s *Service) deliverLeadRole(ctx context.Context, id domain.ThreadID) bool {
	_, renewed, err := s.renewSession(ctx, id, 0)
	return err == nil && renewed
}
