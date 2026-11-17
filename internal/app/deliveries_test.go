package app_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/gitgateway"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/secrets"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

const (
	deliveryHead   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	deliveryTree   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	deliveryCommit = "cccccccccccccccccccccccccccccccccccccccc"
)

type deliveryRuntime struct {
	mu         sync.Mutex
	inspection ports.DeliveryInspection
	commits    int
	pushes     int
	push       ports.DeliveryPushRequest
	commit     ports.DeliveryCommitRequest
	pushErr    error
}

func (r *deliveryRuntime) InspectDelivery(
	context.Context, string,
) (ports.DeliveryInspection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inspection, nil
}

func (r *deliveryRuntime) CommitDelivery(
	_ context.Context, request ports.DeliveryCommitRequest,
) (ports.DeliveryCommitResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commits++
	r.commit = request
	if request.ExpectedHEAD != r.inspection.HEAD || request.ExpectedTree != r.inspection.Tree {
		return ports.DeliveryCommitResult{}, domain.ErrConflict
	}
	r.inspection.HEAD = deliveryCommit
	r.inspection.Dirty = false
	return ports.DeliveryCommitResult{Commit: deliveryCommit, Tree: deliveryTree}, nil
}

func (r *deliveryRuntime) PushDelivery(
	_ context.Context, request ports.DeliveryPushRequest,
) (ports.DeliveryPushResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushes++
	r.push = request
	if r.pushErr != nil {
		return ports.DeliveryPushResult{}, r.pushErr
	}
	return ports.DeliveryPushResult{
		Commit: request.SourceCommit, DestinationRef: request.DestinationRef,
	}, nil
}

// fakeGitGateway stands in for the Station Git gateway. It records each grant
// and answers remote-ref reads from refs.
type fakeGitGateway struct {
	mu      sync.Mutex
	refs    map[string]string
	grants  []gitgateway.Grant
	revoked int
	readErr error
}

func (g *fakeGitGateway) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNotFound)
}

func (g *fakeGitGateway) Issue(grant gitgateway.Grant) (string, func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.grants = append(g.grants, grant)
	return strings.Repeat("c", 64), func() {
		g.mu.Lock()
		g.revoked++
		g.mu.Unlock()
	}, nil
}

func (g *fakeGitGateway) RemoteRef(_ context.Context, _ string, _ gitgateway.Credential, ref string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.readErr != nil {
		return "", g.readErr
	}
	return g.refs[ref], nil
}

func (g *fakeGitGateway) lastGrant() (gitgateway.Grant, int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.grants) == 0 {
		return gitgateway.Grant{}, 0, g.revoked
	}
	return g.grants[len(g.grants)-1], len(g.grants), g.revoked
}

// configureFakeGitGateway attaches a fake gateway, as meridiand does at start.
func configureFakeGitGateway(t *testing.T, service *app.Service) *fakeGitGateway {
	t.Helper()
	gateway := &fakeGitGateway{refs: map[string]string{}}
	if err := service.ConfigureGitGateway(gateway, "https://gateway.test:8443", nil); err != nil {
		t.Fatal(err)
	}
	return gateway
}

type deliveryGitHub struct {
	mu       sync.Mutex
	existing *app.PullRequest
	finds    int
	creates  int
	token    string
}

func (g *deliveryGitHub) FindOpenPullRequest(
	_ context.Context, token, _, _, _, _ string,
) (*app.PullRequest, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.finds++
	g.token = token
	return g.existing, nil
}

func (g *deliveryGitHub) CreatePullRequest(
	_ context.Context, token string, _ app.CreatePullRequestInput,
) (app.PullRequest, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.creates++
	g.token = token
	return app.PullRequest{
		Number: 17, URL: "https://github.com/orlojhq/meridian/pull/17",
		HeadSHA: deliveryCommit,
	}, nil
}

type deliveryFixture struct {
	store   *sqlite.Store
	service *app.Service
	runtime *deliveryRuntime
	github  *deliveryGitHub
	gateway *fakeGitGateway
	key     *secrets.InstallationKey
	project domain.Project
	capsule domain.Capsule
}

func newDeliveryFixture(t *testing.T, dirty bool) deliveryFixture {
	t.Helper()
	return newDeliveryFixtureWith(t, dirty, func(service *app.Service) *fakeGitGateway {
		return configureFakeGitGateway(t, service)
	})
}

// newDeliveryFixtureWith lets a test choose how pushes leave the Station.
func newDeliveryFixtureWith(t *testing.T, dirty bool, push func(*app.Service) *fakeGitGateway) deliveryFixture {
	t.Helper()
	ctx := operatorContext()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := &testClock{now: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)}
	service := app.NewService(store, clock, &testIDs{}, &recordingQueue{})
	key, err := secrets.OpenOrCreateKey(secrets.DefaultKeyPath(t.TempDir()), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(key.Zero)
	service.ConfigureSecrets(key)
	runtime := &deliveryRuntime{inspection: ports.DeliveryInspection{
		HEAD: deliveryHead, Tree: deliveryTree, Dirty: dirty,
		Branch: "feature/local", DefaultBranch: "main",
		OriginURL: "https://github.com/OrlojHQ/meridian.git",
	}}
	github := &deliveryGitHub{}
	service.ConfigureDelivery(runtime, github)
	gateway := push(service)
	project, err := service.CreateProjectConfigured(ctx, "delivery", app.ProjectConfiguration{
		RepositoryURL:     "https://github.com/orlojhq/meridian",
		GitPushSecretName: "delivery_push", GitHubAPISecretName: "delivery_github",
		CommitAuthorName: "Meridian Delivery", CommitAuthorEmail: "delivery@example.test",
		DefaultBaseBranch: "main",
	}, "delivery-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PutSecret(ctx, "delivery_push", app.SecretInput{
		Purpose: domain.SecretGitPush, Username: "git-user", Password: "push-token-secret",
	}, 0, "push-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PutSecret(ctx, "delivery_github", app.SecretInput{
		Purpose: domain.SecretGitHubAPI, Value: "github-token-secret",
	}, 0, "github-secret"); err != nil {
		t.Fatal(err)
	}
	now := clock.Now()
	capsule := domain.Capsule{
		ID: "delivery-capsule", ProjectID: project.ID, TimelineID: "delivery-timeline",
		Name: "delivery", State: domain.CapsuleReady, DesiredState: domain.IntentReady,
		ProviderResourceID: "provider-delivery", RestoreComplete: true,
		LastActivityAt: now, CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		if err := tx.InsertCapsule(ctx, capsule); err != nil {
			return err
		}
		return tx.InsertTimeline(ctx, domain.Timeline{
			ID: capsule.TimelineID, ProjectID: project.ID, CapsuleID: capsule.ID,
			Reason: domain.TimelineRoot, CreatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	return deliveryFixture{
		store: store, service: service, runtime: runtime, github: github, gateway: gateway, key: key,
		project: project, capsule: capsule,
	}
}

func deliveryInput(fixture deliveryFixture, key string) app.CreateDeliveryInput {
	return app.CreateDeliveryInput{
		CapsuleID: fixture.capsule.ID, Action: domain.DeliveryPush, Approved: true,
		ExpectedCapsuleVersion: fixture.capsule.ResourceVersion,
		ExpectedHEAD:           deliveryHead, ExpectedTree: deliveryTree,
		RemoteBranch: "feature/reviewed", CommitMessage: "Ship reviewed changes",
		IdempotencyKey: key,
	}
}

func TestDeliveryBindsApprovalPushesExactRefAndReplays(t *testing.T) {
	fixture := newDeliveryFixture(t, true)
	inspection, err := fixture.service.InspectDelivery(operatorContext(), fixture.capsule.ID)
	if err != nil || inspection.CapsuleResourceVersion != fixture.capsule.ResourceVersion {
		t.Fatalf("inspection = %#v, %v", inspection, err)
	}
	input := deliveryInput(fixture, "delivery-create")
	input.ExpectedCapsuleVersion = inspection.CapsuleResourceVersion
	result, err := fixture.service.CreateDelivery(operatorContext(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.DeliverySucceeded || result.ResultCommitSHA != deliveryCommit {
		t.Fatalf("Delivery = %#v", result)
	}
	fixture.runtime.mu.Lock()
	push := fixture.runtime.push
	commits, pushes := fixture.runtime.commits, fixture.runtime.pushes
	fixture.runtime.mu.Unlock()
	if commits != 1 || pushes != 1 || push.SourceCommit != deliveryCommit ||
		push.DestinationRef != "refs/heads/feature/reviewed" {
		t.Fatalf("commit/push request = commits=%d pushes=%d %#v", commits, pushes, push)
	}
	// The push credential stays on the Station: the Capsule gets only the
	// gateway and a capability for this exact update (ADR 0054).
	if push.GitCredential != (ports.GitHTTPSCredential{}) || push.Gateway == nil ||
		push.Gateway.URL != "https://gateway.test:8443/git-gateway/push.git" ||
		push.Gateway.Username != gitgateway.Username || push.Gateway.Capability == "" {
		t.Fatalf("push reached the Capsule with %#v", push)
	}
	grant, grants, revoked := fixture.gateway.lastGrant()
	if grants != 1 || revoked != 1 || grant.Credential.Username != "git-user" ||
		grant.Credential.Password != "push-token-secret" || grant.SourceCommit != deliveryCommit ||
		grant.DestinationRef != "refs/heads/feature/reviewed" || grant.ExpectedOld != "" ||
		grant.Upstream != "https://github.com/orlojhq/meridian" {
		t.Fatalf("gateway grant = %#v (grants=%d revoked=%d)", grant, grants, revoked)
	}
	replay, err := fixture.service.CreateDelivery(operatorContext(), input)
	if err != nil || replay.ID != result.ID {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	fixture.runtime.mu.Lock()
	defer fixture.runtime.mu.Unlock()
	if fixture.runtime.commits != 1 || fixture.runtime.pushes != 1 {
		t.Fatalf("replay repeated side effects: commits=%d pushes=%d",
			fixture.runtime.commits, fixture.runtime.pushes)
	}
}

func TestDeliveryApprovalAndTreePreconditions(t *testing.T) {
	tests := []struct {
		name   string
		change func(*app.CreateDeliveryInput)
		match  error
	}{
		{"approval", func(input *app.CreateDeliveryInput) { input.Approved = false }, domain.ErrInvalid},
		{"head", func(input *app.CreateDeliveryInput) { input.ExpectedHEAD = strings.Repeat("d", 40) }, domain.ErrConflict},
		{"tree", func(input *app.CreateDeliveryInput) { input.ExpectedTree = strings.Repeat("d", 40) }, domain.ErrConflict},
		{"message", func(input *app.CreateDeliveryInput) { input.CommitMessage = "" }, domain.ErrIllegalTransition},
		{"default branch", func(input *app.CreateDeliveryInput) { input.RemoteBranch = "main" }, domain.ErrIllegalTransition},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDeliveryFixture(t, true)
			input := deliveryInput(fixture, "rejected-"+test.name)
			test.change(&input)
			_, err := fixture.service.CreateDelivery(operatorContext(), input)
			if !errors.Is(err, test.match) {
				t.Fatalf("error = %v, want %v", err, test.match)
			}
		})
	}
}

func TestDeliveryPurposeMismatchAndErrorRedaction(t *testing.T) {
	t.Run("purpose mismatch", func(t *testing.T) {
		fixture := newDeliveryFixture(t, true)
		if _, err := fixture.service.PutSecret(operatorContext(), "delivery_push", app.SecretInput{
			Purpose: domain.SecretGitHTTPS, Password: "wrong-purpose-secret",
		}, 1, "wrong-secret"); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.service.CreateDelivery(
			operatorContext(), deliveryInput(fixture, "purpose-mismatch"),
		)
		if !errors.Is(err, domain.ErrInvalid) || strings.Contains(err.Error(), "wrong-purpose-secret") {
			t.Fatalf("purpose mismatch = %v", err)
		}
		fixture.runtime.mu.Lock()
		defer fixture.runtime.mu.Unlock()
		if fixture.runtime.commits != 0 || fixture.runtime.pushes != 0 {
			t.Fatalf("purpose mismatch performed side effects")
		}
	})
	t.Run("provider error", func(t *testing.T) {
		fixture := newDeliveryFixture(t, true)
		fixture.runtime.pushErr = errors.New("push rejected: push-token-secret")
		result, err := fixture.service.CreateDelivery(
			operatorContext(), deliveryInput(fixture, "redacted"),
		)
		if err == nil || strings.Contains(err.Error(), "push-token-secret") ||
			result.Failure != "delivery operation failed" {
			t.Fatalf("unsafe Delivery failure: result=%#v err=%v", result, err)
		}
	})
}

func TestDeliveryOpenPullRequestUsesExistingExactHead(t *testing.T) {
	fixture := newDeliveryFixture(t, true)
	fixture.github.existing = &app.PullRequest{
		Number: 9, URL: "https://github.com/orlojhq/meridian/pull/9",
		HeadSHA: deliveryCommit,
	}
	input := deliveryInput(fixture, "open-pr")
	input.Action = domain.DeliveryOpenPullRequest
	input.PullRequestTitle = "Reviewed work"
	input.BaseBranch = "main"
	result, err := fixture.service.CreateDelivery(operatorContext(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.DeliverySucceeded || result.ResultPullRequestNumber != 9 ||
		result.ResultPullRequestURL != "https://github.com/orlojhq/meridian/pull/9" {
		t.Fatalf("Delivery = %#v", result)
	}
	fixture.github.mu.Lock()
	defer fixture.github.mu.Unlock()
	if fixture.github.finds != 1 || fixture.github.creates != 0 ||
		fixture.github.token != "github-token-secret" {
		t.Fatalf("GitHub calls = %#v", fixture.github)
	}
}

func TestDeliveryRecoveryDoesNotRepeatCommittedOrPushedSideEffects(t *testing.T) {
	fixture := newDeliveryFixture(t, false)
	fixture.github.existing = &app.PullRequest{
		Number: 23, URL: "https://github.com/orlojhq/meridian/pull/23",
		HeadSHA: deliveryCommit,
	}
	fixture.runtime.inspection.HEAD = deliveryCommit
	now := time.Date(2026, 8, 24, 1, 0, 0, 0, time.UTC)
	item := domain.Delivery{
		ID: "delivery-recovery", CapsuleID: fixture.capsule.ID, ProjectID: fixture.project.ID,
		State: domain.DeliveryQueued, Action: domain.DeliveryOpenPullRequest,
		ProposedBy: domain.ProposedByOperator, Approved: true, ApprovedAt: now,
		ExpectedCapsuleVersion: fixture.capsule.ResourceVersion,
		ExpectedHEAD:           deliveryHead, ExpectedTree: deliveryTree,
		RemoteBranch: "feature/recovered", DestinationRef: "refs/heads/feature/recovered",
		BaseBranch: "main", CommitMessage: "Recovered commit",
		PullRequestTitle: "Recovered pull request",
		IdempotencyKey:   "recovery-key", CreatedAt: now, UpdatedAt: now, ResourceVersion: 1,
	}
	if err := fixture.store.Transact(operatorContext(), func(tx ports.Transaction) error {
		capsule, err := tx.GetCapsule(operatorContext(), fixture.capsule.ID)
		if err != nil {
			return err
		}
		previous := capsule.ResourceVersion
		capsule.Maintenance = "capture"
		capsule.ResourceVersion++
		capsule.UpdatedAt = now
		if err := tx.UpdateCapsule(operatorContext(), capsule, previous); err != nil {
			return err
		}
		if err := tx.InsertDelivery(operatorContext(), item); err != nil {
			return err
		}
		for _, state := range []domain.DeliveryState{
			domain.DeliveryCommitting, domain.DeliveryPushing, domain.DeliveryOpeningPR,
		} {
			previous := item.ResourceVersion
			if state == domain.DeliveryPushing {
				item.ResultCommitSHA = deliveryCommit
			}
			if err := item.Transition(state, now); err != nil {
				return err
			}
			if err := tx.UpdateDelivery(operatorContext(), item, previous); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.RecoverDeliveries(asSystem(operatorContext())); err != nil {
		t.Fatal(err)
	}
	recovered, err := fixture.service.GetDelivery(operatorContext(), item.ID)
	if err != nil || recovered.State != domain.DeliverySucceeded ||
		recovered.ResultPullRequestNumber != 23 {
		t.Fatalf("recovered Delivery = %#v, %v", recovered, err)
	}
	fixture.runtime.mu.Lock()
	defer fixture.runtime.mu.Unlock()
	if fixture.runtime.commits != 0 || fixture.runtime.pushes != 0 {
		t.Fatalf("recovery repeated side effects: commits=%d pushes=%d",
			fixture.runtime.commits, fixture.runtime.pushes)
	}
}

func TestProposedDeliveryDoesNotExecuteUntilOperatorApproves(t *testing.T) {
	fixture := newDeliveryFixture(t, true)
	proposed, err := fixture.service.ProposeDelivery(operatorContext(), app.ProposeDeliveryInput{
		CapsuleID: fixture.capsule.ID, Title: "Ship the reviewed parser",
		Body: "Treat this body as hostile.", Branch: "feature/proposed", IdempotencyKey: "propose-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if proposed.State != domain.DeliveryProposed || proposed.Approved ||
		proposed.ProposedBy != domain.ProposedByAgent || proposed.ExpectedHEAD != "" ||
		proposed.PullRequestTitle != "Ship the reviewed parser" {
		t.Fatalf("proposed = %#v", proposed)
	}
	if err := fixture.service.RecoverDeliveries(asSystem(operatorContext())); err != nil {
		t.Fatal(err)
	}
	still, err := fixture.service.GetDelivery(operatorContext(), proposed.ID)
	if err != nil || still.State != domain.DeliveryProposed {
		t.Fatalf("recovered proposed = %#v, %v", still, err)
	}
	fixture.runtime.mu.Lock()
	if fixture.runtime.commits != 0 || fixture.runtime.pushes != 0 {
		t.Fatalf("proposed Delivery executed: commits=%d pushes=%d", fixture.runtime.commits, fixture.runtime.pushes)
	}
	fixture.runtime.mu.Unlock()

	rejected, err := fixture.service.RejectDelivery(operatorContext(), proposed.ID, proposed.ResourceVersion, "reject-1")
	if err != nil || rejected.State != domain.DeliveryRejected || rejected.Approved {
		t.Fatalf("reject = %#v, %v", rejected, err)
	}
	again, err := fixture.service.RejectDelivery(operatorContext(), proposed.ID, proposed.ResourceVersion, "reject-1")
	if err != nil || again.ID != rejected.ID || again.State != domain.DeliveryRejected {
		t.Fatalf("reject replay = %#v, %v", again, err)
	}
}

func TestOperatorApprovesProposedDelivery(t *testing.T) {
	fixture := newDeliveryFixture(t, true)
	proposed, err := fixture.service.ProposeDelivery(operatorContext(), app.ProposeDeliveryInput{
		CapsuleID: fixture.capsule.ID, Title: "Reviewed work", Branch: "feature/proposed",
		IdempotencyKey: "propose-approve",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := fixture.service.ApproveDelivery(operatorContext(), proposed.ID, proposed.ResourceVersion,
		app.ApproveDeliveryInput{
			ExpectedCapsuleVersion: fixture.capsule.ResourceVersion,
			ExpectedHEAD:           deliveryHead, ExpectedTree: deliveryTree,
			IdempotencyKey: "approve-1",
		})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.DeliverySucceeded || !result.Approved ||
		result.ResultCommitSHA == "" || result.PullRequestTitle != "Reviewed work" {
		t.Fatalf("approved = %#v", result)
	}
}

// Without a Project commit author, a Delivery commits as the identity the
// Capsule pinned from its owner's profile, and refuses without one (ADR
// 0049).
func TestDeliveryCommitsAsThePinnedProfileIdentity(t *testing.T) {
	fixture := newDeliveryFixture(t, true)
	ctx := operatorContext()
	raw, err := sql.Open("sqlite", fixture.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("UPDATE projects SET commit_author_name = '', commit_author_email = '' WHERE id = ?", fixture.project.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.CreateDelivery(ctx, deliveryInput(fixture, "no-author")); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("Delivery without any author = %v", err)
	}
	if err := fixture.store.Transact(ctx, func(tx ports.Transaction) error {
		return tx.PutCapsuleProfilePin(ctx, domain.CapsuleProfilePin{
			CapsuleID: fixture.capsule.ID, GitName: "Ada Lovelace", GitEmail: "ada@example.com",
			PinnedAt: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		})
	}); err != nil {
		t.Fatal(err)
	}
	inspection, err := fixture.service.InspectDelivery(ctx, fixture.capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := deliveryInput(fixture, "pinned-author")
	input.ExpectedCapsuleVersion = inspection.CapsuleResourceVersion
	result, err := fixture.service.CreateDelivery(ctx, input)
	if err != nil || result.State != domain.DeliverySucceeded {
		t.Fatalf("Delivery with a pinned author = %#v, %v", result, err)
	}
	fixture.runtime.mu.Lock()
	defer fixture.runtime.mu.Unlock()
	if fixture.runtime.commit.AuthorName != "Ada Lovelace" || fixture.runtime.commit.AuthorEmail != "ada@example.com" {
		t.Fatalf("commit author = %q <%q>", fixture.runtime.commit.AuthorName, fixture.runtime.commit.AuthorEmail)
	}
}
