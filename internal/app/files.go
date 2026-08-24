package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

func (s *Service) ListWorkspaceFiles(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	filePath, after string,
	limit int,
) (ports.WorkspaceFilePage, error) {
	if s.browser == nil {
		return ports.WorkspaceFilePage{}, domain.ErrUnsupported
	}
	if limit < 0 || limit > 512 || len(filePath) > 4096 || len(after) > 255 {
		return ports.WorkspaceFilePage{}, fmt.Errorf("%w: invalid file browser request", domain.ErrInvalid)
	}
	capsule, err := s.readyCapsule(ctx, capsuleID)
	if err != nil {
		return ports.WorkspaceFilePage{}, err
	}
	result, err := s.browser.ListWorkspaceFiles(
		ctx, capsule.ProviderResourceID, filePath, after, limit,
	)
	if err == nil {
		_ = s.TouchCapsuleActivity(ctx, capsuleID)
	}
	return result, err
}

func (s *Service) ReadWorkspaceFile(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	filePath string,
) (ports.WorkspaceFile, error) {
	if s.browser == nil {
		return ports.WorkspaceFile{}, domain.ErrUnsupported
	}
	if filePath == "" || len(filePath) > 4096 {
		return ports.WorkspaceFile{}, fmt.Errorf("%w: file path is required", domain.ErrInvalid)
	}
	capsule, err := s.readyCapsule(ctx, capsuleID)
	if err != nil {
		return ports.WorkspaceFile{}, err
	}
	result, err := s.browser.ReadWorkspaceFile(ctx, capsule.ProviderResourceID, filePath)
	if err == nil {
		_ = s.TouchCapsuleActivity(ctx, capsuleID)
	}
	return result, err
}

func (s *Service) readyCapsule(
	ctx context.Context,
	id domain.CapsuleID,
) (domain.Capsule, error) {
	var capsule domain.Capsule
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		capsule, err = reader.GetCapsule(ctx, id)
		return err
	})
	if err != nil {
		return domain.Capsule{}, err
	}
	if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady {
		return domain.Capsule{}, fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
	}
	if capsule.Maintenance != "" {
		return domain.Capsule{}, fmt.Errorf("%w: Capsule maintenance is active", domain.ErrConflict)
	}
	return capsule, nil
}

type WorkspaceExport struct {
	Archive       io.ReadCloser
	Metadata      ports.SnapshotMetadata
	RepositoryURL string
}

// ExportWorkspace holds the Capsule maintenance lease until Archive is closed.
func (s *Service) ExportWorkspace(
	ctx context.Context,
	capsuleID domain.CapsuleID,
) (WorkspaceExport, error) {
	if s.snapshotter == nil {
		return WorkspaceExport{}, domain.ErrUnsupported
	}
	var capsule domain.Capsule
	var project domain.Project
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		capsule, err = tx.GetCapsule(ctx, capsuleID)
		if err != nil {
			return err
		}
		if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady {
			return fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
		}
		if capsule.Maintenance != "" {
			return fmt.Errorf("%w: Capsule maintenance is active", domain.ErrConflict)
		}
		active, err := tx.HasActiveRun(ctx, capsuleID)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("%w: Capsule has an active Run", domain.ErrConflict)
		}
		project, err = tx.GetProject(ctx, capsule.ProjectID)
		if err != nil {
			return err
		}
		previous := capsule.ResourceVersion
		now := s.clock.Now().UTC()
		capsule.Maintenance = "capture"
		capsule.LastActivityAt = now
		capsule.UpdatedAt = now
		capsule.ResourceVersion++
		if err := tx.UpdateCapsule(ctx, capsule, previous); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "capsule", string(capsule.ID),
			"capsule.sync_started", capsule.ResourceVersion, nil)
	})
	if err != nil {
		return WorkspaceExport{}, err
	}
	capture, err := s.snapshotter.CaptureWorkspace(ctx, capsule.ProviderResourceID)
	if err != nil {
		_ = s.clearMaintenance(context.Background(), capsuleID, "capture")
		return WorkspaceExport{}, err
	}
	if capture.Archive == nil {
		_ = s.clearMaintenance(context.Background(), capsuleID, "capture")
		return WorkspaceExport{}, fmt.Errorf("%w: provider returned no archive", domain.ErrCorrupt)
	}
	return WorkspaceExport{
		Archive: &maintenanceArchive{
			ReadCloser: capture.Archive,
			release: func() error {
				return s.clearMaintenance(context.Background(), capsuleID, "capture")
			},
		},
		Metadata: capture.Metadata, RepositoryURL: project.RepositoryURL,
	}, nil
}

type maintenanceArchive struct {
	io.ReadCloser
	once    sync.Once
	release func() error
	err     error
}

func (r *maintenanceArchive) Close() error {
	r.once.Do(func() {
		r.err = errors.Join(r.ReadCloser.Close(), r.release())
	})
	return r.err
}

func safeHeaderMetadata(value string, limit int) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
