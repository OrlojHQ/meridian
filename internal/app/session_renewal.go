package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/providergateway"
)

// sessionLease is what this daemon process knows about the gateway lease a
// structured session's harness was started with (ADR 0058).
type sessionLease struct {
	id [32]byte
	// gateway is false for a session started without a provider
	// connection, which has no lease to renew.
	gateway bool
}

// sessionRenewalMargin is how much lease a session must have left to take a
// new prompt without renewal. A turn that starts with less could outlive it.
var sessionRenewalMargin = 2 * time.Hour

func (s *Service) recordSessionLease(run domain.RunID, connection *harnesssetup.Connection) {
	lease := sessionLease{}
	if connection != nil && connection.Token != "" {
		lease = sessionLease{id: providergateway.LeaseID(connection.Token), gateway: true}
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

// renewStaleSession restarts an idle structured session whose gateway lease
// is stale before a prompt is appended to it, so the prompt reaches the
// provider instead of failing inside the harness (ADR 0058). The new session
// resumes the harness's own session, so the conversation continues, and it
// gets a fresh lease, the current trust bundle, and the current gateway URL.
//
// It changes nothing, and returns expected, when the session is mid-turn or
// waiting on the operator, when expected is out of date, when the request is
// a replay, or when the harness never reported resume state. Otherwise it
// returns the Thread version the caller's append should expect.
func (s *Service) renewStaleSession(
	ctx context.Context, id domain.ThreadID, expected domain.ResourceVersion, scope, key string,
) (domain.ResourceVersion, error) {
	if s.gateway == nil {
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
	thread, run, capsule, ok := s.renewalCandidate(ctx, id, expected)
	if !ok {
		return expected, nil
	}
	// Ingest what the session already produced, so the turn state below is
	// current and nothing from it is left behind with the old Run.
	if err := s.SyncThread(ctx, id); err != nil {
		return expected, nil
	}
	unlock := s.lockThreadSync(id)
	defer unlock()
	// Check again under the lock: another prompt may have renewed it.
	thread, run, capsule, ok = s.renewalCandidate(ctx, id, expected)
	if !ok {
		return expected, nil
	}
	attention, err := s.ThreadAttention(ctx, thread)
	if err != nil || attention.Turn == ThreadTurnWorking || attention.Awaiting != nil {
		return expected, nil
	}
	if _, _, err := s.latestResumeState(ctx, thread); err != nil {
		return expected, nil
	}

	if _, err := s.transitionRun(ctx, run.ID, domain.RunCancelling, nil); err != nil {
		return expected, nil
	}
	cancelCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), harnessCancelWait)
	runtimeRun, cancelErr := s.structured.CancelStructured(cancelCtx, capsule.ProviderResourceID, run.ID)
	stop()
	switch {
	case errors.Is(cancelErr, domain.ErrRuntimeStopped):
		// Runtime recovery fails the Run and restarts the Capsule (ADR 0044).
		return expected, cancelErr
	case cancelErr != nil || !runtimeRun.State.Terminal():
		// The old harness did not confirm it stopped. Starting another
		// one on the same harness session could fork it, so end this one
		// the way a stop does and let the operator resume it.
		if _, err := s.forceEndRun(ctx, run.ID, id); err != nil {
			return expected, err
		}
		return expected, domain.ErrHarnessUnresponsive
	}
	// The old Run's own end frame is not ingested: the Thread moves to the
	// new Run before the sync lock is released, so the transcript shows no
	// stop the operator did not ask for.
	if _, err := s.transitionRun(ctx, run.ID, domain.RunCancelled, nil); err != nil {
		return expected, err
	}

	fresh, err := s.GetThread(ctx, id)
	if err != nil {
		return expected, err
	}
	result, err := s.beginThreadSession(ctx, id, fresh.ResourceVersion, "renew-"+string(run.ID), true)
	if err != nil {
		// Leave the Thread as a stopped session the operator can resume.
		if pauseErr := s.pauseCompletedThread(ctx, id, run.ID); pauseErr != nil {
			return expected, errors.Join(err, pauseErr)
		}
		return expected, err
	}
	if expected == 0 {
		return 0, nil
	}
	return result.Thread.ResourceVersion, nil
}

// renewalCandidate loads a Thread whose live session holds a stale lease.
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
		!s.sessionLeaseStale(ctx, thread, capsule, run.ID) {
		return domain.Thread{}, domain.Run{}, domain.Capsule{}, false
	}
	return thread, run, capsule, true
}
