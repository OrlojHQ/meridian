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

// Scheduled Project Threads (ADR 0057). A schedule is a clock, a fixed
// prompt, and limits. Each fire starts one fresh Project Thread through the
// same path as POST /projects/{id}/threads, attributed to the Project's
// owner. A schedule never ships: Delivery still needs the operator.

const (
	OriginSchedule     = "schedule"
	OriginFromSchedule = "from_schedule"
	// scheduleOnTime is how late a clock fire may start and still count as
	// on time rather than a catch-up.
	scheduleOnTime = 2 * time.Minute
	// scheduleSpacingSamples is how many upcoming fires a save checks for
	// the minimum spacing.
	scheduleSpacingSamples = 500
)

// ScheduleView is one schedule as its owner sees it, prompt included.
type ScheduleView struct {
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
	Active          *ScheduleFireView      `json:"active,omitempty"`
	LastFire        *ScheduleFireView      `json:"lastFire,omitempty"`
	ResourceVersion domain.ResourceVersion `json:"resourceVersion"`
	CreatedAt       time.Time              `json:"createdAt"`
	UpdatedAt       time.Time              `json:"updatedAt"`
}

// ScheduleFireView is one fire. It carries codes, never prompt text.
type ScheduleFireView struct {
	ID             string     `json:"id"`
	ScheduleID     string     `json:"scheduleId"`
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

// CapsuleScheduleView is the content-free note on a Capsule a fire started.
type CapsuleScheduleView struct {
	ScheduleID   string `json:"scheduleId"`
	ScheduleName string `json:"scheduleName"`
	FireID       string `json:"fireId"`
	State        string `json:"state"`
	Outcome      string `json:"outcome,omitempty"`
}

// ScheduleInput creates a schedule. Zero limits take their defaults; a nil
// Enabled means enabled.
type ScheduleInput struct {
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

// ScheduleUpdate changes the named fields of a schedule.
type ScheduleUpdate struct {
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
// harness in a Project reported, for the schedule form to offer.
type ReportedModelsView struct {
	Models        []adapterproto.ModelOption `json:"models"`
	Current       string                     `json:"current,omitempty"`
	Efforts       []adapterproto.ModelOption `json:"efforts"`
	CurrentEffort string                     `json:"currentEffort,omitempty"`
	ThreadID      string                     `json:"threadId,omitempty"`
}

// scheduleContent is the sealed part of a schedule.
type scheduleContent struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

type scheduleReplay struct {
	ID domain.ScheduleID `json:"id"`
}

type scheduleFireReplay struct {
	ID domain.ScheduleFireID `json:"id"`
}

// ConfigureSchedules sets the installation live-Capsule cap that scheduled
// fires respect. Zero means no cap.
func (s *Service) ConfigureSchedules(maxLive int) {
	s.scheduleMaxLive = maxLive
}

// StartSchedules runs the schedule clock until ctx is cancelled.
func (s *Service) StartSchedules(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.TickSchedules(ctx)
			case <-s.scheduleWake:
				_ = s.TickSchedules(ctx)
			}
		}
	}()
}

func (s *Service) wakeSchedules() {
	if s.scheduleWake == nil {
		return
	}
	select {
	case s.scheduleWake <- struct{}{}:
	default:
	}
}

func (s *Service) schedulesSupported() error {
	if s.structured == nil || s.transcriptKey == nil || s.secretKey == nil {
		return fmt.Errorf("%w: scheduled Threads need structured sessions", domain.ErrUnsupported)
	}
	return nil
}

// ListSchedules lists a Project's schedules.
func (s *Service) ListSchedules(ctx context.Context, id domain.ProjectID) ([]ScheduleView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return nil, err
	}
	views := []ScheduleView{}
	err := s.store.View(ctx, func(reader ports.Reader) error {
		items, err := reader.ListSchedules(ctx, id)
		if err != nil {
			return err
		}
		for _, item := range items {
			view, err := s.scheduleView(ctx, reader, item)
			if err != nil {
				return err
			}
			views = append(views, view)
		}
		return nil
	})
	return views, err
}

// GetSchedule returns one schedule.
func (s *Service) GetSchedule(ctx context.Context, id domain.ScheduleID) (ScheduleView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ScheduleView{}, err
	}
	var view ScheduleView
	err := s.store.View(ctx, func(reader ports.Reader) error {
		item, err := reader.GetSchedule(ctx, id)
		if err != nil {
			return err
		}
		view, err = s.scheduleView(ctx, reader, item)
		return err
	})
	return view, err
}

// CreateSchedule saves a new schedule on a Project.
func (s *Service) CreateSchedule(
	ctx context.Context, projectID domain.ProjectID, input ScheduleInput, key string,
) (ScheduleView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ScheduleView{}, err
	}
	if err := s.schedulesSupported(); err != nil {
		return ScheduleView{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return ScheduleView{}, err
	}
	scope := "project:" + string(projectID) + ":schedule-create"
	var view ScheduleView
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[scheduleReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			item, err := tx.GetSchedule(ctx, replay.ID)
			if err != nil {
				return err
			}
			view, err = s.scheduleView(ctx, tx, item)
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
		schedule := domain.Schedule{
			ID: domain.ScheduleID(s.ids.NewID()), ProjectID: project.ID, OwnerUserID: project.OwnerUserID,
			Name: strings.TrimSpace(input.Name), Harness: strings.TrimSpace(input.Harness),
			Cadence: strings.TrimSpace(input.Cadence), TimeZone: strings.TrimSpace(input.TimeZone),
			MaxRun: domain.ScheduleDefaultRun, CatchUp: domain.ScheduleDefaultCatchUp,
			MaxKept: domain.ScheduleDefaultKept, Enabled: input.Enabled == nil || *input.Enabled,
			ResourceVersion: 1, CreatedAt: now, UpdatedAt: now,
		}
		if input.MaxRunSeconds != 0 {
			schedule.MaxRun = time.Duration(input.MaxRunSeconds) * time.Second
		}
		if input.CatchUpSeconds != nil {
			schedule.CatchUp = time.Duration(*input.CatchUpSeconds) * time.Second
		}
		if input.MaxKept != 0 {
			schedule.MaxKept = input.MaxKept
		}
		content := scheduleContent{
			Prompt: input.Prompt, Model: strings.TrimSpace(input.Model), Effort: strings.TrimSpace(input.Effort),
		}
		if err := s.prepareSchedule(ctx, tx, project, &schedule, content, now); err != nil {
			return err
		}
		if err := tx.InsertSchedule(ctx, schedule); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditScheduleCreate, "schedule", string(schedule.ID)); err != nil {
			return err
		}
		if err := putReplay(ctx, tx, scope, key, scheduleReplay{ID: schedule.ID}, now); err != nil {
			return err
		}
		view, err = s.scheduleView(ctx, tx, schedule)
		return err
	})
	if err == nil {
		s.wakeSchedules()
	}
	return view, err
}

// UpdateSchedule changes a schedule. Turning a schedule on clears the reason
// Meridian turned it off and the start failure count.
func (s *Service) UpdateSchedule(
	ctx context.Context, id domain.ScheduleID, input ScheduleUpdate, key string,
) (ScheduleView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ScheduleView{}, err
	}
	if err := s.schedulesSupported(); err != nil {
		return ScheduleView{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return ScheduleView{}, err
	}
	if input.ExpectedResourceVersion <= 0 {
		return ScheduleView{}, fmt.Errorf("%w: expected resource version is required", domain.ErrInvalid)
	}
	scope := "schedule:" + string(id) + ":update"
	var view ScheduleView
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, ok, err := getReplay[scheduleReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			item, err := tx.GetSchedule(ctx, id)
			if err != nil {
				return err
			}
			view, err = s.scheduleView(ctx, tx, item)
			return err
		}
		schedule, err := tx.GetSchedule(ctx, id)
		if err != nil {
			return err
		}
		if schedule.ResourceVersion != input.ExpectedResourceVersion {
			return domain.ErrConflict
		}
		project, err := tx.GetProject(ctx, schedule.ProjectID)
		if err != nil {
			return err
		}
		if err := projectAcceptsWork(project); err != nil {
			return err
		}
		content, err := s.openScheduleContent(schedule)
		if err != nil {
			return err
		}
		setString := func(dst *string, value *string) {
			if value != nil {
				*dst = strings.TrimSpace(*value)
			}
		}
		setString(&schedule.Name, input.Name)
		setString(&schedule.Harness, input.Harness)
		setString(&schedule.Cadence, input.Cadence)
		setString(&schedule.TimeZone, input.TimeZone)
		setString(&content.Model, input.Model)
		setString(&content.Effort, input.Effort)
		if input.Prompt != nil {
			content.Prompt = *input.Prompt
		}
		if input.MaxRunSeconds != nil {
			schedule.MaxRun = time.Duration(*input.MaxRunSeconds) * time.Second
		}
		if input.CatchUpSeconds != nil {
			schedule.CatchUp = time.Duration(*input.CatchUpSeconds) * time.Second
		}
		if input.MaxKept != nil {
			schedule.MaxKept = *input.MaxKept
		}
		if input.Enabled != nil {
			if *input.Enabled && !schedule.Enabled {
				schedule.DisabledReason = ""
				schedule.StartFailures = 0
			}
			schedule.Enabled = *input.Enabled
		}
		now := s.clock.Now().UTC()
		previous := schedule.ResourceVersion
		schedule.ResourceVersion++
		schedule.UpdatedAt = now
		if err := s.prepareSchedule(ctx, tx, project, &schedule, content, now); err != nil {
			return err
		}
		if err := tx.UpdateSchedule(ctx, schedule, previous); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditScheduleUpdate, "schedule", string(schedule.ID)); err != nil {
			return err
		}
		if err := putReplay(ctx, tx, scope, key, scheduleReplay{ID: schedule.ID}, now); err != nil {
			return err
		}
		view, err = s.scheduleView(ctx, tx, schedule)
		return err
	})
	if err == nil {
		s.wakeSchedules()
	}
	return view, err
}

// DeleteSchedule removes a schedule and its fire history. A run in progress
// keeps going as an ordinary Thread; Capsules it kept stay until the
// operator deletes them.
func (s *Service) DeleteSchedule(
	ctx context.Context, id domain.ScheduleID, expected domain.ResourceVersion, key string,
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
	scope := "schedule:" + string(id) + ":delete"
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, ok, err := getReplay[scheduleReplay](ctx, tx, scope, key); err != nil || ok {
			return err
		}
		schedule, err := tx.GetSchedule(ctx, id)
		if err != nil {
			return err
		}
		if schedule.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if err := tx.DeleteSchedule(ctx, id); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditScheduleDelete, "schedule", string(id)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, scheduleReplay{ID: id}, s.clock.Now().UTC())
	})
}

// RunSchedule fires a schedule now. The same rules apply as for a clock
// fire: an overlapping run, a full backlog, or the live-Capsule cap records
// a skip instead. It works on a turned-off schedule too.
func (s *Service) RunSchedule(ctx context.Context, id domain.ScheduleID, key string) (ScheduleFireView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ScheduleFireView{}, err
	}
	if err := s.schedulesSupported(); err != nil {
		return ScheduleFireView{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return ScheduleFireView{}, err
	}
	scope := "schedule:" + string(id) + ":run"
	var fire domain.ScheduleFire
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[scheduleFireReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			fire, err = tx.GetScheduleFire(ctx, replay.ID)
			return err
		}
		schedule, err := tx.GetSchedule(ctx, id)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		fire, err = s.gateFire(ctx, tx, &schedule, domain.ScheduleTriggerManual, now, now)
		if err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditScheduleRun, "schedule", string(id)); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, scheduleFireReplay{ID: fire.ID}, now)
	})
	if err != nil {
		return ScheduleFireView{}, err
	}
	s.wakeSchedules()
	return scheduleFireView(fire), nil
}

// ListScheduleFires lists a schedule's fires, newest first.
func (s *Service) ListScheduleFires(
	ctx context.Context, id domain.ScheduleID, page ports.Page,
) ([]ScheduleFireView, bool, error) {
	if err := requirePrincipal(ctx); err != nil {
		return nil, false, err
	}
	views := []ScheduleFireView{}
	var more bool
	err := s.store.View(ctx, func(reader ports.Reader) error {
		items, hasMore, err := reader.ListScheduleFires(ctx, id, page)
		if err != nil {
			return err
		}
		more = hasMore
		for _, item := range items {
			views = append(views, scheduleFireView(item))
		}
		return nil
	})
	return views, more, err
}

// CapsuleSchedules returns the schedule note for each Capsule a fire started.
func (s *Service) CapsuleSchedules(
	ctx context.Context, ids []domain.CapsuleID,
) (map[domain.CapsuleID]CapsuleScheduleView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return nil, err
	}
	result := map[domain.CapsuleID]CapsuleScheduleView{}
	err := s.store.View(ctx, func(reader ports.Reader) error {
		fires, err := reader.ScheduleFiresByCapsule(ctx, ids)
		if err != nil {
			return err
		}
		names := map[domain.ScheduleID]string{}
		for id, fire := range fires {
			name, ok := names[fire.ScheduleID]
			if !ok {
				schedule, err := reader.GetSchedule(ctx, fire.ScheduleID)
				if errors.Is(err, domain.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				name = schedule.Name
				names[fire.ScheduleID] = name
			}
			result[id] = CapsuleScheduleView{
				ScheduleID: string(fire.ScheduleID), ScheduleName: name, FireID: string(fire.ID),
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

// prepareSchedule validates schedule and content together, seals the
// content, and sets the next fire.
func (s *Service) prepareSchedule(
	ctx context.Context, tx ports.Transaction, project domain.Project,
	schedule *domain.Schedule, content scheduleContent, now time.Time,
) error {
	if !domain.ValidScheduleName(schedule.Name) {
		return fmt.Errorf("%w: name must be 1 to 48 lowercase letters, digits, or dashes", domain.ErrInvalid)
	}
	if _, err := s.structuredHarnessImage(project, schedule.Harness); err != nil {
		return err
	}
	if strings.TrimSpace(content.Prompt) == "" {
		return fmt.Errorf("%w: a schedule needs a prompt", domain.ErrInvalid)
	}
	if len(content.Prompt) > domain.ScheduleMaxPromptBytes {
		return fmt.Errorf("%w: prompt is longer than 32 KiB", domain.ErrResourceExhausted)
	}
	if content.Model != "" && !adapterproto.ValidModelID(content.Model) {
		return fmt.Errorf("%w: model must be a bounded identifier", domain.ErrInvalid)
	}
	if content.Effort != "" && !adapterproto.ValidModelID(content.Effort) {
		return fmt.Errorf("%w: effort must be a bounded identifier", domain.ErrInvalid)
	}
	if schedule.MaxRun < domain.ScheduleMinRun || schedule.MaxRun > domain.ScheduleMaxRun {
		return fmt.Errorf("%w: maximum run time must be from 5 minutes to 24 hours", domain.ErrInvalid)
	}
	if schedule.CatchUp < 0 || schedule.CatchUp > domain.ScheduleMaxCatchUp {
		return fmt.Errorf("%w: catch-up window must be from 0 to 24 hours", domain.ErrInvalid)
	}
	if schedule.MaxKept < 1 || schedule.MaxKept > domain.ScheduleMaxKept {
		return fmt.Errorf("%w: kept Capsules must be from 1 to 20", domain.ErrInvalid)
	}
	cadence, err := domain.ParseCadence(schedule.Cadence, schedule.TimeZone)
	if err != nil {
		return err
	}
	if err := cadence.CheckSpacing(now, scheduleSpacingSamples); err != nil {
		return err
	}
	others, err := tx.ListSchedules(ctx, project.ID)
	if err != nil {
		return err
	}
	for _, other := range others {
		if other.ID != schedule.ID && other.Name == schedule.Name {
			return fmt.Errorf("%w: this Project already has a schedule named %s", domain.ErrConflict, schedule.Name)
		}
	}
	plain, err := json.Marshal(content)
	if err != nil {
		return err
	}
	defer wipe(plain)
	schedule.ContentNonce, schedule.ContentCiphertext, err = s.secretKey.SealBlob(scheduleContentLabel(*schedule), plain)
	if err != nil {
		return err
	}
	if schedule.Enabled {
		schedule.NextFireAt = cadence.Next(now)
	} else {
		schedule.NextFireAt = time.Time{}
	}
	return nil
}

// scheduleContentLabel binds the sealed content to its schedule and Project,
// so a ciphertext cannot be moved to another row. It leaves the owner out so
// a departed-member transfer can hand the prompt to the new owner.
func scheduleContentLabel(schedule domain.Schedule) string {
	return "schedule\x00" + string(schedule.ID) + "\x00" + string(schedule.ProjectID)
}

func (s *Service) openScheduleContent(schedule domain.Schedule) (scheduleContent, error) {
	if s.secretKey == nil {
		return scheduleContent{}, domain.ErrUnsupported
	}
	plain, err := s.secretKey.OpenBlob(scheduleContentLabel(schedule), schedule.ContentNonce, schedule.ContentCiphertext)
	if err != nil {
		return scheduleContent{}, err
	}
	defer wipe(plain)
	var content scheduleContent
	if err := json.Unmarshal(plain, &content); err != nil {
		return scheduleContent{}, domain.ErrCorrupt
	}
	return content, nil
}

func (s *Service) scheduleView(ctx context.Context, reader ports.Reader, schedule domain.Schedule) (ScheduleView, error) {
	content, err := s.openScheduleContent(schedule)
	if err != nil {
		return ScheduleView{}, err
	}
	view := ScheduleView{
		ID: string(schedule.ID), ProjectID: string(schedule.ProjectID), Name: schedule.Name,
		Harness: schedule.Harness, Model: content.Model, Effort: content.Effort, Prompt: content.Prompt,
		Cadence: schedule.Cadence, TimeZone: schedule.TimeZone,
		MaxRunSeconds: int(schedule.MaxRun / time.Second), CatchUpSeconds: int(schedule.CatchUp / time.Second),
		MaxKept: schedule.MaxKept, Enabled: schedule.Enabled, DisabledReason: schedule.DisabledReason,
		NextFireAt: optionalTimeView(schedule.NextFireAt), LastFireAt: optionalTimeView(schedule.LastFireAt),
		ResourceVersion: schedule.ResourceVersion, CreatedAt: schedule.CreatedAt, UpdatedAt: schedule.UpdatedAt,
	}
	view.KeptCapsules, err = reader.CountKeptScheduleCapsules(ctx, schedule.ID)
	if err != nil {
		return ScheduleView{}, err
	}
	if active, err := reader.ActiveScheduleFire(ctx, schedule.ID); err == nil {
		item := scheduleFireView(active)
		view.Active = &item
	} else if !errors.Is(err, domain.ErrNotFound) {
		return ScheduleView{}, err
	}
	fires, _, err := reader.ListScheduleFires(ctx, schedule.ID, ports.Page{Limit: 1})
	if err != nil {
		return ScheduleView{}, err
	}
	if len(fires) > 0 {
		item := scheduleFireView(fires[0])
		view.LastFire = &item
	}
	return view, nil
}

func scheduleFireView(fire domain.ScheduleFire) ScheduleFireView {
	return ScheduleFireView{
		ID: string(fire.ID), ScheduleID: string(fire.ScheduleID), Trigger: string(fire.Trigger),
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

// TickSchedules fires due schedules and advances fires in progress. It is
// safe to call from tests.
func (s *Service) TickSchedules(ctx context.Context) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	if s.schedulesSupported() != nil {
		return nil
	}
	now := s.clock.Now().UTC()
	var due []domain.Schedule
	var active []domain.ScheduleFire
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		if due, err = reader.ListDueSchedules(ctx, now, 20); err != nil {
			return err
		}
		active, err = reader.ListActiveScheduleFires(ctx, 50)
		return err
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, schedule := range due {
		if err := s.fireDue(ctx, schedule.ID); err != nil && !errors.Is(err, domain.ErrConflict) {
			errs = append(errs, err)
		}
	}
	// Re-read: a fire inserted above starts on this tick, not the next.
	if len(due) > 0 {
		if err := s.store.View(ctx, func(reader ports.Reader) error {
			var err error
			active, err = reader.ListActiveScheduleFires(ctx, 50)
			return err
		}); err != nil {
			return err
		}
	}
	for _, fire := range active {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.advanceFire(ctx, fire.ID); err != nil && !scheduleRetryable(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// scheduleRetryable reports errors that a later tick resolves on its own.
func scheduleRetryable(err error) bool {
	return errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrIllegalTransition)
}

// fireDue handles one schedule whose next fire time has passed: it records
// missed times, fires at most once, and moves the clock on.
func (s *Service) fireDue(ctx context.Context, id domain.ScheduleID) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		schedule, err := tx.GetSchedule(ctx, id)
		if err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		if !schedule.Enabled || schedule.NextFireAt.IsZero() || schedule.NextFireAt.After(now) {
			return nil
		}
		cadence, err := domain.ParseCadence(schedule.Cadence, schedule.TimeZone)
		if err != nil {
			return err
		}
		times, older := dueTimes(cadence, schedule.NextFireAt, now)
		var fire *time.Time
		var trigger domain.ScheduleFireTrigger
		if len(times) > 0 {
			latest := times[len(times)-1]
			switch late := now.Sub(latest); {
			case late <= scheduleOnTime:
				fire, trigger = &latest, domain.ScheduleTriggerClock
			case late <= schedule.CatchUp:
				fire, trigger = &latest, domain.ScheduleTriggerCatchUp
			}
			if fire != nil {
				times = times[:len(times)-1]
			}
		}
		if older {
			times = append([]time.Time{schedule.NextFireAt}, times...)
		}
		if len(times) > domain.ScheduleMissedRecords {
			times = times[len(times)-domain.ScheduleMissedRecords:]
		}
		for _, at := range times {
			if err := s.recordMissed(ctx, tx, schedule, at, now); err != nil {
				return err
			}
		}
		if fire != nil {
			if _, err := tx.GetScheduleFireAt(ctx, schedule.ID, *fire); errors.Is(err, domain.ErrNotFound) {
				if _, err := s.gateFire(ctx, tx, &schedule, trigger, *fire, now); err != nil {
					return err
				}
				if err := s.audit(ctx, tx, domain.AuditScheduleFire, "schedule", string(schedule.ID)); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
		// gateFire may have turned the schedule off; read what it wrote.
		current, err := tx.GetSchedule(ctx, id)
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
		return tx.UpdateSchedule(ctx, current, before)
	})
}

// dueTimes lists fire times from next up to now. Times more than a day old
// can never be caught up, so they are not listed one by one: older reports
// that some exist.
func dueTimes(cadence domain.Cadence, next, now time.Time) ([]time.Time, bool) {
	floor := now.Add(-domain.ScheduleMaxCatchUp - time.Hour)
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
	ctx context.Context, tx ports.Transaction, schedule domain.Schedule, at, now time.Time,
) error {
	if _, err := tx.GetScheduleFireAt(ctx, schedule.ID, at); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return tx.InsertScheduleFire(ctx, domain.ScheduleFire{
		ID: domain.ScheduleFireID(s.ids.NewID()), ScheduleID: schedule.ID, ProjectID: schedule.ProjectID,
		OwnerUserID: schedule.OwnerUserID, Trigger: domain.ScheduleTriggerClock, ScheduledFor: at,
		State: domain.ScheduleFireMissed, Reason: domain.ScheduleReasonMissed, FinishedAt: now,
		ResourceVersion: 1, CreatedAt: now, UpdatedAt: now,
	})
}

// gateFire records one fire: a skip with its reason, or a starting fire the
// next tick turns into a Thread. It may turn the schedule off, which the
// caller persists.
func (s *Service) gateFire(
	ctx context.Context, tx ports.Transaction, schedule *domain.Schedule,
	trigger domain.ScheduleFireTrigger, at, now time.Time,
) (domain.ScheduleFire, error) {
	fire := domain.ScheduleFire{
		ID: domain.ScheduleFireID(s.ids.NewID()), ScheduleID: schedule.ID, ProjectID: schedule.ProjectID,
		OwnerUserID: schedule.OwnerUserID, Trigger: trigger, ScheduledFor: at,
		State: domain.ScheduleFireStarting, ResourceVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	reason, err := s.fireBlocked(ctx, tx, schedule)
	if err != nil {
		return domain.ScheduleFire{}, err
	}
	if reason != "" {
		fire.State, fire.Reason, fire.FinishedAt = domain.ScheduleFireSkipped, reason, now
	}
	if reason == domain.ScheduleDisabledHarness {
		previous := schedule.ResourceVersion
		schedule.Enabled, schedule.DisabledReason, schedule.NextFireAt = false, reason, time.Time{}
		schedule.ResourceVersion++
		schedule.UpdatedAt = now
		if err := tx.UpdateSchedule(ctx, *schedule, previous); err != nil {
			return domain.ScheduleFire{}, err
		}
	}
	return fire, tx.InsertScheduleFire(ctx, fire)
}

func (s *Service) fireBlocked(ctx context.Context, tx ports.Transaction, schedule *domain.Schedule) (string, error) {
	project, err := tx.GetProject(ctx, schedule.ProjectID)
	if err != nil {
		return "", err
	}
	if projectAcceptsWork(project) != nil {
		return domain.ScheduleReasonProject, nil
	}
	if _, err := s.structuredHarnessImage(project, schedule.Harness); err != nil {
		return domain.ScheduleDisabledHarness, nil
	}
	if schedule.OwnerUserID != "" {
		user, err := tx.GetUser(ctx, schedule.OwnerUserID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && !user.DisabledAt.IsZero()) {
			return domain.ScheduleReasonOwner, nil
		}
		if err != nil {
			return "", err
		}
	}
	if _, err := tx.ActiveScheduleFire(ctx, schedule.ID); err == nil {
		return domain.ScheduleReasonOverlap, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return "", err
	}
	kept, err := tx.CountKeptScheduleCapsules(ctx, schedule.ID)
	if err != nil {
		return "", err
	}
	if kept >= schedule.MaxKept {
		return domain.ScheduleReasonBacklog, nil
	}
	if s.scheduleMaxLive > 0 {
		live, err := tx.CountLiveCapsules(ctx)
		if err != nil {
			return "", err
		}
		if live >= s.scheduleMaxLive {
			return domain.ScheduleReasonCapacity, nil
		}
	}
	return "", nil
}

// advanceFire moves one fire in progress forward by at most one step.
func (s *Service) advanceFire(ctx context.Context, id domain.ScheduleFireID) error {
	var fire domain.ScheduleFire
	var schedule domain.Schedule
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		if fire, err = reader.GetScheduleFire(ctx, id); err != nil {
			return err
		}
		schedule, err = reader.GetSchedule(ctx, fire.ScheduleID)
		return err
	})
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	if fire.ThreadID == "" {
		return s.startFire(ctx, fire, schedule)
	}
	if fire.State != domain.ScheduleFireCleaning && !fire.DeadlineAt.IsZero() && now.After(fire.DeadlineAt) {
		return s.stopFire(ctx, fire, domain.ScheduleOutcomeTimedOut)
	}
	switch fire.State {
	case domain.ScheduleFireStarting:
		return s.awaitSession(ctx, fire, schedule)
	case domain.ScheduleFireSelecting:
		return s.selectModel(ctx, fire, schedule)
	case domain.ScheduleFireRunning:
		return s.watchRun(ctx, fire)
	case domain.ScheduleFireCleaning:
		return s.cleanFire(ctx, fire)
	}
	return nil
}

// startFire creates the fire's Thread. The idempotency key is the fire, so
// a daemon that stopped after creating the Thread and before recording it
// gets the same Thread back.
func (s *Service) startFire(ctx context.Context, fire domain.ScheduleFire, schedule domain.Schedule) error {
	location, err := time.LoadLocation(schedule.TimeZone)
	if err != nil {
		location = time.UTC
	}
	name := schedule.Name + " " + fire.ScheduledFor.In(location).Format("2006-01-02 15:04")
	result, err := s.CreateProjectThread(ctx, CreateProjectThreadInput{
		ProjectID: schedule.ProjectID, Name: name, Harness: schedule.Harness,
		IdempotencyKey: "schedule:" + string(fire.ID),
	})
	if err != nil {
		if scheduleRetryable(err) {
			return err
		}
		return s.failStart(ctx, fire, domain.ScheduleReasonStartFailed)
	}
	return s.updateFire(ctx, fire, func(current *domain.ScheduleFire, now time.Time) {
		current.CapsuleID, current.ThreadID = result.Intent.CapsuleID, result.Intent.ThreadID
		current.StartedAt = now
		current.DeadlineAt = now.Add(schedule.MaxRun)
	})
}

// awaitSession waits for the Thread's structured session to start.
func (s *Service) awaitSession(ctx context.Context, fire domain.ScheduleFire, schedule domain.Schedule) error {
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
		return s.failStart(ctx, fire, domain.ScheduleReasonStartFailed)
	}
	if thread.ID == "" || thread.State != domain.ThreadActive || run.ID == "" || run.State != domain.RunRunning {
		return nil
	}
	return s.updateFire(ctx, fire, func(current *domain.ScheduleFire, now time.Time) {
		current.State = domain.ScheduleFireSelecting
		current.SelectingSince = now
	})
}

// selectModel applies the schedule's model and effort before the prompt is
// sent. Nothing falls back to the harness default (ADR 0057): a run on a
// model the operator did not pick fails instead.
func (s *Service) selectModel(ctx context.Context, fire domain.ScheduleFire, schedule domain.Schedule) error {
	content, err := s.openScheduleContent(schedule)
	if err != nil {
		return err
	}
	defer func() { content.Prompt = "" }()
	if content.Model == "" && content.Effort == "" {
		return s.sendSchedulePrompt(ctx, fire, content.Prompt)
	}
	thread, capsule, err := s.fireThread(ctx, fire)
	if err != nil {
		return err
	}
	if thread.State != domain.ThreadActive || thread.CurrentRunID == "" {
		return s.failStart(ctx, fire, domain.ScheduleReasonSessionEnded)
	}
	runtimeRun, err := s.structured.GetStructured(ctx, capsule.ProviderResourceID, thread.CurrentRunID)
	if err != nil {
		return err
	}
	if content.Model != "" && !runtimeRun.Models {
		return s.failStart(ctx, fire, domain.ScheduleReasonModelNone)
	}
	if content.Effort != "" && !runtimeRun.Effort {
		return s.failStart(ctx, fire, domain.ScheduleReasonEffortNone)
	}
	now := s.clock.Now().UTC()
	waited := now.Sub(fire.SelectingSince) > domain.ScheduleModelWait
	set, err := s.latestModelSet(ctx, thread)
	if err != nil {
		return err
	}
	if set == nil {
		if waited {
			return s.failStart(ctx, fire, domain.ScheduleReasonModelMissing)
		}
		return nil
	}
	if content.Model != "" && set.Current != content.Model {
		if fire.ModelRequested {
			if waited {
				return s.failStart(ctx, fire, domain.ScheduleReasonModelRefused)
			}
			return nil
		}
		if !hasModelOption(set.Options, content.Model) {
			return s.failStart(ctx, fire, domain.ScheduleReasonModelAbsent)
		}
		if _, err := s.SetThreadModel(ctx, thread.ID, thread.ResourceVersion, content.Model,
			"schedule-model-"+string(fire.ID)); err != nil {
			return err
		}
		return s.updateFire(ctx, fire, func(current *domain.ScheduleFire, now time.Time) {
			current.ModelRequested, current.SelectingSince = true, now
		})
	}
	if content.Effort != "" && (set.Effort == nil || set.Effort.Current != content.Effort) {
		if fire.EffortRequested {
			if waited {
				return s.failStart(ctx, fire, domain.ScheduleReasonEffortRefused)
			}
			return nil
		}
		if set.Effort == nil || !hasModelOption(set.Effort.Options, content.Effort) {
			return s.failStart(ctx, fire, domain.ScheduleReasonEffortAbsent)
		}
		if _, err := s.SetThreadEffort(ctx, thread.ID, thread.ResourceVersion, content.Effort,
			"schedule-effort-"+string(fire.ID)); err != nil {
			return err
		}
		return s.updateFire(ctx, fire, func(current *domain.ScheduleFire, now time.Time) {
			current.EffortRequested, current.SelectingSince = true, now
		})
	}
	return s.sendSchedulePrompt(ctx, fire, content.Prompt)
}

func hasModelOption(options []adapterproto.ModelOption, id string) bool {
	for _, option := range options {
		if option.ID == id {
			return true
		}
	}
	return false
}

// sendSchedulePrompt delivers the schedule's prompt as the Thread's first
// user turn, labeled as coming from the schedule.
func (s *Service) sendSchedulePrompt(ctx context.Context, fire domain.ScheduleFire, prompt string) error {
	frame := func(messageID domain.ThreadMessageID, thread domain.Thread, saved persistedThreadBlock) adapterproto.Frame {
		return userMessageFrame(messageID, thread.ID, saved)
	}
	payload := persistedThreadBlock{
		Content: prompt, Origin: &ThreadOrigin{Source: OriginSchedule, Kind: OriginFromSchedule},
	}
	if _, err := s.appendAndSendHook(ctx, fire.ThreadID, 0, "schedule-prompt-"+string(fire.ID), "schedule",
		domain.ThreadRoleUser, domain.ThreadMessagePrompt, domain.ThreadBlockText, payload, frame, nil); err != nil {
		return err
	}
	err := s.updateFire(ctx, fire, func(current *domain.ScheduleFire, _ time.Time) {
		current.State = domain.ScheduleFireRunning
	})
	if err != nil {
		return err
	}
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		schedule, err := tx.GetSchedule(ctx, fire.ScheduleID)
		if err != nil || schedule.StartFailures == 0 {
			return err
		}
		previous := schedule.ResourceVersion
		schedule.StartFailures = 0
		schedule.ResourceVersion++
		schedule.UpdatedAt = s.clock.Now().UTC()
		return tx.UpdateSchedule(ctx, schedule, previous)
	})
}

// watchRun decides the outcome once the Thread has stayed idle, with
// nothing waiting on the operator and no checks running, for
// ScheduleIdleSettle.
func (s *Service) watchRun(ctx context.Context, fire domain.ScheduleFire) error {
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
			if fire.Outcome == domain.ScheduleOutcomeWaiting && fire.IdleSince.IsZero() {
				return nil
			}
			return s.updateFire(ctx, fire, func(current *domain.ScheduleFire, _ time.Time) {
				current.Outcome, current.IdleSince = domain.ScheduleOutcomeWaiting, time.Time{}
			})
		}
		if attention.Turn != ThreadTurnIdle {
			if fire.Outcome == domain.ScheduleOutcomeNone && fire.IdleSince.IsZero() {
				return nil
			}
			return s.updateFire(ctx, fire, func(current *domain.ScheduleFire, _ time.Time) {
				current.Outcome, current.IdleSince = domain.ScheduleOutcomeNone, time.Time{}
			})
		}
		if fire.IdleSince.IsZero() {
			return s.updateFire(ctx, fire, func(current *domain.ScheduleFire, now time.Time) {
				current.Outcome, current.IdleSince = domain.ScheduleOutcomeNone, now
			})
		}
		if now.Sub(fire.IdleSince) < domain.ScheduleIdleSettle {
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
	return s.updateFire(ctx, fire, func(current *domain.ScheduleFire, now time.Time) {
		current.Outcome, current.Reason = outcome, reason
		if outcome == domain.ScheduleOutcomeNoChanges {
			current.State = domain.ScheduleFireCleaning
			return
		}
		current.State, current.FinishedAt = domain.ScheduleFireFinished, now
	})
}

// fireOutcome is proposed, changed, or no_changes. Anything the daemon
// cannot read for sure is changed: doubt keeps the Capsule.
func (s *Service) fireOutcome(ctx context.Context, capsule domain.Capsule) (domain.ScheduleOutcome, string, error) {
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
		return domain.ScheduleOutcomeProposed, "", nil
	}
	if s.delivery == nil || capsule.State != domain.CapsuleReady || capsule.ProviderResourceID == "" {
		return domain.ScheduleOutcomeChanged, domain.ScheduleReasonInspectFailed, nil
	}
	inspection, err := s.delivery.InspectDelivery(ctx, capsule.ProviderResourceID)
	if err != nil {
		return domain.ScheduleOutcomeChanged, domain.ScheduleReasonInspectFailed, nil
	}
	if inspection.Dirty || inspection.AheadCount > 0 || inspection.HasDeliverableChanges {
		return domain.ScheduleOutcomeChanged, "", nil
	}
	return domain.ScheduleOutcomeNoChanges, "", nil
}

// cleanFire ends the session of an empty run, archives its Thread, and
// deletes its Capsule. The encrypted transcript stays. Each step that
// changes something returns, and the next tick takes the next step.
func (s *Service) cleanFire(ctx context.Context, fire domain.ScheduleFire) error {
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
			_, err := s.CancelThread(ctx, thread.ID, thread.ResourceVersion, "schedule-cancel-"+string(fire.ID))
			return err
		}
	}
	if thread.State != domain.ThreadArchived && thread.State != domain.ThreadDeleted {
		_, err := s.ArchiveThread(ctx, thread.ID, thread.ResourceVersion, "schedule-archive-"+string(fire.ID))
		return err
	}
	if capsule.State != domain.CapsuleDeleted && capsule.DesiredState != domain.IntentDeleted {
		if _, err := s.DeleteCapsule(ctx, capsule.ID, capsule.ResourceVersion,
			"schedule-delete-"+string(fire.ID)); err != nil {
			return err
		}
	}
	return s.updateFire(ctx, fire, func(current *domain.ScheduleFire, now time.Time) {
		current.State, current.CapsuleDeleted, current.FinishedAt = domain.ScheduleFireFinished, true, now
	})
}

// stopFire ends a run that hit its time limit: it cancels the session and
// pauses the Capsule. Nothing is deleted.
func (s *Service) stopFire(ctx context.Context, fire domain.ScheduleFire, outcome domain.ScheduleOutcome) error {
	s.haltRun(ctx, fire)
	return s.updateFire(ctx, fire, func(current *domain.ScheduleFire, now time.Time) {
		current.State, current.Outcome, current.FinishedAt = domain.ScheduleFireFinished, outcome, now
	})
}

// haltRun cancels the fire's session and pauses its Capsule, best effort:
// idle pause still applies if either step cannot happen now.
func (s *Service) haltRun(ctx context.Context, fire domain.ScheduleFire) {
	thread, capsule, err := s.fireThread(ctx, fire)
	if err != nil {
		return
	}
	if thread.State == domain.ThreadActive && thread.CurrentRunID != "" {
		if run, err := s.GetRunStored(ctx, thread.CurrentRunID); err == nil && !run.State.Terminal() {
			_, _ = s.CancelThread(ctx, thread.ID, thread.ResourceVersion, "schedule-stop-"+string(fire.ID))
		}
	}
	if capsule.State == domain.CapsuleReady && capsule.DesiredState == domain.IntentReady {
		if current, err := s.GetCapsule(ctx, capsule.ID); err == nil {
			_, _ = s.PauseCapsule(ctx, current.ID, current.ResourceVersion, "schedule-pause-"+string(fire.ID))
		}
	}
}

// failStart ends a fire that failed before the agent's first turn. Its
// Capsule, if any, is stopped and kept for the operator to look at. Three in
// a row turn the schedule off.
func (s *Service) failStart(ctx context.Context, fire domain.ScheduleFire, reason string) error {
	if fire.ThreadID != "" {
		s.haltRun(ctx, fire)
	}
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetScheduleFire(ctx, fire.ID)
		if err != nil {
			return err
		}
		if current.ResourceVersion != fire.ResourceVersion {
			return domain.ErrConflict
		}
		now := s.clock.Now().UTC()
		previous := current.ResourceVersion
		current.State, current.Outcome, current.Reason, current.FinishedAt =
			domain.ScheduleFireFinished, domain.ScheduleOutcomeFailed, reason, now
		current.ResourceVersion++
		current.UpdatedAt = now
		if err := tx.UpdateScheduleFire(ctx, current, previous); err != nil {
			return err
		}
		schedule, err := tx.GetSchedule(ctx, fire.ScheduleID)
		if err != nil {
			return err
		}
		before := schedule.ResourceVersion
		schedule.StartFailures++
		if schedule.StartFailures >= domain.ScheduleStartFailureLimit && schedule.Enabled {
			schedule.Enabled = false
			schedule.DisabledReason = domain.ScheduleDisabledStartFailures
			schedule.NextFireAt = time.Time{}
		}
		schedule.ResourceVersion++
		schedule.UpdatedAt = now
		return tx.UpdateSchedule(ctx, schedule, before)
	})
}

func (s *Service) updateFire(
	ctx context.Context, fire domain.ScheduleFire, change func(*domain.ScheduleFire, time.Time),
) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetScheduleFire(ctx, fire.ID)
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
		return tx.UpdateScheduleFire(ctx, current, previous)
	})
}

func (s *Service) fireThread(ctx context.Context, fire domain.ScheduleFire) (domain.Thread, domain.Capsule, error) {
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
