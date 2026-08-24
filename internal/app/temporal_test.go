package app_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/artifacts"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/provider/fake"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

type temporalRuntime struct {
	mu         sync.Mutex
	restores   map[string][]byte
	captures   int
	captureErr error
	restoreErr error
}

func (r *temporalRuntime) CaptureWorkspace(context.Context, string) (ports.WorkspaceCapture, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.captures++
	if r.captureErr != nil {
		return ports.WorkspaceCapture{}, r.captureErr
	}
	return ports.WorkspaceCapture{
		Archive: io.NopCloser(bytes.NewReader([]byte("deterministic-workspace"))),
		Metadata: ports.SnapshotMetadata{
			ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			GitBranch:   "main", GitHEAD: "0123456789abcdef", GitDirtySummary: " M file",
		},
	}, nil
}

func (r *temporalRuntime) RestoreWorkspace(
	_ context.Context,
	resourceID, _ string,
	_ int64,
	reader io.Reader,
) error {
	r.mu.Lock()
	if r.restoreErr != nil {
		err := r.restoreErr
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	value, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.restores == nil {
		r.restores = make(map[string][]byte)
	}
	r.restores[resourceID] = value
	return nil
}

type setupCacheProvider struct {
	*fake.Provider
	mu       sync.Mutex
	requests map[domain.CapsuleID]ports.CreateCapsuleRequest
}

func newSetupCacheProvider() *setupCacheProvider {
	return &setupCacheProvider{
		Provider: fake.New(fake.Options{}),
		requests: make(map[domain.CapsuleID]ports.CreateCapsuleRequest),
	}
}

func (p *setupCacheProvider) Capabilities(context.Context) (ports.ProviderCapabilities, error) {
	return ports.ProviderCapabilities{Pause: true, Snapshot: true}, nil
}

func (p *setupCacheProvider) Create(
	ctx context.Context,
	request ports.CreateCapsuleRequest,
) (ports.ProviderResource, error) {
	p.mu.Lock()
	p.requests[request.CapsuleID] = request
	p.mu.Unlock()
	return p.Provider.Create(ctx, request)
}

func TestTemporalCaptureDescendantsRewindAndSeal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifactDataDir := t.TempDir()
	artifactStore, err := artifacts.Open(artifactDataDir)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := fake.New(fake.Options{})
	snapshotter := &temporalRuntime{}
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.ConfigureTemporal(snapshotter, artifactStore)
	reconciler.Start(ctx)
	defer reconciler.Close()
	service := app.NewService(store, clock, ids, reconciler)
	service.ConfigureTemporal(snapshotter, artifactStore)

	project, err := service.CreateProject(ctx, "project", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := service.CreateCapsule(ctx, project.ID, "source", "capsule-key")
	if err != nil {
		t.Fatal(err)
	}
	capsule = waitReady(t, service, capsule.ID)
	moment, err := service.CaptureMoment(ctx, capsule.ID, "checkpoint", capsule.ResourceVersion, "capture-key")
	if err != nil {
		t.Fatal(err)
	}
	rootManifest, rootBytes := readManifest(t, artifactStore, moment)
	assertManifestMatchesMoment(t, rootManifest, moment)
	if rootManifest.ParentMomentID != "" || rootManifest.Final {
		t.Fatalf("root manifest lineage = %#v", rootManifest)
	}
	capsule, err = service.GetCapsule(ctx, capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.CaptureMoment(
		ctx, capsule.ID, "second checkpoint", capsule.ResourceVersion, "capture-child-key",
	)
	if err != nil {
		t.Fatal(err)
	}
	childManifest, _ := readManifest(t, artifactStore, child)
	assertManifestMatchesMoment(t, childManifest, child)
	if child.ParentMomentID != moment.ID || childManifest.ParentMomentID != moment.ID ||
		child.ArchiveSHA256 != moment.ArchiveSHA256 ||
		child.ManifestSHA256 == moment.ManifestSHA256 ||
		!child.CreatedAt.After(moment.CreatedAt) {
		t.Fatalf("child manifest lineage/dedup mismatch: root=%#v child=%#v", moment, child)
	}
	deduplicated, err := artifactStore.Publish(ctx, bytes.NewReader(rootBytes))
	if err != nil {
		t.Fatal(err)
	}
	if deduplicated.Digest != moment.ManifestSHA256 {
		t.Fatalf("unchanged manifest digest = %s, want %s", deduplicated.Digest, moment.ManifestSHA256)
	}
	replay, err := service.CaptureMoment(ctx, capsule.ID, "ignored", capsule.ResourceVersion, "capture-key")
	if err != nil || replay.ID != moment.ID {
		t.Fatalf("capture replay = %#v, %v", replay, err)
	}
	shardOne, err := service.CreateShard(ctx, moment.ID, "shard-one", "shard-one-key")
	if err != nil {
		t.Fatal(err)
	}
	shardTwo, err := service.CreateShard(ctx, moment.ID, "shard-two", "shard-two-key")
	if err != nil {
		t.Fatal(err)
	}
	one := waitReady(t, service, shardOne.Capsule.ID)
	two := waitReady(t, service, shardTwo.Capsule.ID)
	if one.TimelineID == two.TimelineID || !one.RestoreComplete || !two.RestoreComplete {
		t.Fatalf("Shard identity/restore mismatch: %#v %#v", one, two)
	}
	rewound, err := service.Rewind(ctx, capsule.ID, moment.ID, "rewound", "rewind-key")
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, service, rewound.Capsule.ID)
	view, err := service.GetTimeline(ctx, rewound.Timeline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Ancestry) != 2 || view.Ancestry[0].Reason != domain.TimelineRewind ||
		view.Ancestry[1].ID != capsule.TimelineID {
		t.Fatalf("ancestry = %#v", view.Ancestry)
	}
	capsule, err = service.GetCapsule(ctx, capsule.ID)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := service.Seal(ctx, capsule.ID, capsule.ResourceVersion, "seal-key")
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Capsule.State != domain.CapsuleSealed || !sealed.Moment.Final {
		t.Fatalf("seal result = %#v", sealed)
	}
	finalManifest, _ := readManifest(t, artifactStore, sealed.Moment)
	assertManifestMatchesMoment(t, finalManifest, sealed.Moment)
	if !finalManifest.Final || finalManifest.ParentMomentID != child.ID ||
		sealed.Moment.ManifestSHA256 == child.ManifestSHA256 {
		t.Fatalf("final manifest = %#v", finalManifest)
	}
	manifestPath := filepath.Join(
		artifactDataDir, "artifacts", "sha256",
		sealed.Moment.ManifestSHA256[:2], sealed.Moment.ManifestSHA256,
	)
	if err := os.WriteFile(manifestPath, []byte("corrupt manifest"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := artifactStore.Open(ctx, sealed.Moment.ManifestSHA256); !errors.Is(err, domain.ErrCorrupt) {
		t.Fatalf("corrupt manifest open error = %v", err)
	}
	replayedSeal, err := service.Seal(ctx, capsule.ID, capsule.ResourceVersion, "seal-key")
	if err != nil || replayedSeal.Moment.ID != sealed.Moment.ID {
		t.Fatalf("seal replay = %#v, %v", replayedSeal, err)
	}
	if _, err := service.DeleteCapsule(
		ctx, capsule.ID, sealed.Capsule.ResourceVersion, "delete-sealed",
	); err == nil {
		t.Fatal("sealed Capsule accepted deletion")
	}
}

func TestSetupMomentCacheReuseIsolationAndFailureSemantics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifactStore, err := artifacts.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC)}
	ids := &testIDs{}
	provider := newSetupCacheProvider()
	snapshotter := &temporalRuntime{}
	reconciler := app.NewReconciler(store, provider, clock, ids)
	reconciler.ConfigureTemporal(snapshotter, artifactStore)
	reconciler.Start(ctx)
	defer reconciler.Close()
	service := app.NewService(store, clock, ids, reconciler)
	service.ConfigureTemporal(snapshotter, artifactStore)

	config := app.ProjectConfiguration{
		RepositoryURL:  "https://example.test/repository.git",
		Setup:          []string{"sh", "-c", "make setup"},
		ImageReference: "example/image:stable",
	}
	project, err := service.CreateProjectConfigured(ctx, "cached", config, "project-cache")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateCapsule(ctx, project.ID, "first", "capsule-first")
	if err != nil {
		t.Fatal(err)
	}
	first = waitReady(t, service, first.ID)
	configHash := testProjectConfigurationHash(t, project)
	var cache domain.SetupMomentCache
	var internal domain.Moment
	if err := store.View(ctx, func(reader ports.Reader) error {
		var err error
		cache, err = reader.GetSetupMomentCache(ctx, project.ID, configHash)
		if err != nil {
			return err
		}
		internal, err = reader.GetMoment(ctx, cache.MomentID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if internal.Kind != domain.MomentSetupCache || internal.CapsuleID != first.ID {
		t.Fatalf("setup cache Moment = %#v", internal)
	}
	if _, err := service.GetMoment(ctx, internal.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("internal Moment lookup = %v", err)
	}
	page, err := service.ListMoments(ctx, first.ID, 0, 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("timeline exposed setup cache: %#v, %v", page, err)
	}

	second, err := service.CreateCapsule(ctx, project.ID, "second", "capsule-second")
	if err != nil {
		t.Fatal(err)
	}
	second = waitReady(t, service, second.ID)
	provider.mu.Lock()
	firstRequest := provider.requests[first.ID]
	secondRequest := provider.requests[second.ID]
	provider.mu.Unlock()
	if firstRequest.Restore || firstRequest.RepositoryURL != config.RepositoryURL ||
		!secondRequest.Restore || secondRequest.RepositoryURL != "" ||
		len(secondRequest.Setup) != 0 || secondRequest.ImageReference != internal.ImageDigest {
		t.Fatalf("setup cache create requests: first=%#v second=%#v", firstRequest, secondRequest)
	}
	snapshotter.mu.Lock()
	captures := snapshotter.captures
	restored := append([]byte(nil), snapshotter.restores["fake-"+string(second.ID)]...)
	snapshotter.mu.Unlock()
	if captures != 1 || string(restored) != "deterministic-workspace" {
		t.Fatalf("cache captures=%d restored=%q", captures, restored)
	}

	snapshotter.mu.Lock()
	snapshotter.restoreErr = errors.New("restore rejected")
	snapshotter.mu.Unlock()
	failing, err := service.CreateCapsule(ctx, project.ID, "restore-fails", "capsule-restore-fails")
	if err != nil {
		t.Fatal(err)
	}
	failing = waitFailed(t, service, failing.ID)
	if !strings.Contains(failing.Failure, "restore rejected") {
		t.Fatalf("restore failure = %q", failing.Failure)
	}

	snapshotter.mu.Lock()
	snapshotter.restoreErr = nil
	snapshotter.captureErr = errors.New("capture unavailable")
	snapshotter.mu.Unlock()
	other, err := service.CreateProjectConfigured(ctx, "isolated", config, "project-isolated")
	if err != nil {
		t.Fatal(err)
	}
	uncached, err := service.CreateCapsule(ctx, other.ID, "uncached", "capsule-uncached")
	if err != nil {
		t.Fatal(err)
	}
	uncached = waitReady(t, service, uncached.ID)
	provider.mu.Lock()
	uncachedRequest := provider.requests[uncached.ID]
	provider.mu.Unlock()
	if uncachedRequest.Restore {
		t.Fatal("setup cache was reused across Projects")
	}
	if err := store.View(ctx, func(reader ports.Reader) error {
		_, err := reader.GetSetupMomentCache(ctx, other.ID, testProjectConfigurationHash(t, other))
		return err
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("capture failure unexpectedly installed cache: %v", err)
	}
}

func testProjectConfigurationHash(t *testing.T, project domain.Project) string {
	t.Helper()
	value, err := json.Marshal(struct {
		RepositoryURL  string   `json:"repositoryUrl"`
		Setup          []string `json:"setup"`
		ImageReference string   `json:"imageReference"`
		GitSecretName  string   `json:"gitSecretName"`
	}{
		RepositoryURL:  project.RepositoryURL,
		Setup:          project.Setup,
		ImageReference: project.ImageReference,
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func waitFailed(t *testing.T, service *app.Service, id domain.CapsuleID) domain.Capsule {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		capsule, err := service.GetCapsule(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if capsule.State == domain.CapsuleFailed {
			return capsule
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Capsule %s did not fail", id)
	return domain.Capsule{}
}

type decodedMomentManifest struct {
	Version          string            `json:"version"`
	ArchiveSHA256    string            `json:"archiveSha256"`
	ArchiveSize      int64             `json:"archiveSize"`
	SourceCapsuleID  domain.CapsuleID  `json:"sourceCapsuleId"`
	SourceTimelineID domain.TimelineID `json:"sourceTimelineId"`
	ParentMomentID   domain.MomentID   `json:"parentMomentId,omitempty"`
	ImageDigest      string            `json:"imageDigest"`
	ProjectSetupHash string            `json:"projectSetupHash"`
	GitBranch        string            `json:"gitBranch,omitempty"`
	GitHEAD          string            `json:"gitHead,omitempty"`
	GitDirtySummary  string            `json:"gitDirtySummary,omitempty"`
	CapturedAt       time.Time         `json:"capturedAt"`
	Final            bool              `json:"final"`
}

func readManifest(
	t *testing.T,
	store ports.ArtifactStore,
	moment domain.Moment,
) (decodedMomentManifest, []byte) {
	t.Helper()
	reader, size, err := store.Open(context.Background(), moment.ManifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	value, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read manifest: %v, close: %v", readErr, closeErr)
	}
	if int64(len(value)) != size {
		t.Fatalf("manifest size = %d, want %d", len(value), size)
	}
	var manifest decodedMomentManifest
	if err := json.Unmarshal(value, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest, value
}

func assertManifestMatchesMoment(
	t *testing.T,
	manifest decodedMomentManifest,
	moment domain.Moment,
) {
	t.Helper()
	if manifest.Version != "meridian.moment.v1" ||
		manifest.ArchiveSHA256 != moment.ArchiveSHA256 ||
		manifest.ArchiveSize != moment.ArchiveSize ||
		manifest.SourceCapsuleID != moment.CapsuleID ||
		manifest.SourceTimelineID != moment.TimelineID ||
		manifest.ParentMomentID != moment.ParentMomentID ||
		manifest.ImageDigest != moment.ImageDigest ||
		manifest.ProjectSetupHash != moment.ProjectSetupHash ||
		manifest.GitBranch != moment.GitBranch ||
		manifest.GitHEAD != moment.GitHEAD ||
		manifest.GitDirtySummary != moment.GitDirtySummary ||
		!manifest.CapturedAt.Equal(moment.CreatedAt) ||
		manifest.Final != moment.Final {
		t.Fatalf("manifest does not match Moment: manifest=%#v moment=%#v", manifest, moment)
	}
}

func waitReady(t *testing.T, service *app.Service, id domain.CapsuleID) domain.Capsule {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		capsule, err := service.GetCapsule(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if capsule.State == domain.CapsuleReady {
			return capsule
		}
		if capsule.State == domain.CapsuleFailed {
			t.Fatalf("Capsule failed: %s", capsule.Failure)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Capsule %s did not become Ready", id)
	return domain.Capsule{}
}
