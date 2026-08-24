package daemon_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/daemon"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/provider/fake"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
)

func TestPreviewListenerRequiresLiteralLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		if err := daemon.ValidatePreviewListenAddress(address); err != nil {
			t.Fatalf("%s rejected: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8081", "[::]:8081", "192.0.2.1:8081", "localhost:8081", ":8081"} {
		if err := daemon.ValidatePreviewListenAddress(address); err == nil {
			t.Fatalf("%s accepted", address)
		}
	}
	if err := daemon.Run(context.Background(), daemon.Config{
		Provider: "fake", Listen: "127.0.0.1:0",
		PreviewListen: "0.0.0.0:0", DataDir: t.TempDir(),
	}); err == nil {
		t.Fatal("daemon startup accepted a non-loopback preview listener")
	}
}

type recoveryClock struct{}

func (recoveryClock) Now() time.Time { return time.Now().UTC() }

type recoveryIDs struct {
	mu   sync.Mutex
	next int
}

func (i *recoveryIDs) NewID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	return fmt.Sprintf("recovery-%03d", i.next)
}

type holdQueue struct{}

func (holdQueue) Enqueue(context.Context, domain.CapsuleID) error { return nil }

func TestRestartRecoversPersistedNonterminalCapsule(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	directory := t.TempDir()
	ids := &recoveryIDs{}

	firstStore, err := sqlite.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	firstService := app.NewService(firstStore, recoveryClock{}, ids, holdQueue{})
	project, err := firstService.CreateProject(ctx, "project", "project-key")
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := firstService.CreateCapsule(ctx, project.ID, "capsule", "capsule-key")
	if err != nil {
		t.Fatal(err)
	}
	if capsule.State != domain.CapsuleCreating {
		t.Fatalf("initial state = %s", capsule.State)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := sqlite.Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	reconciler := app.NewReconciler(
		secondStore, fake.New(fake.Options{}), recoveryClock{}, ids,
	)
	reconciler.Start(ctx)
	defer reconciler.Close()
	secondService := app.NewService(secondStore, recoveryClock{}, ids, reconciler)
	if err := reconciler.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		recovered, err := secondService.GetCapsule(ctx, capsule.ID)
		if err != nil {
			t.Fatal(err)
		}
		if recovered.State == domain.CapsuleReady {
			if recovered.ProviderResourceID == "" {
				t.Fatal("provider resource ID was not persisted")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered state = %s", recovered.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	persistedProject, err := secondService.GetProject(ctx, project.ID)
	if err != nil || persistedProject.Name != project.Name {
		t.Fatalf("persisted project = %#v, %v", persistedProject, err)
	}
}
