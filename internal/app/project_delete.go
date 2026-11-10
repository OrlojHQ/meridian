package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// DeleteProject begins or resumes cascade deletion. Capsules are deleted
// through the same reconciler path as DeleteCapsule. Transcripts, artifacts,
// environment, and exclusive named-secret ciphertext are removed once every
// owned container is gone. A container the provider cannot remove leaves the
// Project failed, with the reason, until a later delete resumes.
func (s *Service) DeleteProject(
	ctx context.Context,
	id domain.ProjectID,
	expected domain.ResourceVersion,
	key string,
) (domain.Project, error) {
	if expected <= 0 {
		return domain.Project{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.Project{}, err
	}
	return s.deleteProject(ctx, id, expected, key, true)
}

func (s *Service) deleteProject(
	ctx context.Context,
	id domain.ProjectID,
	expected domain.ResourceVersion,
	key string,
	recordIdempotency bool,
) (domain.Project, error) {
	scope := "project:" + string(id) + ":delete"
	var result domain.Project
	var enqueue []domain.CapsuleID
	var digests []string
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if recordIdempotency {
			replay, ok, err := getReplay[domain.Project](ctx, tx, scope, key)
			if err != nil {
				return err
			}
			if ok {
				result = replay
				ids, err := s.capsulesAwaitingCleanup(ctx, tx, id)
				if err != nil {
					return err
				}
				enqueue = ids
				return nil
			}
		}
		project, err := tx.GetProject(ctx, id)
		if err != nil {
			return err
		}
		if project.State == domain.ProjectDeleted {
			result = project
			if recordIdempotency {
				return putReplay(ctx, tx, scope, key, result, project.UpdatedAt)
			}
			return nil
		}
		if project.ResourceVersion != expected {
			return domain.ErrConflict
		}
		capsules, err := listAllCapsules(ctx, tx, id)
		if err != nil {
			return err
		}
		for index := range capsules {
			capsule := &capsules[index]
			if err := s.requestCapsuleDelete(ctx, tx, capsule); err != nil {
				return err
			}
			if capsule.State != domain.CapsuleDeleted {
				enqueue = append(enqueue, capsule.ID)
			}
		}
		now := s.clock.Now().UTC()
		if len(enqueue) == 0 {
			purged, hashes, err := tx.PurgeProjectContents(ctx, project, now)
			if err != nil {
				return err
			}
			digests = hashes
			if err := s.appendEvent(ctx, tx, "project", string(id), "project.deleted", purged.ResourceVersion, nil); err != nil {
				return err
			}
			result = purged
			if recordIdempotency {
				return putReplay(ctx, tx, scope, key, result, purged.UpdatedAt)
			}
			return nil
		}
		previous := project.ResourceVersion
		if project.State != domain.ProjectDeleting {
			project.State = domain.ProjectDeleting
			project.Failure = ""
			project.UpdatedAt = now
			project.ResourceVersion++
			if err := tx.UpdateProjectState(ctx, project, previous); err != nil {
				return err
			}
			if err := s.appendEvent(ctx, tx, "project", string(id), "project.delete_requested", project.ResourceVersion, nil); err != nil {
				return err
			}
		}
		result = project
		if recordIdempotency {
			return putReplay(ctx, tx, scope, key, result, project.UpdatedAt)
		}
		return nil
	})
	if err != nil {
		return domain.Project{}, err
	}
	for _, capsuleID := range enqueue {
		if err := s.queue.Enqueue(ctx, capsuleID); err != nil {
			return result, err
		}
	}
	if err := s.releaseArtifacts(ctx, digests); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) capsulesAwaitingCleanup(
	ctx context.Context,
	reader ports.Reader,
	id domain.ProjectID,
) ([]domain.CapsuleID, error) {
	project, err := reader.GetProject(ctx, id)
	if err != nil || project.State == domain.ProjectDeleted || project.State == domain.ProjectActive {
		return nil, err
	}
	capsules, err := listAllCapsules(ctx, reader, id)
	if err != nil {
		return nil, err
	}
	var ids []domain.CapsuleID
	for _, capsule := range capsules {
		if capsule.State != domain.CapsuleDeleted {
			ids = append(ids, capsule.ID)
		}
	}
	return ids, nil
}

// AdvanceProjectDeletion finishes a deleting Project once every Capsule is
// Deleted, or records the provider failure that stopped container cleanup.
func (s *Service) AdvanceProjectDeletion(ctx context.Context, id domain.ProjectID) error {
	var digests []string
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		project, err := tx.GetProject(ctx, id)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		}
		if project.State != domain.ProjectDeleting && project.State != domain.ProjectFailed {
			return nil
		}
		capsules, err := listAllCapsules(ctx, tx, id)
		if err != nil {
			return err
		}
		var pending bool
		var failure string
		for _, capsule := range capsules {
			if capsule.State == domain.CapsuleDeleted {
				continue
			}
			if capsule.State == domain.CapsuleFailed && capsule.DesiredState == domain.IntentDeleted {
				if failure == "" {
					failure = capsuleDeleteFailure(capsule)
				}
				continue
			}
			pending = true
		}
		if failure != "" {
			return s.markProjectDeleteFailed(ctx, tx, project, failure)
		}
		if pending {
			return nil
		}
		now := s.clock.Now().UTC()
		purged, hashes, err := tx.PurgeProjectContents(ctx, project, now)
		if err != nil {
			return err
		}
		digests = hashes
		return s.appendEvent(ctx, tx, "project", string(id), "project.deleted", purged.ResourceVersion, nil)
	})
	if err != nil {
		return err
	}
	return s.releaseArtifacts(ctx, digests)
}

// RecoverProjectDeletions resumes Projects left in deleting or failed after a restart.
func (s *Service) RecoverProjectDeletions(ctx context.Context) error {
	var projects []domain.Project
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		projects, err = reader.ListProjectsInDeletion(ctx)
		return err
	}); err != nil {
		return err
	}
	for _, project := range projects {
		if _, err := s.deleteProject(ctx, project.ID, project.ResourceVersion, "", false); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *Service) requestCapsuleDelete(
	ctx context.Context,
	tx ports.Transaction,
	capsule *domain.Capsule,
) error {
	if capsule.State == domain.CapsuleDeleted {
		return nil
	}
	if capsule.State == domain.CapsuleDeleting && capsule.DesiredState == domain.IntentDeleted {
		return nil
	}
	active, err := tx.HasActiveRun(ctx, capsule.ID)
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("%w: cancel the active Run or Thread before deleting the Project", domain.ErrConflict)
	}
	if capsule.Maintenance != "" {
		return fmt.Errorf("%w: capsule maintenance %q is active", domain.ErrConflict, capsule.Maintenance)
	}
	previous := capsule.ResourceVersion
	if err := capsule.Transition(domain.CapsuleDeleting, s.clock.Now()); err != nil {
		return err
	}
	capsule.DesiredState = domain.IntentDeleted
	capsule.Failure = ""
	if err := tx.UpdateCapsule(ctx, *capsule, previous); err != nil {
		return err
	}
	return s.appendEvent(ctx, tx, "capsule", string(capsule.ID), "capsule.delete_requested", capsule.ResourceVersion, nil)
}

func (s *Service) markProjectDeleteFailed(
	ctx context.Context,
	tx ports.Transaction,
	project domain.Project,
	failure string,
) error {
	failure = boundFailure(failure)
	if project.State == domain.ProjectFailed && project.Failure == failure {
		return nil
	}
	previous := project.ResourceVersion
	project.State = domain.ProjectFailed
	project.Failure = failure
	project.UpdatedAt = s.clock.Now().UTC()
	project.ResourceVersion++
	if err := tx.UpdateProjectState(ctx, project, previous); err != nil {
		return err
	}
	return s.appendEvent(ctx, tx, "project", string(project.ID), "project.delete_failed", project.ResourceVersion, nil)
}

func capsuleDeleteFailure(capsule domain.Capsule) string {
	reason := strings.TrimSpace(capsule.Failure)
	if reason == "" {
		reason = "owned container cleanup failed"
	}
	name := capsule.Name
	if name == "" {
		name = string(capsule.ID)
	}
	return boundFailure(name + ": " + reason)
}

func boundFailure(message string) string {
	message = strings.ReplaceAll(message, "\x00", "")
	if len(message) <= 1024 {
		return message
	}
	cut := 0
	for index := range message {
		if index > 1024 {
			break
		}
		cut = index
	}
	if cut == 0 || !utf8.ValidString(message[:cut]) {
		return message[:1024]
	}
	return message[:cut]
}

func listAllCapsules(
	ctx context.Context,
	reader ports.Reader,
	projectID domain.ProjectID,
) ([]domain.Capsule, error) {
	var all []domain.Capsule
	offset := 0
	for {
		items, more, err := reader.ListCapsules(ctx, projectID, "", ports.Page{Offset: offset, Limit: 100})
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if !more {
			return all, nil
		}
		offset += len(items)
		if offset > 100000 {
			return nil, fmt.Errorf("%w: too many capsules", domain.ErrResourceExhausted)
		}
	}
}

func projectAcceptsWork(project domain.Project) error {
	if project.State == "" || project.State == domain.ProjectActive {
		return nil
	}
	return fmt.Errorf("%w: project is %s", domain.ErrIllegalTransition, project.State)
}

type artifactRemover interface {
	Remove(context.Context, string) error
}

func (s *Service) releaseArtifacts(ctx context.Context, digests []string) error {
	remover, ok := s.artifacts.(artifactRemover)
	if !ok || len(digests) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, digest := range digests {
		if _, ok := seen[digest]; ok {
			continue
		}
		seen[digest] = struct{}{}
		var referenced bool
		if err := s.store.View(ctx, func(reader ports.Reader) error {
			var err error
			referenced, err = reader.ArtifactReferenced(ctx, digest)
			return err
		}); err != nil {
			return err
		}
		if referenced {
			continue
		}
		if err := remover.Remove(ctx, digest); err != nil {
			return err
		}
	}
	return nil
}
