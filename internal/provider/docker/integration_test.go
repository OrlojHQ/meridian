package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/artifacts"
	"github.com/OrlojHQ/meridian/internal/capsuleproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/harnesssetup"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/provider/contract"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	"github.com/OrlojHQ/meridian/internal/system"
	"github.com/OrlojHQ/meridian/internal/transcripts"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

func TestDockerProviderIntegrationMomentsLineageAndSeal(t *testing.T) {
	if os.Getenv("MERIDIAN_DOCKER_TEST") != "1" {
		t.Skip("MERIDIAN_DOCKER_TEST=1 is not set")
	}
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	if image == "" {
		image = "meridian-capsule-integration:dev"
	}
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, path.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifactStore, err := artifacts.Open(path.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(Config{
		Image: image, StateDir: path.Join(root, "provider"),
		OperationTimeout: 30 * time.Second, SetupTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	reconciler := app.NewReconciler(store, provider, system.Clock{}, system.IDs{})
	reconciler.ConfigureTemporal(provider, artifactStore)
	reconciler.Start(ctx)
	defer reconciler.Close()
	service := app.NewService(store, system.Clock{}, system.IDs{}, reconciler, provider)
	service.ConfigureTemporal(provider, artifactStore)
	transcriptKey, err := transcripts.NewInstallationKey("docker-integration", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer transcriptKey.Zero()
	service.ConfigureThreads(provider, transcriptKey)
	var resources []domain.CapsuleID
	t.Cleanup(func() {
		for _, capsuleID := range resources {
			_ = provider.Delete(context.Background(), provider.resourceNames(capsuleID).container)
		}
	})
	project, err := service.CreateProjectConfigured(ctx, "moments", app.ProjectConfiguration{
		RepositoryURL: "file:///fixture", ImageReference: image,
	}, "moments-project")
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.CreateCapsule(ctx, project.ID, "source", "moments-source")
	if err != nil {
		t.Fatal(err)
	}
	resources = append(resources, source.ID)
	source = waitCapsuleReady(t, service, source.ID)
	threadResult, err := service.CreateThread(ctx, app.CreateThreadInput{
		CapsuleID: source.ID, Harness: "mock-structured", FirstMessage: "first turn",
		Start: true, IdempotencyKey: "docker-thread-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstPage := waitThreadBlock(t, service, threadResult.Thread.ID, 0, func(block app.ThreadBlockView) bool {
		return block.Content == "mock:first turn"
	})
	var sawToolStart, sawToolResult bool
	for _, block := range firstPage.Items {
		if block.Event == nil {
			continue
		}
		sawToolStart = sawToolStart || block.Event.Type == adapterproto.KindToolStart
		sawToolResult = sawToolResult || block.Event.Type == adapterproto.KindToolResult
	}
	if !sawToolStart || !sawToolResult {
		t.Fatalf("structured tool round trip = %#v", firstPage.Items)
	}
	reconnectCursor := firstPage.NextCursor
	currentThread, err := service.GetThread(ctx, threadResult.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendThreadMessage(
		ctx, currentThread.ID, currentThread.ResourceVersion,
		"second turn", "docker-thread-second",
	); err != nil {
		t.Fatal(err)
	}
	secondPage := waitThreadBlock(
		t, service, currentThread.ID, reconnectCursor,
		func(block app.ThreadBlockView) bool { return block.Content == "mock:second turn" },
	)
	if secondPage.NextCursor <= reconnectCursor {
		t.Fatalf("Thread replay cursor = %d after %d", secondPage.NextCursor, reconnectCursor)
	}
	currentThread, err = service.GetThread(ctx, currentThread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SendThreadMessage(
		ctx, currentThread.ID, currentThread.ResourceVersion,
		"permission request", "docker-thread-permission",
	); err != nil {
		t.Fatal(err)
	}
	permissionPage := waitThreadBlock(
		t, service, currentThread.ID, secondPage.NextCursor,
		func(block app.ThreadBlockView) bool {
			return block.Event != nil &&
				block.Event.Type == adapterproto.KindPermissionRequest &&
				block.Event.MessageID != ""
		},
	)
	var permissionID string
	for _, block := range permissionPage.Items {
		if block.Event != nil && block.Event.Type == adapterproto.KindPermissionRequest {
			permissionID = block.Event.MessageID
		}
	}
	currentThread, err = service.GetThread(ctx, currentThread.ID)
	if err != nil {
		t.Fatal(err)
	}
	allow := "allow"
	if _, err := service.RespondThread(
		ctx, currentThread.ID, currentThread.ResourceVersion,
		permissionID, &allow, nil, "docker-thread-allow",
	); err != nil {
		t.Fatal(err)
	}
	waitThreadBlock(
		t, service, currentThread.ID, permissionPage.NextCursor,
		func(block app.ThreadBlockView) bool { return block.Content == "mock:permission:allow" },
	)
	currentThread, err = service.GetThread(ctx, currentThread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelThread(
		ctx, currentThread.ID, currentThread.ResourceVersion, "docker-thread-cancel",
	); err != nil {
		t.Fatal(err)
	}
	currentThread = waitThreadPaused(t, service, currentThread.ID)
	if _, err := service.ArchiveThread(
		ctx, currentThread.ID, currentThread.ResourceVersion, "docker-thread-archive",
	); err != nil {
		t.Fatal(err)
	}
	source, err = service.GetCapsule(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	moment, err := service.CaptureMoment(
		ctx, source.ID, "clean fixture", source.ResourceVersion, "moments-capture",
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateShard(ctx, moment.ID, "first", "moments-shard-first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateShard(ctx, moment.ID, "second", "moments-shard-second")
	if err != nil {
		t.Fatal(err)
	}
	resources = append(resources, first.Capsule.ID, second.Capsule.ID)
	firstCapsule := waitCapsuleReady(t, service, first.Capsule.ID)
	secondCapsule := waitCapsuleReady(t, service, second.Capsule.ID)
	run, err := service.StartRun(
		ctx, firstCapsule.ID, "mock-jsonl", "diverge first", "moments-first-run", 0, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	for !run.State.Terminal() {
		run, err = service.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	firstStatus, err := service.GitStatus(ctx, firstCapsule.ID)
	if err != nil || !strings.Contains(firstStatus.Content, "fixture.txt") {
		t.Fatalf("first status = %#v, %v", firstStatus, err)
	}
	secondStatus, err := service.GitStatus(ctx, secondCapsule.ID)
	if err != nil || strings.Contains(secondStatus.Content, "fixture.txt") {
		t.Fatalf("second Capsule observed cross-write: %#v, %v", secondStatus, err)
	}
	rewound, err := service.Rewind(
		ctx, source.ID, moment.ID, "rewound", "moments-rewind",
	)
	if err != nil {
		t.Fatal(err)
	}
	resources = append(resources, rewound.Capsule.ID)
	waitCapsuleReady(t, service, rewound.Capsule.ID)
	view, err := service.GetTimeline(ctx, rewound.Timeline.ID)
	if err != nil || len(view.Ancestry) != 2 || view.Ancestry[1].ID != source.TimelineID {
		t.Fatalf("rewind ancestry = %#v, %v", view, err)
	}
	source, err = service.GetCapsule(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := service.Seal(ctx, source.ID, source.ResourceVersion, "moments-seal")
	if err != nil || !sealed.Moment.Final || sealed.Capsule.State != domain.CapsuleSealed {
		t.Fatalf("seal = %#v, %v", sealed, err)
	}
	if _, err := service.StartRun(ctx, source.ID, "mock-jsonl", "no", "sealed-run", 0, 0); err == nil {
		t.Fatal("sealed Capsule accepted Run")
	}
	if _, err := service.DeleteCapsule(
		ctx, source.ID, sealed.Capsule.ResourceVersion, "sealed-delete",
	); err == nil {
		t.Fatal("sealed Capsule accepted deletion")
	}
}

func waitCapsuleReady(t *testing.T, service *app.Service, id domain.CapsuleID) domain.Capsule {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		capsule, err := service.GetCapsule(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if capsule.State == domain.CapsuleReady {
			return capsule
		}
		if capsule.State == domain.CapsuleFailed || time.Now().After(deadline) {
			t.Fatalf("Capsule readiness = %#v", capsule)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitThreadBlock(
	t *testing.T,
	service *app.Service,
	id domain.ThreadID,
	after int64,
	match func(app.ThreadBlockView) bool,
) app.ThreadBlockPage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		page, err := service.ThreadBlocks(context.Background(), id, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range page.Items {
			if match(block) {
				return page
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("matching Thread block did not arrive: %#v", page)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitThreadPaused(t *testing.T, service *app.Service, id domain.ThreadID) domain.Thread {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, _ = service.ThreadBlocks(context.Background(), id, 0, 100)
		thread, err := service.GetThread(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if thread.State == domain.ThreadPaused {
			return thread
		}
		if time.Now().After(deadline) {
			t.Fatalf("Thread did not pause after terminal session: %#v", thread)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDockerProviderIntegrationControlPlaneRestartRecoversRunningRun(t *testing.T) {
	if os.Getenv("MERIDIAN_DOCKER_TEST") != "1" {
		t.Skip("MERIDIAN_DOCKER_TEST=1 is not set")
	}
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	if image == "" {
		image = "meridian-capsule-integration:dev"
	}
	root := t.TempDir()
	config := Config{
		Image: image, StateDir: path.Join(root, "provider"),
		OperationTimeout: 30 * time.Second, SetupTimeout: time.Minute,
	}
	ctx, cancel := context.WithCancel(context.Background())
	store, err := sqlite.Open(ctx, path.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	reconciler := app.NewReconciler(store, provider, system.Clock{}, system.IDs{})
	reconciler.Start(ctx)
	service := app.NewService(store, system.Clock{}, system.IDs{}, reconciler, provider)
	project, err := service.CreateProjectConfigured(ctx, "restart", app.ProjectConfiguration{
		RepositoryURL: "file:///fixture", ImageReference: image,
	}, "restart-project")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "restart", "restart-capsule")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for capsule.State != domain.CapsuleReady {
		capsule, err = service.GetCapsule(ctx, capsule.ID)
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("Capsule readiness = %#v, %v", capsule, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	run, err := service.StartRun(ctx, capsule.ID, "mock-long", "long", "restart-run", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for run.State != domain.RunRunning {
		run, err = service.GetRun(ctx, run.ID)
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("Run start = %#v, %v", run, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	resourceID := capsule.ProviderResourceID
	cancel()
	reconciler.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}

	restartCtx, restartCancel := context.WithCancel(context.Background())
	defer restartCancel()
	store, err = sqlite.Open(restartCtx, path.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	t.Cleanup(func() {
		cleanup, cleanupErr := New(config)
		if cleanupErr == nil {
			defer cleanup.Close()
			_ = cleanup.Delete(context.Background(), resourceID)
		}
	})
	reconciler = app.NewReconciler(store, provider, system.Clock{}, system.IDs{})
	reconciler.Start(restartCtx)
	defer reconciler.Close()
	service = app.NewService(store, system.Clock{}, system.IDs{}, reconciler, provider)
	if err := reconciler.Recover(restartCtx); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverRuns(restartCtx); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.GetRun(restartCtx, run.ID)
	if err != nil || recovered.State != domain.RunRunning {
		t.Fatalf("recovered Run = %#v, %v", recovered, err)
	}
	cancelled, err := service.CancelRun(
		restartCtx, recovered.ID, recovered.ResourceVersion, "restart-cancel",
	)
	if err != nil {
		t.Fatalf("cancel recovered Run = %#v, %v", cancelled, err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for cancelled.State != domain.RunCancelled {
		cancelled, err = service.GetRun(restartCtx, recovered.ID)
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("cancel recovered Run = %#v, %v", cancelled, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDockerProviderIntegrationRunsPTYGitAndCancellation(t *testing.T) {
	if os.Getenv("MERIDIAN_DOCKER_TEST") != "1" {
		t.Skip("MERIDIAN_DOCKER_TEST=1 is not set")
	}
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	if image == "" {
		image = "meridian-capsule-integration:dev"
	}
	provider, err := New(Config{
		Image: image, StateDir: t.TempDir(), OperationTimeout: 30 * time.Second,
		SetupTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	capsuleID := domain.CapsuleID(fmt.Sprintf("run-integration-%d", time.Now().UnixNano()))
	resource, err := provider.Create(context.Background(), ports.CreateCapsuleRequest{
		CapsuleID: capsuleID, RepositoryURL: "file:///fixture", ImageReference: image,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Delete(context.Background(), resource.ID) })

	runID := domain.RunID("mock-jsonl")
	if _, err := provider.StartRun(context.Background(), ports.RuntimeRunRequest{
		RunID: runID, ResourceID: resource.ID, Harness: "mock-jsonl", Prompt: "edit",
	}); err != nil {
		t.Fatal(err)
	}
	run := waitRuntimeRun(t, provider, resource.ID, runID)
	if run.State != domain.RunSucceeded || !run.HasExit || run.ExitStatus != 0 {
		t.Fatalf("Run = %#v", run)
	}
	events, err := provider.RunEvents(context.Background(), resource.ID, runID, 0)
	if err != nil || len(events.Items) == 0 {
		t.Fatalf("events = %#v, %v", events, err)
	}
	status, err := provider.GitStatus(context.Background(), resource.ID)
	if err != nil || !strings.Contains(status.Content, "fixture.txt") {
		t.Fatalf("status = %#v, %v", status, err)
	}
	diff, err := provider.GitDiff(context.Background(), resource.ID)
	if err != nil || !strings.Contains(diff.Content, "mock edit complete") {
		t.Fatalf("diff = %#v, %v", diff, err)
	}

	ptyID := domain.RunID("mock-pty")
	if _, err := provider.StartRun(context.Background(), ports.RuntimeRunRequest{
		RunID: ptyID, ResourceID: resource.ID, Harness: "mock-pty", Columns: 80, Rows: 24,
	}); err != nil {
		t.Fatal(err)
	}
	waitRuntimeState(t, provider, resource.ID, ptyID, domain.RunRunning)
	attachment, err := provider.AttachRun(context.Background(), resource.ID, ptyID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachment.Write(context.Background(), false, []byte(`{"type":"resize","columns":100,"rows":40}`)); err != nil {
		t.Fatal(err)
	}
	if err := attachment.Write(context.Background(), true, []byte("interactive\n")); err != nil {
		t.Fatal(err)
	}
	cursor := readRuntimeOutput(t, attachment, "started")
	_ = attachment.Close()
	waitRuntimeRun(t, provider, resource.ID, ptyID)
	reconnected, err := provider.AttachRun(context.Background(), resource.ID, ptyID, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if next := readRuntimeOutput(t, reconnected, "completed"); next <= cursor {
		t.Fatalf("replay cursor = %d after %d", next, cursor)
	}
	_ = reconnected.Close()

	previewID := domain.RunID("mock-preview")
	if _, err := provider.StartRun(context.Background(), ports.RuntimeRunRequest{
		RunID: previewID, ResourceID: resource.ID, Harness: "mock-preview",
	}); err != nil {
		t.Fatal(err)
	}
	waitRuntimeState(t, provider, resource.ID, previewID, domain.RunRunning)
	deadline := time.Now().Add(5 * time.Second)
	var previewPort uint16
	for previewPort == 0 {
		discovered, err := provider.DiscoverPreviewPorts(context.Background(), resource.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range discovered {
			if item.Port == 3456 {
				previewPort = item.Port
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("preview port was not discovered: %#v", discovered)
		}
		time.Sleep(20 * time.Millisecond)
	}
	previewRequest, _ := http.NewRequest(
		http.MethodGet, "http://preview.invalid/health?scope=capsule", nil,
	)
	previewResponse, err := provider.ForwardPreviewHTTP(
		context.Background(), resource.ID, previewPort, previewRequest,
	)
	if err != nil || previewResponse.StatusCode != http.StatusOK ||
		string(previewResponse.Body) != "preview /health?scope=capsule" {
		t.Fatalf("preview response = %#v, %v", previewResponse, err)
	}
	if _, err := provider.CancelRun(context.Background(), resource.ID, previewID); err != nil {
		t.Fatal(err)
	}
	waitRuntimeRun(t, provider, resource.ID, previewID)

	longID := domain.RunID("mock-long")
	if _, err := provider.StartRun(context.Background(), ports.RuntimeRunRequest{
		RunID: longID, ResourceID: resource.ID, Harness: "mock-long", Prompt: "long",
	}); err != nil {
		t.Fatal(err)
	}
	waitRuntimeState(t, provider, resource.ID, longID, domain.RunRunning)
	if _, err := provider.CancelRun(context.Background(), resource.ID, longID); err != nil {
		t.Fatal(err)
	}
	if cancelled := waitRuntimeRun(t, provider, resource.ID, longID); cancelled.State != domain.RunCancelled {
		t.Fatalf("cancelled Run = %#v", cancelled)
	}

	structuredID := domain.RunID("mock-structured")
	if _, err := provider.StartStructured(context.Background(), ports.RuntimeStructuredStartRequest{
		ResourceID: resource.ID, RunID: structuredID, Harness: "mock-structured",
		Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindStart,
			ID: "start-structured", SessionID: "session-structured",
		},
	}); err != nil {
		t.Fatal(err)
	}
	structuredDeadline := time.Now().Add(5 * time.Second)
	for {
		run, err := provider.GetStructured(context.Background(), resource.ID, structuredID)
		if err == nil && run.State == domain.RunRunning {
			break
		}
		if err != nil || time.Now().After(structuredDeadline) {
			t.Fatalf("structured start = %#v, %v", run, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := provider.SendStructured(context.Background(), ports.RuntimeStructuredSendRequest{
		ResourceID: resource.ID, RunID: structuredID,
		Frame: adapterproto.Frame{
			Protocol: adapterproto.Version, Type: adapterproto.KindUserMessage,
			ID: "message-structured", SessionID: "session-structured",
			Role: adapterproto.RoleUser, Content: "multi-turn",
		},
	}); err != nil {
		t.Fatal(err)
	}
	var sawDelta, sawFinal, sawTool bool
	for !(sawDelta && sawFinal && sawTool) {
		events, err := provider.StructuredEvents(
			context.Background(), resource.ID, structuredID, 0,
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events.Items {
			switch event.Frame.Type {
			case adapterproto.KindAssistantDelta:
				sawDelta = true
			case adapterproto.KindAssistantMessage:
				sawFinal = true
			case adapterproto.KindToolResult:
				sawTool = true
			}
		}
		if time.Now().After(structuredDeadline) {
			t.Fatalf("structured events = %#v", events)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := provider.CancelStructured(
		context.Background(), resource.ID, structuredID,
	); err != nil {
		t.Fatal(err)
	}
}

func TestDockerProviderIntegrationContract(t *testing.T) {
	if os.Getenv("MERIDIAN_DOCKER_TEST") != "1" {
		t.Skip("MERIDIAN_DOCKER_TEST=1 is not set")
	}
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	if image == "" {
		image = "meridian-capsule:dev"
	}
	provider, err := New(Config{Image: image, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	contract.Test(t, func() ports.CapsuleProvider { return provider })
}

func TestDockerProviderIntegrationWorkspaceRecoveryAndOwnership(t *testing.T) {
	if os.Getenv("MERIDIAN_DOCKER_TEST") != "1" {
		t.Skip("MERIDIAN_DOCKER_TEST=1 is not set")
	}
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	if image == "" {
		image = "meridian-capsule-integration:dev"
	}
	stateDirectory := t.TempDir()
	capsuleID := domain.CapsuleID(fmt.Sprintf("integration-%d", time.Now().UnixNano()))
	request := ports.CreateCapsuleRequest{
		CapsuleID:      capsuleID,
		RepositoryURL:  "file:///fixture",
		Setup:          []string{"/usr/bin/touch", "setup-complete"},
		ImageReference: image,
	}
	config := Config{
		Image: image, StateDir: stateDirectory,
		OperationTimeout: 30 * time.Second, SetupTimeout: time.Minute,
	}

	first, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	unlabeled := "meridian-integration-unlabeled-" + fmt.Sprint(time.Now().UnixNano())
	labeled := "meridian-integration-labeled-" + fmt.Sprint(time.Now().UnixNano())
	if _, err := first.owned.VolumeCreate(context.Background(), client.VolumeCreateOptions{Name: unlabeled}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.owned.VolumeCreate(context.Background(), client.VolumeCreateOptions{
		Name: labeled, Labels: first.labels("unrelated-capsule", "workspace", ""),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cleanupErr := New(config)
		if cleanupErr == nil {
			defer cleanup.Close()
			_, _ = cleanup.owned.VolumeRemove(
				context.Background(), unlabeled, client.VolumeRemoveOptions{Force: true},
			)
			_, _ = cleanup.owned.VolumeRemove(
				context.Background(), labeled, client.VolumeRemoveOptions{Force: true},
			)
			_ = cleanup.Delete(context.Background(), cleanup.resourceNames(capsuleID).container)
		}
	})

	resource, err := first.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resource.State != ports.ProviderReady || !strings.HasPrefix(resource.ImageDigest, "sha256:") {
		t.Fatalf("created resource = %#v", resource)
	}
	assertWorkspace(t, first, resource.ID)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	adopted, err := restarted.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.ID != resource.ID || adopted.State != ports.ProviderReady {
		t.Fatalf("adopted resource = %#v, want %#v", adopted, resource)
	}
	paused, err := restarted.Pause(context.Background(), adopted.ID)
	if err != nil || paused.State != ports.ProviderPaused {
		t.Fatalf("pause = %#v, %v", paused, err)
	}
	resumed, err := restarted.Resume(context.Background(), adopted.ID)
	if err != nil || resumed.State != ports.ProviderReady {
		t.Fatalf("resume = %#v, %v", resumed, err)
	}
	assertWorkspace(t, restarted, adopted.ID)
	if err := restarted.Delete(context.Background(), adopted.ID); err != nil {
		t.Fatal(err)
	}

	names := restarted.resourceNames(capsuleID)
	if _, err := restarted.owned.ContainerInspect(
		context.Background(), names.container, client.ContainerInspectOptions{},
	); !errdefs.IsNotFound(err) {
		t.Fatalf("owned container still exists: %v", err)
	}
	if _, err := restarted.owned.NetworkInspect(
		context.Background(), names.network, client.NetworkInspectOptions{},
	); !errdefs.IsNotFound(err) {
		t.Fatalf("owned network still exists: %v", err)
	}
	if _, err := restarted.owned.VolumeInspect(
		context.Background(), names.volume, client.VolumeInspectOptions{},
	); !errdefs.IsNotFound(err) {
		t.Fatalf("owned volume still exists: %v", err)
	}
	for _, name := range []string{unlabeled, labeled} {
		if _, err := restarted.owned.VolumeInspect(
			context.Background(), name, client.VolumeInspectOptions{},
		); err != nil {
			t.Fatalf("unrelated volume %q was touched: %v", name, err)
		}
	}
}

func assertWorkspace(t *testing.T, provider *Provider, containerID string) {
	t.Helper()
	result, err := provider.owned.CopyFromContainer(
		context.Background(), containerID,
		client.CopyFromContainerOptions{SourcePath: workspace},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer result.Content.Close()
	reader := tar.NewReader(result.Content)
	found := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := path.Base(header.Name)
		if name != "fixture.txt" && name != "setup-complete" {
			continue
		}
		if header.Uid != 10001 || header.Gid != 10001 {
			t.Fatalf("%s ownership = %d:%d", name, header.Uid, header.Gid)
		}
		if name == "fixture.txt" {
			value, err := io.ReadAll(io.LimitReader(reader, 1024))
			if err != nil {
				t.Fatal(err)
			}
			if string(value) != "deterministic fixture\n" {
				t.Fatalf("fixture content = %q", value)
			}
		}
		found[name] = true
	}
	if !found["fixture.txt"] || !found["setup-complete"] {
		t.Fatalf("workspace files = %#v", found)
	}
}

func waitRuntimeRun(
	t *testing.T,
	provider *Provider,
	resourceID string,
	runID domain.RunID,
) ports.RuntimeRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		run, err := provider.GetRun(context.Background(), resourceID, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.State.Terminal() {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("Run %s did not finish: %#v", runID, run)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitRuntimeState(
	t *testing.T,
	provider *Provider,
	resourceID string,
	runID domain.RunID,
	want domain.RunState,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := provider.GetRun(context.Background(), resourceID, runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.State == want {
			return
		}
		if run.State.Terminal() || time.Now().After(deadline) {
			t.Fatalf("Run %s state = %s, want %s", runID, run.State, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readRuntimeOutput(t *testing.T, attachment ports.RuntimeAttachment, want string) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		binary, value, err := attachment.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if binary {
			continue
		}
		var event capsuleproto.RunEvent
		if json.Unmarshal(value, &event) != nil || event.Data == "" {
			continue
		}
		output, err := base64.StdEncoding.DecodeString(event.Data)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(output), want) {
			return event.Sequence
		}
	}
}

func TestDockerProviderIntegrationPersonalSetup(t *testing.T) {
	if os.Getenv("MERIDIAN_DOCKER_TEST") != "1" {
		t.Skip("MERIDIAN_DOCKER_TEST=1 is not set")
	}
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	if image == "" {
		image = "meridian-capsule-integration:dev"
	}
	provider, err := New(Config{Image: image, StateDir: t.TempDir(), OperationTimeout: 30 * time.Second, SetupTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	ctx := context.Background()
	resource, err := provider.Create(ctx, ports.CreateCapsuleRequest{CapsuleID: domain.CapsuleID(fmt.Sprintf("setup-integration-%d", time.Now().UnixNano())), RepositoryURL: "file:///fixture", ImageReference: image})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Delete(ctx, resource.ID); err != nil {
			t.Error(err)
		}
	}()
	setup := &harnesssetup.RuntimeSetup{Revision: "integration-revision", Bundle: harnesssetup.Bundle{Harness: "codex", Dependencies: []string{"cowsay@1.6.0"}, Files: []harnesssetup.File{{Path: ".codex/AGENTS.md", Content: "personal-setup-integration"}}}}
	for i := 0; i < 2; i++ {
		id := domain.RunID(fmt.Sprintf("setup-run-%d", i))
		if _, err := provider.StartRun(ctx, ports.RuntimeRunRequest{RunID: id, ResourceID: resource.ID, Harness: "codex", Setup: setup}); err != nil {
			t.Fatal(err)
		}
		run := waitRuntimeRun(t, provider, resource.ID, id)
		if run.State != domain.RunSucceeded {
			t.Fatalf("setup run failed: %#v", run)
		}
	}
}

func TestDockerProviderIntegrationStagedPreparation(t *testing.T) {
	if os.Getenv("MERIDIAN_DOCKER_TEST") != "1" {
		t.Skip("MERIDIAN_DOCKER_TEST=1 is not set")
	}
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	if image == "" {
		image = "meridian-capsule-integration:dev"
	}
	provider, err := New(Config{Image: image, StateDir: t.TempDir(), OperationTimeout: 30 * time.Second, SetupTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	ctx := context.Background()
	create := func(suffix string) ports.ProviderResource {
		resource, err := provider.Create(ctx, ports.CreateCapsuleRequest{CapsuleID: domain.CapsuleID(fmt.Sprintf("staged-%s-%d", suffix, time.Now().UnixNano())), RepositoryURL: "file:///fixture", ImageReference: image})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := provider.Delete(context.Background(), resource.ID); err != nil {
				t.Error(err)
			}
		})
		return resource
	}
	first := create("first")
	identity, err := provider.PreparationIdentity(ctx, first.ID)
	if err != nil || len(identity.SourceRevision) != 40 || identity.Platform == "" {
		t.Fatalf("identity=%#v err=%v", identity, err)
	}
	setup := []string{"sh", "-c", "test -z \"$MERIDIAN_CAPSULE_TOKEN\" && printf prepared >> /workspace/setup-result"}
	key := strings.Repeat("a", 64)
	for i := 0; i < 2; i++ {
		if err := provider.FinishPreparation(ctx, first.ID, setup, identity.SourceRevision, key); err != nil {
			t.Fatal(err)
		}
	}
	captured, err := provider.CaptureWorkspace(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := io.ReadAll(captured.Archive)
	_ = captured.Archive.Close()
	if err != nil {
		t.Fatal(err)
	}
	second := create("second")
	sum := sha256.Sum256(archive)
	if err := provider.RestoreWorkspace(ctx, second.ID, hex.EncodeToString(sum[:]), int64(len(archive)), bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	// A successful marker prevents repeating setup after restart/retry, and a
	// separate runtime can mutate its restored workspace without changing the first.
	if err := provider.FinishPreparation(ctx, second.ID, []string{"sh", "-c", "test \"$(cat /workspace/setup-result)\" = prepared && printf second >> /workspace/setup-result"}, identity.SourceRevision, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if err := provider.FinishPreparation(ctx, first.ID, []string{"sh", "-c", "test \"$(cat /workspace/setup-result)\" = prepared"}, identity.SourceRevision, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if err := provider.FinishPreparation(ctx, first.ID, []string{"true"}, strings.Repeat("f", 40), strings.Repeat("d", 64)); err == nil {
		t.Fatal("changed source accepted")
	}
	cancelCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	started := time.Now()
	err = provider.FinishPreparation(cancelCtx, first.ID, []string{"sh", "-c", "sleep 30"}, identity.SourceRevision, strings.Repeat("e", 64))
	cancel()
	if err == nil {
		t.Fatal("canceled setup succeeded")
	}
	checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
	defer checkCancel()
	if _, err := provider.PreparationIdentity(checkCtx, first.ID); err != nil {
		t.Fatalf("setup did not release runtime after cancellation: %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("setup cancellation was not bounded")
	}

}

func TestDockerProviderIntegrationPreparedNativeCapsules(t *testing.T) {
	if os.Getenv("MERIDIAN_DOCKER_TEST") != "1" {
		t.Skip("MERIDIAN_DOCKER_TEST=1 is not set")
	}
	image := os.Getenv("MERIDIAN_CAPSULE_IMAGE")
	if image == "" {
		image = "meridian-capsule-integration:dev"
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	store, err := sqlite.Open(ctx, path.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifactStore, err := artifacts.Open(path.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := New(Config{Image: image, StateDir: path.Join(root, "provider"), OperationTimeout: 30 * time.Second, SetupTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	reconciler := app.NewReconciler(store, provider, system.Clock{}, system.IDs{})
	reconciler.ConfigureTemporal(provider, artifactStore)
	reconciler.Start(ctx)
	defer reconciler.Close()
	service := app.NewService(store, system.Clock{}, system.IDs{}, reconciler, provider)
	service.ConfigureTemporal(provider, artifactStore)
	project, err := service.CreateProjectConfigured(ctx, "prepared", app.ProjectConfiguration{RepositoryURL: "file:///fixture", ImageReference: image, HarnessImages: []domain.HarnessImage{{Name: "codex", ImageReference: image}}, Setup: []string{"sh", "-c", "printf prepared > /workspace/setup-result"}}, "prepared-project")
	if err != nil {
		t.Fatal(err)
	}
	var capsules []domain.Capsule
	for i := 0; i < 2; i++ {
		started := time.Now()
		c, err := service.CreateCapsuleForHarness(ctx, project.ID, fmt.Sprintf("native-%d", i), "codex", fmt.Sprintf("native-key-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = provider.Delete(context.Background(), provider.resourceNames(c.ID).container) })
		c = waitCapsuleReady(t, service, c.ID)
		capsules = append(capsules, c)
		if c.Preparation == nil || c.Preparation.Reused != (i == 1) {
			t.Fatalf("launch %d preparation=%#v", i, c.Preparation)
		}
		t.Logf("launch %d ready in %s (reused=%t)", i, time.Since(started), c.Preparation.Reused)
	}
	if capsules[0].ProviderResourceID == capsules[1].ProviderResourceID {
		t.Fatal("Capsules share mutable runtime")
	}
	// Invalidate preparation without replacing a user's existing Capsule.
	environment, err := service.ProjectEnvironment(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateProjectEnvironment(ctx, project.ID, app.EnvironmentMutation{Rebuild: true, ExpectedResourceVersion: environment.ResourceVersion}, "rebuild-project"); err != nil {
		t.Fatal(err)
	}
	c, err := service.CreateCapsuleForHarness(ctx, project.ID, "rebuilt", "codex", "rebuilt-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Delete(context.Background(), provider.resourceNames(c.ID).container) })
	c = waitCapsuleReady(t, service, c.ID)
	if c.Preparation == nil || c.Preparation.Reused {
		t.Fatalf("rebuild incorrectly reused: %#v", c.Preparation)
	}
}
