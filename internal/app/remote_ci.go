package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/githubci"
	"github.com/OrlojHQ/meridian/internal/ports"
)

// RemoteCIOrigin is a commit Meridian has already pushed to a pull request.
// Recording it does not push and does not read a token.
type RemoteCIOrigin struct {
	CapsuleID  domain.CapsuleID
	DeliveryID domain.DeliveryID
	Owner      string
	Repository string
	Number     int64
	URL        string
	Head       string
	Tree       string
}

// RemoteCISummaryView is the content-free CI line on a Capsule. It has no log tail.
type RemoteCISummaryView struct {
	State          string `json:"state"`
	Label          string `json:"label"`
	Head           string `json:"head,omitempty"`
	Attempt        int    `json:"attempt,omitempty"`
	MaxAttempts    int    `json:"maxAttempts,omitempty"`
	Passed         int    `json:"passed,omitempty"`
	Failed         int    `json:"failed,omitempty"`
	Pending        int    `json:"pending,omitempty"`
	PullRequestURL string `json:"pullRequestUrl,omitempty"`
	PushReview     bool   `json:"pushReview,omitempty"`
	ReworkSent     bool   `json:"reworkSent,omitempty"`
}

// RemoteCICheckView is one GitHub check. Output is the redacted tail.
type RemoteCICheckView struct {
	Name       string `json:"name"`
	Source     string `json:"source"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url,omitempty"`
	Output     string `json:"output,omitempty"`
}

// RemoteCIView is the Capsule detail, including tails.
type RemoteCIView struct {
	RemoteCISummaryView
	Checks []RemoteCICheckView `json:"checks"`
}

// ConfigureRemoteCI attaches the GitHub reader. A nil reader still records
// watches; the station shows Connect GitHub until a reader is configured.
func (s *Service) ConfigureRemoteCI(reader githubci.Reader) {
	s.remoteCI = reader
}

// SetRemoteCIImmediatePoll makes the next poll due immediately. Tests use it.
func (s *Service) SetRemoteCIImmediatePoll(on bool) {
	s.ciImmediate = on
}

// StartRemoteCI polls due watches from meridiand. There is no inbound webhook.
// ApplyRemoteCI is the seam a later webhook or GitHub App would call instead.
func (s *Service) StartRemoteCI(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.TickRemoteCI(ctx)
			case <-s.ciWake:
				_ = s.TickRemoteCI(ctx)
			}
		}
	}()
}

// RecordRemoteCI starts a watch for a commit that is already on a pull request.
func (s *Service) RecordRemoteCI(ctx context.Context, origin RemoteCIOrigin) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		return s.insertRemoteCI(ctx, tx, origin)
	})
	if err == nil {
		s.wakeCI()
	}
	return err
}

func (s *Service) insertRemoteCI(ctx context.Context, tx ports.Transaction, origin RemoteCIOrigin) error {
	if origin.CapsuleID == "" || origin.Number <= 0 || origin.Head == "" ||
		origin.Owner == "" || origin.Repository == "" {
		return fmt.Errorf("%w: remote CI needs a pull request and a commit", domain.ErrInvalid)
	}
	capsule, err := tx.GetCapsule(ctx, origin.CapsuleID)
	if err != nil {
		return err
	}
	if existing, err := tx.LatestRemoteCI(ctx, origin.CapsuleID); err == nil && existing.GitHead == origin.Head {
		return nil
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if err := tx.SupersedeRemoteCI(ctx, origin.CapsuleID, origin.Head); err != nil {
		return err
	}
	control, err := tx.GetCheckControl(ctx, origin.CapsuleID)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	watch := domain.RemoteCIWatch{
		ID: domain.RemoteCIWatchID(s.ids.NewID()), CapsuleID: capsule.ID, ProjectID: capsule.ProjectID,
		OwnerUserID: capsule.OwnerUserID, DeliveryID: origin.DeliveryID,
		RepositoryOwner: origin.Owner, RepositoryName: origin.Repository,
		PullNumber: origin.Number, PullURL: origin.URL, GitHead: origin.Head, GitTree: origin.Tree,
		State: domain.RemoteCIWaiting, Epoch: control.Epoch, NextPollAt: now,
		ResourceVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	return tx.InsertRemoteCI(ctx, watch)
}

// TickRemoteCI polls watches that are due. It never pushes.
func (s *Service) TickRemoteCI(ctx context.Context) error {
	if err := requirePrincipal(ctx); err != nil {
		return err
	}
	var due []domain.RemoteCIWatch
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		due, err = reader.ListDueRemoteCI(ctx, s.clock.Now().UTC(), 20)
		return err
	})
	if err != nil {
		return err
	}
	for _, watch := range due {
		if err := s.pollRemoteCI(ctx, watch.ID); err != nil && !errors.Is(err, domain.ErrConflict) {
			return err
		}
	}
	return nil
}

// RemoteCI returns the stored reading for a Capsule, including redacted tails.
func (s *Service) RemoteCI(ctx context.Context, id domain.CapsuleID) (RemoteCIView, error) {
	if err := requirePrincipal(ctx); err != nil {
		return RemoteCIView{}, err
	}
	var view RemoteCIView
	err := s.store.View(ctx, func(reader ports.Reader) error {
		watch, err := reader.LatestRemoteCI(ctx, id)
		if err != nil {
			return err
		}
		capsule, err := reader.GetCapsule(ctx, id)
		if err != nil {
			return err
		}
		config, err := s.loadChecks(ctx, reader, capsule.ProjectID)
		if err != nil {
			return err
		}
		view.RemoteCISummaryView = remoteCISummary(watch, config)
		checks, err := reader.ListRemoteCIChecks(ctx, watch.ID)
		if err != nil {
			return err
		}
		view.Checks = make([]RemoteCICheckView, 0, len(checks))
		for _, check := range checks {
			view.Checks = append(view.Checks, RemoteCICheckView{
				Name: check.Name, Source: check.Source, Conclusion: check.Conclusion,
				URL: check.URL, Output: s.openCIOutput(watch, check),
			})
		}
		return nil
	})
	return view, err
}

func (s *Service) pollRemoteCI(ctx context.Context, id domain.RemoteCIWatchID) error {
	var watch domain.RemoteCIWatch
	var project domain.Project
	var capsule domain.Capsule
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		watch, err = reader.GetRemoteCI(ctx, id)
		if err != nil {
			return err
		}
		capsule, err = reader.GetCapsule(ctx, watch.CapsuleID)
		if err != nil {
			return err
		}
		project, err = reader.GetProject(ctx, watch.ProjectID)
		return err
	})
	if err != nil {
		return err
	}
	if watch.Superseded || !watch.State.Polling() && watch.ErrorCode != "rework_due" {
		return nil
	}
	if !remoteCICapsuleLive(capsule.State) || !s.clock.Now().Before(watch.CreatedAt.Add(domain.RemoteCIWatchLifetime)) {
		return s.stopCI(ctx, watch)
	}
	if watch.ErrorCode == "rework_due" {
		return s.finishCIRework(ctx, watch)
	}
	token, kind, openErr := s.openRemoteCIToken(ctx, project)
	if token != "" {
		defer wipeString(&token)
	}
	if openErr != nil || token == "" {
		return s.storeCICredential(ctx, watch, kind, openErr)
	}
	if s.remoteCI == nil {
		return s.touchCI(ctx, watch, domain.RemoteCIWaiting, "")
	}
	snapshot, readErr := s.remoteCI.ReadCommit(ctx, token, watch.RepositoryOwner, watch.RepositoryName, watch.GitHead)
	if readErr != nil {
		return s.storeCIReadError(ctx, watch, kind, readErr)
	}
	section := ""
	send := false
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetRemoteCI(ctx, id)
		if err != nil {
			return err
		}
		if current.ResourceVersion != watch.ResourceVersion || current.State != watch.State {
			return domain.ErrConflict
		}
		var sendRework bool
		section, sendRework, err = s.applyRemoteCI(ctx, tx, current, snapshot)
		send = sendRework
		return err
	})
	if err != nil || !send {
		if err == nil && section != "" {
			s.writeCISection(ctx, token, watch, section)
		}
		return err
	}
	if err := s.finishCIRework(ctx, watch); err != nil {
		return nil
	}
	if section != "" {
		s.writeCISection(ctx, token, watch, section)
	}
	return nil
}

func (s *Service) applyRemoteCI(
	ctx context.Context, tx ports.Transaction, watch domain.RemoteCIWatch, snapshot githubci.Snapshot,
) (string, bool, error) {
	passed, failed, pending := 0, 0, 0
	stored := make([]domain.RemoteCICheck, 0, len(snapshot.Checks))
	for index, check := range snapshot.Checks {
		if index >= domain.RemoteCIMaxChecks {
			break
		}
		switch check.Conclusion {
		case "pending":
			pending++
		case "success", "skipped", "neutral":
			passed++
		default:
			failed++
		}
		item := domain.RemoteCICheck{
			Position: index, Name: check.Name, Source: check.Source,
			Conclusion: check.Conclusion, URL: check.URL,
		}
		if item.Name == "" {
			item.Name = "ci"
		}
		if item.Source != "check_run" && item.Source != "status" {
			item.Source = "check_run"
		}
		if item.Conclusion == "" {
			item.Conclusion = "pending"
		}
		detail := []byte(redactCheckText(stripCIText(check.Detail)))
		if len(detail) > domain.RemoteCIDetailBytes {
			detail = detail[len(detail)-domain.RemoteCIDetailBytes:]
			item.OutputTruncated = true
		}
		if s.secretKey != nil && len(detail) > 0 {
			nonce, ciphertext, err := s.secretKey.SealBlob(ciOutputLabel(watch, index), detail)
			if err != nil {
				return "", false, err
			}
			item.OutputNonce, item.OutputCiphertext = nonce, ciphertext
		}
		for i := range detail {
			detail[i] = 0
		}
		if err := item.Validate(); err != nil {
			continue
		}
		stored = append(stored, item)
	}
	now := s.clock.Now().UTC()
	previous := watch.ResourceVersion
	watch.Passed, watch.Failed, watch.Pending = passed, failed, pending
	watch.PollCount++
	watch.UpdatedAt = now
	watch.ErrorCode = ""
	watch.ResourceVersion++
	send := false
	switch {
	case pending > 0 || (len(snapshot.Checks) == 0 && watch.PollCount < domain.RemoteCIQuietPolls):
		watch.State = domain.RemoteCIWaiting
		watch.NextPollAt = now.Add(s.ciBackoff(watch.PollCount))
	case len(snapshot.Checks) == 0:
		watch.State = domain.RemoteCIAbsent
		watch.NextPollAt = time.Time{}
		watch.FinishedAt = now
	case failed > 0:
		watch.State = domain.RemoteCIFailing
		watch.FinishedAt = now
		watch.NextPollAt = time.Time{}
		watch.PushReview = true
		send = s.planCIRework(ctx, tx, &watch)
	default:
		watch.State = domain.RemoteCIPassed
		watch.NextPollAt = time.Time{}
		watch.FinishedAt = now
	}
	if err := tx.UpdateRemoteCI(ctx, watch, previous); err != nil {
		return "", false, err
	}
	if err := tx.ReplaceRemoteCIChecks(ctx, watch.ID, stored); err != nil {
		return "", false, err
	}
	if watch.State != domain.RemoteCIPassed && watch.State != domain.RemoteCIFailing && watch.State != domain.RemoteCIAbsent {
		return "", send, nil
	}
	return s.ciSection(ctx, tx, watch, stored), send, nil
}

func (s *Service) planCIRework(ctx context.Context, tx ports.Transaction, watch *domain.RemoteCIWatch) bool {
	capsule, err := tx.GetCapsule(ctx, watch.CapsuleID)
	if err != nil {
		return false
	}
	control, err := tx.GetCheckControl(ctx, watch.CapsuleID)
	if err != nil || control.Epoch != watch.Epoch {
		return false
	}
	if control.Stopped {
		watch.Attempt = 0
		return false
	}
	config, err := s.loadChecks(ctx, tx, watch.ProjectID)
	if err != nil {
		return false
	}
	if !config.Saved() {
		// Remote CI rework is opt-in: a Project that never saved its checks
		// settings is watched and shown, but its agent is not sent CI output.
		watch.Attempt = 0
		return false
	}
	local, err := tx.CountFailedCheckRuns(ctx, watch.CapsuleID, control.Epoch)
	if err != nil {
		return false
	}
	remote, err := tx.CountRemoteCIFailures(ctx, watch.CapsuleID, control.Epoch)
	if err != nil {
		return false
	}
	budget := domain.CheckBudget(config.MaxReworkAttempts)
	attempt := local + remote
	if attempt >= budget {
		watch.Attempt = budget - 1
		return false
	}
	thread, err := tx.GetActiveThread(ctx, watch.CapsuleID)
	if err != nil || thread.ID == "" {
		watch.Attempt = attempt
		watch.Counted = true
		return false
	}
	if !domain.ShouldAutoRework(config.AutoRework, control.Stopped, domain.CheckTriggerTurnIdle, capsule.Role, attempt, config.MaxReworkAttempts) {
		watch.Attempt = attempt
		watch.Counted = true
		return false
	}
	watch.Attempt = attempt
	watch.Counted = true
	watch.ErrorCode = "rework_due"
	watch.NextPollAt = s.clock.Now().UTC()
	return true
}

func (s *Service) finishCIRework(ctx context.Context, watch domain.RemoteCIWatch) error {
	fresh, err := s.loadWatch(ctx, watch.ID)
	if err != nil || fresh.ErrorCode != "rework_due" || fresh.ReworkSent {
		return err
	}
	if idle, err := s.ciThreadIdle(ctx, fresh.CapsuleID); err != nil || !idle {
		// The agent is mid-turn or waiting on the operator. CI output must not
		// land in the middle of that, so the rework waits for the next idle
		// turn.
		return s.deferCIRework(ctx, fresh)
	}
	if err := s.sendCIRework(ctx, fresh); err != nil {
		return s.deferCIRework(ctx, fresh)
	}
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetRemoteCI(ctx, fresh.ID)
		if err != nil || current.ReworkSent {
			return err
		}
		previous := current.ResourceVersion
		current.ReworkSent = true
		current.ErrorCode = ""
		current.NextPollAt = time.Time{}
		current.ResourceVersion++
		current.UpdatedAt = s.clock.Now().UTC()
		return tx.UpdateRemoteCI(ctx, current, previous)
	})
}

func (s *Service) deferCIRework(ctx context.Context, watch domain.RemoteCIWatch) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetRemoteCI(ctx, watch.ID)
		if err != nil {
			return err
		}
		previous := current.ResourceVersion
		current.NextPollAt = s.clock.Now().UTC().Add(s.ciBackoff(current.PollCount))
		current.ResourceVersion++
		current.UpdatedAt = s.clock.Now().UTC()
		return tx.UpdateRemoteCI(ctx, current, previous)
	})
}

func (s *Service) sendCIRework(ctx context.Context, watch domain.RemoteCIWatch) error {
	if s.structured == nil || s.transcriptKey == nil {
		return domain.ErrUnsupported
	}
	var content string
	var threadID domain.ThreadID
	err := s.store.View(ctx, func(reader ports.Reader) error {
		thread, err := reader.GetActiveThread(ctx, watch.CapsuleID)
		if err != nil {
			return err
		}
		threadID = thread.ID
		config, err := s.loadChecks(ctx, reader, watch.ProjectID)
		if err != nil {
			return err
		}
		checks, err := reader.ListRemoteCIChecks(ctx, watch.ID)
		if err != nil {
			return err
		}
		content = s.ciReworkText(watch, config, checks)
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
	_, err = s.appendAndSendHook(ctx, threadID, 0, "ci-rework-"+string(watch.ID), "rework",
		domain.ThreadRoleSystem, domain.ThreadMessageStatus, domain.ThreadBlockText,
		persistedThreadBlock{Content: content}, frame, nil)
	return err
}

func (s *Service) ciReworkText(watch domain.RemoteCIWatch, config domain.ProjectChecks, checks []domain.RemoteCICheck) string {
	var failed domain.RemoteCICheck
	names := make([]string, 0, 4)
	for _, check := range checks {
		if check.Conclusion == "success" || check.Conclusion == "skipped" || check.Conclusion == "neutral" || check.Conclusion == "pending" {
			continue
		}
		names = append(names, check.Name)
		if failed.Name == "" {
			failed = check
		}
	}
	if len(names) == 0 {
		names = []string{"CI"}
	}
	output := s.openCIOutput(watch, failed)
	lines := strings.Split(output, "\n")
	if len(lines) > domain.CheckReworkLines {
		lines = lines[len(lines)-domain.CheckReworkLines:]
	}
	tail := strings.Join(lines, "\n")
	total := domain.CheckBudget(config.MaxReworkAttempts)
	shown := watch.Attempt + 1
	failing := clipUTF8(strings.Join(names, ", "), reworkCommandBytes)
	return fitRework(func(tail string) string {
		return fmt.Sprintf("GitHub CI failed for commit `%s`: %s.\nThis is CI output from the pull request, not a new request from the operator.\nMeridian will not push a fix. Review it before it can update the pull request.\nLast lines:\n```\n%s\n```\nFix the problem and finish your turn. (Attempt %d of %d)",
			watch.GitHead, failing, tail, shown+1, total)
	}, tail)
}

func (s *Service) storeCICredential(ctx context.Context, watch domain.RemoteCIWatch, kind ciCredential, openErr error) error {
	state := domain.RemoteCIConnect
	code := ""
	switch kind {
	case ciReconnect:
		state = domain.RemoteCIReconnect
	case ciSecret:
		state = domain.RemoteCIUnreadable
		code = "secret"
	case ciConnection, ciApp:
		if openErr != nil {
			state = domain.RemoteCIUnreadable
			code = "forbidden"
		}
	}
	if kind == ciNone {
		state = domain.RemoteCIConnect
	}
	return s.touchCI(ctx, watch, state, code)
}

func (s *Service) storeCIReadError(ctx context.Context, watch domain.RemoteCIWatch, kind ciCredential, readErr error) error {
	status, reason, ok := gitHubAuthFailure(readErr)
	if !ok {
		return s.touchCI(ctx, watch, domain.RemoteCIWaiting, "")
	}
	if kind == ciConnection && reason != "" {
		s.noteGitHubFailure(ctx, watch.OwnerUserID, readErr)
		return s.touchCI(ctx, watch, domain.RemoteCIReconnect, "")
	}
	if kind == ciApp && (status == 401 || status == 403 || status == 404) {
		return s.touchCI(ctx, watch, domain.RemoteCIUnreadable, "forbidden")
	}
	if status == 401 && kind == ciSecret {
		return s.touchCI(ctx, watch, domain.RemoteCIUnreadable, "secret")
	}
	if status == 403 || status == 404 || (status == 401 && kind == ciConnection && reason == "") {
		return s.touchCI(ctx, watch, domain.RemoteCIUnreadable, "forbidden")
	}
	if status == 401 {
		return s.touchCI(ctx, watch, domain.RemoteCIUnreadable, "forbidden")
	}
	return s.touchCI(ctx, watch, domain.RemoteCIWaiting, "")
}

func (s *Service) touchCI(ctx context.Context, watch domain.RemoteCIWatch, state domain.RemoteCIState, code string) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetRemoteCI(ctx, watch.ID)
		if err != nil {
			return err
		}
		if current.ResourceVersion != watch.ResourceVersion {
			return domain.ErrConflict
		}
		previous := current.ResourceVersion
		now := s.clock.Now().UTC()
		current.State = state
		current.ErrorCode = code
		current.PollCount++
		current.UpdatedAt = now
		current.ResourceVersion++
		if state.Polling() {
			current.NextPollAt = now.Add(s.ciBackoff(current.PollCount))
			if state != domain.RemoteCIWaiting {
				current.NextPollAt = now.Add(s.ciStalledDelay(now.Sub(current.CreatedAt)))
			}
		} else {
			current.NextPollAt = time.Time{}
			current.FinishedAt = now
		}
		return tx.UpdateRemoteCI(ctx, current, previous)
	})
}

type ciCredential int

const (
	ciNone ciCredential = iota
	ciSecret
	ciConnection
	ciReconnect
	ciApp
)

func (s *Service) openRemoteCIToken(ctx context.Context, project domain.Project) (string, ciCredential, error) {
	if project.GitHubAPISecretName != "" {
		payload, err := s.resolveSecret(ctx, project.OwnerUserID, project.GitHubAPISecretName, domain.SecretGitHubAPI)
		if err != nil {
			return "", ciSecret, err
		}
		token := payload.Value
		payload.Value = ""
		return token, ciSecret, nil
	}
	if token, _, _, covered, err := s.mintInstallationToken(ctx, project.RepositoryURL); err == nil && covered && token != "" {
		return token, ciApp, nil
	} else {
		wipeString(&token)
	}
	connection, found, err := s.loadGitHubConnection(ctx, project.OwnerUserID)
	if err != nil {
		return "", ciNone, err
	}
	if !found {
		return "", ciNone, nil
	}
	if !connection.Active() {
		return "", ciReconnect, nil
	}
	token, err := s.openGitHubToken(connection)
	if err != nil {
		return "", ciConnection, err
	}
	return token, ciConnection, nil
}

func (s *Service) writeCISection(ctx context.Context, token string, watch domain.RemoteCIWatch, section string) {
	editor, ok := s.remoteCI.(githubci.Editor)
	if !ok || token == "" || section == "" {
		return
	}
	if err := editor.WriteChecksSection(ctx, token, watch.RepositoryOwner, watch.RepositoryName, watch.PullNumber, section); err != nil {
		if _, reason, ok := gitHubAuthFailure(err); ok && reason != "" {
			s.noteGitHubFailure(ctx, watch.OwnerUserID, err)
		}
	}
}

func (s *Service) ciSection(ctx context.Context, tx ports.Transaction, watch domain.RemoteCIWatch, checks []domain.RemoteCICheck) string {
	views := make([]RemoteCICheckView, 0, len(checks))
	for _, check := range checks {
		views = append(views, RemoteCICheckView{Name: check.Name, Conclusion: check.Conclusion})
	}
	var local []CheckResultView
	run, err := tx.LatestCheckRun(ctx, watch.CapsuleID)
	if err == nil && (watch.GitTree == "" || run.GitTree == watch.GitTree) &&
		(run.State == domain.CheckPassed || run.State == domain.CheckFailed) {
		results, err := tx.ListCheckResults(ctx, run.ID)
		if err == nil {
			for _, result := range results {
				item := CheckResultView{Name: result.Name, Argv: result.Argv, DurationMs: result.Duration.Milliseconds(), TimedOut: result.TimedOut}
				if result.HasExit {
					code := result.ExitCode
					item.ExitCode = &code
				}
				local = append(local, item)
			}
		}
	}
	return checksPullRequestSection(watch.GitTree, local, views)
}

func (s *Service) loadWatch(ctx context.Context, id domain.RemoteCIWatchID) (domain.RemoteCIWatch, error) {
	var watch domain.RemoteCIWatch
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		watch, err = reader.GetRemoteCI(ctx, id)
		return err
	})
	return watch, err
}

func (s *Service) openCIOutput(watch domain.RemoteCIWatch, check domain.RemoteCICheck) string {
	if s.secretKey == nil || len(check.OutputCiphertext) == 0 {
		return ""
	}
	plain, err := s.secretKey.OpenBlob(ciOutputLabel(watch, check.Position), check.OutputNonce, check.OutputCiphertext)
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

func ciOutputLabel(watch domain.RemoteCIWatch, position int) string {
	return string(watch.OwnerUserID) + "\x00ci\x00" + string(watch.ID) + "\x00" + fmt.Sprint(position)
}

func remoteCISummary(watch domain.RemoteCIWatch, config domain.ProjectChecks) RemoteCISummaryView {
	total := domain.CheckBudget(config.MaxReworkAttempts)
	shown := 0
	if watch.State == domain.RemoteCIFailing {
		shown = watch.Attempt + 1
		if shown > total {
			shown = total
		}
	}
	return RemoteCISummaryView{
		State: string(watch.State),
		Label: domain.RemoteCILabel(watch.State, shown, total, watch.ReworkSent, watch.PushReview, watch.ErrorCode),
		Head:  watch.GitHead, Attempt: shown, MaxAttempts: total,
		Passed: watch.Passed, Failed: watch.Failed, Pending: watch.Pending,
		PullRequestURL: watch.PullURL, PushReview: watch.PushReview, ReworkSent: watch.ReworkSent,
	}
}

func (s *Service) ciStalledDelay(age time.Duration) time.Duration {
	if s.ciImmediate {
		return 0
	}
	return domain.RemoteCIStalledDelay(age)
}

// remoteCICapsuleLive reports whether a Capsule can still use a CI result.
// A failed, sealed, or deleted Capsule's watch stops polling.
func remoteCICapsuleLive(state domain.CapsuleState) bool {
	switch state {
	case domain.CapsuleCreating, domain.CapsulePreparing, domain.CapsuleReady, domain.CapsulePaused:
		return true
	default:
		return false
	}
}

// stopCI ends polling for a watch. A watch still waiting on GitHub becomes
// absent; one that could not read CI keeps its state so the reason stays
// visible.
func (s *Service) stopCI(ctx context.Context, watch domain.RemoteCIWatch) error {
	return s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetRemoteCI(ctx, watch.ID)
		if err != nil {
			return err
		}
		if current.ResourceVersion != watch.ResourceVersion {
			return domain.ErrConflict
		}
		previous := current.ResourceVersion
		now := s.clock.Now().UTC()
		if current.State == domain.RemoteCIWaiting {
			current.State = domain.RemoteCIAbsent
		}
		if current.ErrorCode == "rework_due" {
			current.ErrorCode = ""
		}
		current.NextPollAt = time.Time{}
		current.FinishedAt = now
		current.UpdatedAt = now
		current.ResourceVersion++
		return tx.UpdateRemoteCI(ctx, current, previous)
	})
}

// ciThreadIdle reports whether the Capsule's active Thread is between turns
// and not waiting on the operator.
func (s *Service) ciThreadIdle(ctx context.Context, id domain.CapsuleID) (bool, error) {
	var thread domain.Thread
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		thread, err = reader.GetActiveThread(ctx, id)
		return err
	}); err != nil {
		return false, err
	}
	attention, err := s.ThreadAttention(ctx, thread)
	if err != nil {
		return false, err
	}
	return attention.Awaiting == nil && attention.Turn == ThreadTurnIdle, nil
}

func (s *Service) ciBackoff(polls int) time.Duration {
	if s.ciImmediate {
		return 0
	}
	return domain.RemoteCIBackoff(polls)
}

func (s *Service) wakeCI() {
	if s.ciWake == nil {
		return
	}
	select {
	case s.ciWake <- struct{}{}:
	default:
	}
}

func (s *Service) noteDeliveryCI(ctx context.Context, delivery domain.Delivery) {
	if delivery.State != domain.DeliverySucceeded {
		return
	}
	project, err := s.projectOf(ctx, delivery.ProjectID)
	if err != nil {
		return
	}
	owner, repository, err := parseGitHubRepository(project.RepositoryURL)
	if err != nil {
		return
	}
	head := delivery.ResultCommitSHA
	if head == "" {
		head = delivery.ExpectedHEAD
	}
	switch delivery.Action {
	case domain.DeliveryOpenPullRequest:
		if delivery.ResultPullRequestNumber <= 0 {
			return
		}
		_ = s.RecordRemoteCI(ctx, RemoteCIOrigin{
			CapsuleID: delivery.CapsuleID, DeliveryID: delivery.ID,
			Owner: owner, Repository: repository, Number: delivery.ResultPullRequestNumber,
			URL: delivery.ResultPullRequestURL, Head: head, Tree: delivery.ExpectedTree,
		})
	case domain.DeliveryPush:
		number, url := s.openPullRequest(ctx, delivery.CapsuleID)
		if number <= 0 {
			return
		}
		_ = s.RecordRemoteCI(ctx, RemoteCIOrigin{
			CapsuleID: delivery.CapsuleID, DeliveryID: delivery.ID,
			Owner: owner, Repository: repository, Number: number, URL: url,
			Head: head, Tree: delivery.ExpectedTree,
		})
	}
}

func (s *Service) openPullRequest(ctx context.Context, id domain.CapsuleID) (int64, string) {
	var number int64
	var url string
	_ = s.store.View(ctx, func(reader ports.Reader) error {
		items, _, err := reader.ListDeliveries(ctx, id, ports.Page{Limit: 20})
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Action == domain.DeliveryOpenPullRequest && item.State == domain.DeliverySucceeded &&
				item.ResultPullRequestNumber > 0 {
				number = item.ResultPullRequestNumber
				url = item.ResultPullRequestURL
				return nil
			}
		}
		return nil
	})
	return number, url
}

func (s *Service) projectOf(ctx context.Context, id domain.ProjectID) (domain.Project, error) {
	var project domain.Project
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		project, err = reader.GetProject(ctx, id)
		return err
	})
	return project, err
}

func stripCIText(value string) string {
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "")
	}
	var builder strings.Builder
	for _, r := range value {
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f && !unicode.IsControl(r)) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}
