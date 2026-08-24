package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"go.opentelemetry.io/otel"
)

type MomentPage struct {
	Items      []domain.Moment
	NextOffset int
}

type TimelineView struct {
	Timeline domain.Timeline
	Ancestry []domain.Timeline
}

type DescendantResult struct {
	Capsule  domain.Capsule
	Timeline domain.Timeline
	Reason   domain.TimelineReason
}

type SealResult struct {
	Capsule domain.Capsule
	Moment  domain.Moment
}

type momentManifest struct {
	Version          string            `json:"version"`
	ArchiveSHA256    string            `json:"archiveSha256"`
	ArchiveSize      int64             `json:"archiveSize"`
	SourceCapsuleID  domain.CapsuleID  `json:"sourceCapsuleId"`
	SourceTimelineID domain.TimelineID `json:"sourceTimelineId"`
	ParentMomentID   domain.MomentID   `json:"parentMomentId,omitempty"`
	ImageDigest      string            `json:"imageDigest"`
	ProjectSetupHash string            `json:"projectSetupHash"`
	GitBranch        string            `json:"gitBranch,omitempty"`
	GitHEAD          string            `json:"gitHead,omitempty"`
	GitDirtySummary  string            `json:"gitDirtySummary,omitempty"`
	CapturedAt       time.Time         `json:"capturedAt"`
	Final            bool              `json:"final"`
}

func (s *Service) CaptureMoment(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	name string,
	expected domain.ResourceVersion,
	key string,
) (domain.Moment, error) {
	name, err := requireName(name)
	if err != nil {
		return domain.Moment{}, err
	}
	if expected <= 0 {
		return domain.Moment{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.Moment{}, err
	}
	if s.snapshotter == nil || s.artifacts == nil {
		return domain.Moment{}, domain.ErrUnsupported
	}
	scope := "moment:capture:" + string(capsuleID)
	var replay domain.Moment
	var replayed bool
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		replay, replayed, err = getReplay[domain.Moment](ctx, reader, scope, key)
		return err
	}); err != nil {
		return domain.Moment{}, err
	}
	if replayed {
		return replay, nil
	}
	capsule, project, err := s.acquireMaintenance(ctx, capsuleID, expected, "capture")
	if err != nil {
		return domain.Moment{}, err
	}
	success := false
	defer func() {
		if !success {
			_ = s.clearMaintenance(context.Background(), capsuleID, "capture")
		}
	}()
	ctx, span := otel.Tracer("github.com/OrlojHQ/meridian").Start(ctx, "moment.capture")
	started := time.Now()
	moment, err := s.captureArtifacts(ctx, capsule, project, name, false)
	span.End()
	if s.observer != nil {
		size := int64(-1)
		if err == nil {
			size = moment.ArchiveSize
		}
		s.observer.Snapshot(time.Since(started), size, operationResult(err))
	}
	if err != nil {
		return domain.Moment{}, err
	}
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetCapsule(ctx, capsuleID)
		if err != nil {
			return err
		}
		if current.Maintenance != "capture" || current.State != domain.CapsuleReady {
			return domain.ErrConflict
		}
		if existing, ok, err := getReplay[domain.Moment](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			moment = existing
			return nil
		}
		if err := tx.InsertMoment(ctx, moment); err != nil {
			return err
		}
		previous := current.ResourceVersion
		current.Maintenance = ""
		current.UpdatedAt = s.clock.Now().UTC()
		current.ResourceVersion++
		if err := tx.UpdateCapsule(ctx, current, previous); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "moment", string(moment.ID), "moment.created", 1, nil); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, moment, moment.CreatedAt)
	})
	success = err == nil
	return moment, err
}

func (s *Service) acquireMaintenance(
	ctx context.Context,
	id domain.CapsuleID,
	expected domain.ResourceVersion,
	operation string,
) (domain.Capsule, domain.Project, error) {
	var capsule domain.Capsule
	var project domain.Project
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		capsule, err = tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		if capsule.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady {
			return fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
		}
		if capsule.Maintenance != "" {
			return fmt.Errorf("%w: Capsule maintenance is active", domain.ErrConflict)
		}
		active, err := tx.HasActiveRun(ctx, id)
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
		capsule.Maintenance = operation
		capsule.UpdatedAt = s.clock.Now().UTC()
		capsule.ResourceVersion++
		if err := tx.UpdateCapsule(ctx, capsule, previous); err != nil {
			return err
		}
		return s.appendEvent(
			ctx, tx, "capsule", string(id), "capsule."+operation+"_started",
			capsule.ResourceVersion, nil,
		)
	})
	return capsule, project, err
}

func (s *Service) clearMaintenance(ctx context.Context, id domain.CapsuleID, operation string) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		capsule, err := tx.GetCapsule(ctx, id)
		if err != nil || capsule.Maintenance != operation {
			return err
		}
		previous := capsule.ResourceVersion
		capsule.Maintenance = ""
		capsule.UpdatedAt = s.clock.Now().UTC()
		capsule.ResourceVersion++
		return tx.UpdateCapsule(ctx, capsule, previous)
	})
}

func (s *Service) captureArtifacts(
	ctx context.Context,
	capsule domain.Capsule,
	project domain.Project,
	name string,
	final bool,
) (domain.Moment, error) {
	var parent domain.Moment
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		parent, err = reader.LatestMoment(ctx, capsule.TimelineID)
		return err
	})
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.Moment{}, err
	}
	return s.captureArtifactsWithParent(ctx, capsule, project, name, final, parent, domain.MomentTimeline)
}

func (s *Service) captureSetupArtifacts(
	ctx context.Context,
	capsule domain.Capsule,
	project domain.Project,
) (domain.Moment, error) {
	return s.captureArtifactsWithParent(
		ctx, capsule, project, "Internal setup cache", false, domain.Moment{}, domain.MomentSetupCache,
	)
}

func (s *Service) captureArtifactsWithParent(
	ctx context.Context,
	capsule domain.Capsule,
	project domain.Project,
	name string,
	final bool,
	parent domain.Moment,
	kind domain.MomentKind,
) (domain.Moment, error) {
	capturedAt := s.clock.Now().UTC()
	providerStarted := time.Now()
	capture, err := s.snapshotter.CaptureWorkspace(ctx, capsule.ProviderResourceID)
	if s.observer != nil {
		s.observer.ProviderOperation(s.providerName, "capture", operationResult(err), time.Since(providerStarted))
	}
	if err != nil {
		return domain.Moment{}, err
	}
	if capture.Archive == nil {
		return domain.Moment{}, fmt.Errorf("%w: provider returned no archive", domain.ErrCorrupt)
	}
	archive, publishErr := s.artifacts.Publish(ctx, capture.Archive)
	closeErr := capture.Archive.Close()
	if publishErr != nil {
		if s.observer != nil {
			s.observer.ArtifactFailure("publish")
		}
		return domain.Moment{}, publishErr
	}
	if closeErr != nil {
		return domain.Moment{}, closeErr
	}
	projectHash, err := hashProjectConfiguration(project)
	if err != nil {
		return domain.Moment{}, err
	}
	metadata := capture.Metadata
	metadata.GitBranch = bounded(metadata.GitBranch, 512)
	metadata.GitHEAD = bounded(metadata.GitHEAD, 128)
	metadata.GitDirtySummary = bounded(metadata.GitDirtySummary, 4096)
	manifest := momentManifest{
		Version: "meridian.moment.v1", ArchiveSHA256: archive.Digest,
		ArchiveSize: archive.Size, SourceCapsuleID: capsule.ID,
		SourceTimelineID: capsule.TimelineID, ParentMomentID: parent.ID,
		ImageDigest: metadata.ImageDigest, ProjectSetupHash: projectHash,
		GitBranch: metadata.GitBranch, GitHEAD: metadata.GitHEAD,
		GitDirtySummary: metadata.GitDirtySummary, CapturedAt: capturedAt,
		Final: final,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return domain.Moment{}, err
	}
	manifestArtifact, err := s.artifacts.Publish(ctx, bytes.NewReader(manifestBytes))
	if err != nil {
		if s.observer != nil {
			s.observer.ArtifactFailure("publish")
		}
		return domain.Moment{}, err
	}
	moment := domain.Moment{
		ID: domain.MomentID(s.ids.NewID()), ProjectID: capsule.ProjectID,
		CapsuleID: capsule.ID, TimelineID: capsule.TimelineID,
		ParentMomentID: parent.ID, Name: name, ArchiveSHA256: archive.Digest,
		ArchiveSize: archive.Size, ManifestSHA256: manifestArtifact.Digest,
		ImageDigest: metadata.ImageDigest, ProjectSetupHash: projectHash,
		GitBranch: metadata.GitBranch, GitHEAD: metadata.GitHEAD,
		GitDirtySummary: metadata.GitDirtySummary, CreatedAt: capturedAt,
		Final: final, Kind: kind,
	}
	return moment, moment.Validate()
}

func hashProjectConfiguration(project domain.Project) (string, error) {
	value, err := json.Marshal(struct {
		RepositoryURL      string   `json:"repositoryUrl"`
		Setup              []string `json:"setup"`
		ImageReference     string   `json:"imageReference"`
		GitSecretName      string   `json:"gitSecretName"`
		HarnessSecretNames []string `json:"harnessSecretNames,omitempty"`
	}{
		RepositoryURL: project.RepositoryURL, Setup: project.Setup,
		ImageReference:     project.ImageReference,
		GitSecretName:      project.GitSecretName,
		HarnessSecretNames: project.HarnessSecretNames,
	})
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:]), nil
}

func bounded(value string, limit int) string {
	value = strings.ReplaceAll(value, "\x00", "")
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func (s *Service) GetMoment(ctx context.Context, id domain.MomentID) (domain.Moment, error) {
	var result domain.Moment
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		result, err = reader.GetMoment(ctx, id)
		return err
	})
	if err == nil && result.Kind != domain.MomentTimeline {
		return domain.Moment{}, domain.ErrNotFound
	}
	return result, err
}

func (s *Service) ListMoments(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	offset, limit int,
) (MomentPage, error) {
	page := normalizePage(offset, limit)
	var result MomentPage
	err := s.store.View(ctx, func(reader ports.Reader) error {
		capsule, err := reader.GetCapsule(ctx, capsuleID)
		if err != nil {
			return err
		}
		var more bool
		result.Items, more, err = reader.ListMoments(ctx, capsule.TimelineID, page)
		if more {
			result.NextOffset = page.Offset + len(result.Items)
		}
		return err
	})
	return result, err
}

func (s *Service) GetTimeline(ctx context.Context, id domain.TimelineID) (TimelineView, error) {
	var result TimelineView
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		result.Timeline, err = reader.GetTimeline(ctx, id)
		if err != nil {
			return err
		}
		result.Ancestry, err = reader.ListTimelineAncestry(ctx, id)
		if err != nil {
			return err
		}
		seen := make(map[domain.TimelineID]struct{}, len(result.Ancestry))
		for _, timeline := range result.Ancestry {
			if timeline.ProjectID != result.Timeline.ProjectID {
				return fmt.Errorf("%w: Timeline ancestry crosses projects", domain.ErrInvalid)
			}
			if _, exists := seen[timeline.ID]; exists {
				return fmt.Errorf("%w: cyclic Timeline ancestry", domain.ErrInvalid)
			}
			seen[timeline.ID] = struct{}{}
		}
		return nil
	})
	return result, err
}

func (s *Service) CreateShard(
	ctx context.Context,
	momentID domain.MomentID,
	name, key string,
) (DescendantResult, error) {
	return s.createDescendant(ctx, "", momentID, name, key, domain.TimelineShard)
}

func (s *Service) Rewind(
	ctx context.Context,
	source domain.CapsuleID,
	momentID domain.MomentID,
	name, key string,
) (DescendantResult, error) {
	return s.createDescendant(ctx, source, momentID, name, key, domain.TimelineRewind)
}

func (s *Service) createDescendant(
	ctx context.Context,
	source domain.CapsuleID,
	momentID domain.MomentID,
	name, key string,
	reason domain.TimelineReason,
) (DescendantResult, error) {
	name, err := requireName(name)
	if err != nil {
		return DescendantResult{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return DescendantResult{}, err
	}
	if s.snapshotter == nil || s.artifacts == nil {
		return DescendantResult{}, domain.ErrUnsupported
	}
	scope := "timeline:" + string(reason) + ":" + string(momentID)
	var result DescendantResult
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[DescendantResult](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			result = replay
			return nil
		}
		moment, err := tx.GetMoment(ctx, momentID)
		if err != nil {
			return err
		}
		if moment.Kind != domain.MomentTimeline {
			return domain.ErrNotFound
		}
		if reason == domain.TimelineRewind && moment.CapsuleID != source {
			return fmt.Errorf("%w: Rewind Moment does not belong to source Capsule", domain.ErrIllegalTransition)
		}
		if source != "" {
			capsule, err := tx.GetCapsule(ctx, source)
			if err != nil {
				return err
			}
			if capsule.ProjectID != moment.ProjectID {
				return fmt.Errorf("%w: Rewind crosses projects", domain.ErrIllegalTransition)
			}
		}
		now := s.clock.Now().UTC()
		capsuleID := domain.CapsuleID(s.ids.NewID())
		timelineID := domain.TimelineID(s.ids.NewID())
		result = DescendantResult{
			Capsule: domain.Capsule{
				ID: capsuleID, ProjectID: moment.ProjectID, TimelineID: timelineID,
				Name: name, State: domain.CapsuleCreating, DesiredState: domain.IntentReady,
				OriginMomentID: moment.ID, RestoreComplete: false, Maintenance: "restore",
				LastActivityAt: now, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
			},
			Timeline: domain.Timeline{
				ID: timelineID, ProjectID: moment.ProjectID, CapsuleID: capsuleID,
				ForkedFromMomentID: moment.ID, Reason: reason, CreatedAt: now,
			},
			Reason: reason,
		}
		if err := tx.InsertCapsule(ctx, result.Capsule); err != nil {
			return err
		}
		if err := tx.InsertTimeline(ctx, result.Timeline); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "capsule", string(capsuleID), "capsule."+string(reason)+"_requested", 1, nil); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, result, now)
	})
	if err == nil {
		err = s.queue.Enqueue(ctx, result.Capsule.ID)
	}
	return result, err
}

func (s *Service) Seal(
	ctx context.Context,
	capsuleID domain.CapsuleID,
	expected domain.ResourceVersion,
	key string,
) (SealResult, error) {
	if expected <= 0 {
		return SealResult{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return SealResult{}, err
	}
	if s.snapshotter == nil || s.artifacts == nil {
		return SealResult{}, domain.ErrUnsupported
	}
	scope := "capsule:seal:" + string(capsuleID)
	var result SealResult
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		replay, ok, err := getReplay[SealResult](ctx, reader, scope, key)
		if ok {
			result = replay
		}
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}); err != nil {
		return SealResult{}, err
	}
	if result.Moment.ID != "" {
		return result, nil
	}
	capsule, project, err := s.acquireMaintenance(ctx, capsuleID, expected, "seal")
	if err != nil {
		return SealResult{}, err
	}
	success := false
	defer func() {
		if !success {
			_ = s.clearMaintenance(context.Background(), capsuleID, "seal")
		}
	}()
	ctx, span := otel.Tracer("github.com/OrlojHQ/meridian").Start(ctx, "moment.seal")
	started := time.Now()
	moment, err := s.captureArtifacts(ctx, capsule, project, "Final seal", true)
	span.End()
	if s.observer != nil {
		size := int64(-1)
		if err == nil {
			size = moment.ArchiveSize
		}
		s.observer.Snapshot(time.Since(started), size, operationResult(err))
	}
	if err != nil {
		return SealResult{}, err
	}
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetCapsule(ctx, capsuleID)
		if err != nil {
			return err
		}
		if current.Maintenance != "seal" || current.State != domain.CapsuleReady {
			return domain.ErrConflict
		}
		if err := tx.InsertMoment(ctx, moment); err != nil {
			return err
		}
		previous := current.ResourceVersion
		current.Maintenance = ""
		current.DesiredState = domain.IntentSealed
		if err := current.Transition(domain.CapsuleSealed, s.clock.Now()); err != nil {
			return err
		}
		if err := tx.UpdateCapsule(ctx, current, previous); err != nil {
			return err
		}
		result = SealResult{Capsule: current, Moment: moment}
		if err := s.appendEvent(ctx, tx, "capsule", string(capsuleID), "capsule.sealed", current.ResourceVersion, nil); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, result, current.UpdatedAt)
	})
	success = err == nil
	return result, err
}
