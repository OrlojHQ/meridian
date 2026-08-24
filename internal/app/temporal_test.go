package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	mu       sync.Mutex
	restores map[string][]byte
}

func (r *temporalRuntime) CaptureWorkspace(context.Context, string) (ports.WorkspaceCapture, error) {
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
