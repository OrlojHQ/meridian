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
	"github.com/OrlojHQ/meridian/internal/providergateway"
	"github.com/OrlojHQ/meridian/internal/secrets"
)

const defaultPageSize = 50

type Service struct {
	gateway       *providergateway.Gateway
	gatewayURL    string
	store         ports.Store
	clock         ports.Clock
	ids           ports.IDSource
	queue         ports.ReconcileQueue
	runtime       ports.CapsuleRuntime
	preview       ports.PreviewRuntime
	snapshotter   ports.WorkspaceSnapshotter
	browser       ports.WorkspaceBrowser
	delivery      ports.DeliveryRuntime
	github        GitHubClient
	artifacts     ports.ArtifactStore
	observer      ports.Observer
	providerName  string
	structured    ports.StructuredRuntime
	profiles      ports.HarnessProfileRuntime
	transcriptKey transcriptKey
	secretKey     *secrets.InstallationKey
	runSync       [64]sync.Mutex
	threadSync    [64]sync.Mutex
	deliverySync  [64]sync.Mutex
}

func (s *Service) ConfigureSecrets(key *secrets.InstallationKey) {
	s.secretKey = key
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

func (s *Service) ConfigureBrowse(browser ports.WorkspaceBrowser) {
	s.browser = browser
}

func (s *Service) ConfigureDelivery(runtime ports.DeliveryRuntime, github GitHubClient) {
	s.delivery = runtime
	s.github = github
}

func (s *Service) ConfigureThreads(runtime ports.StructuredRuntime, key transcriptKey) {
	s.structured = runtime
	s.profiles = runtime
	s.transcriptKey = key
}

func (s *Service) ConfigureHarnessProfiles(runtime ports.HarnessProfileRuntime) {
	s.profiles = runtime
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
	RepositoryURL       string
	Setup               []string
	ImageReference      string
	HarnessImages       []domain.HarnessImage
	GitSecretName       string
	HarnessSecretNames  []string
	GitPushSecretName   string
	GitHubAPISecretName string
	CommitAuthorName    string
	CommitAuthorEmail   string
	DefaultBaseBranch   string
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
			ID:                  domain.ProjectID(s.ids.NewID()),
			Name:                name,
			RepositoryURL:       config.RepositoryURL,
			Setup:               append([]string(nil), config.Setup...),
			ImageReference:      config.ImageReference,
			HarnessImages:       append([]domain.HarnessImage(nil), config.HarnessImages...),
			GitSecretName:       config.GitSecretName,
			HarnessSecretNames:  append([]string(nil), config.HarnessSecretNames...),
			GitPushSecretName:   config.GitPushSecretName,
			GitHubAPISecretName: config.GitHubAPISecretName,
			CommitAuthorName:    config.CommitAuthorName,
			CommitAuthorEmail:   config.CommitAuthorEmail,
			DefaultBaseBranch:   config.DefaultBaseBranch,
			CreatedAt:           now,
			UpdatedAt:           now,
			ResourceVersion:     1,
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
	if config.GitSecretName != "" {
		if err := validateSecretName(config.GitSecretName); err != nil {
			return err
		}
		parsed, err := url.Parse(config.RepositoryURL)
		if err != nil || parsed.Scheme != "https" {
			return fmt.Errorf("%w: gitSecretName requires an HTTPS repository URL", domain.ErrInvalid)
		}
	}
	if config.GitPushSecretName != "" {
		if err := validateSecretName(config.GitPushSecretName); err != nil {
			return err
		}
		parsed, err := url.Parse(config.RepositoryURL)
		if err != nil || parsed.Scheme != "https" || parsed.User != nil {
			return fmt.Errorf("%w: gitPushSecretName requires a credential-free HTTPS repository URL", domain.ErrInvalid)
		}
	}
	if config.GitHubAPISecretName != "" {
		if err := validateSecretName(config.GitHubAPISecretName); err != nil {
			return err
		}
	}
	if (config.CommitAuthorName == "") != (config.CommitAuthorEmail == "") ||
		len(config.CommitAuthorName) > 320 || len(config.CommitAuthorEmail) > 320 ||
		strings.ContainsAny(config.CommitAuthorName+config.CommitAuthorEmail, "\x00\r\n<>") {
		return fmt.Errorf("%w: commit author name and email must be supplied together", domain.ErrInvalid)
	}
	if config.DefaultBaseBranch != "" && (!validBranch(config.DefaultBaseBranch) ||
		strings.HasPrefix(config.DefaultBaseBranch, "refs/")) {
		return fmt.Errorf("%w: default base branch is invalid", domain.ErrInvalid)
	}
	if len(config.HarnessSecretNames) > 64 {
		return fmt.Errorf("%w: too many authorized harness secrets", domain.ErrInvalid)
	}
	seenSecrets := make(map[string]struct{}, len(config.HarnessSecretNames))
	for _, name := range config.HarnessSecretNames {
		if err := validateSecretName(name); err != nil {
			return err
		}
		if _, exists := seenSecrets[name]; exists {
			return fmt.Errorf("%w: duplicate authorized harness secret", domain.ErrInvalid)
		}
		seenSecrets[name] = struct{}{}
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
	if len(config.HarnessImages) > domain.MaxHarnessImages {
		return fmt.Errorf("%w: too many harness images", domain.ErrInvalid)
	}
	seenImages := make(map[string]struct{}, len(config.HarnessImages))
	for _, item := range config.HarnessImages {
		if !domain.ValidHarnessName(item.Name) {
			return fmt.Errorf("%w: harness image name is invalid", domain.ErrInvalid)
		}
		if !domain.ValidImageReference(item.ImageReference) {
			return fmt.Errorf("%w: harness image reference is invalid", domain.ErrInvalid)
		}
		if _, exists := seenImages[item.Name]; exists {
			return fmt.Errorf("%w: duplicate harness image %q", domain.ErrInvalid, item.Name)
		}
		seenImages[item.Name] = struct{}{}
	}
	return nil
}

func (s *Service) UpdateProjectHarnessImages(
	ctx context.Context,
	id domain.ProjectID,
	images []domain.HarnessImage,
	expected domain.ResourceVersion,
) (domain.Project, error) {
	if err := validateProjectConfiguration(ProjectConfiguration{HarnessImages: images}); err != nil {
		return domain.Project{}, err
	}
	if expected <= 0 {
		return domain.Project{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	var result domain.Project
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		project, err := tx.GetProject(ctx, id)
		if err != nil {
			return err
		}
		if project.ResourceVersion != expected {
			return domain.ErrConflict
		}
		now := s.clock.Now().UTC()
		project.HarnessImages = append([]domain.HarnessImage(nil), images...)
		project.UpdatedAt = now
		project.ResourceVersion++
		if err := tx.UpdateProject(ctx, project, expected); err != nil {
			return err
		}
		if err := s.appendEvent(
			ctx, tx, "project", string(project.ID), "project.harness_images_updated",
			project.ResourceVersion, nil,
		); err != nil {
			return err
		}
		result = project
		return nil
	})
	return result, err
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
	return s.CreateCapsuleForHarness(ctx, projectID, name, "", idempotencyKey)
}

func (s *Service) CreateCapsuleForHarness(
	ctx context.Context,
	projectID domain.ProjectID,
	name, harness, idempotencyKey string,
	setupSelection ...string,
) (domain.Capsule, error) {
	selection := ""
	if len(setupSelection) > 0 {
		selection = setupSelection[0]
	}
	name, err := requireName(name)
	if err != nil {
		return domain.Capsule{}, err
	}
	harness = strings.TrimSpace(harness)
	if harness != "" && !domain.ValidHarnessName(harness) {
		return domain.Capsule{}, fmt.Errorf("%w: harness pack is invalid", domain.ErrInvalid)
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
			savedSelection, _, selectionErr := getReplay[string](ctx, tx, scope+":setup", idempotencyKey)
			if selectionErr != nil {
				return selectionErr
			}
			if replay.Name != name || replay.LauncherHarness != harness || savedSelection != selection {
				return fmt.Errorf(
					"%w: Idempotency-Key was used with a different request",
					domain.ErrConflict,
				)
			}
			result = replay
			return nil
		}
		project, err := tx.GetProject(ctx, projectID)
		if err != nil {
			return err
		}
		image := ""
		if harness != "" {
			if len(project.HarnessImages) == 0 {
				return fmt.Errorf(
					"%w: Project has no allowlisted harness packs",
					domain.ErrInvalid,
				)
			}
			image, err = project.ImageForHarness(harness)
			if err != nil {
				return err
			}
		}
		now := s.clock.Now().UTC()
		timelineID := domain.TimelineID(s.ids.NewID())
		result = domain.Capsule{
			ID:              domain.CapsuleID(s.ids.NewID()),
			ProjectID:       projectID,
			TimelineID:      timelineID,
			Name:            name,
			LauncherHarness: harness,
			State:           domain.CapsuleCreating,
			DesiredState:    domain.IntentReady,
			RestoreComplete: true,
			ImageReference:  image,
			LastActivityAt:  now,
			CreatedAt:       now,
			UpdatedAt:       now,
			ResourceVersion: 1,
		}
		if err := tx.InsertCapsule(ctx, result); err != nil {
			return err
		}

		if err := s.pinHarnessSetups(ctx, tx, result.ID, selection); err != nil {
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
		if err := putReplay(ctx, tx, scope+":setup", idempotencyKey, selection, now); err != nil {
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
		if err == nil {
			if p, loadErr := reader.GetPreparation(ctx, id); loadErr == nil {
				result.Preparation = p.Progress()
			} else if !errors.Is(loadErr, domain.ErrNotFound) {
				return loadErr
			}
		}
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
		if err == nil {
			for i := range result.Items {
				p, loadErr := reader.GetPreparation(ctx, result.Items[i].ID)
				if loadErr == nil {
					result.Items[i].Preparation = p.Progress()
				} else if !errors.Is(loadErr, domain.ErrNotFound) {
					return loadErr
				}
			}
		}
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

func (s *Service) RetryCapsule(ctx context.Context, id domain.CapsuleID, expected domain.ResourceVersion, key string) (domain.Capsule, error) {
	return s.mutateCapsule(ctx, id, expected, key, "retry", domain.IntentReady, domain.CapsuleFailed)
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
		if operation == "retry" {
			preparation, err := tx.GetPreparation(ctx, id)
			if err != nil {
				return err
			}
			if preparation.Stage == "ready" {
				return fmt.Errorf("%w: only failed preparation can be retried", domain.ErrIllegalTransition)
			}
			active, err := tx.HasActiveRun(ctx, id)
			if err != nil {
				return err
			}
			if active {
				return domain.ErrConflict
			}
			intent, err := tx.GetProjectThreadIntentByCapsule(ctx, id)
			if err == nil && intent.State == domain.ProjectThreadFailed && intent.FailureCode == "capsule_failed" {
				previous := intent.ResourceVersion
				intent.State = domain.ProjectThreadProvisioning
				intent.FailureCode = ""
				intent.FailureMessage = ""
				intent.UpdatedAt = s.clock.Now().UTC()
				intent.ResourceVersion++
				if err := tx.UpdateProjectThreadIntent(ctx, intent, previous); err != nil {
					return err
				}
			} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
				return err
			}
			if current.ProviderResourceID != "" {
				preparation.Stage = "reclone"
				if preparation.Legacy {
					preparation.Stage = "preparing"
				}
				preparation.CacheMiss = "Retry requested; preparing fresh"
				preparation.UpdatedAt = s.clock.Now().UTC()
				if err := tx.PutPreparation(ctx, preparation); err != nil {
					return err
				}
			}
			current.State = domain.CapsulePreparing
			if current.ProviderResourceID == "" {
				current.State = domain.CapsuleCreating
			}
			current.Failure = ""
		}
		previousVersion := current.ResourceVersion
		current.DesiredState = intent
		current.UpdatedAt = s.clock.Now().UTC()
		if operation == "resume" {
			current.LastActivityAt = current.UpdatedAt
		}
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
