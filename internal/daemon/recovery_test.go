package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/app"
	"github.com/OrlojHQ/meridian/internal/daemon"
	"github.com/OrlojHQ/meridian/internal/datadir"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/store/sqlite"
	fake "github.com/OrlojHQ/meridian/internal/testutil/fakeprovider"
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
		Provider: "docker", Listen: "127.0.0.1:0",
		PreviewListen: "0.0.0.0:0", DataDir: t.TempDir(),
	}); err == nil {
		t.Fatal("daemon startup accepted a non-loopback preview listener")
	}
}

func TestAPIListenerRequiresAuthenticationAndExplicitNonLoopbackOptIn(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		if err := daemon.ValidateAPIListenAddress(address, false, true); err != nil {
			t.Fatalf("%s rejected: %v", address, err)
		}
	}
	if err := daemon.ValidateAPIListenAddress("0.0.0.0:8080", false, true); err == nil {
		t.Fatal("non-loopback API accepted without explicit opt-in")
	}
	if err := daemon.ValidateAPIListenAddress("0.0.0.0:8080", true, false); err == nil {
		t.Fatal("non-loopback API accepted without authentication")
	}
	if err := daemon.ValidateAPIListenAddress("0.0.0.0:8080", true, true); err != nil {
		t.Fatalf("authenticated explicit non-loopback API rejected: %v", err)
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

func TestSecondDaemonOnOneDataDirectoryIsRefused(t *testing.T) {
	directory := t.TempDir()
	daemon.UseTestProvider(fake.New(fake.Options{}))
	t.Cleanup(daemon.ClearTestProvider)
	config := daemon.Config{
		Provider: "docker", Listen: "127.0.0.1:0", PreviewListen: "127.0.0.1:0", DataDir: directory,
		DataLease: datadir.Options{Interval: 50 * time.Millisecond, Settle: time.Millisecond},
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { first <- daemon.Run(ctx, config) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(directory, datadir.LeaseFile)); err == nil {
			break
		}
		select {
		case err := <-first:
			t.Fatalf("first daemon stopped: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("first daemon never took the data directory")
		}
		time.Sleep(10 * time.Millisecond)
	}
	err := daemon.Run(context.Background(), config)
	if !errors.Is(err, datadir.ErrInUse) {
		t.Fatalf("second daemon = %v", err)
	}
	cancel()
	// The lease is taken before the rest of startup, so the first daemon
	// may still be starting; cancelling it then is not a failure.
	if err := <-first; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("first daemon: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, datadir.LeaseFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lease left after shutdown: %v", err)
	}
}
