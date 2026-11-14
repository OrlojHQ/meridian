package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/coordproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/principal"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
	"github.com/OrlojHQ/meridian/pkg/client"
)

type idleQueue struct{}

func (idleQueue) Enqueue(context.Context, domain.CapsuleID) error { return nil }

type idleCoordination struct{}

func (idleCoordination) ConfigureCoordination(context.Context, string, coordproto.Config) error {
	return nil
}

func (idleCoordination) PollCoordination(ctx context.Context, _ string, _, _ uint64, wait time.Duration) (coordproto.Poll, error) {
	select {
	case <-ctx.Done():
	case <-time.After(wait):
	}
	return coordproto.Poll{Epoch: "idle", Requests: []coordproto.Pending{}, Consumed: map[string]int{}}, nil
}

func (idleCoordination) ReplyCoordination(context.Context, string, []coordproto.Reply) error {
	return nil
}

func (idleCoordination) NotifyCoordination(context.Context, string, coordproto.Notification) error {
	return nil
}

func TestCoordinationRoutes(t *testing.T) {
	ctx := principal.With(context.Background(), principal.Owner())
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime := &runtime{}
	service := app.NewService(store, clock{}, &ids{}, idleQueue{}, runtime)
	key, err := transcripts.NewInstallationKey("http-coordination", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	service.ConfigureThreads(runtime, key)
	service.ConfigureCoordination(idleCoordination{}, 0)
	tokenValue := "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	token, err := apiauth.ParseToken(tokenValue)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewWithCapabilities(service, ports.ProviderCapabilities{
		Run: true, Structured: true, Coordination: true,
	}, token)
	handler.SetReady(true)
	server := httptest.NewServer(handler)
	defer server.Close()
	api, err := client.NewClient(server.URL, testSecuritySource{token: tokenValue})
	if err != nil {
		t.Fatal(err)
	}

	capabilities, err := api.GetCapabilities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	coordination, ok := capabilities.(*client.Capabilities).Coordination.Get()
	if !ok || !coordination.Supported || coordination.MaxDepth != 1 {
		t.Fatalf("coordination capability = %#v", coordination)
	}
	project, err := api.CreateProject(ctx, &client.CreateProjectRequest{
		Name: "coordination",
		HarnessImages: []client.HarnessImage{
			{Name: "mock", ImageReference: "example@sha256:" + strings.Repeat("a", 64)},
		},
	}, client.CreateProjectParams{IdempotencyKey: "project"})
	if err != nil {
		t.Fatal(err)
	}
	projectID := project.(*client.ProjectHeaders).Response.ID

	tooWide, err := api.CreateProjectThread(ctx, &client.CreateProjectThreadRequest{
		Harness: "mock", Prompt: client.NewOptString("lead"),
		Coordination: client.NewOptCoordinationPolicy(client.CoordinationPolicy{MaxChildren: client.NewOptInt(17)}),
	}, client.CreateProjectThreadParams{ProjectId: projectID, IdempotencyKey: "too-wide"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tooWide.(*client.ProjectThreadIntentHeaders); ok {
		t.Fatal("over-limit coordination policy was accepted")
	}

	for name, seconds := range map[string]int{"short-archive": 30, "long-archive": 2592001, "negative-archive": -1} {
		rejected, err := api.CreateProjectThread(ctx, &client.CreateProjectThreadRequest{
			Harness: "mock", Prompt: client.NewOptString("lead"),
			Coordination: client.NewOptCoordinationPolicy(client.CoordinationPolicy{
				ArchiveAfterSeconds: client.NewOptInt(seconds),
			}),
		}, client.CreateProjectThreadParams{ProjectId: projectID, IdempotencyKey: name})
		if err == nil {
			if _, ok := rejected.(*client.ProjectThreadIntentHeaders); ok {
				t.Fatalf("%s policy was accepted", name)
			}
		}
	}

	created, err := api.CreateProjectThread(ctx, &client.CreateProjectThreadRequest{
		Harness: "mock", Prompt: client.NewOptString("lead"),
		Coordination: client.NewOptCoordinationPolicy(client.CoordinationPolicy{
			MaxChildren: client.NewOptInt(3), DeadlineSeconds: client.NewOptInt(3600),
			ArchiveAfterSeconds: client.NewOptInt(120),
		}),
	}, client.CreateProjectThreadParams{ProjectId: projectID, IdempotencyKey: "lead"})
	if err != nil {
		t.Fatal(err)
	}
	intent := created.(*client.ProjectThreadIntentHeaders).Response
	var capsule domain.Capsule
	if err := store.Transact(ctx, func(tx ports.Transaction) error {
		var err error
		capsule, err = tx.GetCapsule(ctx, domain.CapsuleID(intent.CapsuleId))
		if err != nil {
			return err
		}
		previous := capsule.ResourceVersion
		capsule.State, capsule.ProviderResourceID = domain.CapsuleReady, "resource-lead"
		capsule.ResourceVersion++
		return tx.UpdateCapsule(ctx, capsule, previous)
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReconcileProjectThreadCapsule(ctx, capsule); err != nil {
		t.Fatal(err)
	}

	mains, err := api.ListCapsules(ctx, client.ListCapsulesParams{ProjectId: projectID, Role: client.NewOptCapsuleRole(client.CapsuleRoleMain)})
	if err != nil {
		t.Fatal(err)
	}
	if page := mains.(*client.CapsulePage); len(page.Items) != 1 || page.Items[0].Role.Value != client.CapsuleRoleMain {
		t.Fatalf("main Capsules = %#v", page.Items)
	}
	subs, err := api.ListCapsules(ctx, client.ListCapsulesParams{ProjectId: projectID, Role: client.NewOptCapsuleRole(client.CapsuleRoleSub)})
	if err != nil || len(subs.(*client.CapsulePage).Items) != 0 {
		t.Fatalf("sub Capsules = %#v, %v", subs, err)
	}

	threadResult, err := api.GetThread(ctx, client.GetThreadParams{ThreadId: intent.ThreadId})
	if err != nil {
		t.Fatal(err)
	}
	thread := threadResult.(*client.ThreadHeaders).Response
	grantID, ok := thread.CoordinationGrantId.Get()
	if !ok || thread.RootThreadId.Value != thread.ID || thread.Depth.Value != 0 || thread.ParentThreadId.Set {
		t.Fatalf("lead Thread coordination fields = %#v", thread)
	}
	grantResult, err := api.GetCoordinationGrant(ctx, client.GetCoordinationGrantParams{GrantId: grantID})
	if err != nil {
		t.Fatal(err)
	}
	grant := grantResult.(*client.CoordinationGrantHeaders).Response
	if grant.State != client.CoordinationGrantStateActive || grant.MaxChildren != 3 ||
		grant.RootThreadId != thread.ID || grant.DeadlineAt.Sub(grant.CreatedAt) != time.Hour ||
		grant.ArchiveAfterSeconds != 120 || grant.SpawnApproval != client.CoordinationGrantSpawnApprovalAuto {
		t.Fatalf("grant = %#v", grant)
	}
	patched, err := api.UpdateCoordinationGrant(ctx, &client.UpdateCoordinationGrantRequest{
		ExpectedResourceVersion: grant.ResourceVersion,
		SpawnApproval: client.NewOptUpdateCoordinationGrantRequestSpawnApproval(
			client.UpdateCoordinationGrantRequestSpawnApprovalOperator),
		MaxChildren: client.NewOptInt(5),
	}, client.UpdateCoordinationGrantParams{GrantId: grantID, IdempotencyKey: "grant-patch"})
	if err != nil {
		t.Fatal(err)
	}
	grant = patched.(*client.CoordinationGrantHeaders).Response
	if grant.SpawnApproval != client.CoordinationGrantSpawnApprovalOperator || grant.MaxChildren != 5 {
		t.Fatalf("patched grant = %#v", grant)
	}
	listed, err := api.ListCoordinationRequests(ctx, client.ListCoordinationRequestsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if page, ok := listed.(*client.CoordinationRequestPage); !ok || len(page.Items) != 0 {
		t.Fatalf("pending requests = %#v, %v", listed, err)
	}
	leadCapsule, err := service.GetCapsule(ctx, domain.CapsuleID(intent.CapsuleId))
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := api.PromoteCapsule(ctx, &client.PromoteCapsuleRequest{
		ExpectedResourceVersion: int64(leadCapsule.ResourceVersion), Detach: client.NewOptBool(true),
	}, client.PromoteCapsuleParams{CapsuleId: intent.CapsuleId, IdempotencyKey: "promote-lead"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := promoted.(*client.PromoteCapsuleConflict); !ok {
		t.Fatalf("promote a main Capsule = %#v", promoted)
	}
	links, err := api.ListThreadLinks(ctx, client.ListThreadLinksParams{ThreadId: thread.ID})
	if err != nil || len(links.(*client.ThreadLinkPage).Items) != 0 {
		t.Fatalf("links = %#v, %v", links, err)
	}
	if missing, err := api.GetThreadLink(ctx, client.GetThreadLinkParams{LinkId: "missing"}); err != nil {
		t.Fatal(err)
	} else if _, ok := missing.(*client.ThreadLinkHeaders); ok {
		t.Fatal("unknown link found")
	}
	removedMissing, err := api.RemoveSubagent(ctx, &client.LifecycleMutationRequest{ExpectedResourceVersion: 1},
		client.RemoveSubagentParams{LinkId: "missing", IdempotencyKey: "remove-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := removedMissing.(*client.RemoveSubagentNotFound); !ok {
		t.Fatalf("remove an unknown link = %#v", removedMissing)
	}

	boardResult, err := api.GetBoard(ctx, client.GetBoardParams{
		ProjectId: client.NewOptString(projectID), Expand: client.NewOptGetBoardExpand(client.GetBoardExpandAll),
	})
	if err != nil {
		t.Fatal(err)
	}
	board := boardResult.(*client.BoardPage)
	if len(board.Items) != 1 || board.Truncated || board.Items[0].Capsule.ID != intent.CapsuleId ||
		board.Items[0].Thread.Value.ID != thread.ID || board.Items[0].Surfaced ||
		board.Items[0].Status != client.BoardItemStatusWorking {
		t.Fatalf("board = %#v", board)
	}
	if bad, err := api.GetBoard(ctx, client.GetBoardParams{ProjectId: client.NewOptString("missing")}); err != nil {
		t.Fatal(err)
	} else if _, ok := bad.(*client.BoardPage); ok {
		t.Fatal("board for an unknown Project")
	}
	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodGet, "/board?expand=everything", nil)
	invalidRequest.Header.Set("Authorization", "Bearer "+tokenValue)
	handler.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid expand = %d", invalid.Code)
	}

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/coordination-grants/"+grantID, nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated grant read = %d", unauthenticated.Code)
	}

	stale, err := api.RevokeCoordinationGrant(ctx, &client.RevokeCoordinationGrantRequest{
		ExpectedResourceVersion: grant.ResourceVersion + 1,
	}, client.RevokeCoordinationGrantParams{GrantId: grantID, IdempotencyKey: "stale"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stale.(*client.CoordinationGrantHeaders); ok {
		t.Fatal("stale revoke succeeded")
	}
	revokedResult, err := api.RevokeCoordinationGrant(ctx, &client.RevokeCoordinationGrantRequest{
		ExpectedResourceVersion: grant.ResourceVersion,
	}, client.RevokeCoordinationGrantParams{GrantId: grantID, IdempotencyKey: "revoke"})
	if err != nil {
		t.Fatal(err)
	}
	revoked := revokedResult.(*client.CoordinationGrantHeaders).Response
	if revoked.State != client.CoordinationGrantStateRevoked ||
		revoked.RevokeReason.Value != client.CoordinationGrantRevokeReasonOperator {
		t.Fatalf("revoked grant = %#v", revoked)
	}
}
