package httpapi_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/apiauth"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/httpapi"
	"github.com/OrlojHQ/meridian/internal/observability"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/provider/fake"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/transcripts"
	"github.com/OrlojHQ/meridian/pkg/client"
)

type clock struct{}

func (clock) Now() time.Time { return time.Now().UTC() }

type ids struct {
	mu   sync.Mutex
	next int
}

type testSecuritySource struct {
	token string
}

func (s testSecuritySource) BearerAuth(
	context.Context,
	client.OperationName,
) (client.BearerAuth, error) {
	return client.BearerAuth{Token: s.token}, nil
}

type runtime struct {
	mu        sync.Mutex
	state     map[domain.RunID]ports.RuntimeRun
	sends     []adapterproto.Frame
	cancelErr error
}

type slowThreadWriter struct {
	header http.Header
}

func (w *slowThreadWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (*slowThreadWriter) WriteHeader(int)                  {}
func (*slowThreadWriter) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (*slowThreadWriter) Flush()                           {}
func (*slowThreadWriter) SetWriteDeadline(time.Time) error { return nil }

func (r *runtime) StartRun(_ context.Context, request ports.RuntimeRunRequest) (ports.RuntimeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		r.state = make(map[domain.RunID]ports.RuntimeRun)
	}
	value := ports.RuntimeRun{State: domain.RunRunning, Cursor: 1}
	r.state[request.RunID] = value
	return value, nil
}
func (r *runtime) GetRun(_ context.Context, _ string, id domain.RunID) (ports.RuntimeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state[id], nil
}
func (r *runtime) CancelRun(_ context.Context, _ string, id domain.RunID) (ports.RuntimeRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := ports.RuntimeRun{State: domain.RunCancelled, Cursor: 2}
	r.state[id] = value
	return value, nil
}
func (*runtime) RunEvents(context.Context, string, domain.RunID, uint64) (ports.RuntimeEvents, error) {
	return ports.RuntimeEvents{
		Items: []ports.RuntimeEvent{{Sequence: 1, Type: "record"}}, NextCursor: 1,
	}, nil
}
func (*runtime) AttachRun(context.Context, string, domain.RunID, uint64) (ports.RuntimeAttachment, error) {
	return nil, domain.ErrUnsupported
}
func (*runtime) GitStatus(context.Context, string) (ports.GitResult, error) {
	return ports.GitResult{Content: " M result.txt\n"}, nil
}
func (*runtime) GitDiff(context.Context, string) (ports.GitResult, error) {
	return ports.GitResult{Content: "diff --git"}, nil
}
func (r *runtime) StartStructured(
	_ context.Context, request ports.RuntimeStructuredStartRequest,
) (ports.RuntimeRun, error) {
	return r.StartRun(context.Background(), ports.RuntimeRunRequest{RunID: request.RunID})
}
func (r *runtime) GetStructured(
	ctx context.Context, resource string, id domain.RunID,
) (ports.RuntimeRun, error) {
	return r.GetRun(ctx, resource, id)
}
func (r *runtime) SendStructured(_ context.Context, request ports.RuntimeStructuredSendRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sends = append(r.sends, request.Frame)
	return nil
}
func (*runtime) StructuredEvents(
	context.Context, string, domain.RunID, uint64,
) (ports.RuntimeStructuredEvents, error) {
	return ports.RuntimeStructuredEvents{
		Items: []ports.RuntimeStructuredEvent{{Sequence: 1, Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindAssistantMessage,
			Role: adapterproto.RoleAssistant, MessageID: "http-message", Content: "http-final",
		}}, {Sequence: 2, Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindPermissionRequest,
			ID: "http-permission", Permission: &adapterproto.Permission{
				Kind: "shell", Summary: "http-permission-secret", Options: []string{"allow"},
			},
		}}},
		NextCursor: 2,
	}, nil
}
func (r *runtime) CancelStructured(
	ctx context.Context, resource string, id domain.RunID,
) (ports.RuntimeRun, error) {
	r.mu.Lock()
	cancelErr := r.cancelErr
	r.mu.Unlock()
	if cancelErr != nil {
		return ports.RuntimeRun{}, cancelErr
	}
	return r.CancelRun(ctx, resource, id)
}
func (*runtime) HarnessProfiles(
	context.Context, string,
) ([]ports.RuntimeHarnessProfile, error) {
	return []ports.RuntimeHarnessProfile{{
		Name: "mock", Structured: true, AdapterKind: "mock", Protocol: adapterproto.Version,
	}}, nil
}

func (i *ids) NewID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	return fmt.Sprintf("http-id-%03d", i.next)
}

func TestGeneratedClientLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	idSource := &ids{}
	reconciler := app.NewReconciler(store, fake.New(fake.Options{}), clock{}, idSource)
	reconciler.Start(ctx)
	defer reconciler.Close()
	runtime := &runtime{}
	service := app.NewService(store, clock{}, idSource, reconciler, runtime)
	key, err := transcripts.NewInstallationKey("http-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Zero()
	service.ConfigureThreads(runtime, key)
	reconciler.ConfigureProjectThreads(service.ReconcileProjectThreadCapsule)
	apiTokenValue := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	apiToken, err := apiauth.ParseToken(apiTokenValue)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewWithCapabilities(service, ports.ProviderCapabilities{
		Run: true, Attach: true, Structured: true,
	}, apiToken)
	handler.SetReady(true)
	server := httptest.NewServer(handler)
	defer server.Close()
	api, err := client.NewClient(server.URL, testSecuritySource{token: apiTokenValue})
	if err != nil {
		t.Fatal(err)
	}

	projectResult, err := api.CreateProject(
		ctx,
		&client.CreateProjectRequest{
			Name:           "project",
			RepositoryUrl:  client.NewOptString("https://example.invalid/repository.git"),
			Setup:          []string{"/usr/bin/make", "bootstrap"},
			ImageReference: client.NewOptString("example@sha256:" + strings.Repeat("a", 64)),
			HarnessImages: []client.HarnessImage{
				{Name: "opencode", ImageReference: "example-opencode@sha256:" + strings.Repeat("b", 64)},
				{Name: "mock", ImageReference: "example@sha256:" + strings.Repeat("a", 64)},
			},
		},
		client.CreateProjectParams{IdempotencyKey: "project-key"},
	)
	if err != nil {
		t.Fatal(err)
	}
	projectResponse, ok := projectResult.(*client.ProjectHeaders)
	if !ok {
		t.Fatalf("create project response = %T", projectResult)
	}
	if repositoryURL, ok := projectResponse.Response.RepositoryUrl.Get(); !ok ||
		repositoryURL != "https://example.invalid/repository.git" ||
		len(projectResponse.Response.Setup) != 2 ||
		len(projectResponse.Response.HarnessImages) != 2 ||
		projectResponse.Response.HarnessImages[0].Name != "opencode" ||
		projectResponse.Response.HarnessImages[1].Name != "mock" {
		t.Fatalf("project configuration = %#v", projectResponse.Response)
	}
	replayResult, err := api.CreateProject(
		ctx,
		&client.CreateProjectRequest{Name: "different"},
		client.CreateProjectParams{IdempotencyKey: "project-key"},
	)
	if err != nil {
		t.Fatal(err)
	}
	replay := replayResult.(*client.ProjectHeaders).Response
	if replay.ID != projectResponse.Response.ID {
		t.Fatalf("idempotent project ID = %q, want %q", replay.ID, projectResponse.Response.ID)
	}
	unauthenticatedSpawn := httptest.NewRecorder()
	handlerRequest := httptest.NewRequest(
		http.MethodPost, "/projects/"+projectResponse.Response.ID+"/threads",
		strings.NewReader(`{"harness":"mock","prompt":"prompt"}`),
	)
	handlerRequest.Header.Set("Idempotency-Key", "unauthenticated-spawn")
	handler.ServeHTTP(unauthenticatedSpawn, handlerRequest)
	if unauthenticatedSpawn.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated Project Thread status = %d", unauthenticatedSpawn.Code)
	}
	spawnResponse, err := api.CreateProjectThread(
		ctx,
		&client.CreateProjectThreadRequest{Harness: "mock", Prompt: "project prompt"},
		client.CreateProjectThreadParams{
			ProjectId: projectResponse.Response.ID, IdempotencyKey: "project-thread-key",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	spawned, ok := spawnResponse.(*client.ProjectThreadIntentHeaders)
	if !ok || spawned.Response.State != client.ProjectThreadIntentStateProvisioning {
		t.Fatalf("spawn response = %#v (%T)", spawnResponse, spawnResponse)
	}
	var spawnedIntent client.ProjectThreadIntent
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		current, getErr := api.GetProjectThreadIntent(
			ctx, client.GetProjectThreadIntentParams{IntentId: spawned.Response.ID},
		)
		if getErr != nil {
			t.Fatal(getErr)
		}
		spawnedIntent = current.(*client.ProjectThreadIntentHeaders).Response
		if spawnedIntent.State == client.ProjectThreadIntentStateReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if spawnedIntent.State != client.ProjectThreadIntentStateReady ||
		spawnedIntent.CapsuleId == "" || spawnedIntent.ThreadId == "" ||
		spawnedIntent.RunId == "" {
		t.Fatalf("ready Project Thread intent = %#v", spawnedIntent)
	}
	// Intent readiness and asynchronous prompt delivery are separate transitions.
	var delivered []adapterproto.Frame
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		runtime.mu.Lock()
		delivered = append([]adapterproto.Frame(nil), runtime.sends...)
		if len(delivered) > 0 {
			runtime.sends = nil
		}
		runtime.mu.Unlock()
		if len(delivered) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(delivered) != 1 || delivered[0].Content != "project prompt" {
		t.Fatalf("Project Thread delivery = %#v", delivered)
	}

	createResult, err := api.CreateCapsule(
		ctx,
		&client.CreateCapsuleRequest{Name: "capsule", Harness: client.NewOptString("mock")},
		client.CreateCapsuleParams{
			ProjectId:      projectResponse.Response.ID,
			IdempotencyKey: "capsule-key",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	created, ok := createResult.(*client.CapsuleHeaders)
	if !ok {
		t.Fatalf("create capsule response = %#v (%T)", createResult, createResult)
	}
	harness, hasHarness := created.Response.Harness.Get()
	if created.Response.State != client.CapsuleStateCreating ||
		!hasHarness || harness != "mock" {
		t.Fatalf("create capsule response = %#v (%T)", createResult, createResult)
	}
	ready := waitState(t, ctx, api, created.Response.ID, client.CapsuleStateReady)

	threadResult, err := api.CreateThread(ctx, &client.CreateThreadRequest{
		Harness: "mock", FirstMessage: client.NewOptString("http-secret"),
		Start: client.NewOptBool(false),
	}, client.CreateThreadParams{CapsuleId: ready.ID, IdempotencyKey: "thread-key"})
	if err != nil {
		t.Fatal(err)
	}
	threadCreated, ok := threadResult.(*client.ThreadMutationResultHeaders)
	if !ok {
		t.Fatalf("create Thread response = %T", threadResult)
	}
	startResult, err := api.StartThread(ctx,
		&client.LifecycleMutationRequest{
			ExpectedResourceVersion: threadCreated.Response.Thread.ResourceVersion,
		},
		client.StartThreadParams{
			ThreadId: threadCreated.Response.Thread.ID, IdempotencyKey: "thread-start",
		})
	if err != nil {
		t.Fatal(err)
	}
	threadStarted, ok := startResult.(*client.ThreadSessionMutationHeaders)
	if !ok {
		t.Fatalf("start deferred Thread response = %T", startResult)
	}
	runtime.mu.Lock()
	if len(runtime.sends) != 1 || runtime.sends[0].Content != "http-secret" {
		t.Fatalf("deferred HTTP delivery = %#v", runtime.sends)
	}
	runtime.mu.Unlock()
	blockResult, err := api.ListThreadBlocks(ctx, client.ListThreadBlocksParams{
		ThreadId: threadStarted.Response.Thread.ID, Limit: client.NewOptInt(100),
	})
	if err != nil {
		t.Fatal(err)
	}
	blocks, ok := blockResult.(*client.ThreadBlockPage)
	if !ok || len(blocks.Items) != 3 {
		t.Fatalf("Thread blocks = %#v (%T)", blockResult, blockResult)
	}
	if content, ok := blocks.Items[0].Content.Get(); !ok || content != "http-secret" {
		t.Fatalf("decrypted first block = %#v", blocks.Items[0])
	}
	metrics := observability.NewMetrics()
	handler.ConfigureObservability(metrics)
	slowRequest := httptest.NewRequest(
		http.MethodGet,
		"/threads/"+threadStarted.Response.Thread.ID+"/blocks/stream?after=0",
		nil,
	)
	slowRequest.Header.Set("Authorization", "Bearer "+apiTokenValue)
	slowDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(&slowThreadWriter{}, slowRequest)
		close(slowDone)
	}()
	select {
	case <-slowDone:
	case <-time.After(time.Second):
		t.Fatal("slow Thread consumer blocked stream handler")
	}
	metricsResponse := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metricsResponse.Body.String(),
		`meridian_event_backpressure_total{kind="slow_client"} 1`) {
		t.Fatalf("slow-consumer metric = %s", metricsResponse.Body.String())
	}
	if strings.Contains(metricsResponse.Body.String(), threadStarted.Response.Thread.ID) ||
		strings.Contains(metricsResponse.Body.String(), "http-secret") {
		t.Fatal("Thread identity or content leaked into Prometheus output")
	}
	threadStreamContext, stopThreadStream := context.WithCancel(ctx)
	threadStreamRequest, _ := http.NewRequestWithContext(
		threadStreamContext, http.MethodGet,
		server.URL+"/threads/"+threadStarted.Response.Thread.ID+"/blocks/stream", nil,
	)
	threadStreamRequest.Header.Set("Last-Event-ID", "1")
	threadStreamRequest.Header.Set("Authorization", "Bearer "+apiTokenValue)
	threadStreamResponse, err := http.DefaultClient.Do(threadStreamRequest)
	if err != nil {
		t.Fatal(err)
	}
	threadScanner := bufio.NewScanner(threadStreamResponse.Body)
	if !threadScanner.Scan() || threadScanner.Text() != "id: 2" {
		t.Fatalf("Thread SSE reconnect first line = %q", threadScanner.Text())
	}
	stopThreadStream()
	_ = threadStreamResponse.Body.Close()
	threadGet, err := api.GetThread(ctx, client.GetThreadParams{ThreadId: threadStarted.Response.Thread.ID})
	if err != nil {
		t.Fatal(err)
	}
	currentThread := threadGet.(*client.ThreadHeaders).Response
	if awaiting, ok := currentThread.Awaiting.Get(); !ok ||
		awaiting.Kind != client.ThreadAwaitingKindPermission || awaiting.Since.IsZero() {
		t.Fatalf("Thread awaiting = %#v", currentThread.Awaiting)
	}
	for _, path := range []string{
		"/threads/" + currentThread.ID,
		"/capsules/" + ready.ID + "/threads",
	} {
		rawRequest := httptest.NewRequest(http.MethodGet, path, nil)
		rawRequest.Header.Set("Authorization", "Bearer "+apiTokenValue)
		rawResponse := httptest.NewRecorder()
		handler.ServeHTTP(rawResponse, rawRequest)
		if rawResponse.Code != http.StatusOK ||
			!strings.Contains(rawResponse.Body.String(), `"awaiting":{"kind":"permission"`) ||
			strings.Contains(rawResponse.Body.String(), "http-permission-secret") {
			t.Fatalf("%s = %d %s", path, rawResponse.Code, rawResponse.Body.String())
		}
	}
	runtime.mu.Lock()
	runtime.cancelErr = errors.New("injected runtime cancel failure")
	runtime.mu.Unlock()
	if _, err := api.CancelThread(ctx,
		&client.LifecycleMutationRequest{ExpectedResourceVersion: currentThread.ResourceVersion},
		client.CancelThreadParams{ThreadId: currentThread.ID, IdempotencyKey: "thread-cancel"},
	); err == nil {
		t.Fatal("HTTP cancel hid runtime failure")
	}
	runtime.mu.Lock()
	runtime.cancelErr = nil
	runtime.mu.Unlock()
	cancelThread, err := api.CancelThread(ctx,
		&client.LifecycleMutationRequest{ExpectedResourceVersion: currentThread.ResourceVersion},
		client.CancelThreadParams{ThreadId: currentThread.ID, IdempotencyKey: "thread-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cancelThread.(*client.ThreadSessionMutationHeaders); !ok {
		t.Fatalf("cancel Thread response = %T", cancelThread)
	}
	deletedCurrent, err := api.GetThread(ctx, client.GetThreadParams{ThreadId: currentThread.ID})
	if err != nil {
		t.Fatal(err)
	}
	deletedResult, err := api.DeleteThread(ctx, &client.DeleteThreadRequest{
		ExpectedResourceVersion: deletedCurrent.(*client.ThreadHeaders).Response.ResourceVersion,
		Confirmation:            client.DeleteThreadRequestConfirmationCryptoShred,
	}, client.DeleteThreadParams{ThreadId: currentThread.ID, IdempotencyKey: "thread-delete"})
	if err != nil {
		t.Fatal(err)
	}
	if deletedResult.(*client.ThreadMutationHeaders).Response.State != client.ThreadStateDeleted {
		t.Fatalf("deleted Thread response = %#v", deletedResult)
	}
	listedThreads, err := api.ListThreads(ctx, client.ListThreadsParams{
		CapsuleId: ready.ID, Limit: client.NewOptInt(10),
	})
	if err != nil || len(listedThreads.(*client.ThreadPage).Items) != 0 {
		t.Fatalf("normal HTTP Thread list exposed deletion: %#v, %v", listedThreads, err)
	}
	explicitDeleted, err := api.GetThread(ctx, client.GetThreadParams{ThreadId: currentThread.ID})
	if err != nil ||
		explicitDeleted.(*client.ThreadHeaders).Response.State != client.ThreadStateDeleted {
		t.Fatalf("explicit HTTP deleted Thread = %#v, %v", explicitDeleted, err)
	}

	latestCapsuleResult, err := api.GetCapsule(
		ctx, client.GetCapsuleParams{CapsuleId: ready.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	latestCapsule := latestCapsuleResult.(*client.CapsuleHeaders).Response
	conflictResult, err := api.PauseCapsule(
		ctx,
		&client.LifecycleMutationRequest{ExpectedResourceVersion: latestCapsule.ResourceVersion + 100},
		client.PauseCapsuleParams{CapsuleId: ready.ID, IdempotencyKey: "bad-version"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := conflictResult.(*client.PauseCapsuleConflict); !ok {
		t.Fatalf("conflict response = %T", conflictResult)
	}

	pauseResult, err := api.PauseCapsule(
		ctx,
		&client.LifecycleMutationRequest{ExpectedResourceVersion: latestCapsule.ResourceVersion},
		client.PauseCapsuleParams{CapsuleId: ready.ID, IdempotencyKey: "pause-key"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pauseResult.(*client.CapsuleAcceptedHeaders); !ok {
		t.Fatalf("pause response = %T", pauseResult)
	}
	paused := waitState(t, ctx, api, ready.ID, client.CapsuleStatePaused)

	if _, err := api.ResumeCapsule(
		ctx,
		&client.LifecycleMutationRequest{ExpectedResourceVersion: paused.ResourceVersion},
		client.ResumeCapsuleParams{CapsuleId: paused.ID, IdempotencyKey: "resume-key"},
	); err != nil {
		t.Fatal(err)
	}
	resumed := waitState(t, ctx, api, ready.ID, client.CapsuleStateReady)

	runResult, err := api.StartRun(
		ctx,
		&client.StartRunRequest{Harness: "mock", Prompt: "transient"},
		client.StartRunParams{CapsuleId: resumed.ID, IdempotencyKey: "run-key"},
	)
	if err != nil {
		t.Fatal(err)
	}
	started, ok := runResult.(*client.RunHeaders)
	if !ok || started.Response.State != client.RunStateRunning {
		t.Fatalf("start Run response = %#v (%T)", runResult, runResult)
	}
	unauthenticatedStream := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedStream, httptest.NewRequest(
		http.MethodGet, "/runs/"+started.Response.ID+"/events/stream", nil,
	))
	if unauthenticatedStream.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated SSE status = %d", unauthenticatedStream.Code)
	}
	unauthenticatedTicket := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticatedTicket, httptest.NewRequest(
		http.MethodPost, "/runs/"+started.Response.ID+"/attach-ticket", nil,
	))
	if unauthenticatedTicket.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated ticket mint status = %d", unauthenticatedTicket.Code)
	}
	ticketResult, err := api.CreateRunAttachTicket(ctx, client.CreateRunAttachTicketParams{
		RunId: started.Response.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	ticket, ok := ticketResult.(*client.AttachTicket)
	if !ok {
		t.Fatalf("attach ticket response = %T", ticketResult)
	}
	redeemed := httptest.NewRecorder()
	handler.ServeHTTP(redeemed, httptest.NewRequest(
		http.MethodGet,
		"/runs/"+started.Response.ID+"/attach?ticket="+url.QueryEscape(ticket.Ticket),
		nil,
	))
	if redeemed.Code == http.StatusUnauthorized {
		t.Fatal("ticket redemption incorrectly required installation bearer")
	}
	eventResult, err := api.ListRunEvents(ctx, client.ListRunEventsParams{
		RunId: started.Response.ID, After: client.NewOptInt64(0), Limit: client.NewOptInt(100),
	})
	if err != nil {
		t.Fatal(err)
	}
	events, ok := eventResult.(*client.RunEventPage)
	if !ok || len(events.Items) == 0 {
		t.Fatalf("Run events = %#v (%T)", eventResult, eventResult)
	}
	streamContext, stopStream := context.WithCancel(ctx)
	streamRequest, _ := http.NewRequestWithContext(
		streamContext, http.MethodGet,
		server.URL+"/runs/"+started.Response.ID+"/events/stream", nil,
	)
	streamRequest.Header.Set("Last-Event-ID", strconv.FormatInt(events.Items[0].Sequence-1, 10))
	streamRequest.Header.Set("Authorization", "Bearer "+apiTokenValue)
	streamResponse, err := http.DefaultClient.Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(streamResponse.Body)
	if !scanner.Scan() || scanner.Text() != "id: "+strconv.FormatInt(events.Items[0].Sequence, 10) {
		t.Fatalf("SSE reconnect first line = %q", scanner.Text())
	}
	stopStream()
	_ = streamResponse.Body.Close()
	statusResult, err := api.GetCapsuleGitStatus(
		ctx, client.GetCapsuleGitStatusParams{CapsuleId: resumed.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	if status, ok := statusResult.(*client.GitResult); !ok || status.Content == "" {
		t.Fatalf("Git status = %#v", statusResult)
	}
	currentResult, err := api.GetRun(ctx, client.GetRunParams{RunId: started.Response.ID})
	if err != nil {
		t.Fatal(err)
	}
	current := currentResult.(*client.RunHeaders).Response
	cancelResult, err := api.CancelRun(
		ctx,
		&client.LifecycleMutationRequest{ExpectedResourceVersion: current.ResourceVersion},
		client.CancelRunParams{RunId: started.Response.ID, IdempotencyKey: "run-cancel"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled, ok := cancelResult.(*client.RunHeaders); !ok || cancelled.Response.State != client.RunStateCancelled {
		t.Fatalf("cancel Run = %#v", cancelResult)
	}

	deleteCapsuleResult, err := api.GetCapsule(
		ctx, client.GetCapsuleParams{CapsuleId: resumed.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	deleteCapsuleVersion := deleteCapsuleResult.(*client.CapsuleHeaders).Response.ResourceVersion
	if _, err := api.DeleteCapsule(
		ctx,
		&client.LifecycleMutationRequest{ExpectedResourceVersion: deleteCapsuleVersion},
		client.DeleteCapsuleParams{CapsuleId: resumed.ID, IdempotencyKey: "delete-key"},
	); err != nil {
		t.Fatal(err)
	}
	waitState(t, ctx, api, ready.ID, client.CapsuleStateDeleted)
}

func waitState(
	t *testing.T,
	ctx context.Context,
	api *client.Client,
	id string,
	want client.CapsuleState,
) client.Capsule {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err := api.GetCapsule(ctx, client.GetCapsuleParams{CapsuleId: id})
		if err != nil {
			t.Fatal(err)
		}
		response, ok := result.(*client.CapsuleHeaders)
		if !ok {
			t.Fatalf("get capsule response = %T", result)
		}
		if response.Response.State == want {
			return response.Response
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want %s", response.Response.State, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
