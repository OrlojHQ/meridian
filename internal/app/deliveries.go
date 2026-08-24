package app

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"net/url"
	"path"
	"strings"

	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
)

type CreateDeliveryInput struct {
	CapsuleID              domain.CapsuleID
	Action                 domain.DeliveryAction
	Approved               bool
	ExpectedCapsuleVersion domain.ResourceVersion
	ExpectedHEAD           string
	ExpectedTree           string
	RemoteBranch           string
	CommitMessage          string
	PullRequestTitle       string
	PullRequestBody        string
	BaseBranch             string
	IdempotencyKey         string
}

type DeliveryPage struct {
	Items      []domain.Delivery
	NextOffset int
}

func (s *Service) InspectDelivery(
	ctx context.Context, capsuleID domain.CapsuleID,
) (ports.DeliveryInspection, error) {
	if s.delivery == nil {
		return ports.DeliveryInspection{}, domain.ErrUnsupported
	}
	capsule, err := s.readyCapsule(ctx, capsuleID)
	if err != nil {
		return ports.DeliveryInspection{}, err
	}
	result, err := s.delivery.InspectDelivery(ctx, capsule.ProviderResourceID)
	if err != nil {
		return ports.DeliveryInspection{}, safeDeliveryError(err)
	}
	if err := s.TouchCapsuleActivity(ctx, capsuleID); err != nil {
		return ports.DeliveryInspection{}, err
	}
	current, err := s.GetCapsule(ctx, capsuleID)
	if err != nil {
		return ports.DeliveryInspection{}, err
	}
	result.CapsuleResourceVersion = current.ResourceVersion
	return result, nil
}

func (s *Service) CreateDelivery(
	ctx context.Context, input CreateDeliveryInput,
) (domain.Delivery, error) {
	if s.delivery == nil {
		return domain.Delivery{}, domain.ErrUnsupported
	}
	if err := validateCreateDelivery(input); err != nil {
		return domain.Delivery{}, err
	}
	scope := "delivery:create:" + string(input.CapsuleID)
	var replay domain.Delivery
	var replayed bool
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		replay, replayed, err = getReplay[domain.Delivery](
			ctx, reader, scope, input.IdempotencyKey,
		)
		if err == nil && replayed {
			replay, err = reader.GetDelivery(ctx, replay.ID)
		}
		return err
	}); err != nil {
		return domain.Delivery{}, err
	}
	if replayed {
		return replay, nil
	}

	lock := &s.deliverySync[deliveryLockIndex(input.CapsuleID)]
	lock.Lock()
	defer lock.Unlock()

	capsule, project, err := s.acquireMaintenance(
		ctx, input.CapsuleID, input.ExpectedCapsuleVersion, "capture",
	)
	if err != nil {
		return domain.Delivery{}, err
	}
	keepMaintenance := false
	defer func() {
		if !keepMaintenance {
			_ = s.clearMaintenance(context.Background(), input.CapsuleID, "capture")
		}
	}()
	if err := validateDeliveryProject(project, input.Action); err != nil {
		return domain.Delivery{}, err
	}
	if err := s.validateDeliverySecrets(ctx, project, input.Action); err != nil {
		return domain.Delivery{}, err
	}
	inspection, err := s.delivery.InspectDelivery(ctx, capsule.ProviderResourceID)
	if err != nil {
		return domain.Delivery{}, safeDeliveryError(err)
	}
	if inspection.HEAD != input.ExpectedHEAD || inspection.Tree != input.ExpectedTree {
		return domain.Delivery{}, fmt.Errorf("%w: reviewed Git objects changed", domain.ErrConflict)
	}
	if !sameRepository(project.RepositoryURL, inspection.OriginURL) {
		return domain.Delivery{}, fmt.Errorf("%w: Capsule origin does not match Project repository", domain.ErrConflict)
	}
	if inspection.Dirty && input.CommitMessage == "" {
		return domain.Delivery{}, fmt.Errorf("%w: a dirty tree requires a commit message", domain.ErrIllegalTransition)
	}
	if inspection.Dirty && (project.CommitAuthorName == "" || project.CommitAuthorEmail == "") {
		return domain.Delivery{}, fmt.Errorf("%w: Project commit author is required", domain.ErrIllegalTransition)
	}
	if !inspection.Dirty && input.CommitMessage != "" {
		return domain.Delivery{}, fmt.Errorf("%w: a clean tree cannot be committed", domain.ErrIllegalTransition)
	}
	base := input.BaseBranch
	if base == "" {
		base = project.DefaultBaseBranch
	}
	if base == "" {
		base = inspection.DefaultBranch
	}
	if input.Action == domain.DeliveryOpenPullRequest {
		if base == "" || !validBranch(base) || base == input.RemoteBranch {
			return domain.Delivery{}, fmt.Errorf("%w: a distinct valid PR base branch is required", domain.ErrInvalid)
		}
		if _, _, err := parseGitHubRepository(project.RepositoryURL); err != nil {
			return domain.Delivery{}, fmt.Errorf("%w: open_pull_request requires a github.com repository", domain.ErrIllegalTransition)
		}
	}
	if refusedDeliveryBranch(input.RemoteBranch, project.DefaultBaseBranch, inspection.DefaultBranch) {
		return domain.Delivery{}, fmt.Errorf("%w: delivery to a protected/default branch is refused", domain.ErrIllegalTransition)
	}
	now := s.clock.Now().UTC()
	result := domain.Delivery{
		ID: domain.DeliveryID(s.ids.NewID()), CapsuleID: capsule.ID, ProjectID: project.ID,
		State: domain.DeliveryQueued, Action: input.Action, Approved: true, ApprovedAt: now,
		ExpectedCapsuleVersion: input.ExpectedCapsuleVersion,
		ExpectedHEAD:           input.ExpectedHEAD, ExpectedTree: input.ExpectedTree,
		RemoteBranch: input.RemoteBranch, DestinationRef: "refs/heads/" + input.RemoteBranch,
		BaseBranch: base, CommitMessage: input.CommitMessage,
		PullRequestTitle: input.PullRequestTitle, PullRequestBody: input.PullRequestBody,
		IdempotencyKey: input.IdempotencyKey, CreatedAt: now, UpdatedAt: now,
		ResourceVersion: 1,
	}
	if err := result.Validate(); err != nil {
		return domain.Delivery{}, err
	}
	err = s.store.Transact(ctx, func(tx ports.Transaction) error {
		current, err := tx.GetCapsule(ctx, capsule.ID)
		if err != nil {
			return err
		}
		if current.Maintenance != "capture" || current.State != domain.CapsuleReady {
			return domain.ErrConflict
		}
		if existing, ok, err := getReplay[domain.Delivery](
			ctx, tx, scope, input.IdempotencyKey,
		); err != nil {
			return err
		} else if ok {
			result, err = tx.GetDelivery(ctx, existing.ID)
			return err
		}
		if err := tx.InsertDelivery(ctx, result); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "delivery", string(result.ID),
			"delivery.queued", result.ResourceVersion, nil); err != nil {
			return err
		}
		return putReplay(ctx, tx, scope, input.IdempotencyKey, result, now)
	})
	if err != nil {
		return domain.Delivery{}, err
	}
	keepMaintenance = true
	// The durable Delivery owns its coordinator lifetime once persisted. Client
	// disconnects must not strand an ambiguous commit or push.
	return s.executeDelivery(context.WithoutCancel(ctx), result.ID)
}

func (s *Service) GetDelivery(
	ctx context.Context, id domain.DeliveryID,
) (domain.Delivery, error) {
	var result domain.Delivery
	err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		result, err = reader.GetDelivery(ctx, id)
		return err
	})
	return result, err
}

func (s *Service) ListDeliveries(
	ctx context.Context, capsuleID domain.CapsuleID, offset, limit int,
) (DeliveryPage, error) {
	page := normalizePage(offset, limit)
	var result DeliveryPage
	err := s.store.View(ctx, func(reader ports.Reader) error {
		if _, err := reader.GetCapsule(ctx, capsuleID); err != nil {
			return err
		}
		var more bool
		var err error
		result.Items, more, err = reader.ListDeliveries(ctx, capsuleID, page)
		if more {
			result.NextOffset = page.Offset + len(result.Items)
		}
		return err
	})
	return result, err
}

func (s *Service) RecoverDeliveries(ctx context.Context) error {
	if s.delivery == nil {
		return nil
	}
	var items []domain.Delivery
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		items, err = reader.ListRecoverableDeliveries(ctx)
		return err
	}); err != nil {
		return err
	}
	for _, item := range items {
		lock := &s.deliverySync[deliveryLockIndex(item.CapsuleID)]
		lock.Lock()
		_, err := s.executeDelivery(ctx, item.ID)
		lock.Unlock()
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) executeDelivery(
	ctx context.Context, id domain.DeliveryID,
) (result domain.Delivery, returnedErr error) {
	result, err := s.GetDelivery(ctx, id)
	if err != nil || result.State.Terminal() {
		return result, err
	}
	var capsule domain.Capsule
	var project domain.Project
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		capsule, err = reader.GetCapsule(ctx, result.CapsuleID)
		if err == nil {
			project, err = reader.GetProject(ctx, result.ProjectID)
		}
		return err
	}); err != nil {
		return result, err
	}
	defer func() {
		_ = s.clearMaintenance(context.Background(), result.CapsuleID, "capture")
		_ = s.TouchCapsuleActivity(context.Background(), result.CapsuleID)
	}()
	fail := func(cause error) (domain.Delivery, error) {
		message := boundedDeliveryFailure(cause)
		updated, updateErr := s.updateDelivery(ctx, result.ID, func(item *domain.Delivery) error {
			item.Failure = message
			return item.Transition(domain.DeliveryFailed, s.clock.Now())
		})
		if updateErr != nil {
			return result, errors.Join(cause, updateErr)
		}
		result = updated
		return result, safeDeliveryError(cause)
	}
	inspection, err := s.delivery.InspectDelivery(ctx, capsule.ProviderResourceID)
	if err != nil {
		return fail(err)
	}
	if !sameRepository(project.RepositoryURL, inspection.OriginURL) ||
		inspection.Tree != result.ExpectedTree {
		return fail(fmt.Errorf("%w: Delivery Git precondition changed", domain.ErrConflict))
	}
	sourceCommit := result.ResultCommitSHA
	if result.CommitMessage != "" && sourceCommit == "" {
		if inspection.HEAD == result.ExpectedHEAD {
			result, err = s.transitionDelivery(ctx, result.ID, domain.DeliveryCommitting)
			if err != nil {
				return result, err
			}
			committed, commitErr := s.delivery.CommitDelivery(ctx, ports.DeliveryCommitRequest{
				ResourceID: capsule.ProviderResourceID, Message: result.CommitMessage,
				AuthorName: project.CommitAuthorName, AuthorEmail: project.CommitAuthorEmail,
				ExpectedHEAD: result.ExpectedHEAD, ExpectedTree: result.ExpectedTree,
			})
			if commitErr != nil {
				return fail(commitErr)
			}
			if !validObjectID(committed.Commit) || committed.Tree != result.ExpectedTree {
				return fail(fmt.Errorf("%w: Delivery commit result diverged", domain.ErrConflict))
			}
			sourceCommit = committed.Commit
		} else if result.State == domain.DeliveryCommitting && validObjectID(inspection.HEAD) {
			// Commit may have completed before a daemon crash. The reviewed tree
			// remains exact, and maintenance prevents another control-plane write.
			sourceCommit = inspection.HEAD
		} else {
			return fail(fmt.Errorf("%w: Delivery HEAD precondition changed", domain.ErrConflict))
		}
		result, err = s.updateDelivery(ctx, result.ID, func(item *domain.Delivery) error {
			item.ResultCommitSHA = sourceCommit
			return item.Transition(domain.DeliveryPushing, s.clock.Now())
		})
		if err != nil {
			return result, err
		}
	} else {
		if sourceCommit == "" {
			if inspection.HEAD != result.ExpectedHEAD {
				return fail(fmt.Errorf("%w: Delivery HEAD precondition changed", domain.ErrConflict))
			}
			sourceCommit = result.ExpectedHEAD
			result, err = s.updateDelivery(ctx, result.ID, func(item *domain.Delivery) error {
				item.ResultCommitSHA = sourceCommit
				return item.Transition(domain.DeliveryPushing, s.clock.Now())
			})
			if err != nil {
				return result, err
			}
		} else if result.State != domain.DeliveryPushing &&
			result.State != domain.DeliveryOpeningPR {
			return fail(fmt.Errorf("%w: invalid Delivery recovery state", domain.ErrConflict))
		}
	}

	if result.State == domain.DeliveryPushing {
		payload, err := s.resolveSecret(ctx, project.GitPushSecretName, domain.SecretGitPush)
		if err != nil {
			return fail(err)
		}
		credential := ports.GitHTTPSCredential{
			Username: payload.Username, Password: payload.Password,
		}
		payload.Username, payload.Password, payload.Value = "", "", ""
		expectedOldRef := ""
		pushed, pushErr := s.delivery.PushDelivery(ctx, ports.DeliveryPushRequest{
			ResourceID: capsule.ProviderResourceID, SourceCommit: sourceCommit,
			DestinationRef: result.DestinationRef, ExpectedOldRef: &expectedOldRef,
			GitCredential: credential,
		})
		credential.Username, credential.Password = "", ""
		if pushErr != nil {
			return fail(pushErr)
		}
		if pushed.Commit != sourceCommit || pushed.DestinationRef != result.DestinationRef {
			return fail(fmt.Errorf("%w: Delivery push result diverged", domain.ErrConflict))
		}
		if result.Action == domain.DeliveryPush {
			result, err = s.transitionDelivery(ctx, result.ID, domain.DeliverySucceeded)
			return result, err
		}
		result, err = s.transitionDelivery(ctx, result.ID, domain.DeliveryOpeningPR)
		if err != nil {
			return result, err
		}
	}
	if result.State == domain.DeliveryOpeningPR {
		if s.github == nil {
			return fail(domain.ErrUnsupported)
		}
		owner, repository, err := parseGitHubRepository(project.RepositoryURL)
		if err != nil {
			return fail(err)
		}
		payload, err := s.resolveSecret(
			ctx, project.GitHubAPISecretName, domain.SecretGitHubAPI,
		)
		if err != nil {
			return fail(err)
		}
		token := payload.Value
		payload.Value, payload.Username, payload.Password = "", "", ""
		existing, findErr := s.github.FindOpenPullRequest(
			ctx, token, owner, repository, result.RemoteBranch, result.BaseBranch,
		)
		if findErr != nil {
			token = ""
			return fail(findErr)
		}
		var pullRequest PullRequest
		if existing != nil {
			if existing.HeadSHA != sourceCommit {
				token = ""
				return fail(fmt.Errorf("%w: existing pull request has divergent head", domain.ErrConflict))
			}
			pullRequest = *existing
		} else {
			pullRequest, err = s.github.CreatePullRequest(ctx, token, CreatePullRequestInput{
				Owner: owner, Repository: repository, Head: result.RemoteBranch,
				Base: result.BaseBranch, Title: result.PullRequestTitle,
				Body: result.PullRequestBody,
			})
			if err != nil {
				token = ""
				return fail(err)
			}
		}
		if pullRequest.HeadSHA != sourceCommit {
			token = ""
			return fail(fmt.Errorf("%w: pull request head diverged", domain.ErrConflict))
		}
		token = ""
		result, err = s.updateDelivery(ctx, result.ID, func(item *domain.Delivery) error {
			item.ResultPullRequestURL = pullRequest.URL
			item.ResultPullRequestNumber = pullRequest.Number
			return item.Transition(domain.DeliverySucceeded, s.clock.Now())
		})
		return result, err
	}
	return result, nil
}

func (s *Service) transitionDelivery(
	ctx context.Context, id domain.DeliveryID, state domain.DeliveryState,
) (domain.Delivery, error) {
	return s.updateDelivery(ctx, id, func(item *domain.Delivery) error {
		return item.Transition(state, s.clock.Now())
	})
}

func (s *Service) updateDelivery(
	ctx context.Context,
	id domain.DeliveryID,
	change func(*domain.Delivery) error,
) (domain.Delivery, error) {
	var result domain.Delivery
	err := s.store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		result, err = tx.GetDelivery(ctx, id)
		if err != nil {
			return err
		}
		previous := result.ResourceVersion
		if err := change(&result); err != nil {
			return err
		}
		if err := tx.UpdateDelivery(ctx, result, previous); err != nil {
			return err
		}
		return s.appendEvent(ctx, tx, "delivery", string(result.ID),
			"delivery."+string(result.State), result.ResourceVersion, nil)
	})
	return result, err
}

func validateCreateDelivery(input CreateDeliveryInput) error {
	if !input.Approved {
		return fmt.Errorf("%w: explicit approval is required", domain.ErrInvalid)
	}
	if !input.Action.Valid() || input.ExpectedCapsuleVersion <= 0 ||
		!validObjectID(input.ExpectedHEAD) || !validObjectID(input.ExpectedTree) ||
		!validBranch(input.RemoteBranch) {
		return fmt.Errorf("%w: invalid Delivery request", domain.ErrInvalid)
	}
	if err := requireIdempotency(input.IdempotencyKey); err != nil {
		return err
	}
	if len(input.CommitMessage) > 16<<10 || strings.ContainsRune(input.CommitMessage, '\x00') ||
		len(input.PullRequestTitle) > 512 || strings.ContainsAny(input.PullRequestTitle, "\x00\r\n") ||
		len(input.PullRequestBody) > 64<<10 || strings.ContainsRune(input.PullRequestBody, '\x00') ||
		len(input.BaseBranch) > 255 || input.BaseBranch != "" && !validBranch(input.BaseBranch) {
		return fmt.Errorf("%w: invalid Delivery metadata", domain.ErrInvalid)
	}
	if input.Action == domain.DeliveryOpenPullRequest && input.PullRequestTitle == "" {
		return fmt.Errorf("%w: pull request title is required", domain.ErrInvalid)
	}
	if input.Action == domain.DeliveryPush &&
		(input.PullRequestTitle != "" || input.PullRequestBody != "" || input.BaseBranch != "") {
		return fmt.Errorf("%w: push action cannot include pull request metadata", domain.ErrInvalid)
	}
	return nil
}

func validateDeliveryProject(project domain.Project, action domain.DeliveryAction) error {
	if project.RepositoryURL == "" || project.GitPushSecretName == "" {
		return fmt.Errorf("%w: Project delivery repository and git_push secret are required", domain.ErrIllegalTransition)
	}
	repository, err := url.Parse(project.RepositoryURL)
	if err != nil || repository.Scheme != "https" || repository.Host == "" ||
		repository.User != nil || repository.RawQuery != "" || repository.Fragment != "" {
		return fmt.Errorf("%w: Delivery requires a credential-free HTTPS repository", domain.ErrIllegalTransition)
	}
	if action == domain.DeliveryOpenPullRequest && project.GitHubAPISecretName == "" {
		return fmt.Errorf("%w: Project github_api secret is required", domain.ErrIllegalTransition)
	}
	return nil
}

func (s *Service) validateDeliverySecrets(
	ctx context.Context, project domain.Project, action domain.DeliveryAction,
) error {
	return s.store.View(ctx, func(reader ports.Reader) error {
		push, err := reader.GetSecret(ctx, project.GitPushSecretName)
		if err != nil {
			return err
		}
		if push.Purpose != domain.SecretGitPush {
			return fmt.Errorf("%w: git push secret purpose mismatch", domain.ErrInvalid)
		}
		if action != domain.DeliveryOpenPullRequest {
			return nil
		}
		github, err := reader.GetSecret(ctx, project.GitHubAPISecretName)
		if err != nil {
			return err
		}
		if github.Purpose != domain.SecretGitHubAPI {
			return fmt.Errorf("%w: GitHub API secret purpose mismatch", domain.ErrInvalid)
		}
		return nil
	})
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && (len(decoded) == 20 || len(decoded) == 32)
}

func validBranch(value string) bool {
	if value == "" || len(value) > 255 || strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasSuffix(value, ".") || strings.Contains(value, "..") ||
		strings.Contains(value, "//") || strings.Contains(value, "@{") ||
		strings.ContainsAny(value, " ~^:?*[\\\x00\r\n") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." ||
			strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func refusedDeliveryBranch(branch string, configuredDefaults ...string) bool {
	switch strings.ToLower(branch) {
	case "main", "master", "trunk":
		return true
	}
	for _, value := range configuredDefaults {
		if value != "" && branch == value {
			return true
		}
	}
	return false
}

func sameRepository(first, second string) bool {
	a, errA := canonicalRepository(first)
	b, errB := canonicalRepository(second)
	return errA == nil && errB == nil && a == b
}

func canonicalRepository(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return "", errors.New("unsupported repository URL")
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port != "" && !((parsed.Scheme == "https" && port == "443") ||
		(parsed.Scheme == "http" && port == "80")) {
		host += ":" + port
	}
	repositoryPath := strings.TrimSuffix(strings.Trim(path.Clean(parsed.Path), "/"), ".git")
	if repositoryPath == "" || repositoryPath == "." || repositoryPath == ".." {
		return "", errors.New("invalid repository path")
	}
	if host == "github.com" {
		repositoryPath = strings.ToLower(repositoryPath)
	}
	return host + "/" + strings.TrimPrefix(repositoryPath, "/"), nil
}

func boundedDeliveryFailure(err error) string {
	message := "delivery operation failed"
	switch {
	case errors.Is(err, domain.ErrConflict):
		message = "delivery precondition conflict"
	case errors.Is(err, domain.ErrInvalid):
		message = "delivery configuration invalid"
	case errors.Is(err, domain.ErrUnsupported):
		message = "delivery operation unsupported"
	case errors.Is(err, context.Canceled):
		message = "delivery operation canceled"
	case errors.Is(err, context.DeadlineExceeded):
		message = "delivery operation timed out"
	}
	if len(message) > 512 {
		return message[:512]
	}
	return message
}

func safeDeliveryError(err error) error {
	message := boundedDeliveryFailure(err)
	switch {
	case errors.Is(err, domain.ErrConflict):
		return fmt.Errorf("%w: %s", domain.ErrConflict, message)
	case errors.Is(err, domain.ErrInvalid):
		return fmt.Errorf("%w: %s", domain.ErrInvalid, message)
	case errors.Is(err, domain.ErrUnsupported):
		return fmt.Errorf("%w: %s", domain.ErrUnsupported, message)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%w: %s", context.Canceled, message)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %s", context.DeadlineExceeded, message)
	default:
		return errors.New(message)
	}
}

func deliveryLockIndex(id domain.CapsuleID) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(id))
	return int(hash.Sum32() % 64)
}
