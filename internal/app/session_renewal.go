package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/providergateway"
)

// sessionLease is what this daemon process knows about a structured
// session's harness: the gateway lease it was started with (ADR 0058) and
// the coordination role in its Start or Resume metadata (ADR 0059).
type sessionLease struct {
	id [32]byte
	// gateway is false for a session started without a provider
	// connection, which has no lease to renew.
	gateway bool
	role    string
}

// sessionRenewalMargin is how much lease a session must have left to take a
// new prompt without renewal. A turn that starts with less could outlive it.
var sessionRenewalMargin = 2 * time.Hour

func (s *Service) recordSessionLease(run domain.RunID, connection *harnesssetup.Connection, role string) {
	lease := sessionLease{role: role}
	if connection != nil && connection.Token != "" {
		lease.id, lease.gateway = providergateway.LeaseID(connection.Token), true
	}
	s.sessionLeasesMu.Lock()
	defer s.sessionLeasesMu.Unlock()
	if s.sessionLeases == nil {
		s.sessionLeases = map[domain.RunID]sessionLease{}
	}
	s.sessionLeases[run] = lease
}

func (s *Service) forgetSessionLease(run domain.RunID) {
	s.sessionLeasesMu.Lock()
	defer s.sessionLeasesMu.Unlock()
	delete(s.sessionLeases, run)
}

// sessionLeaseStale reports whether a live session's harness holds a gateway
// lease that cannot carry another turn: expired, revoked, close to expiry,
// or issued by an earlier daemon process. An earlier process also had its
// own gateway port and CA, so the harness cannot reach this one at all.
func (s *Service) sessionLeaseStale(
	ctx context.Context, thread domain.Thread, capsule domain.Capsule, run domain.RunID,
) bool {
	if s.gateway == nil {
		return false
	}
	s.sessionLeasesMu.Lock()
	lease, known := s.sessionLeases[run]
	s.sessionLeasesMu.Unlock()
	if known {
		return lease.gateway && s.gateway.LeaseRemaining(lease.id) < sessionRenewalMargin
	}
	connection, err := s.ProjectProviderConnection(
		ctx, capsule.ProjectID, strings.TrimSuffix(thread.AdapterID, "-structured"))
	return err == nil && connection != ""
}

// sessionRoleStale reports whether a live session's harness was started
// without the lead role its Thread now holds, because the operator made the
// Thread a lead after the session started (ADR 0059 section 2). For a
// session this process started, the role it was given is known exactly.
// For one an earlier process started, a grant created after the Run started
// means the session predates the role. Only the upgrade counts: revoking a
// grant leaves a plain Thread and restarts nothing.
func (s *Service) sessionRoleStale(
	ctx context.Context, thread domain.Thread, capsule domain.Capsule, run domain.Run,
) bool {
	if s.coordination == nil || capsule.Role != domain.CapsuleMain {
		return false
	}
	var grant domain.CoordinationGrant
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		grant, err = reader.GetCoordinationGrantByRootThread(ctx, thread.ID)
		return err
	}); err != nil || grant.State != domain.GrantActive {
		return false
	}
	s.sessionLeasesMu.Lock()
	lease, known := s.sessionLeases[run.ID]
	s.sessionLeasesMu.Unlock()
	if known {
		return lease.role != adapterproto.CoordinationLead
	}
	return !run.StartedAt.IsZero() && run.StartedAt.Before(grant.CreatedAt)
}

// renewStaleSession restarts an idle structured session before a prompt is
// appended to it when its harness cannot take the prompt as it is: its
// gateway lease is stale, so the prompt would fail inside the harness
// (ADR 0058), or it was started before the operator made the Thread a lead,
// so the harness does not know its role (ADR 0059). The new session resumes
// the harness's own session, so the conversation continues, and it gets a
// fresh lease, the current trust bundle, the current gateway URL, and the
// Thread's current role.
//
// It changes nothing, and returns expected, when the session is mid-turn or
// waiting on the operator, when expected is out of date, when the request is
// a replay, or when the harness never reported resume state. Otherwise it
// returns the Thread version the caller's append should expect.
func (s *Service) renewStaleSession(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, scope, key string,
) (domain.ResourceVersion, error) {
	if s.gateway == nil && s.coordination == nil {
		return expected, nil
	}
	var replayed bool
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		_, replayed, err = getReplay[threadOutcome](ctx, reader, scope, key)
		return err
	}); err != nil || replayed {
		return expected, nil
	}
	renewed, _, err := s.renewSession(ctx, id, expected)
	return renewed, err
}

// renewSession is the stop and resume behind renewStaleSession, without the
// replay check, for callers that are not appending a prompt. It reports
// whether it restarted the session.
func (s *Service) renewSession(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion,
) (domain.ResourceVersion, bool, error) {
	thread, run, capsule, ok := s.renewalCandidate(ctx, id, expected)
	if !ok {
		return expected, false, nil
	}
	// Ingest what the session already produced, so the turn state below is
	// current and nothing from it is left behind with the old Run.
	if err := s.SyncThread(ctx, id); err != nil {
		return expected, false, nil
	}
	unlock := s.lockThreadSync(id)
	defer unlock()
	// Check again under the lock: another prompt may have renewed it.
	thread, run, capsule, ok = s.renewalCandidate(ctx, id, expected)
	if !ok {
		return expected, false, nil
	}
	attention, err := s.ThreadAttention(ctx, thread)
	if err != nil || attention.Turn == ThreadTurnWorking || attention.Awaiting != nil {
		return expected, false, nil
	}
	if _, _, err := s.latestResumeState(ctx, thread); err != nil {
		return expected, false, nil
	}

	if _, err := s.transitionRun(ctx, run.ID, domain.RunCancelling, nil); err != nil {
		return expected, false, nil
	}
	cancelCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), harnessCancelWait)
	runtimeRun, cancelErr := s.structured.CancelStructured(cancelCtx, capsule.ProviderResourceID, run.ID)
	stop()
	switch {
	case errors.Is(cancelErr, domain.ErrRuntimeStopped):
		// Runtime recovery fails the Run and restarts the Capsule (ADR 0044).
		return expected, false, cancelErr
	case cancelErr != nil || !runtimeRun.State.Terminal():
		// The old harness did not confirm it stopped. Starting another
		// one on the same harness session could fork it, so end this one
		// the way a stop does and let the operator resume it.
		if _, err := s.forceEndRun(ctx, run.ID, id); err != nil {
			return expected, false, err
		}
		return expected, false, domain.ErrHarnessUnresponsive
	}
	// The old Run's own end frame is not ingested: the Thread moves to the
	// new Run before the sync lock is released, so the transcript shows no
	// stop the operator did not ask for.
	if _, err := s.transitionRun(ctx, run.ID, domain.RunCancelled, nil); err != nil {
		return expected, false, err
	}

	fresh, err := s.GetThread(ctx, id)
	if err != nil {
		return expected, false, err
	}
	result, err := s.beginThreadSession(ctx, id, fresh.ResourceVersion, "renew-"+string(run.ID), true)
	if err != nil {
		// Leave the Thread as a stopped session the operator can resume.
		if pauseErr := s.pauseCompletedThread(ctx, id, run.ID); pauseErr != nil {
			return expected, false, errors.Join(err, pauseErr)
		}
		return expected, false, err
	}
	if expected == 0 {
		return 0, true, nil
	}
	return result.Thread.ResourceVersion, true, nil
}

// renewalCandidate loads a Thread whose live session holds a stale lease or
// a stale role.
func (s *Service) renewalCandidate(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion,
) (domain.Thread, domain.Run, domain.Capsule, bool) {
	thread, err := s.GetThread(ctx, id)
	if err != nil || thread.State != domain.ThreadActive || thread.CurrentRunID == "" ||
		expected != 0 && thread.ResourceVersion != expected {
		return domain.Thread{}, domain.Run{}, domain.Capsule{}, false
	}
	run, err := s.GetRunStored(ctx, thread.CurrentRunID)
	if err != nil || run.State != domain.RunRunning {
		return domain.Thread{}, domain.Run{}, domain.Capsule{}, false
	}
	capsule, err := s.GetCapsule(ctx, thread.CapsuleID)
	if err != nil || capsule.State != domain.CapsuleReady ||
		capsule.DesiredState != domain.IntentReady || capsule.Maintenance != "" ||
		!s.sessionLeaseStale(ctx, thread, capsule, run.ID) && !s.sessionRoleStale(ctx, thread, capsule, run) {
		return domain.Thread{}, domain.Run{}, domain.Capsule{}, false
	}
	return thread, run, capsule, true
}
