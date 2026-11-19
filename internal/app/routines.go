package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

// Routines (ADR 0057). A routine is a clock, a fixed
// prompt, and limits. Each fire starts one fresh Project Thread through the
// same path as POST /projects/{id}/threads, attributed to the Project's
// owner. A routine never ships: Delivery still needs the operator.

const (
	OriginRoutine     = "routine"
	OriginFromRoutine = "from_routine"
	// routineOnTime is how late a clock fire may start and still count as
	// on time rather than a catch-up.
	routineOnTime = 2 * time.Minute
	// routineSpacingSamples is how many upcoming fires a save checks for
	// the minimum spacing.
	routineSpacingSamples = 500
)

// RoutineView is one routine as its owner sees it, prompt included.
type RoutineView struct {
	ID              string                 `json:"id"`
	ProjectID       string                 `json:"projectId"`
	Name            string                 `json:"name"`
	Harness         string                 `json:"harness"`
	Model           string                 `json:"model,omitempty"`
	Effort          string                 `json:"effort,omitempty"`
	Prompt          string                 `json:"prompt"`
	Cadence         string                 `json:"cadence"`
	TimeZone        string                 `json:"timeZone"`
	MaxRunSeconds   int                    `json:"maxRunSeconds"`
	CatchUpSeconds  int                    `json:"catchUpSeconds"`
	MaxKept         int                    `json:"maxKept"`
	Enabled         bool                   `json:"enabled"`
	DisabledReason  string                 `json:"disabledReason,omitempty"`
	NextFireAt      *time.Time             `json:"nextFireAt,omitempty"`
	LastFireAt      *time.Time             `json:"lastFireAt,omitempty"`
	KeptCapsules    int                    `json:"keptCapsules"`
	Active          *RoutineFireView       `json:"active,omitempty"`
	LastFire        *RoutineFireView       `json:"lastFire,omitempty"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion"`
	CreatedAt       time.Time              `json:"createdAt"`
	UpdatedAt       time.Time              `json:"updatedAt"`
}

// RoutineFireView is one fire. It carries codes, never prompt text.
type RoutineFireView struct {
	ID             string     `json:"id"`
	RoutineID      string     `json:"routineId"`
	Trigger        string     `json:"trigger"`
	ScheduledFor   time.Time  `json:"scheduledFor"`
	State          string     `json:"state"`
	Outcome        string     `json:"outcome,omitempty"`
	Reason         string     `json:"reason,omitempty"`
	CapsuleID      string     `json:"capsuleId,omitempty"`
	ThreadID       string     `json:"threadId,omitempty"`
	CapsuleDeleted bool       `json:"capsuleDeleted"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// CapsuleRoutineView is the content-free note on a Capsule a fire started.
type CapsuleRoutineView struct {
	RoutineID   string `json:"routineId"`
	RoutineName string `json:"routineName"`
	FireID      string `json:"fireId"`
	State       string `json:"state"`
	Outcome     string `json:"outcome,omitempty"`
}

// RoutineInput creates a routine. Zero limits take their defaults; a nil
// Enabled means enabled.
type RoutineInput struct {
	Name           string `json:"name"`
	Harness        string `json:"harness"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	Prompt         string `json:"prompt"`
	Cadence        string `json:"cadence"`
	TimeZone       string `json:"timeZone"`
	MaxRunSeconds  int    `json:"maxRunSeconds"`
	CatchUpSeconds *int   `json:"catchUpSeconds"`
	MaxKept        int    `json:"maxKept"`
	Enabled        *bool  `json:"enabled"`
}

// RoutineUpdate changes the named fields of a routine.
type RoutineUpdate struct {
	Name                    *string                `json:"name"`
	Harness                 *string                `json:"harness"`
	Model                   *string                `json:"model"`
	Effort                  *string                `json:"effort"`
	Prompt                  *string                `json:"prompt"`
	Cadence                 *string                `json:"cadence"`
	TimeZone                *string                `json:"timeZone"`
	MaxRunSeconds           *int                   `json:"maxRunSeconds"`
	CatchUpSeconds          *int                   `json:"catchUpSeconds"`
	MaxKept                 *int                   `json:"maxKept"`
	Enabled                 *bool                  `json:"enabled"`
	ExpectedResourceVersion domain.ResourceVersion `json:"expectedResourceVersion"`
}

// ReportedModelsView is the model list the most recent session of one
// harness in a Project reported, for the routine form to offer.
type ReportedModelsView struct {
	Models        []adapterproto.ModelOption `json:"models"`
	Current       string                     `json:"current,omitempty"`
	Efforts       []adapterproto.ModelOption `json:"efforts"`
	CurrentEffort string                     `json:"currentEffort,omitempty"`
	ThreadID      string                     `json:"threadId,omitempty"`
}

// routineContent is the sealed part of a routine.
type routineContent struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

type routineReplay struct {
	ID domain.RoutineID `json:"id"`
}

type routineFireReplay struct {
	ID domain.RoutineFireID `json:"id"`
}

// ConfigureRoutines sets the installation live-Capsule cap that scheduled
// fires respect. Zero means no cap.
func (s *Service) ConfigureRoutines(maxLive int) {
	s.routineMaxLive = maxLive
}

// StartRoutines runs the routine clock until ctx is cancelled.
func (s *Service) StartRoutines(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.TickRoutines(ctx)
			case <-s.routineWake:
				_ = s.TickRoutines(ctx)
			}
		}
	}()
}

func (s *Service) wakeRoutines() {
	if s.routineWake == nil {
		return
	}
	select {
	case s.routineWake <- struct{}{}:
	default:
	}
}

func (s *Service) routinesSupported() error {
	if s.structured == nil || s.transcriptKey == nil || s.secretKey == nil {
		return fmt.Errorf("%w: scheduled Threads need structured sessions", domain.ErrUnsupported)
	}
	return nil
}

// ListRoutines lists a Project's routines.
func (s *Service) ListRoutines(ctx context.Context, id domain.ProjectID) ([]RoutineView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return nil, err
	}
	views := []RoutineView{}
	err := s.store.View(ctx, func(reader ports.Reader) error {
		items, err := reader.ListRoutines(ctx, id)
		if err != nil {
			return err
		}
		for _, item := range items {
			view, err := s.routineView(ctx, reader, item)
			if err != nil {
				return err
			}
			views = append(views, view)
		}
		return nil
	})
	return views, err
}

// ListOwnedRoutines lists every routine the caller owns, across Projects,
// for the Routines page.
func (s *Service) ListOwnedRoutines(ctx context.Context) ([]RoutineView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return nil, err
	}
	scope, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	views := []RoutineView{}
	err = s.store.View(ctx, func(reader ports.Reader) error {
		items, err := reader.ListOwnedRoutines(ctx, scope.keyOwner())
		if err != nil {
			return err
		}
		for _, item := range items {
			view, err := s.routineView(ctx, reader, item)
			if err != nil {
				return err
			}
			views = append(views, view)
		}
		return nil
	})
	return views, err
}

// GetRoutine returns one routine.
func (s *Service) GetRoutine(ctx context.Context, id domain.RoutineID) (RoutineView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return RoutineView{}, err
	}
	var view RoutineView
	err := s.store.View(ctx, func(reader ports.Reader) error {
		item, err := reader.GetRoutine(ctx, id)
		if err != nil {
			return err
		}
		view, err = s.routineView(ctx, reader, item)
		return err
	})
	return view, err
}

// CreateRoutine saves a new routine on a Project.
func (s *Service) CreateRoutine(
	ctx context.Context, projectID domain.ProjectID, input RoutineInput, key string,
) (RoutineView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return RoutineView{}, err
	}
	if err := s.routinesSupported(); err != nil {
		return RoutineView{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return RoutineView{}, err
	}
	scope := "project:" + string(projectID) + ":routine-create"
	var view RoutineView
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[routineReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			item, err := tx.GetRoutine(ctx, replay.ID)
			if err != nil {
				return err
			}
			view, err = s.routineView(ctx, tx, item)
			return err
		}
		project, err := tx.GetProject(ctx, projectID)
		if err != nil {
			return err
		}
		if err := projectAcceptsWork(project); err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		routine := domain.Routine{
			ID: domain.RoutineID(s.ids.NewID()), ProjectID: project.ID, OwnerUserID: project.OwnerUserID,
			Name: strings.TrimSpace(input.Name), Harness: strings.TrimSpace(input.Harness),
			Cadence: strings.TrimSpace(input.Cadence), TimeZone: strings.TrimSpace(input.TimeZone),
			MaxRun: domain.RoutineDefaultRun, CatchUp: domain.RoutineDefaultCatchUp,
			MaxKept: domain.RoutineDefaultKept, Enabled: input.Enabled == nil || *input.Enabled,
			ResourceVersion: 1, CreatedAt: now, UpdatedAt: now,
		}
		if input.MaxRunSeconds != 0 {
			routine.MaxRun = time.Duration(input.MaxRunSeconds) * time.Second
		}
		if input.CatchUpSeconds != nil {
			routine.CatchUp = time.Duration(*input.CatchUpSeconds) * time.Second
		}
		if input.MaxKept != 0 {
			routine.MaxKept = input.MaxKept
		}
		content := routineContent{
			Prompt: input.Prompt, Model: strings.TrimSpace(input.Model), Effort: strings.TrimSpace(input.Effort),
		}
		if err := s.prepareRoutine(ctx, tx, project, &routine, content, now); err != nil {
			return err
		}
		if err := tx.InsertRoutine(ctx, routine); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditRoutineCreate, "routine", string(routine.ID)); err != nil {
			return err
		}
		if err := putReplay(ctx, tx, scope, key, routineReplay{ID: routine.ID}, now); err != nil {
			return err
		}
		view, err = s.routineView(ctx, tx, routine)
		return err
	})
	if err == nil {
		s.wakeRoutines()
	}
	return view, err
}

// UpdateRoutine changes a routine. Turning a routine on clears the reason
// Meridian turned it off and the start failure count.
func (s *Service) UpdateRoutine(
	ctx context.Context, id domain.RoutineID, input RoutineUpdate, key string,
) (RoutineView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return RoutineView{}, err
	}
	if err := s.routinesSupported(); err != nil {
		return RoutineView{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return RoutineView{}, err
	}
	if input.ExpectedResourceVersion <= 0 {
		return RoutineView{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	scope := "routine:" + string(id) + ":update"
	var view RoutineView
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, ok, err := getReplay[routineReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			item, err := tx.GetRoutine(ctx, id)
			if err != nil {
				return err
			}
			view, err = s.routineView(ctx, tx, item)
			return err
		}
		routine, err := tx.GetRoutine(ctx, id)
		if err != nil {
			return err
		}
		if routine.ResourceVersion != input.ExpectedResourceVersion {
			return domain.ErrConflict
		}
		project, err := tx.GetProject(ctx, routine.ProjectID)
		if err != nil {
			return err
		}
		if err := projectAcceptsWork(project); err != nil {
			return err
		}
		content, err := s.openRoutineContent(routine)
		if err != nil {
			return err
		}
		setString := func(dst *string, value *string) {
			if value != nil {
				*dst = strings.TrimSpace(*value)
			}
		}
		setString(&routine.Name, input.Name)
		setString(&routine.Harness, input.Harness)
		setString(&routine.Cadence, input.Cadence)
		setString(&routine.TimeZone, input.TimeZone)
		setString(&content.Model, input.Model)
		setString(&content.Effort, input.Effort)
		if input.Prompt != nil {
			content.Prompt = *input.Prompt
		}
		if input.MaxRunSeconds != nil {
			routine.MaxRun = time.Duration(*input.MaxRunSeconds) * time.Second
		}
		if input.CatchUpSeconds != nil {
			routine.CatchUp = time.Duration(*input.CatchUpSeconds) * time.Second
		}
		if input.MaxKept != nil {
			routine.MaxKept = *input.MaxKept
		}
		if input.Enabled != nil {
			if *input.Enabled && !routine.Enabled {
				routine.DisabledReason = ""
				routine.StartFailures = 0
			}
			routine.Enabled = *input.Enabled
		}
		now := s.clock.Now().UTC()
		previous := routine.ResourceVersion
		routine.ResourceVersion++
		routine.UpdatedAt = now
		if err := s.prepareRoutine(ctx, tx, project, &routine, content, now); err != nil {
			return err
		}
		if err := tx.UpdateRoutine(ctx, routine, previous); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditRoutineUpdate, "routine", string(routine.ID)); err != nil {
			return err
		}
		if err := putReplay(ctx, tx, scope, key, routineReplay{ID: routine.ID}, now); err != nil {
			return err
		}
		view, err = s.routineView(ctx, tx, routine)
		return err
	})
	if err == nil {
		s.wakeRoutines()
	}
	return view, err
}

// DeleteRoutine removes a routine and its fire history. A run in progress
// keeps going as an ordinary Thread; Capsules it kept stay until the
// operator deletes them.
func (s *Service) DeleteRoutine(
	ctx context.Context, id domain.RoutineID, expected domain.ResourceVersion, key string,
) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	if err := requireIdempotency(key); err != nil {
		return err
	}
	if expected <= 0 {
		return fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	scope := "routine:" + string(id) + ":delete"
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, ok, err := getReplay[routineReplay](ctx, tx, scope, key); err != nil || ok {
			return err
		}
		routine, err := tx.GetRoutine(ctx, id)
		if err != nil {
			return err
		}
		if routine.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if err := tx.DeleteRoutine(ctx, id); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditRoutineDelete, "routine", string(id)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, routineReplay{ID: id}, s.clock.Now().UTC())
	})
}

// RunRoutine fires a routine now. The same rules apply as for a clock
// fire: an overlapping run, a full backlog, or the live-Capsule cap records
// a skip instead. It works on a turned-off routine too.
func (s *Service) RunRoutine(ctx context.Context, id domain.RoutineID, key string) (RoutineFireView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return RoutineFireView{}, err
	}
	if err := s.routinesSupported(); err != nil {
		return RoutineFireView{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return RoutineFireView{}, err
	}
	scope := "routine:" + string(id) + ":run"
	var fire domain.RoutineFire
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[routineFireReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			fire, err = tx.GetRoutineFire(ctx, replay.ID)
			return err
		}
		routine, err := tx.GetRoutine(ctx, id)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		fire, err = s.gateFire(ctx, tx, &routine, domain.RoutineTriggerManual, now, now)
		if err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditRoutineRun, "routine", string(id)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, routineFireReplay{ID: fire.ID}, now)
	})
	if err != nil {
		return RoutineFireView{}, err
	}
	s.wakeRoutines()
	return routineFireView(fire), nil
}

// ListRoutineFires lists a routine's fires, newest first.
func (s *Service) ListRoutineFires(
	ctx context.Context, id domain.RoutineID, page ports.Page,
) ([]RoutineFireView, bool, error) {
	if err := requirePrincipal(ctx); err != nil {
		return nil, false, err
	}
	views := []RoutineFireView{}
	var more bool
	err := s.store.View(ctx, func(reader ports.Reader) error {
		items, hasMore, err := reader.ListRoutineFires(ctx, id, page)
		if err != nil {
			return err
		}
		more = hasMore
		for _, item := range items {
			views = append(views, routineFireView(item))
		}
		return nil
	})
	return views, more, err
}

// CapsuleRoutines returns the routine note for each Capsule a fire started.
func (s *Service) CapsuleRoutines(
	ctx context.Context, ids []domain.CapsuleID,
) (map[domain.CapsuleID]CapsuleRoutineView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return nil, err
	}
	result := map[domain.CapsuleID]CapsuleRoutineView{}
	err := s.store.View(ctx, func(reader ports.Reader) error {
		fires, err := reader.RoutineFiresByCapsule(ctx, ids)
		if err != nil {
			return err
		}
		names := map[domain.RoutineID]string{}
		for id, fire := range fires {
			name, ok := names[fire.RoutineID]
			if !ok {
				routine, err := reader.GetRoutine(ctx, fire.RoutineID)
				if errors.Is(err, domain.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				name = routine.Name
				names[fire.RoutineID] = name
			}
			result[id] = CapsuleRoutineView{
				RoutineID: string(fire.RoutineID), RoutineName: name, FireID: string(fire.ID),
				State: string(fire.State), Outcome: string(fire.Outcome),
			}
		}
		return nil
	})
	return result, err
}

// ReportedModels returns the models the most recent session of harness in
// the Project reported, decrypted in the daemon from that Thread's
// transcript. A Project with no such session reports none.
func (s *Service) ReportedModels(ctx context.Context, id domain.ProjectID, harness string) (ReportedModelsView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ReportedModelsView{}, err
	}
	view := ReportedModelsView{Models: []adapterproto.ModelOption{}, Efforts: []adapterproto.ModelOption{}}
	if s.transcriptKey == nil || !domain.ValidHarnessName(harness) {
		return view, nil
	}
	var threads []domain.Thread
	err := s.store.View(ctx, func(reader ports.Reader) error {
		if _, err := reader.GetProject(ctx, id); err != nil {
			return err
		}
		capsules, _, err := reader.ListCapsules(ctx, id, domain.CapsuleMain, ports.Page{Limit: 50})
		if err != nil {
			return err
		}
		for _, capsule := range capsules {
			page, _, err := reader.ListThreads(ctx, capsule.ID, ports.Page{Limit: 5})
			if err != nil {
				return err
			}
			for _, thread := range page {
				if thread.AdapterID == harness && thread.MessageCount > 0 {
					threads = append(threads, thread)
				}
			}
		}
		return nil
	})
	if err != nil {
		return view, err
	}
	var newest domain.Thread
	for _, thread := range threads {
		if thread.UpdatedAt.After(newest.UpdatedAt) {
			newest = thread
		}
	}
	if newest.ID == "" {
		return view, nil
	}
	set, err := s.latestModelSet(ctx, newest)
	if err != nil || set == nil {
		return view, err
	}
	view.Models, view.Current, view.ThreadID = set.Options, set.Current, string(newest.ID)
	if set.Effort != nil {
		view.Efforts, view.CurrentEffort = set.Effort.Options, set.Effort.Current
	}
	return view, nil
}

// prepareRoutine validates routine and content together, seals the
// content, and sets the next fire.
func (s *Service) prepareRoutine(
	ctx context.Context, tx ports.Transaction, project domain.Project,
	routine *domain.Routine, content routineContent, now time.Time,
) error {
	if !domain.ValidRoutineName(routine.Name) {
		return fmt.Errorf("%w: name must be 1 to 48 lowercase letters, digits, or dashes", domain.ErrInvalid)
	}
	if _, err := s.structuredHarnessImage(project, routine.Harness); err != nil {
		return err
	}
	if strings.TrimSpace(content.Prompt) == "" {
		return fmt.Errorf("%w: a routine needs a prompt", domain.ErrInvalid)
	}
	if len(content.Prompt) > domain.RoutineMaxPromptBytes {
		return fmt.Errorf("%w: prompt is longer than 32 KiB", domain.ErrResourceExhausted)
	}
	if content.Model != "" && !adapterproto.ValidModelID(content.Model) {
		return fmt.Errorf("%w: model must be a bounded identifier", domain.ErrInvalid)
	}
	if content.Effort != "" && !adapterproto.ValidModelID(content.Effort) {
		return fmt.Errorf("%w: effort must be a bounded identifier", domain.ErrInvalid)
	}
	if routine.MaxRun < domain.RoutineMinRun || routine.MaxRun > domain.RoutineMaxRun {
		return fmt.Errorf("%w: maximum run time must be from 5 minutes to 24 hours", domain.ErrInvalid)
	}
	if routine.CatchUp < 0 || routine.CatchUp > domain.RoutineMaxCatchUp {
		return fmt.Errorf("%w: catch-up window must be from 0 to 24 hours", domain.ErrInvalid)
	}
	if routine.MaxKept < 1 || routine.MaxKept > domain.RoutineMaxKept {
		return fmt.Errorf("%w: kept Capsules must be from 1 to 20", domain.ErrInvalid)
	}
	cadence, err := domain.ParseCadence(routine.Cadence, routine.TimeZone)
	if err != nil {
		return err
	}
	if err := cadence.CheckSpacing(now, routineSpacingSamples); err != nil {
		return err
	}
	others, err := tx.ListRoutines(ctx, project.ID)
	if err != nil {
		return err
	}
	for _, other := range others {
		if other.ID != routine.ID && other.Name == routine.Name {
			return fmt.Errorf("%w: this Project already has a routine named %s", domain.ErrConflict, routine.Name)
		}
	}
	plain, err := json.Marshal(content)
	if err != nil {
		return err
	}
	defer wipe(plain)
	routine.ContentNonce, routine.ContentCiphertext, err = s.secretKey.SealBlob(routineContentLabel(*routine), plain)
	if err != nil {
		return err
	}
	if routine.Enabled {
		routine.NextFireAt = cadence.Next(now)
	} else {
		routine.NextFireAt = time.Time{}
	}
	return nil
}

// routineContentLabel binds the sealed content to its routine and Project,
// so a ciphertext cannot be moved to another row. It leaves the owner out so
// a departed-member transfer can hand the prompt to the new owner.
func routineContentLabel(routine domain.Routine) string {
	return "routine\x00" + string(routine.ID) + "\x00" + string(routine.ProjectID)
}

func (s *Service) openRoutineContent(routine domain.Routine) (routineContent, error) {
	if s.secretKey == nil {
		return routineContent{}, domain.ErrUnsupported
	}
	plain, err := s.secretKey.OpenBlob(routineContentLabel(routine), routine.ContentNonce, routine.ContentCiphertext)
	if err != nil {
		return routineContent{}, err
	}
	defer wipe(plain)
	var content routineContent
	if err := json.Unmarshal(plain, &content); err != nil {
		return routineContent{}, domain.ErrCorrupt
	}
	return content, nil
}

func (s *Service) routineView(ctx context.Context, reader ports.Reader, routine domain.Routine) (RoutineView, error) {
	content, err := s.openRoutineContent(routine)
	if err != nil {
		return RoutineView{}, err
	}
	view := RoutineView{
		ID: string(routine.ID), ProjectID: string(routine.ProjectID), Name: routine.Name,
		Harness: routine.Harness, Model: content.Model, Effort: content.Effort, Prompt: content.Prompt,
		Cadence: routine.Cadence, TimeZone: routine.TimeZone,
		MaxRunSeconds: int(routine.MaxRun / time.Second), CatchUpSeconds: int(routine.CatchUp / time.Second),
		MaxKept: routine.MaxKept, Enabled: routine.Enabled, DisabledReason: routine.DisabledReason,
		NextFireAt: optionalTimeView(routine.NextFireAt), LastFireAt: optionalTimeView(routine.LastFireAt),
		ResourceVersion: routine.ResourceVersion, CreatedAt: routine.CreatedAt, UpdatedAt: routine.UpdatedAt,
	}
	view.KeptCapsules, err = reader.CountKeptRoutineCapsules(ctx, routine.ID)
	if err != nil {
		return RoutineView{}, err
	}
	if active, err := reader.ActiveRoutineFire(ctx, routine.ID); err == nil {
		item := routineFireView(active)
		view.Active = &item
	} else if !errors.Is(err, domain.ErrNotFound) {
		return RoutineView{}, err
	}
	fires, _, err := reader.ListRoutineFires(ctx, routine.ID, ports.Page{Limit: 1})
	if err != nil {
		return RoutineView{}, err
	}
	if len(fires) > 0 {
		item := routineFireView(fires[0])
		view.LastFire = &item
	}
	return view, nil
}

func routineFireView(fire domain.RoutineFire) RoutineFireView {
	return RoutineFireView{
		ID: string(fire.ID), RoutineID: string(fire.RoutineID), Trigger: string(fire.Trigger),
		ScheduledFor: fire.ScheduledFor, State: string(fire.State), Outcome: string(fire.Outcome),
		Reason: fire.Reason, CapsuleID: string(fire.CapsuleID), ThreadID: string(fire.ThreadID),
		CapsuleDeleted: fire.CapsuleDeleted, StartedAt: optionalTimeView(fire.StartedAt),
		FinishedAt: optionalTimeView(fire.FinishedAt), CreatedAt: fire.CreatedAt,
	}
}

func optionalTimeView(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copied := value
	return &copied
}

// TickRoutines fires due routines and advances fires in progress. It is
// safe to call from tests.
func (s *Service) TickRoutines(ctx context.Context) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	if s.routinesSupported() != nil {
		return nil
	}
	now := s.clock.Now().UTC()
	var due []domain.Routine
	var active []domain.RoutineFire
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		if due, err = reader.ListDueRoutines(ctx, now, 20); err != nil {
			return err
		}
		active, err = reader.ListActiveRoutineFires(ctx, 50)
		return err
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, routine := range due {
		if err := s.fireDue(ctx, routine.ID); err != nil && !errors.Is(err, domain.ErrConflict) {
			errs = append(errs, err)
		}
	}
	// Re-read: a fire inserted above starts on this tick, not the next.
	if len(due) > 0 {
		if err := s.store.View(ctx, func(reader ports.Reader) error {
			var err error
			active, err = reader.ListActiveRoutineFires(ctx, 50)
			return err
		}); err != nil {
			return err
		}
	}
	for _, fire := range active {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.advanceFire(ctx, fire.ID); err != nil && !routineRetryable(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// routineRetryable reports errors that a later tick resolves on its own.
func routineRetryable(err error) bool {
	return errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrIllegalTransition)
}

// fireDue handles one routine whose next fire time has passed: it records
// missed times, fires at most once, and moves the clock on.
func (s *Service) fireDue(ctx context.Context, id domain.RoutineID) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		routine, err := tx.GetRoutine(ctx, id)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		if !routine.Enabled || routine.NextFireAt.IsZero() || routine.NextFireAt.After(now) {
			return nil
		}
		cadence, err := domain.ParseCadence(routine.Cadence, routine.TimeZone)
		if err != nil {
			return err
		}
		times, older := dueTimes(cadence, routine.NextFireAt, now)
		var fire *time.Time
		var trigger domain.RoutineFireTrigger
		if len(times) > 0 {
			latest := times[len(times)-1]
			switch late := now.Sub(latest); {
			case late <= routineOnTime:
				fire, trigger = &latest, domain.RoutineTriggerClock
			case late <= routine.CatchUp:
				fire, trigger = &latest, domain.RoutineTriggerCatchUp
			}
			if fire != nil {
				times = times[:len(times)-1]
			}
		}
		if older {
			times = append([]time.Time{routine.NextFireAt}, times...)
		}
		if len(times) > domain.RoutineMissedRecords {
			times = times[len(times)-domain.RoutineMissedRecords:]
		}
		for _, at := range times {
			if err := s.recordMissed(ctx, tx, routine, at, now); err != nil {
				return err
			}
		}
		if fire != nil {
			if _, err := tx.GetRoutineFireAt(ctx, routine.ID, *fire); errors.Is(err, domain.ErrNotFound) {
				if _, err := s.gateFire(ctx, tx, &routine, trigger, *fire, now); err != nil {
					return err
				}
				if err := s.audit(ctx, tx, domain.AuditRoutineFire, "routine", string(routine.ID)); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		// gateFire may have turned the routine off; read what it wrote.
		current, err := tx.GetRoutine(ctx, id)
		if err != nil {
			return err
		}
		before := current.ResourceVersion
		current.NextFireAt = time.Time{}
		if current.Enabled {
			current.NextFireAt = cadence.Next(now)
		}
		if fire != nil {
			current.LastFireAt = *fire
		}
		current.ResourceVersion++
		current.UpdatedAt = now
		return tx.UpdateRoutine(ctx, current, before)
	})
}

// dueTimes lists fire times from next up to now. Times more than a day old
// can never be caught up, so they are not listed one by one: older reports
// that some exist.
func dueTimes(cadence domain.Cadence, next, now time.Time) ([]time.Time, bool) {
	floor := now.Add(-domain.RoutineMaxCatchUp - time.Hour)
	older := next.Before(floor)
	at := next
	if older {
		at = cadence.Next(floor)
	}
	var times []time.Time
	for !at.IsZero() && !at.After(now) && len(times) < 1000 {
		times = append(times, at)
		at = cadence.Next(at)
	}
	return times, older
}

func (s *Service) recordMissed(
	ctx context.Context, tx ports.Transaction, routine domain.Routine, at, now time.Time,
) error {
	if _, err := tx.GetRoutineFireAt(ctx, routine.ID, at); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return tx.InsertRoutineFire(ctx, domain.RoutineFire{
		ID: domain.RoutineFireID(s.ids.NewID()), RoutineID: routine.ID, ProjectID: routine.ProjectID,
		OwnerUserID: routine.OwnerUserID, Trigger: domain.RoutineTriggerClock, ScheduledFor: at,
		State: domain.RoutineFireMissed, Reason: domain.RoutineReasonMissed, FinishedAt: now,
		ResourceVersion: 1, CreatedAt: now, UpdatedAt: now,
	})
}

// gateFire records one fire: a skip with its reason, or a starting fire the
// next tick turns into a Thread. It may turn the routine off, which the
// caller persists.
func (s *Service) gateFire(
	ctx context.Context, tx ports.Transaction, routine *domain.Routine,
	trigger domain.RoutineFireTrigger, at, now time.Time,
) (domain.RoutineFire, error) {
	fire := domain.RoutineFire{
		ID: domain.RoutineFireID(s.ids.NewID()), RoutineID: routine.ID, ProjectID: routine.ProjectID,
		OwnerUserID: routine.OwnerUserID, Trigger: trigger, ScheduledFor: at,
		State: domain.RoutineFireStarting, ResourceVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	reason, err := s.fireBlocked(ctx, tx, routine)
	if err != nil {
		return domain.RoutineFire{}, err
	}
	if reason != "" {
		fire.State, fire.Reason, fire.FinishedAt = domain.RoutineFireSkipped, reason, now
	}
	if reason == domain.RoutineDisabledHarness {
		previous := routine.ResourceVersion
		routine.Enabled, routine.DisabledReason, routine.NextFireAt = false, reason, time.Time{}
		routine.ResourceVersion++
		routine.UpdatedAt = now
		if err := tx.UpdateRoutine(ctx, *routine, previous); err != nil {
			return domain.RoutineFire{}, err
		}
	}
	return fire, tx.InsertRoutineFire(ctx, fire)
}

func (s *Service) fireBlocked(ctx context.Context, tx ports.Transaction, routine *domain.Routine) (string, error) {
	project, err := tx.GetProject(ctx, routine.ProjectID)
	if err != nil {
		return "", err
	}
	if projectAcceptsWork(project) != nil {
		return domain.RoutineReasonProject, nil
	}
	if _, err := s.structuredHarnessImage(project, routine.Harness); err != nil {
		return domain.RoutineDisabledHarness, nil
	}
	if routine.OwnerUserID != "" {
		user, err := tx.GetUser(ctx, routine.OwnerUserID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && !user.DisabledAt.IsZero()) {
			return domain.RoutineReasonOwner, nil
		}
		if err != nil {
			return "", err
		}
	}
	if _, err := tx.ActiveRoutineFire(ctx, routine.ID); err == nil {
		return domain.RoutineReasonOverlap, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return "", err
	}
	kept, err := tx.CountKeptRoutineCapsules(ctx, routine.ID)
	if err != nil {
		return "", err
	}
	if kept >= routine.MaxKept {
		return domain.RoutineReasonBacklog, nil
	}
	if s.routineMaxLive > 0 {
		live, err := tx.CountLiveCapsules(ctx)
		if err != nil {
			return "", err
		}
		if live >= s.routineMaxLive {
			return domain.RoutineReasonCapacity, nil
		}
	}
	return "", nil
}

// advanceFire moves one fire in progress forward by at most one step.
func (s *Service) advanceFire(ctx context.Context, id domain.RoutineFireID) error {
	var fire domain.RoutineFire
	var routine domain.Routine
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		if fire, err = reader.GetRoutineFire(ctx, id); err != nil {
			return err
		}
		routine, err = reader.GetRoutine(ctx, fire.RoutineID)
		return err
	})
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	if fire.ThreadID == "" {
		return s.startFire(ctx, fire, routine)
	}
	if fire.State != domain.RoutineFireCleaning && !fire.DeadlineAt.IsZero() && now.After(fire.DeadlineAt) {
		return s.stopFire(ctx, fire, domain.RoutineOutcomeTimedOut)
	}
	switch fire.State {
	case domain.RoutineFireStarting:
		return s.awaitSession(ctx, fire, routine)
	case domain.RoutineFireSelecting:
		return s.selectModel(ctx, fire, routine)
	case domain.RoutineFireRunning:
		return s.watchRun(ctx, fire)
	case domain.RoutineFireCleaning:
		return s.cleanFire(ctx, fire)
	}
	return nil
}

// startFire creates the fire's Thread. The idempotency key is the fire, so
// a daemon that stopped after creating the Thread and before recording it
// gets the same Thread back.
func (s *Service) startFire(ctx context.Context, fire domain.RoutineFire, routine domain.Routine) error {
	location, err := time.LoadLocation(routine.TimeZone)
	if err != nil {
		location = time.UTC
	}
	name := routine.Name + " " + fire.ScheduledFor.In(location).Format("2006-01-02 15:04")
	result, err := s.CreateProjectThread(ctx, CreateProjectThreadInput{
		ProjectID: routine.ProjectID, Name: name, Harness: routine.Harness,
		IdempotencyKey: "routine:" + string(fire.ID),
	})
	if err != nil {
		if routineRetryable(err) {
			return err
		}
		return s.failStart(ctx, fire, domain.RoutineReasonStartFailed)
	}
	return s.updateFire(ctx, fire, func(current *domain.RoutineFire, now time.Time) {
		current.CapsuleID, current.ThreadID = result.Intent.CapsuleID, result.Intent.ThreadID
		current.StartedAt = now
		current.DeadlineAt = now.Add(routine.MaxRun)
	})
}

// awaitSession waits for the Thread's structured session to start.
func (s *Service) awaitSession(ctx context.Context, fire domain.RoutineFire, routine domain.Routine) error {
	var capsule domain.Capsule
	var thread domain.Thread
	var run domain.Run
	var intentFailed bool
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		if capsule, err = reader.GetCapsule(ctx, fire.CapsuleID); err != nil {
			return err
		}
		intent, err := reader.GetProjectThreadIntentByCapsule(ctx, fire.CapsuleID)
		if err != nil {
			return err
		}
		intentFailed = intent.State == domain.ProjectThreadFailed
		if thread, err = reader.GetThread(ctx, fire.ThreadID); errors.Is(err, domain.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if thread.CurrentRunID != "" {
			run, err = reader.GetRun(ctx, thread.CurrentRunID)
		}
		return err
	})
	if err != nil {
		return err
	}
	if intentFailed || capsule.State == domain.CapsuleFailed || capsule.DesiredState == domain.IntentDeleted ||
		(run.ID != "" && run.State.Terminal()) {
		return s.failStart(ctx, fire, domain.RoutineReasonStartFailed)
	}
	if thread.ID == "" || thread.State != domain.ThreadActive || run.ID == "" || run.State != domain.RunRunning {
		return nil
	}
	return s.updateFire(ctx, fire, func(current *domain.RoutineFire, now time.Time) {
		current.State = domain.RoutineFireSelecting
		current.SelectingSince = now
	})
}

// selectModel applies the routine's model and effort before the prompt is
// sent. Nothing falls back to the harness default (ADR 0057): a run on a
// model the operator did not pick fails instead.
func (s *Service) selectModel(ctx context.Context, fire domain.RoutineFire, routine domain.Routine) error {
	content, err := s.openRoutineContent(routine)
	if err != nil {
		return err
	}
	defer func() { content.Prompt = "" }()
	if content.Model == "" && content.Effort == "" {
		return s.sendRoutinePrompt(ctx, fire, content.Prompt)
	}
	thread, capsule, err := s.fireThread(ctx, fire)
	if err != nil {
		return err
	}
	if thread.State != domain.ThreadActive || thread.CurrentRunID == "" {
		return s.failStart(ctx, fire, domain.RoutineReasonSessionEnded)
	}
	runtimeRun, err := s.structured.GetStructured(ctx, capsule.ProviderResourceID, thread.CurrentRunID)
	if err != nil {
		return err
	}
	if content.Model != "" && !runtimeRun.Models {
		return s.failStart(ctx, fire, domain.RoutineReasonModelNone)
	}
	if content.Effort != "" && !runtimeRun.Effort {
		return s.failStart(ctx, fire, domain.RoutineReasonEffortNone)
	}
	now := s.clock.Now().UTC()
	waited := now.Sub(fire.SelectingSince) > domain.RoutineModelWait
	set, err := s.latestModelSet(ctx, thread)
	if err != nil {
		return err
	}
	if set == nil {
		if waited {
			return s.failStart(ctx, fire, domain.RoutineReasonModelMissing)
		}
		return nil
	}
	if content.Model != "" && set.Current != content.Model {
		if fire.ModelRequested {
			if waited {
				return s.failStart(ctx, fire, domain.RoutineReasonModelRefused)
			}
			return nil
		}
		if !hasModelOption(set.Options, content.Model) {
			return s.failStart(ctx, fire, domain.RoutineReasonModelAbsent)
		}
		if _, err := s.SetThreadModel(ctx, thread.ID, thread.ResourceVersion, content.Model,
			"routine-model-"+string(fire.ID)); err != nil {
			return err
		}
		return s.updateFire(ctx, fire, func(current *domain.RoutineFire, now time.Time) {
			current.ModelRequested, current.SelectingSince = true, now
		})
	}
	if content.Effort != "" && (set.Effort == nil || set.Effort.Current != content.Effort) {
		if fire.EffortRequested {
			if waited {
				return s.failStart(ctx, fire, domain.RoutineReasonEffortRefused)
			}
			return nil
		}
		if set.Effort == nil || !hasModelOption(set.Effort.Options, content.Effort) {
			return s.failStart(ctx, fire, domain.RoutineReasonEffortAbsent)
		}
		if _, err := s.SetThreadEffort(ctx, thread.ID, thread.ResourceVersion, content.Effort,
			"routine-effort-"+string(fire.ID)); err != nil {
			return err
		}
		return s.updateFire(ctx, fire, func(current *domain.RoutineFire, now time.Time) {
			current.EffortRequested, current.SelectingSince = true, now
		})
	}
	return s.sendRoutinePrompt(ctx, fire, content.Prompt)
}

func hasModelOption(options []adapterproto.ModelOption, id string) bool {
	for _, option := range options {
		if option.ID == id {
			return true
		}
	}
	return false
}

// sendRoutinePrompt delivers the routine's prompt as the Thread's first
// user turn, labeled as coming from the routine.
func (s *Service) sendRoutinePrompt(ctx context.Context, fire domain.RoutineFire, prompt string) error {
	frame := func(messageID domain.ThreadMessageID, thread domain.Thread, saved persistedThreadBlock) adapterproto.Frame {
		return userMessageFrame(messageID, thread.ID, saved)
	}
	payload := persistedThreadBlock{
		Content: prompt, Origin: &ThreadOrigin{Source: OriginRoutine, Kind: OriginFromRoutine},
	}
	if _, err := s.appendAndSendHook(ctx, fire.ThreadID, 0, "routine-prompt-"+string(fire.ID), "routine",
		domain.ThreadRoleUser, domain.ThreadMessagePrompt, domain.ThreadBlockText, payload, frame, nil); err != nil {
		return err
	}
	err := s.updateFire(ctx, fire, func(current *domain.RoutineFire, _ time.Time) {
		current.State = domain.RoutineFireRunning
	})
	if err != nil {
		return err
	}
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		routine, err := tx.GetRoutine(ctx, fire.RoutineID)
		if err != nil || routine.StartFailures == 0 {
			return err
		}
		previous := routine.ResourceVersion
		routine.StartFailures = 0
		routine.ResourceVersion++
		routine.UpdatedAt = s.clock.Now().UTC()
		return tx.UpdateRoutine(ctx, routine, previous)
	})
}

// watchRun decides the outcome once the Thread has stayed idle, with
// nothing waiting on the operator and no checks running, for
// RoutineIdleSettle.
func (s *Service) watchRun(ctx context.Context, fire domain.RoutineFire) error {
	thread, capsule, err := s.fireThread(ctx, fire)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	ended := thread.State != domain.ThreadActive || thread.CurrentRunID == ""
	if !ended {
		run, err := s.GetRunStored(ctx, thread.CurrentRunID)
		if err != nil {
			return err
		}
		ended = run.State.Terminal()
	}
	if !ended {
		attention, err := s.ThreadAttention(ctx, thread)
		if err != nil {
			return err
		}
		if attention.Awaiting != nil {
			if fire.Outcome == domain.RoutineOutcomeWaiting && fire.IdleSince.IsZero() {
				return nil
			}
			return s.updateFire(ctx, fire, func(current *domain.RoutineFire, _ time.Time) {
				current.Outcome, current.IdleSince = domain.RoutineOutcomeWaiting, time.Time{}
			})
		}
		if attention.Turn != ThreadTurnIdle {
			if fire.Outcome == domain.RoutineOutcomeNone && fire.IdleSince.IsZero() {
				return nil
			}
			return s.updateFire(ctx, fire, func(current *domain.RoutineFire, _ time.Time) {
				current.Outcome, current.IdleSince = domain.RoutineOutcomeNone, time.Time{}
			})
		}
		if fire.IdleSince.IsZero() {
			return s.updateFire(ctx, fire, func(current *domain.RoutineFire, now time.Time) {
				current.Outcome, current.IdleSince = domain.RoutineOutcomeNone, now
			})
		}
		if now.Sub(fire.IdleSince) < domain.RoutineIdleSettle {
			return nil
		}
		checking := false
		if err := s.store.View(ctx, func(reader ports.Reader) error {
			_, err := reader.ActiveCheckRun(ctx, capsule.ID)
			if err == nil {
				checking = true
				return nil
			}
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		}); err != nil {
			return err
		}
		if checking {
			return nil
		}
	}
	outcome, reason, err := s.fireOutcome(ctx, capsule)
	if err != nil {
		return err
	}
	return s.updateFire(ctx, fire, func(current *domain.RoutineFire, now time.Time) {
		current.Outcome, current.Reason = outcome, reason
		if outcome == domain.RoutineOutcomeNoChanges {
			current.State = domain.RoutineFireCleaning
			return
		}
		current.State, current.FinishedAt = domain.RoutineFireFinished, now
	})
}

// fireOutcome is proposed, changed, or no_changes. Anything the daemon
// cannot read for sure is changed: doubt keeps the Capsule.
func (s *Service) fireOutcome(ctx context.Context, capsule domain.Capsule) (domain.RoutineOutcome, string, error) {
	proposed := false
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		deliveries, _, err := reader.ListDeliveries(ctx, capsule.ID, ports.Page{Limit: 50})
		if err != nil {
			return err
		}
		for _, delivery := range deliveries {
			if delivery.State != domain.DeliveryRejected && delivery.State != domain.DeliveryFailed {
				proposed = true
			}
		}
		return nil
	}); err != nil {
		return "", "", err
	}
	if proposed {
		return domain.RoutineOutcomeProposed, "", nil
	}
	if s.delivery == nil || capsule.State != domain.CapsuleReady || capsule.ProviderResourceID == "" {
		return domain.RoutineOutcomeChanged, domain.RoutineReasonInspectFailed, nil
	}
	inspection, err := s.delivery.InspectDelivery(ctx, capsule.ProviderResourceID)
	if err != nil {
		return domain.RoutineOutcomeChanged, domain.RoutineReasonInspectFailed, nil
	}
	if inspection.Dirty || inspection.AheadCount > 0 || inspection.HasDeliverableChanges {
		return domain.RoutineOutcomeChanged, "", nil
	}
	return domain.RoutineOutcomeNoChanges, "", nil
}

// cleanFire ends the session of an empty run, archives its Thread, and
// deletes its Capsule. The encrypted transcript stays. Each step that
// changes something returns, and the next tick takes the next step.
func (s *Service) cleanFire(ctx context.Context, fire domain.RoutineFire) error {
	thread, capsule, err := s.fireThread(ctx, fire)
	if err != nil {
		return err
	}
	if thread.State == domain.ThreadActive && thread.CurrentRunID != "" {
		run, err := s.GetRunStored(ctx, thread.CurrentRunID)
		if err != nil {
			return err
		}
		if !run.State.Terminal() {
			if run.State == domain.RunCancelling {
				return nil
			}
			_, err := s.CancelThread(ctx, thread.ID, thread.ResourceVersion, "routine-cancel-"+string(fire.ID))
			return err
		}
	}
	if thread.State != domain.ThreadArchived && thread.State != domain.ThreadDeleted {
		_, err := s.ArchiveThread(ctx, thread.ID, thread.ResourceVersion, "routine-archive-"+string(fire.ID))
		return err
	}
	if capsule.State != domain.CapsuleDeleted && capsule.DesiredState != domain.IntentDeleted {
		if _, err := s.DeleteCapsule(ctx, capsule.ID, capsule.ResourceVersion,
			"routine-delete-"+string(fire.ID)); err != nil {
			return err
		}
	}
	return s.updateFire(ctx, fire, func(current *domain.RoutineFire, now time.Time) {
		current.State, current.CapsuleDeleted, current.FinishedAt = domain.RoutineFireFinished, true, now
	})
}

// stopFire ends a run that hit its time limit: it cancels the session and
// pauses the Capsule. Nothing is deleted.
func (s *Service) stopFire(ctx context.Context, fire domain.RoutineFire, outcome domain.RoutineOutcome) error {
	s.haltRun(ctx, fire)
	return s.updateFire(ctx, fire, func(current *domain.RoutineFire, now time.Time) {
		current.State, current.Outcome, current.FinishedAt = domain.RoutineFireFinished, outcome, now
	})
}

// haltRun cancels the fire's session and pauses its Capsule, best effort:
// idle pause still applies if either step cannot happen now.
func (s *Service) haltRun(ctx context.Context, fire domain.RoutineFire) {
	thread, capsule, err := s.fireThread(ctx, fire)
	if err != nil {
		return
	}
	if thread.State == domain.ThreadActive && thread.CurrentRunID != "" {
		if run, err := s.GetRunStored(ctx, thread.CurrentRunID); err == nil && !run.State.Terminal() {
			_, _ = s.CancelThread(ctx, thread.ID, thread.ResourceVersion, "routine-stop-"+string(fire.ID))
		}
	}
	if capsule.State == domain.CapsuleReady && capsule.DesiredState == domain.IntentReady {
		if current, err := s.GetCapsule(ctx, capsule.ID); err == nil {
			_, _ = s.PauseCapsule(ctx, current.ID, current.ResourceVersion, "routine-pause-"+string(fire.ID))
		}
	}
}

// failStart ends a fire that failed before the agent's first turn. Its
// Capsule, if any, is stopped and kept for the operator to look at. Three in
// a row turn the routine off.
func (s *Service) failStart(ctx context.Context, fire domain.RoutineFire, reason string) error {
	if fire.ThreadID != "" {
		s.haltRun(ctx, fire)
	}
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetRoutineFire(ctx, fire.ID)
		if err != nil {
			return err
		}
		if current.ResourceVersion != fire.ResourceVersion {
			return domain.ErrConflict
		}
		now := s.clock.Now().UTC()
		previous := current.ResourceVersion
		current.State, current.Outcome, current.Reason, current.FinishedAt =
			domain.RoutineFireFinished, domain.RoutineOutcomeFailed, reason, now
		current.ResourceVersion++
		current.UpdatedAt = now
		if err := tx.UpdateRoutineFire(ctx, current, previous); err != nil {
			return err
		}
		routine, err := tx.GetRoutine(ctx, fire.RoutineID)
		if err != nil {
			return err
		}
		before := routine.ResourceVersion
		routine.StartFailures++
		if routine.StartFailures >= domain.RoutineStartFailureLimit && routine.Enabled {
			routine.Enabled = false
			routine.DisabledReason = domain.RoutineDisabledStartFailures
			routine.NextFireAt = time.Time{}
		}
		routine.ResourceVersion++
		routine.UpdatedAt = now
		return tx.UpdateRoutine(ctx, routine, before)
	})
}

func (s *Service) updateFire(
	ctx context.Context, fire domain.RoutineFire, change func(*domain.RoutineFire, time.Time),
) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetRoutineFire(ctx, fire.ID)
		if err != nil {
			return err
		}
		if current.ResourceVersion != fire.ResourceVersion {
			return domain.ErrConflict
		}
		now := s.clock.Now().UTC()
		previous := current.ResourceVersion
		change(&current, now)
		current.ResourceVersion++
		current.UpdatedAt = now
		return tx.UpdateRoutineFire(ctx, current, previous)
	})
}

func (s *Service) fireThread(ctx context.Context, fire domain.RoutineFire) (domain.Thread, domain.Capsule, error) {
	var thread domain.Thread
	var capsule domain.Capsule
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		if thread, err = reader.GetThread(ctx, fire.ThreadID); err != nil {
			return err
		}
		capsule, err = reader.GetCapsule(ctx, fire.CapsuleID)
		return err
	})
	return thread, capsule, err
}

// latestModelSet returns the most recent models report in the Thread's
// trailing transcript window, decrypted in memory, or nil when there is
// none. Option IDs are what set_model and set_effort name.
func (s *Service) latestModelSet(ctx context.Context, thread domain.Thread) (*adapterproto.ModelSet, error) {
	if s.transcriptKey == nil || thread.MessageCount == 0 {
		return nil, nil
	}
	dek, err := s.unwrapThread(thread)
	if err != nil {
		return nil, err
	}
	defer wipe(dek)
	after := max(thread.MessageCount-threadAttentionWindow, 0)
	var messages []domain.ThreadMessage
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		messages, _, err = reader.ListThreadMessages(ctx, thread.ID, after, threadAttentionWindow)
		return err
	}); err != nil {
		return nil, err
	}
	var latest *adapterproto.ModelSet
	for _, message := range messages {
		plaintext, err := transcripts.DecryptMessage(dek, message)
		if err != nil {
			return nil, err
		}
		for _, raw := range plaintext {
			var payload persistedThreadBlock
			if json.Unmarshal(raw, &payload) != nil || payload.Event == nil {
				continue
			}
			if payload.Event.Type == adapterproto.KindModels && payload.Event.Models != nil {
				copied := *payload.Event.Models
				latest = &copied
			}
		}
		transcripts.ZeroPlaintextBlocks(plaintext)
	}
	return latest, nil
}
