package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func (r *Reconciler) setupCacheEnabled(ctx context.Context) bool {
	if r.snapshotter == nil || r.artifacts == nil {
		return false
	}
	capabilities, err := r.provider.Capabilities(ctx)
	return err == nil && capabilities.Snapshot
}

func (r *Reconciler) prepareSetupCacheRestore(
	ctx context.Context,
	capsule domain.Capsule,
	project domain.Project,
) (bool, error) {
	if capsule.OriginMomentID != "" || capsule.LauncherHarness != "" ||
		!r.setupCacheEnabled(ctx) {
		return false, nil
	}
	configHash, err := hashPreparedWorkspace(project, capsule.WorkspaceImage(project))
	if err != nil {
		return false, err
	}
	var cache domain.SetupMomentCache
	var moment domain.Moment
	err = r.store.View(ctx, func(reader ports.Reader) error {
		var err error
		cache, err = reader.GetSetupMomentCache(ctx, project.ID, configHash)
		if err != nil {
			return err
		}
		moment, err = reader.GetMoment(ctx, cache.MomentID)
		return err
	})
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if moment.Kind != domain.MomentSetupCache ||
		moment.ProjectID != capsule.ProjectID ||
		moment.ProjectSetupHash != configHash {
		return false, fmt.Errorf("%w: invalid setup Moment cache", domain.ErrCorrupt)
	}
	previous := capsule.ResourceVersion
	capsule.OriginMomentID = moment.ID
	capsule.RestoreComplete = false
	capsule.Maintenance = "restore"
	capsule.UpdatedAt = r.clock.Now().UTC()
	capsule.ResourceVersion++
	if err := r.saveCapsule(
		ctx, capsule, previous, "capsule.setup_cache_restore_requested",
	); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Reconciler) captureSetupMomentCache(
	ctx context.Context,
	capsule domain.Capsule,
) error {
	if capsule.LauncherHarness != "" || !r.setupCacheEnabled(ctx) {
		return nil
	}
	var project domain.Project
	if err := r.store.View(ctx, func(reader ports.Reader) error {
		var err error
		project, err = reader.GetProject(ctx, capsule.ProjectID)
		return err
	}); err != nil {
		return err
	}
	configHash, err := hashPreparedWorkspace(project, capsule.WorkspaceImage(project))
	if err != nil {
		return err
	}
	if err := r.store.View(ctx, func(reader ports.Reader) error {
		_, err := reader.GetSetupMomentCache(ctx, project.ID, configHash)
		return err
	}); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}

	captureService := &Service{
		store: r.store, clock: r.clock, ids: r.ids,
		snapshotter: r.snapshotter, artifacts: r.artifacts,
		observer: r.observer, providerName: r.providerName,
	}
	moment, err := captureService.captureSetupArtifacts(ctx, capsule, project)
	if err != nil {
		return err
	}
	return r.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetCapsule(ctx, capsule.ID)
		if err != nil {
			return err
		}
		if current.State != domain.CapsulePreparing ||
			current.OriginMomentID != "" ||
			current.Maintenance != "" {
			return domain.ErrConflict
		}
		if _, err := tx.GetSetupMomentCache(ctx, project.ID, configHash); err == nil {
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if err := tx.InsertMoment(ctx, moment); err != nil {
			return err
		}
		if err := tx.PutSetupMomentCache(ctx, domain.SetupMomentCache{
			ProjectID: project.ID, ConfigHash: configHash,
			MomentID: moment.ID, CreatedAt: moment.CreatedAt,
		}); err != nil {
			return err
		}
		return tx.AppendEvent(ctx, domain.Event{
			ID: domain.EventID(r.ids.NewID()), AggregateType: "project",
			AggregateID: string(project.ID), Type: "project.setup_cache_created",
			Timestamp: moment.CreatedAt, ResourceVersion: project.ResourceVersion,
		})
	})
}
