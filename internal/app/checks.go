package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

const (
	checksMarkerStart = "<!-- meridian-checks -->"
	checksMarkerEnd   = "<!-- /meridian-checks -->"
)

// ProjectChecksView is the owner's check list. An empty Checks slice means
// the Project behaves as it did before checks existed.
type ProjectChecksView struct {
	Checks            []CheckCommandView     `json:"checks"`
	AutoRework        bool                   `json:"autoRework"`
	MaxReworkAttempts int                    `json:"maxReworkAttempts"`
	ResourceVersion   domain.ResourceVersion `json:"resourceVersion"`
	UpdatedAt         *time.Time             `json:"updatedAt,omitempty"`
}

// CheckCommandView is one argv. TimeoutSeconds is the per-command limit.
type CheckCommandView struct {
	Name           string   `json:"name"`
	Argv           []string `json:"argv"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

// ProjectChecksUpdate replaces the list. expectedResourceVersion is the
// Project's current version.
type ProjectChecksUpdate struct {
	Checks                  []CheckCommandView     `json:"checks"`
	AutoRework              bool                   `json:"autoRework"`
	MaxReworkAttempts       int                    `json:"maxReworkAttempts"`
	ExpectedResourceVersion domain.ResourceVersion `json:"expectedResourceVersion"`
}

// CheckSuggestionView is a repository file the owner may copy into the form.
// Reading it never changes what runs.
type CheckSuggestionView struct {
	Checks  []CheckCommandView `json:"checks"`
	Ignored bool               `json:"ignored,omitempty"`
}

// CheckRunView is one suite execution. Output tails are redacted. The pull
// request section is the text the server appends, with no log output.
type CheckRunView struct {
	ID                 string                 `json:"id"`
	CapsuleID          string                 `json:"capsuleId"`
	ProjectID          string                 `json:"projectId"`
	ThreadID           string                 `json:"threadId,omitempty"`
	Attempt            int                    `json:"attempt"`
	MaxAttempts        int                    `json:"maxAttempts"`
	Trigger            string                 `json:"trigger"`
	State              string                 `json:"state"`
	Phase              string                 `json:"phase,omitempty"`
	GitHead            string                 `json:"gitHead,omitempty"`
	GitTree            string                 `json:"gitTree,omitempty"`
	StartedAt          *time.Time             `json:"startedAt,omitempty"`
	FinishedAt         *time.Time             `json:"finishedAt,omitempty"`
	ReworkSent         bool                   `json:"reworkSent"`
	Banner             string                 `json:"banner,omitempty"`
	PullRequestSection string                 `json:"pullRequestSection,omitempty"`
	Results            []CheckResultView      `json:"results"`
	ResourceVersion    domain.ResourceVersion `json:"resourceVersion"`
	CreatedAt          time.Time              `json:"createdAt"`
}

// CheckResultView is one command. Output is the redacted tail.
type CheckResultView struct {
	Name            string   `json:"name"`
	Argv            []string `json:"argv"`
	ExitCode        *int     `json:"exitCode,omitempty"`
	DurationMs      int64    `json:"durationMs"`
	TimedOut        bool     `json:"timedOut"`
	OutputTruncated bool     `json:"outputTruncated"`
	Output          string   `json:"output,omitempty"`
}

// ChecksSummaryView is the content-free Capsule field.
type ChecksSummaryView struct {
	Configured       bool                 `json:"configured"`
	Phase            string               `json:"phase,omitempty"`
	Attempt          int                  `json:"attempt,omitempty"`
	MaxAttempts      int                  `json:"maxAttempts,omitempty"`
	Tree             string               `json:"tree,omitempty"`
	Head             string               `json:"head,omitempty"`
	FinishedAt       *time.Time           `json:"finishedAt,omitempty"`
	ReworkSent       bool                 `json:"reworkSent,omitempty"`
	AutoRework       bool                 `json:"autoRework"`
	Stopped          bool                 `json:"stopped,omitempty"`
	ProposalWithheld bool                 `json:"proposalWithheld,omitempty"`
	Passed           int                  `json:"passed,omitempty"`
	Failed           int                  `json:"failed,omitempty"`
	RunID            string               `json:"runId,omitempty"`
	RemoteCI         *RemoteCISummaryView `json:"remoteCI,omitempty"`
}

type checksReplay struct {
	Hash string
	View ProjectChecksView
}

func (s *Service) ProjectChecks(ctx context.Context, id domain.ProjectID) (ProjectChecksView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ProjectChecksView{}, err
	}
	var view ProjectChecksView
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		view, err = s.projectChecksView(ctx, reader, id)
		return err
	})
	return view, err
}

func (s *Service) UpdateProjectChecks(ctx context.Context, id domain.ProjectID, input ProjectChecksUpdate, key string) (ProjectChecksView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return ProjectChecksView{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return ProjectChecksView{}, err
	}
	if input.ExpectedResourceVersion <= 0 {
		return ProjectChecksView{}, domain.ErrInvalid
	}
	config := domain.ProjectChecks{
		ProjectID: id, AutoRework: input.AutoRework, MaxReworkAttempts: input.MaxReworkAttempts,
		Checks: normalizeCommands(input.Checks),
	}
	if len(config.Checks) == 0 {
		config.AutoRework = true
		if config.MaxReworkAttempts == 0 {
			config.MaxReworkAttempts = 3
		}
	}
	if err := config.Validate(); err != nil {
		return ProjectChecksView{}, err
	}
	var view ProjectChecksView
	scope := "project:" + string(id) + ":checks"
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[checksReplay](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			if replay.Hash != setupFingerprint(input) {
				return domain.ErrConflict
			}
			view = replay.View
			return nil
		}
		project, err := tx.GetProject(ctx, id)
		if err != nil {
			return err
		}
		if err := projectAcceptsWork(project); err != nil {
			return err
		}
		if project.ResourceVersion != input.ExpectedResourceVersion {
			return domain.ErrConflict
		}
		now := s.clock.Now().UTC()
		config.UpdatedAt = now
		if err := tx.PutProjectChecks(ctx, config); err != nil {
			return err
		}
		previous := project.ResourceVersion
		project.ResourceVersion++
		project.UpdatedAt = now
		if err := tx.UpdateProject(ctx, project, previous); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "project", string(id), "project.checks_updated", project.ResourceVersion, nil); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, domain.AuditProjectChecksUpdate, "project", string(id)); err != nil {
			return err
		}
		view, err = s.projectChecksView(ctx, tx, id)
		if err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, checksReplay{Hash: setupFingerprint(input), View: view}, now)
	})
	if err != nil {
		s.auditFailure(ctx, domain.AuditProjectChecksUpdate, "project", string(id), err)
	}
	return view, err
}

func (s *Service) SuggestProjectChecks(ctx context.Context, projectID domain.ProjectID, capsuleID domain.CapsuleID) (CheckSuggestionView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return CheckSuggestionView{}, err
	}
	if s.checks == nil {
		return CheckSuggestionView{}, nil
	}
	var capsule domain.Capsule
	err := s.store.View(ctx, func(reader ports.Reader) error {
		if _, err := reader.GetProject(ctx, projectID); err != nil {
			return err
		}
		if capsuleID != "" {
			item, err := reader.GetCapsule(ctx, capsuleID)
			if err != nil {
				return err
			}
			if item.ProjectID != projectID {
				return domain.ErrNotFound
			}
			capsule = item
			return nil
		}
		items, _, err := reader.ListCapsules(ctx, projectID, "", ports.Page{Limit: 20})
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.State == domain.CapsuleReady && item.Role != domain.CapsuleSub {
				capsule = item
				break
			}
		}
		return nil
	})
	if err != nil {
		return CheckSuggestionView{}, err
	}
	if capsule.ProviderResourceID == "" || capsule.State != domain.CapsuleReady {
		return CheckSuggestionView{}, nil
	}
	suggestion, err := s.checks.SuggestChecks(ctx, capsule.ProviderResourceID)
	if err != nil {
		return CheckSuggestionView{Ignored: true}, nil
	}
	view := CheckSuggestionView{Ignored: false}
	for _, check := range suggestion.Checks {
		timeout := int(check.Timeout / time.Second)
		if timeout == 0 {
			timeout = domain.DefaultCheckTimeoutSeconds
		}
		command := domain.CheckCommand{Name: check.Name, Argv: check.Argv, TimeoutSeconds: timeout}
		if err := command.Validate(); err != nil {
			return CheckSuggestionView{Ignored: true}, nil
		}
		view.Checks = append(view.Checks, CheckCommandView{
			Name: command.Name, Argv: append([]string{}, command.Argv...), TimeoutSeconds: command.TimeoutSeconds,
		})
	}
	return view, nil
}

func (s *Service) CreateCheckRun(ctx context.Context, id domain.CapsuleID, key string) (CheckRunView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return CheckRunView{}, err
	}
	if err := requireIdempotency(key); err != nil {
		return CheckRunView{}, err
	}
	if s.checks == nil {
		return CheckRunView{}, domain.ErrUnsupported
	}
	var view CheckRunView
	scope := "capsule:" + string(id) + ":checks"
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if replay, ok, err := getReplay[domain.CheckRunID](ctx, tx, scope, key); err != nil {
			return err
		} else if ok {
			run, err := tx.GetCheckRun(ctx, replay)
			if err != nil {
				return err
			}
			view, err = s.checkRunView(ctx, tx, run)
			return err
		}
		run, err := s.queueCheck(ctx, tx, id, domain.CheckTriggerManual, 0, "", "", false)
		if err != nil {
			return err
		}
		view, err = s.checkRunView(ctx, tx, run)
		if err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, key, run.ID, run.CreatedAt)
	})
	if err == nil {
		s.wakeChecks()
	}
	return view, err
}

func (s *Service) ListCheckRuns(ctx context.Context, id domain.CapsuleID, page ports.Page) ([]CheckRunView, bool, error) {
	if err := requirePrincipal(ctx); err != nil {
		return nil, false, err
	}
	var views []CheckRunView
	var more bool
	err := s.store.View(ctx, func(reader ports.Reader) error {
		runs, extra, err := reader.ListCheckRuns(ctx, id, page)
		if err != nil {
			return err
		}
		more = extra
		views = make([]CheckRunView, 0, len(runs))
		for _, run := range runs {
			view, err := s.checkRunView(ctx, reader, run)
			if err != nil {
				return err
			}
			views = append(views, view)
		}
		return nil
	})
	return views, more, err
}

func (s *Service) GetCheckRun(ctx context.Context, id domain.CheckRunID) (CheckRunView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return CheckRunView{}, err
	}
	var view CheckRunView
	err := s.store.View(ctx, func(reader ports.Reader) error {
		run, err := reader.GetCheckRun(ctx, id)
		if err != nil {
			return err
		}
		view, err = s.checkRunView(ctx, reader, run)
		return err
	})
	return view, err
}

func (s *Service) CancelCheckRun(ctx context.Context, id domain.CheckRunID, expected domain.ResourceVersion) (CheckRunView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return CheckRunView{}, err
	}
	if expected <= 0 {
		return CheckRunView{}, domain.ErrInvalid
	}
	var view CheckRunView
	var capsuleID domain.CapsuleID
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		run, err := tx.GetCheckRun(ctx, id)
		if err != nil {
			return err
		}
		if run.ResourceVersion != expected {
			return domain.ErrConflict
		}
		if run.State.Terminal() {
			view, err = s.checkRunView(ctx, tx, run)
			return err
		}
		now := s.clock.Now().UTC()
		previous := run.ResourceVersion
		run.State = domain.CheckCancelled
		run.FinishedAt = now
		run.ResourceVersion++
		if err := tx.UpdateCheckRun(ctx, run, previous); err != nil {
			return err
		}
		capsuleID = run.CapsuleID
		view, err = s.checkRunView(ctx, tx, run)
		return err
	})
	if capsuleID != "" {
		s.cancelCheckExec(id)
	}
	return view, err
}

// SetCapsuleAutoRework turns the per-Capsule stop control on or off. Turning
// it off cancels the current loop. The Project setting is unchanged.
func (s *Service) SetCapsuleAutoRework(ctx context.Context, id domain.CapsuleID, enabled bool) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, err := tx.GetCapsule(ctx, id); err != nil {
			return err
		}
		control, err := tx.GetCheckControl(ctx, id)
		if err != nil {
			return err
		}
		control.Stopped = !enabled
		if !enabled {
			control.Epoch++
			if err := tx.CancelQueuedCheckRuns(ctx, id, s.clock.Now().UTC()); err != nil {
				return err
			}
			if err := s.cancelRunningTx(ctx, tx, id); err != nil {
				return err
			}
		}
		return tx.PutCheckControl(ctx, control)
	})
	if err == nil && !enabled {
		s.cancelCapsuleChecks(id)
	}
	return err
}

// CheckSummaries attaches the content-free field for the given Capsules.
func (s *Service) CheckSummaries(ctx context.Context, ids []domain.CapsuleID) (map[domain.CapsuleID]ChecksSummaryView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return nil, err
	}
	result := make(map[domain.CapsuleID]ChecksSummaryView, len(ids))
	err := s.store.View(ctx, func(reader ports.Reader) error {
		for _, id := range ids {
			capsule, err := reader.GetCapsule(ctx, id)
			if err != nil {
				if errors.Is(err, domain.ErrNotFound) {
					continue
				}
				return err
			}
			summary, ok, err := s.summaryIn(ctx, reader, capsule)
			if err != nil || !ok {
				if err != nil {
					return err
				}
				continue
			}
			result[id] = summary
		}
		return nil
	})
	return result, err
}

// StartChecks runs the suite worker until ctx is cancelled. The daemon calls
// it once. A second call is ignored.
func (s *Service) StartChecks(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.TickChecks(ctx)
			case <-s.checkWake:
				_ = s.TickChecks(ctx)
			}
		}
	}()
}

// TickChecks recovers a run left in `running` by a restart, queues automatic
// suites whose turns have gone idle, and executes due work. It is safe to
// call from tests.
func (s *Service) TickChecks(ctx context.Context) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	if s.checks == nil {
		return nil
	}
	if err := s.recoverRunningChecks(ctx); err != nil {
		return err
	}
	if err := s.considerReadyCapsules(ctx); err != nil {
		return err
	}
	if err := s.deliverPendingRework(ctx); err != nil {
		return err
	}
	runs, err := s.dueCheckRuns(ctx)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if err := s.executeCheckRun(ctx, run.ID); err != nil && !errors.Is(err, domain.ErrConflict) {
			return err
		}
	}
	return nil
}

// ConsiderChecks queues a suite for one Capsule when its turn is idle.
func (s *Service) ConsiderChecks(ctx context.Context, id domain.CapsuleID) {
	if err := requirePrincipal(ctx); err != nil {
		return
	}
	if s.checks == nil {
		return
	}
	if err := s.considerOne(ctx, id); err != nil {
		return
	}
	s.wakeChecks()
}

// InterruptChecks stops automatic rework for the current turn. The attempt
// counter resets because the epoch moves on. A cancelled run is not an attempt.
func (s *Service) InterruptChecks(ctx context.Context, id domain.CapsuleID) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		if _, err := tx.GetCapsule(ctx, id); err != nil {
			return err
		}
		control, err := tx.GetCheckControl(ctx, id)
		if err != nil {
			return err
		}
		control.Epoch++
		if err := tx.PutCheckControl(ctx, control); err != nil {
			return err
		}
		now := s.clock.Now().UTC()
		if err := tx.CancelQueuedCheckRuns(ctx, id, now); err != nil {
			return err
		}
		if err := s.cancelRunningTx(ctx, tx, id); err != nil {
			return err
		}
		return tx.DeleteDeliveryCheckGates(ctx, id)
	})
	if err == nil {
		s.cancelCapsuleChecks(id)
	}
	return err
}

func (s *Service) considerReadyCapsules(ctx context.Context) error {
	var ids []domain.CapsuleID
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		ids, err = reader.ListReadyCheckedCapsules(ctx, 50)
		return err
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.considerOne(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) considerOne(ctx context.Context, id domain.CapsuleID) error {
	var thread *domain.Thread
	var run domain.Run
	var hasRun bool
	err := s.store.View(ctx, func(reader ports.Reader) error {
		capsule, err := reader.GetCapsule(ctx, id)
		if err != nil || capsule.State != domain.CapsuleReady || capsule.DesiredState != domain.IntentReady ||
			capsule.Maintenance != "" || capsule.Role == domain.CapsuleSub {
			return err
		}
		config, err := s.loadChecks(ctx, reader, capsule.ProjectID)
		if err != nil || !config.Enabled() {
			return err
		}
		if _, err := reader.ActiveCheckRun(ctx, id); err == nil {
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		active, err := reader.GetActiveThread(ctx, id)
		if err == nil && active.State == domain.ThreadActive && active.CurrentRunID != "" {
			thread = &active
			return nil
		}
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		latest, err := reader.LatestRun(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		run, hasRun = latest, true
		return nil
	})
	if err != nil || (thread == nil && !hasRun) {
		return err
	}
	if thread != nil {
		return s.considerThread(ctx, id, *thread)
	}
	if !run.State.Terminal() {
		return nil
	}
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		latest, err := tx.LatestCheckRun(ctx, id)
		if err == nil && latest.BasisRunID == run.ID {
			return nil
		}
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		_, err = s.queueCheck(ctx, tx, id, domain.CheckTriggerRunExit, 0, "", run.ID, false)
		return err
	})
}

func (s *Service) considerThread(ctx context.Context, id domain.CapsuleID, thread domain.Thread) error {
	attention, err := s.ThreadAttention(ctx, thread)
	if err != nil || attention.Awaiting != nil || attention.Turn != ThreadTurnIdle {
		return err
	}
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetThread(ctx, thread.ID)
		if err != nil {
			return err
		}
		if current.MessageCount != thread.MessageCount {
			return nil
		}
		if _, err := tx.ActiveCheckRun(ctx, id); err == nil {
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		control, err := tx.GetCheckControl(ctx, id)
		if err != nil {
			return err
		}
		capsule, err := tx.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		config, err := s.loadChecks(ctx, tx, capsule.ProjectID)
		if err != nil || !config.Enabled() {
			return err
		}
		latest, err := tx.LatestCheckRun(ctx, id)
		if errors.Is(err, domain.ErrNotFound) {
			_, err = s.queueCheck(ctx, tx, id, domain.CheckTriggerTurnIdle, 0, current.ID, "", true)
			return err
		}
		if err != nil {
			return err
		}
		if latest.Epoch == control.Epoch && latest.BasisMessageCount == current.MessageCount {
			switch latest.State {
			case domain.CheckPassed, domain.CheckError, domain.CheckFailed:
				return nil
			case domain.CheckStale:
				if s.clock.Now().Before(latest.FinishedAt.Add(s.debounce())) {
					return nil
				}
			}
		}
		failed, err := tx.CountFailedCheckRuns(ctx, id, control.Epoch)
		if err != nil {
			return err
		}
		remoteFailed, err := tx.CountRemoteCIFailures(ctx, id, control.Epoch)
		if err != nil {
			return err
		}
		failed += remoteFailed
		if failed >= domain.CheckBudget(config.MaxReworkAttempts) {
			return nil
		}
		_, err = s.queueCheck(ctx, tx, id, domain.CheckTriggerTurnIdle, failed, current.ID, "", true)
		return err
	})
}

func (s *Service) queueCheck(
	ctx context.Context, tx ports.Transaction, id domain.CapsuleID, trigger domain.CheckTrigger,
	attempt int, threadID domain.ThreadID, basisRun domain.RunID, rework bool,
) (domain.CheckRun, error) {
	capsule, err := tx.GetCapsule(ctx, id)
	if err != nil {
		return domain.CheckRun{}, err
	}
	if capsule.State != domain.CapsuleReady {
		return domain.CheckRun{}, fmt.Errorf("%w: Capsule must be Ready", domain.ErrIllegalTransition)
	}
	config, err := s.loadChecks(ctx, tx, capsule.ProjectID)
	if err != nil {
		return domain.CheckRun{}, err
	}
	if !config.Enabled() {
		return domain.CheckRun{}, fmt.Errorf("%w: this Project has no checks", domain.ErrInvalid)
	}
	if trigger == domain.CheckTriggerManual {
		if active, err := tx.ActiveCheckRun(ctx, id); err == nil && active.State == domain.CheckRunning {
			return domain.CheckRun{}, fmt.Errorf("%w: checks are already running", domain.ErrConflict)
		} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return domain.CheckRun{}, err
		}
		if err := tx.CancelQueuedCheckRuns(ctx, id, s.clock.Now().UTC()); err != nil {
			return domain.CheckRun{}, err
		}
	}
	control, err := tx.GetCheckControl(ctx, id)
	if err != nil {
		return domain.CheckRun{}, err
	}
	var basis int64
	if threadID != "" {
		thread, err := tx.GetThread(ctx, threadID)
		if err != nil {
			return domain.CheckRun{}, err
		}
		basis = thread.MessageCount
	}
	now := s.clock.Now().UTC()
	notBefore := now
	if trigger == domain.CheckTriggerTurnIdle {
		notBefore = now.Add(s.debounce())
	}
	allowed := rework && domain.ShouldAutoRework(config.AutoRework, control.Stopped, trigger, capsule.Role, attempt, config.MaxReworkAttempts)
	run := domain.CheckRun{
		ID: domain.CheckRunID(s.ids.NewID()), CapsuleID: capsule.ID, ProjectID: capsule.ProjectID,
		ThreadID: threadID, Attempt: attempt, Trigger: trigger, State: domain.CheckQueued,
		NotBefore: notBefore, Epoch: control.Epoch, BasisMessageCount: basis, BasisRunID: basisRun,
		ReworkAllowed: allowed, ResourceVersion: 1, CreatedAt: now,
	}
	if err := tx.InsertCheckRun(ctx, run); err != nil {
		return domain.CheckRun{}, err
	}
	if err := s.appendEvent(ctx, tx, "capsule", string(capsule.ID), "capsule.checks_started", run.ResourceVersion, checkEventData(run, config)); err != nil {
		return domain.CheckRun{}, err
	}
	return run, nil
}

func (s *Service) executeCheckRun(ctx context.Context, id domain.CheckRunID) error {
	var run domain.CheckRun
	var capsule domain.Capsule
	var commands []domain.CheckCommand
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetCheckRun(ctx, id)
		if err != nil {
			return err
		}
		if current.State != domain.CheckQueued || s.clock.Now().Before(current.NotBefore) {
			return domain.ErrConflict
		}
		capsule, err = tx.GetCapsule(ctx, current.CapsuleID)
		if err != nil {
			return err
		}
		control, err := tx.GetCheckControl(ctx, current.CapsuleID)
		if err != nil {
			return err
		}
		if control.Epoch != current.Epoch || capsule.State != domain.CapsuleReady {
			return s.markCheck(ctx, tx, current, domain.CheckCancelled, "", nil)
		}
		config, err := s.loadChecks(ctx, tx, capsule.ProjectID)
		if err != nil {
			return err
		}
		if !config.Enabled() {
			return s.markCheck(ctx, tx, current, domain.CheckCancelled, "", nil)
		}
		commands = config.Checks
		now := s.clock.Now().UTC()
		previous := current.ResourceVersion
		current.State = domain.CheckRunning
		current.StartedAt = now
		current.ResourceVersion++
		if err := tx.UpdateCheckRun(ctx, current, previous); err != nil {
			return err
		}
		run = current
		return nil
	})
	if err != nil {
		return err
	}
	execCtx, cancel := context.WithCancel(ctx)
	s.trackCheck(run.ID, capsule.ID, cancel)
	defer s.untrackCheck(run.ID)
	request := ports.CheckRequest{OutputLimit: domain.CheckOutputBytes}
	for _, command := range commands {
		request.Commands = append(request.Commands, ports.CheckCommand{
			Name: command.Name, Argv: command.Argv, Timeout: command.Timeout(),
		})
	}
	execution, execErr := s.checks.RunChecks(execCtx, capsule.ProviderResourceID, request)
	return s.finishCheckRun(ctx, run.ID, execution, execErr)
}

func (s *Service) finishCheckRun(ctx context.Context, id domain.CheckRunID, execution ports.CheckExecution, execErr error) error {
	var rework domain.CheckRun
	var send bool
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		run, err := tx.GetCheckRun(ctx, id)
		if err != nil {
			return err
		}
		if run.State != domain.CheckRunning {
			return nil
		}
		control, err := tx.GetCheckControl(ctx, run.CapsuleID)
		if err != nil {
			return err
		}
		if control.Epoch != run.Epoch {
			return s.markCheck(ctx, tx, run, domain.CheckCancelled, "", nil)
		}
		capsule, err := tx.GetCapsule(ctx, run.CapsuleID)
		if err != nil {
			return err
		}
		config, err := s.loadChecks(ctx, tx, run.ProjectID)
		if err != nil {
			return err
		}
		if execErr != nil {
			if errors.Is(execErr, context.Canceled) {
				return s.markCheck(ctx, tx, run, domain.CheckCancelled, "", nil)
			}
			return s.markCheck(ctx, tx, run, domain.CheckError, "runtime", nil)
		}
		results := make([]domain.CheckResult, 0, len(execution.Results))
		passed, failed := 0, 0
		for index, item := range execution.Results {
			output := []byte(redactCheckText(string(item.Output)))
			result := domain.CheckResult{
				Position: index, Name: item.Name, Argv: append([]string{}, item.Argv...),
				ExitCode: item.ExitCode, HasExit: !item.TimedOut, Duration: item.Duration,
				TimedOut: item.TimedOut, OutputTruncated: item.Truncated,
			}
			if s.secretKey != nil && len(output) > 0 {
				label := checkOutputLabel(run, index)
				nonce, ciphertext, err := s.secretKey.SealBlob(label, output)
				if err != nil {
					return err
				}
				result.OutputNonce, result.OutputCiphertext = nonce, ciphertext
			}
			for i := range output {
				output[i] = 0
			}
			if item.ExitCode == 0 && !item.TimedOut {
				passed++
			} else {
				failed++
			}
			results = append(results, result)
		}
		run.GitHead = execution.HeadBefore
		run.GitTree = execution.TreeBefore
		run.Passed, run.Failed = passed, failed
		state := domain.CheckPassed
		if execution.Stale {
			state = domain.CheckStale
		} else if failed > 0 {
			state = domain.CheckFailed
		}
		reworkNow := state == domain.CheckFailed && domain.ShouldAutoRework(config.AutoRework, control.Stopped, run.Trigger, capsule.Role, run.Attempt, config.MaxReworkAttempts) && run.ThreadID != ""
		if !reworkNow {
			run.ReworkAllowed = false
		}
		if err := s.markCheck(ctx, tx, run, state, "", results); err != nil {
			return err
		}
		fresh, err := tx.GetCheckRun(ctx, id)
		if err != nil {
			return err
		}
		if state == domain.CheckFailed && fresh.ReworkAllowed && fresh.ThreadID != "" {
			rework = fresh
			send = true
			return nil
		}
		if state == domain.CheckPassed || state == domain.CheckFailed || state == domain.CheckError {
			return tx.DeleteDeliveryCheckGates(ctx, run.CapsuleID)
		}
		return nil
	})
	if err != nil || !send {
		return err
	}
	if err := s.sendRework(ctx, rework); err != nil {
		_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
			current, err := tx.GetCheckRun(ctx, rework.ID)
			if err != nil {
				return err
			}
			return s.markCheck(ctx, tx, current, domain.CheckError, "rework_undelivered", nil)
		})
		return nil
	}
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetCheckRun(ctx, rework.ID)
		if err != nil || current.State != domain.CheckFailed {
			return err
		}
		previous := current.ResourceVersion
		current.ReworkSent = true
		current.ResourceVersion++
		return tx.UpdateCheckRun(ctx, current, previous)
	})
}

func (s *Service) markCheck(ctx context.Context, tx ports.Transaction, run domain.CheckRun, state domain.CheckRunState, code string, results []domain.CheckResult) error {
	previous := run.ResourceVersion
	run.State = state
	run.ErrorCode = code
	run.FinishedAt = s.clock.Now().UTC()
	if state == domain.CheckCancelled {
		run.ErrorCode = ""
	}
	run.ResourceVersion++
	if err := run.Validate(); err != nil {
		return err
	}
	if err := tx.UpdateCheckRun(ctx, run, previous); err != nil {
		return err
	}
	if results != nil {
		if err := tx.ReplaceCheckResults(ctx, run.ID, results); err != nil {
			return err
		}
	}
	config, err := s.loadChecks(ctx, tx, run.ProjectID)
	if err != nil {
		return err
	}
	return s.appendEvent(ctx, tx, "capsule", string(run.CapsuleID), "capsule.checks_finished", run.ResourceVersion, checkEventData(run, config))
}

func (s *Service) sendRework(ctx context.Context, run domain.CheckRun) error {
	if s.structured == nil || s.transcriptKey == nil {
		return domain.ErrUnsupported
	}
	var content string
	err := s.store.View(ctx, func(reader ports.Reader) error {
		config, err := s.loadChecks(ctx, reader, run.ProjectID)
		if err != nil {
			return err
		}
		results, err := reader.ListCheckResults(ctx, run.ID)
		if err != nil {
			return err
		}
		content = s.reworkText(run, config, results)
		return nil
	})
	if err != nil {
		return err
	}
	frame := func(messageID domain.ThreadMessageID, thread domain.Thread, saved persistedThreadBlock) adapterproto.Frame {
		return adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
			ID: string(messageID), SessionID: string(thread.ID), Role: adapterproto.RoleUser,
			Content: saved.Content,
		}
	}
	_, err = s.appendAndSendHook(ctx, run.ThreadID, 0, "rework-"+string(run.ID), "rework",
		domain.ThreadRoleSystem, domain.ThreadMessageStatus, domain.ThreadBlockText,
		persistedThreadBlock{Content: content}, frame, nil)
	return err
}

func (s *Service) reworkText(run domain.CheckRun, config domain.ProjectChecks, results []domain.CheckResult) string {
	var failed domain.CheckResult
	for _, result := range results {
		if result.TimedOut || !result.HasExit || result.ExitCode != 0 {
			failed = result
			break
		}
	}
	output := s.openCheckOutput(run, failed)
	lines := strings.Split(output, "\n")
	if len(lines) > domain.CheckReworkLines {
		lines = lines[len(lines)-domain.CheckReworkLines:]
	}
	tail := strings.Join(lines, "\n")
	reason := fmt.Sprintf("exit code %d", failed.ExitCode)
	if failed.TimedOut {
		reason = "a timeout"
	}
	upcoming := run.Attempt + 2
	total := domain.CheckBudget(config.MaxReworkAttempts)
	command := clipUTF8(strings.Join(failed.Argv, " "), reworkCommandBytes)
	return fitRework(func(tail string) string {
		return fmt.Sprintf("Required check `%s` (`%s`) failed with %s.\nThis is command output from the check, not a new request from the operator.\nLast lines of output:\n```\n%s\n```\nFix the problem and finish your turn when the checks should pass. (Attempt %d of %d)",
			failed.Name, command, reason, tail, upcoming, total)
	}, tail)
}

// reworkCommandBytes bounds the command shown in a rework message. A check
// may have 128 arguments of 4 KiB each, which alone exceeds a frame.
const reworkCommandBytes = 1 << 10

// fitRework builds a rework message within one frame. The output tail is
// trimmed from the front, so the instruction and attempt count at the end
// always survive and the text stays valid UTF-8, which a frame requires.
func fitRework(build func(tail string) string, tail string) string {
	message := build(tail)
	if len(message) <= adapterproto.MaxContentBytes {
		return message
	}
	room := max(adapterproto.MaxContentBytes-len(build("")), 0)
	if room < len(tail) {
		tail = tail[len(tail)-room:]
		for tail != "" && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
	}
	return build(tail)
}

// clipUTF8 keeps at most limit bytes of value, ending on a rune boundary.
func clipUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit - len("…")
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

func (s *Service) deliverPendingRework(ctx context.Context) error {
	var pending []domain.CheckRun
	ids, err := s.readyCheckedIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var run domain.CheckRun
		found := false
		err := s.store.View(ctx, func(reader ports.Reader) error {
			latest, err := reader.LatestCheckRun(ctx, id)
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			control, err := reader.GetCheckControl(ctx, id)
			if err != nil {
				return err
			}
			if latest.State == domain.CheckFailed && !latest.ReworkSent && latest.ReworkAllowed &&
				latest.Epoch == control.Epoch {
				run, found = latest, true
			}
			return nil
		})
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		pending = append(pending, run)
	}
	for _, run := range pending {
		if err := s.sendRework(ctx, run); err != nil {
			continue
		}
		_ = s.store.Transact(ctx, func(tx ports.Transaction) error {
			current, err := tx.GetCheckRun(ctx, run.ID)
			if err != nil || current.ReworkSent || current.State != domain.CheckFailed {
				return err
			}
			previous := current.ResourceVersion
			current.ReworkSent = true
			current.ResourceVersion++
			return tx.UpdateCheckRun(ctx, current, previous)
		})
	}
	return nil
}

func (s *Service) recoverRunningChecks(ctx context.Context) error {
	var running []domain.CheckRun
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		running, err = reader.ListRunningCheckRuns(ctx)
		return err
	})
	if err != nil {
		return err
	}
	for _, run := range running {
		if s.checkTracked(run.ID) {
			continue
		}
		if err := s.store.Transact(ctx, func(tx ports.Transaction) error {
			current, err := tx.GetCheckRun(ctx, run.ID)
			if err != nil || current.State != domain.CheckRunning {
				return err
			}
			if err := s.markCheck(ctx, tx, current, domain.CheckError, "restarted", nil); err != nil {
				return err
			}
			if current.RestartRetried {
				return nil
			}
			replacement := current
			replacement.ID = domain.CheckRunID(s.ids.NewID())
			replacement.State = domain.CheckQueued
			replacement.StartedAt = time.Time{}
			replacement.FinishedAt = time.Time{}
			replacement.ErrorCode = ""
			replacement.RestartRetried = true
			replacement.ReworkSent = false
			replacement.GitHead, replacement.GitTree = "", ""
			replacement.Passed, replacement.Failed = 0, 0
			replacement.ResourceVersion = 1
			replacement.CreatedAt = s.clock.Now().UTC()
			replacement.NotBefore = replacement.CreatedAt
			return tx.InsertCheckRun(ctx, replacement)
		}); err != nil && !errors.Is(err, domain.ErrConflict) {
			return err
		}
	}
	return nil
}

func (s *Service) dueCheckRuns(ctx context.Context) ([]domain.CheckRun, error) {
	var runs []domain.CheckRun
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		runs, err = reader.ListDueCheckRuns(ctx, s.clock.Now().UTC(), 20)
		return err
	})
	return runs, err
}

func (s *Service) readyCheckedIDs(ctx context.Context) ([]domain.CapsuleID, error) {
	var ids []domain.CapsuleID
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		ids, err = reader.ListReadyCheckedCapsules(ctx, 50)
		return err
	})
	return ids, err
}

func (s *Service) cancelRunningTx(ctx context.Context, tx ports.Transaction, id domain.CapsuleID) error {
	active, err := tx.ActiveCheckRun(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || active.State != domain.CheckRunning {
		return nil
	}
	if err != nil {
		return err
	}
	return s.markCheck(ctx, tx, active, domain.CheckCancelled, "", nil)
}

func (s *Service) loadChecks(ctx context.Context, reader ports.Reader, id domain.ProjectID) (domain.ProjectChecks, error) {
	project, err := reader.GetProject(ctx, id)
	if err != nil {
		return domain.ProjectChecks{}, err
	}
	checks, err := reader.GetProjectChecks(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ProjectChecks{
			ProjectID: id, AutoRework: true, MaxReworkAttempts: 3, ResourceVersion: project.ResourceVersion,
		}, nil
	}
	if err != nil {
		return domain.ProjectChecks{}, err
	}
	checks.ResourceVersion = project.ResourceVersion
	return checks, nil
}

func (s *Service) projectChecksView(ctx context.Context, reader ports.Reader, id domain.ProjectID) (ProjectChecksView, error) {
	checks, err := s.loadChecks(ctx, reader, id)
	if err != nil {
		return ProjectChecksView{}, err
	}
	view := ProjectChecksView{
		AutoRework: checks.AutoRework, MaxReworkAttempts: checks.MaxReworkAttempts,
		ResourceVersion: checks.ResourceVersion, Checks: []CheckCommandView{},
	}
	if !checks.UpdatedAt.IsZero() {
		at := checks.UpdatedAt
		view.UpdatedAt = &at
	}
	for _, check := range checks.Checks {
		view.Checks = append(view.Checks, CheckCommandView{
			Name: check.Name, Argv: append([]string{}, check.Argv...), TimeoutSeconds: check.TimeoutSeconds,
		})
	}
	return view, nil
}

func (s *Service) summaryIn(ctx context.Context, reader ports.Reader, capsule domain.Capsule) (ChecksSummaryView, bool, error) {
	config, err := s.loadChecks(ctx, reader, capsule.ProjectID)
	if err != nil {
		return ChecksSummaryView{}, false, err
	}
	watch, watchErr := reader.LatestRemoteCI(ctx, capsule.ID)
	hasCI := watchErr == nil
	if errors.Is(watchErr, domain.ErrNotFound) {
		watchErr = nil
	}
	if watchErr != nil {
		return ChecksSummaryView{}, false, watchErr
	}
	if !config.Enabled() && !hasCI {
		return ChecksSummaryView{}, false, nil
	}
	control, err := reader.GetCheckControl(ctx, capsule.ID)
	if err != nil {
		return ChecksSummaryView{}, false, err
	}
	withheld, err := reader.CapsuleHasCheckGate(ctx, capsule.ID)
	if err != nil {
		return ChecksSummaryView{}, false, err
	}
	view := ChecksSummaryView{
		Configured: config.Enabled(), AutoRework: config.AutoRework && !control.Stopped,
		Stopped: control.Stopped, MaxAttempts: domain.CheckBudget(config.MaxReworkAttempts),
		ProposalWithheld: withheld,
	}
	if hasCI {
		summary := remoteCISummary(watch, config)
		view.RemoteCI = &summary
	}
	if !config.Enabled() {
		return view, true, nil
	}
	run, err := reader.LatestCheckRun(ctx, capsule.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return view, true, nil
	}
	if err != nil {
		return ChecksSummaryView{}, false, err
	}
	phase := domain.CheckPhaseOf(run.State, run.ReworkSent, config.AutoRework, control.Stopped, run.Trigger, capsule.Role, run.Attempt, config.MaxReworkAttempts)
	view.Phase = string(phase)
	view.Attempt = run.Attempt + 1
	view.Tree = run.GitTree
	view.Head = run.GitHead
	view.ReworkSent = run.ReworkSent
	view.Passed = run.Passed
	view.Failed = run.Failed
	view.RunID = string(run.ID)
	if !run.FinishedAt.IsZero() {
		at := run.FinishedAt
		view.FinishedAt = &at
	}
	return view, true, nil
}

func (s *Service) checkRunView(ctx context.Context, reader ports.Reader, run domain.CheckRun) (CheckRunView, error) {
	capsule, err := reader.GetCapsule(ctx, run.CapsuleID)
	if err != nil {
		return CheckRunView{}, err
	}
	config, err := s.loadChecks(ctx, reader, run.ProjectID)
	if err != nil {
		return CheckRunView{}, err
	}
	control, err := reader.GetCheckControl(ctx, run.CapsuleID)
	if err != nil {
		return CheckRunView{}, err
	}
	results, err := reader.ListCheckResults(ctx, run.ID)
	if err != nil {
		return CheckRunView{}, err
	}
	phase := domain.CheckPhaseOf(run.State, run.ReworkSent, config.AutoRework, control.Stopped, run.Trigger, capsule.Role, run.Attempt, config.MaxReworkAttempts)
	view := CheckRunView{
		ID: string(run.ID), CapsuleID: string(run.CapsuleID), ProjectID: string(run.ProjectID),
		ThreadID: string(run.ThreadID), Attempt: run.Attempt + 1,
		MaxAttempts: domain.CheckBudget(config.MaxReworkAttempts),
		Trigger:     string(run.Trigger), State: string(run.State), Phase: string(phase),
		GitHead: run.GitHead, GitTree: run.GitTree, ReworkSent: run.ReworkSent,
		ResourceVersion: run.ResourceVersion, CreatedAt: run.CreatedAt,
		Banner:  checkBanner(phase, run.Attempt, config.MaxReworkAttempts),
		Results: []CheckResultView{},
	}
	if !run.StartedAt.IsZero() {
		at := run.StartedAt
		view.StartedAt = &at
	}
	if !run.FinishedAt.IsZero() {
		at := run.FinishedAt
		view.FinishedAt = &at
	}
	resultViews := make([]CheckResultView, 0, len(results))
	for index, result := range results {
		item := CheckResultView{
			Name: result.Name, Argv: append([]string{}, result.Argv...),
			DurationMs: result.Duration.Milliseconds(), TimedOut: result.TimedOut,
			OutputTruncated: result.OutputTruncated,
			Output:          s.openCheckOutput(run, result),
		}
		if result.HasExit {
			code := result.ExitCode
			item.ExitCode = &code
		}
		if index == result.Position {
			resultViews = append(resultViews, item)
		}
	}
	view.Results = resultViews
	if run.State == domain.CheckPassed || run.State == domain.CheckFailed {
		view.PullRequestSection = checksPullRequestSection(run.GitTree, resultViews, nil)
	}
	return view, nil
}

func (s *Service) openCheckOutput(run domain.CheckRun, result domain.CheckResult) string {
	if s.secretKey == nil || len(result.OutputCiphertext) == 0 {
		return ""
	}
	plain, err := s.secretKey.OpenBlob(checkOutputLabel(run, result.Position), result.OutputNonce, result.OutputCiphertext)
	if err != nil {
		return ""
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	return string(plain)
}

func checkOutputLabel(run domain.CheckRun, position int) string {
	return string(run.OwnerUserID) + "\x00" + string(run.ID) + "\x00" + fmt.Sprint(position)
}

func (s *Service) checksPullRequestBody(ctx context.Context, capsuleID domain.CapsuleID, tree, body string) (string, error) {
	var section string
	err := s.store.View(ctx, func(reader ports.Reader) error {
		capsule, err := reader.GetCapsule(ctx, capsuleID)
		if err != nil {
			return err
		}
		config, err := s.loadChecks(ctx, reader, capsule.ProjectID)
		if err != nil || !config.Enabled() {
			return err
		}
		run, err := reader.LatestCheckRun(ctx, capsuleID)
		if errors.Is(err, domain.ErrNotFound) || run.GitTree != tree ||
			(run.State != domain.CheckPassed && run.State != domain.CheckFailed) {
			section = checksPullRequestSection(tree, nil, nil)
			return nil
		}
		if err != nil {
			return err
		}
		view, err := s.checkRunView(ctx, reader, run)
		if err != nil {
			return err
		}
		section = view.PullRequestSection
		return nil
	})
	if err != nil || section == "" {
		return body, err
	}
	return appendChecksSection(body, section), nil
}

func checkBanner(phase domain.CheckPhase, attempt, maxRework int) string {
	total := domain.CheckBudget(maxRework)
	shown := attempt + 1
	switch phase {
	case domain.CheckPhasePassed:
		return "Checks passed on this tree"
	case domain.CheckPhaseFailing:
		if shown >= total {
			return fmt.Sprintf("Checks failing (%d/%d)", total, total)
		}
		return "Checks failed"
	case domain.CheckPhaseReworking:
		return fmt.Sprintf("Reworking · attempt %d of %d", shown+1, total)
	case domain.CheckPhaseRunning, domain.CheckPhaseQueued:
		return fmt.Sprintf("Running checks · attempt %d of %d", shown, total)
	case domain.CheckPhaseStale:
		return "Checks are out of date for this tree"
	case domain.CheckPhaseError:
		return "Checks could not finish"
	default:
		return ""
	}
}

func checksPullRequestSection(tree string, results []CheckResultView, remote []RemoteCICheckView) string {
	var builder strings.Builder
	builder.WriteString(checksMarkerStart)
	builder.WriteString("\n## Meridian checks\n\n")
	if tree == "" {
		builder.WriteString("Checks were not run for this tree.\n")
	} else if len(results) == 0 {
		builder.WriteString("Checks were not run for tree `" + tree + "`.\n")
	} else {
		builder.WriteString("Checked tree `" + tree + "`.\n\n")
		for _, result := range results {
			mark := "passed"
			if result.TimedOut || result.ExitCode == nil || *result.ExitCode != 0 {
				mark = "failed"
			}
			seconds := result.DurationMs / 1000
			fmt.Fprintf(&builder, "- %s `%s` (%s) %ds\n", mark, result.Name, strings.Join(result.Argv, " "), seconds)
		}
	}
	if len(remote) > 0 {
		builder.WriteString("\nRemote CI:\n\n")
		for _, check := range remote {
			fmt.Fprintf(&builder, "- %s `%s`\n", check.Conclusion, check.Name)
		}
	}
	builder.WriteString(checksMarkerEnd)
	builder.WriteString("\n")
	return builder.String()
}

func appendChecksSection(body, section string) string {
	if start := strings.Index(body, checksMarkerStart); start >= 0 {
		end := strings.Index(body[start:], checksMarkerEnd)
		if end >= 0 {
			end += start + len(checksMarkerEnd)
			body = strings.TrimSpace(body[:start] + body[end:])
		}
	}
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return section
	}
	return body + "\n\n" + section
}

func normalizeCommands(views []CheckCommandView) []domain.CheckCommand {
	commands := make([]domain.CheckCommand, 0, len(views))
	for _, view := range views {
		timeout := view.TimeoutSeconds
		if timeout == 0 {
			timeout = domain.DefaultCheckTimeoutSeconds
		}
		commands = append(commands, domain.CheckCommand{
			Name: view.Name, Argv: append([]string{}, view.Argv...), TimeoutSeconds: timeout,
		})
	}
	return commands
}

func checkEventData(run domain.CheckRun, config domain.ProjectChecks) []byte {
	payload, err := json.Marshal(struct {
		State       string `json:"state"`
		Attempt     int    `json:"attempt"`
		MaxAttempts int    `json:"maxAttempts"`
		Passed      int    `json:"passed"`
		Failed      int    `json:"failed"`
	}{
		State: string(run.State), Attempt: run.Attempt + 1,
		MaxAttempts: domain.CheckBudget(config.MaxReworkAttempts),
		Passed:      run.Passed, Failed: run.Failed,
	})
	if err != nil {
		return nil
	}
	return payload
}

var (
	secretTokenPattern  = regexp.MustCompile(`(?i)\b(?:ghp_|github_pat_|sk-|xox[baprs]-|AKIA)[A-Za-z0-9_\-]{6,}`)
	secretAssignPattern = regexp.MustCompile(`(?i)((?:authorization\s*:\s*bearer|token|password|secret|api_key)\s*[=:]\s*)\S+`)
)

func redactCheckText(value string) string {
	value = secretTokenPattern.ReplaceAllString(value, "[redacted]")
	return secretAssignPattern.ReplaceAllString(value, "${1}[redacted]")
}

func (s *Service) debounce() time.Duration {
	if s.checkDebounceOn {
		return s.checkDebounce
	}
	return 5 * time.Second
}

func (s *Service) wakeChecks() {
	if s.checkWake == nil {
		return
	}
	select {
	case s.checkWake <- struct{}{}:
	default:
	}
}

func (s *Service) trackCheck(id domain.CheckRunID, capsule domain.CapsuleID, cancel context.CancelFunc) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	if s.checkCancel == nil {
		s.checkCancel = map[domain.CheckRunID]context.CancelFunc{}
	}
	if s.checkOwners == nil {
		s.checkOwners = map[domain.CheckRunID]domain.CapsuleID{}
	}
	s.checkCancel[id] = cancel
	s.checkOwners[id] = capsule
}

func (s *Service) untrackCheck(id domain.CheckRunID) {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	delete(s.checkCancel, id)
	delete(s.checkOwners, id)
}

func (s *Service) checkTracked(id domain.CheckRunID) bool {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	_, ok := s.checkCancel[id]
	return ok
}

func (s *Service) cancelCheckExec(id domain.CheckRunID) {
	s.checkMu.Lock()
	cancel := s.checkCancel[id]
	s.checkMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) cancelCapsuleChecks(id domain.CapsuleID) {
	s.checkMu.Lock()
	var cancels []context.CancelFunc
	for runID, capsuleID := range s.checkOwners {
		if capsuleID == id {
			if cancel := s.checkCancel[runID]; cancel != nil {
				cancels = append(cancels, cancel)
			}
		}
	}
	s.checkMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
