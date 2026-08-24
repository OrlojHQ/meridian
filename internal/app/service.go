// Package app implements Meridian use cases using only domain and port types.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

const defaultPageSize = 50

type Service struct {
	store         ports.Store
	clock         ports.Clock
	ids           ports.IDSource
	queue         ports.ReconcileQueue
	runtime       ports.CapsuleRuntime
	preview       ports.PreviewRuntime
	snapshotter   ports.WorkspaceSnapshotter
	artifacts     ports.ArtifactStore
	observer      ports.Observer
	providerName  string
	structured    ports.StructuredRuntime
	transcriptKey transcriptKey
	runSync       [64]sync.Mutex
	threadSync    [64]sync.Mutex
}

type transcriptKey interface {
	NewDEK() ([]byte, error)
	WrapDEK(domain.ThreadID, []byte) ([]byte, error)
	UnwrapDEK(domain.ThreadID, string, uint32, []byte) ([]byte, error)
	Matches(string, uint32) bool
	Metadata() (string, uint32)
}

func (s *Service) ConfigureObserver(observer ports.Observer, providerName string) {
	s.observer = observer
	s.providerName = providerName
}

func (s *Service) ConfigurePreview(preview ports.PreviewRuntime) {
	s.preview = preview
}

func (s *Service) ConfigureThreads(runtime ports.StructuredRuntime, key transcriptKey) {
	s.structured = runtime
	s.transcriptKey = key
}

func (s *Service) ConfigureTemporal(
	snapshotter ports.WorkspaceSnapshotter,
	artifacts ports.ArtifactStore,
) {
	s.snapshotter = snapshotter
	s.artifacts = artifacts
}

func NewService(
	store ports.Store,
	clock ports.Clock,
	ids ports.IDSource,
	queue ports.ReconcileQueue,
	runtime ...ports.CapsuleRuntime,
) *Service {
	service := &Service{store: store, clock: clock, ids: ids, queue: queue}
	if len(runtime) > 0 {
		service.runtime = runtime[0]
	}
	return service
}

type ProjectPage struct {
	Items      []domain.Project
	NextOffset int
}

type CapsulePage struct {
	Items      []domain.Capsule
	NextOffset int
}

func normalizePage(offset, limit int) ports.Page {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = defaultPageSize
	}
	return ports.Page{Offset: offset, Limit: limit}
}

func requireName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return "", fmt.Errorf("%w: name must be between 1 and 128 characters", domain.ErrInvalid)
	}
	return name, nil
}

func requireIdempotency(key string) error {
	if key == "" || len(key) > 200 {
		return fmt.Errorf("%w: Idempotency-Key must be between 1 and 200 characters", domain.ErrInvalid)
	}
	return nil
}

type ProjectConfiguration struct {
	RepositoryURL  string
	Setup          []string
	ImageReference string
}

func (s *Service) CreateProject(ctx context.Context, name, idempotencyKey string) (domain.Project, error) {
	return s.CreateProjectConfigured(ctx, name, ProjectConfiguration{}, idempotencyKey)
}

func (s *Service) CreateProjectConfigured(
	ctx context.Context,
	name string,
	config ProjectConfiguration,
	idempotencyKey string,
) (domain.Project, error) {
	name, err := requireName(name)
	if err != nil {
		return domain.Project{}, err
	}
	if err := validateProjectConfiguration(config); err != nil {
		return domain.Project{}, err
	}
	if err := requireIdempotency(idempotencyKey); err != nil {
		return domain.Project{}, err
	}
	scope := "project:create"
	var result domain.Project
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[domain.Project](ctx, tx, scope, idempotencyKey); err != nil {
			return err
		} else if ok {
			result = replay
			return nil
		}
		now := s.clock.Now().UTC()
		result = domain.Project{
			ID:              domain.ProjectID(s.ids.NewID()),
			Name:            name,
			RepositoryURL:   config.RepositoryURL,
			Setup:           append([]string(nil), config.Setup...),
			ImageReference:  config.ImageReference,
			CreatedAt:       now,
			UpdatedAt:       now,
			ResourceVersion: 1,
		}
		if err := tx.InsertProject(ctx, result); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "project", string(result.ID), "project.created", 1, nil); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, idempotencyKey, result, now)
	})
	return result, err
}

func validateProjectConfiguration(config ProjectConfiguration) error {
	if len(config.RepositoryURL) > 4096 || strings.ContainsAny(config.RepositoryURL, "\x00\r\n") {
		return fmt.Errorf("%w: repository URL is invalid", domain.ErrInvalid)
	}
	if config.RepositoryURL != "" {
		parsed, err := url.Parse(config.RepositoryURL)
		if err != nil || parsed.User != nil {
			return fmt.Errorf("%w: repository URL is invalid", domain.ErrInvalid)
		}
		switch parsed.Scheme {
		case "http", "https", "git", "ssh", "file":
		case "":
			if !filepath.IsAbs(config.RepositoryURL) {
				return fmt.Errorf("%w: local repository path must be absolute", domain.ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: repository URL scheme is unsupported", domain.ErrInvalid)
		}
	}
	if len(config.Setup) > 128 {
		return fmt.Errorf("%w: setup command has too many arguments", domain.ErrInvalid)
	}
	for _, argument := range config.Setup {
		if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("%w: setup arguments must be non-empty and bounded", domain.ErrInvalid)
		}
	}
	if len(config.ImageReference) > 1024 || strings.ContainsAny(config.ImageReference, "\x00\r\n") {
		return fmt.Errorf("%w: image reference is invalid", domain.ErrInvalid)
	}
	return nil
}

func (s *Service) GetProject(ctx context.Context, id domain.ProjectID) (domain.Project, error) {
	var result domain.Project
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		result, err = reader.GetProject(ctx, id)
		return err
	})
	return result, err
}

func (s *Service) ListProjects(ctx context.Context, offset, limit int) (ProjectPage, error) {
	page := normalizePage(offset, limit)
	var result ProjectPage
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var more bool
		var err error
		result.Items, more, err = reader.ListProjects(ctx, page)
		if more {
			result.NextOffset = page.Offset + len(result.Items)
		}
		return err
	})
	return result, err
}

func (s *Service) CreateCapsule(
	ctx context.Context,
	projectID domain.ProjectID,
	name, idempotencyKey string,
) (domain.Capsule, error) {
	name, err := requireName(name)
	if err != nil {
		return domain.Capsule{}, err
	}
	if err := requireIdempotency(idempotencyKey); err != nil {
		return domain.Capsule{}, err
	}
	scope := "capsule:create:" + string(projectID)
	var result domain.Capsule
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[domain.Capsule](ctx, tx, scope, idempotencyKey); err != nil {
			return err
		} else if ok {
			result = replay
			return nil
		}
		if _, err := tx.GetProject(ctx, projectID); err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		timelineID := domain.TimelineID(s.ids.NewID())
		result = domain.Capsule{
			ID:              domain.CapsuleID(s.ids.NewID()),
			ProjectID:       projectID,
			TimelineID:      timelineID,
			Name:            name,
			State:           domain.CapsuleCreating,
			DesiredState:    domain.IntentReady,
			RestoreComplete: true,
			CreatedAt:       now,
			UpdatedAt:       now,
			ResourceVersion: 1,
		}
		if err := tx.InsertCapsule(ctx, result); err != nil {
			return err
		}
		if err := tx.InsertTimeline(ctx, domain.Timeline{
			ID: timelineID, ProjectID: projectID, CapsuleID: result.ID,
			Reason: domain.TimelineRoot, CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "capsule", string(result.ID), "capsule.create_requested", 1, nil); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, idempotencyKey, result, now)
	})
	if err == nil {
		err = s.queue.Enqueue(ctx, result.ID)
	}
	return result, err
}

func (s *Service) GetCapsule(ctx context.Context, id domain.CapsuleID) (domain.Capsule, error) {
	var result domain.Capsule
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		result, err = reader.GetCapsule(ctx, id)
		return err
	})
	return result, err
}

func (s *Service) ListCapsules(
	ctx context.Context,
	projectID domain.ProjectID,
	offset, limit int,
) (CapsulePage, error) {
	page := normalizePage(offset, limit)
	var result CapsulePage
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var more bool
		var err error
		result.Items, more, err = reader.ListCapsules(ctx, projectID, page)
		if more {
			result.NextOffset = page.Offset + len(result.Items)
		}
		return err
	})
	return result, err
}

func (s *Service) PauseCapsule(
	ctx context.Context,
	id domain.CapsuleID,
	expected domain.ResourceVersion,
	key string,
) (domain.Capsule, error) {
	return s.mutateCapsule(ctx, id, expected, key, "pause", domain.IntentPaused, domain.CapsuleReady)
}

func (s *Service) ResumeCapsule(
	ctx context.Context,
	id domain.CapsuleID,
	expected domain.ResourceVersion,
	key string,
) (domain.Capsule, error) {
	return s.mutateCapsule(ctx, id, expected, key, "resume", domain.IntentReady, domain.CapsulePaused)
}

func (s *Service) DeleteCapsule(
	ctx context.Context,
	id domain.CapsuleID,
	expected domain.ResourceVersion,
	key string,
) (domain.Capsule, error) {
	return s.mutateCapsule(ctx, id, expected, key, "delete", domain.IntentDeleted)
}

func (s *Service) mutateCapsule(
	ctx context.Context,
	id domain.CapsuleID,
	expected domain.ResourceVersion,
	key, operation string,
	intent domain.CapsuleIntent,
	allowedStates ...domain.CapsuleState,
) (domain.Capsule, error) {
	if expected <= 0 {
		return domain.Capsule{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	if err := requireIdempotency(key); err != nil {
		return domain.Capsule{}, err
	}
	scope := "capsule:" + string(id) + ":" + operation
	var result domain.Capsule
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[domain.Capsule](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			result = replay
			return nil
		}
		current, err := tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		if current.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if err := current.CanMutate(); err != nil {
			return err
		}
		if operation == "delete" {
			active, err := tx.HasActiveRun(ctx, id)
			if err != nil {
				return err
			}
			if active {
				return fmt.Errorf("%w: cancel the active Run or Thread before deleting the Capsule", domain.ErrConflict)
			}
		}
		if len(allowedStates) > 0 {
			allowed := false
			for _, state := range allowedStates {
				allowed = allowed || current.State == state
			}
			if !allowed {
				return fmt.Errorf("%w: cannot %s capsule in %s", domain.ErrIllegalTransition, operation, current.State)
			}
		}
		previousVersion := current.ResourceVersion
		current.DesiredState = intent
		current.UpdatedAt = s.clock.Now().UTC()
		current.ResourceVersion++
		if err := tx.UpdateCapsule(ctx, current, previousVersion); err != nil {
			return err
		}
		if err := s.appendEvent(
			ctx, tx, "capsule", string(id), "capsule."+operation+"_requested",
			current.ResourceVersion, nil,
		); err != nil {
			return err
		}
		result = current
		return putReplay(ctx, tx, scope, key, result, current.UpdatedAt)
	})
	if err == nil {
		err = s.queue.Enqueue(ctx, id)
	}
	return result, err
}

func (s *Service) appendEvent(
	ctx context.Context,
	tx ports.Transaction,
	aggregateType, aggregateID, eventType string,
	version domain.ResourceVersion,
	data []byte,
) error {
	return tx.AppendEvent(ctx, domain.Event{
		ID:              domain.EventID(s.ids.NewID()),
		AggregateType:   aggregateType,
		AggregateID:     aggregateID,
		Type:            eventType,
		Timestamp:       s.clock.Now().UTC(),
		ResourceVersion: version,
		Data:            data,
	})
}

func getReplay[T any](
	ctx context.Context,
	reader ports.Reader,
	scope, key string,
) (T, bool, error) {
	var zero T
	record, err := reader.GetIdempotency(ctx, scope, key)
	if errors.Is(err, domain.ErrNotFound) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, err
	}
	var result T
	if err := json.Unmarshal(record.Outcome, &result); err != nil {
		return zero, false, fmt.Errorf("decode idempotency outcome: %w", err)
	}
	return result, true, nil
}

func putReplay[T any](
	ctx context.Context,
	tx ports.Transaction,
	scope, key string,
	outcome T,
	now time.Time,
) error {
	encoded, err := json.Marshal(outcome)
	if err != nil {
		return fmt.Errorf("encode idempotency outcome: %w", err)
	}
	return tx.PutIdempotency(ctx, ports.IdempotencyRecord{
		Scope:     scope,
		Key:       key,
		Outcome:   encoded,
		CreatedAt: now.UTC(),
	})
}
