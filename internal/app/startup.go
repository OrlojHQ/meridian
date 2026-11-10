package app

import (
	"context"
	"errors"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

const (
	startupPersistInterval = 400 * time.Millisecond
	startupEventInterval   = 2 * time.Second
)

type startupMark struct {
	phase     domain.StartupPhase
	current   int64
	total     int64
	persisted time.Time
	evented   time.Time
}

func (r *Reconciler) startupRequest(
	ctx context.Context,
	id domain.CapsuleID,
	request ports.CreateCapsuleRequest,
) ports.CreateCapsuleRequest {
	request.ReportStartup = func(report ports.StartupReport) {
		r.recordStartup(ctx, id, report)
	}
	return request
}

func (r *Reconciler) recordStartup(
	ctx context.Context,
	id domain.CapsuleID,
	report ports.StartupReport,
) {
	now := r.clock.Now().UTC()
	progress, err := domain.NewStartupProgress(
		id, report.Phase, report.Phases, report.CurrentBytes, report.TotalBytes, now,
	)
	if err != nil {
		return
	}
	current, total := int64(0), int64(0)
	if progress.Pull != nil {
		current, total = progress.Pull.CurrentBytes, progress.Pull.TotalBytes
	}
	r.startupMu.Lock()
	if r.startupSeen == nil {
		r.startupSeen = map[domain.CapsuleID]startupMark{}
	}
	prev := r.startupSeen[id]
	phaseChanged := prev.persisted.IsZero() || prev.phase != progress.Phase
	bytesChanged := current != prev.current || total != prev.total
	persist := phaseChanged || (bytesChanged && now.Sub(prev.persisted) >= startupPersistInterval)
	event := persist && (phaseChanged || prev.evented.IsZero() || now.Sub(prev.evented) >= startupEventInterval)
	if !persist {
		r.startupMu.Unlock()
		return
	}
	mark := startupMark{
		phase: progress.Phase, current: current, total: total,
		persisted: now, evented: prev.evented,
	}
	if event {
		mark.evented = now
	}
	r.startupSeen[id] = mark
	r.startupMu.Unlock()

	_ = r.store.Transact(ctx, func(tx ports.Transaction) error {
		capsule, err := tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		if capsule.State != domain.CapsuleCreating && capsule.State != domain.CapsulePreparing {
			return nil
		}
		if err := tx.PutStartup(ctx, progress); err != nil {
			return err
		}
		if !event {
			return nil
		}
		payload, err := domain.StartupEventData(progress)
		if err != nil {
			return err
		}
		return tx.AppendEvent(ctx, domain.Event{
			ID:              domain.EventID(r.ids.NewID()),
			AggregateType:   "capsule",
			AggregateID:     string(id),
			Type:            "capsule.startup",
			Timestamp:       now,
			ResourceVersion: capsule.ResourceVersion,
			Data:            payload,
		})
	})
}

func (r *Reconciler) clearStartupMark(id domain.CapsuleID) {
	r.startupMu.Lock()
	delete(r.startupSeen, id)
	r.startupMu.Unlock()
}

func loadStartup(ctx context.Context, reader ports.Reader, capsule *domain.Capsule) error {
	switch capsule.State {
	case domain.CapsuleCreating, domain.CapsulePreparing, domain.CapsuleFailed:
	default:
		return nil
	}
	progress, err := reader.GetStartup(ctx, capsule.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	capsule.Startup = &progress
	return nil
}
