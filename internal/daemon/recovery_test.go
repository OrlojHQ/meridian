package daemon_test

import (
	"context"
	"database/sql"
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

// A fresh install, such as the Homebrew service, starts with no data
// directory. The daemon creates it private instead of failing the lease.
// The test stops the daemon inside its first migration transaction, which
// must read as a clean stop rather than a transaction error.
func TestDaemonCreatesMissingDataDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "Application Support", "meridian")
	daemon.UseTestProvider(fake.New(fake.Options{}))
	t.Cleanup(daemon.ClearTestProvider)
	config := daemon.Config{
		Provider: "docker", Listen: "127.0.0.1:0", PreviewListen: "127.0.0.1:0", DataDir: directory,
		DataLease: datadir.Options{Interval: 50 * time.Millisecond, Settle: time.Millisecond},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := false
	t.Cleanup(sqlite.SetCommitHookForTesting(func(tx *sql.Tx) {
		if stopped {
			return
		}
		stopped = true
		if _, err := os.Stat(filepath.Join(directory, datadir.LeaseFile)); err != nil {
			t.Errorf("startup wrote to the data directory before taking it: %v", err)
		}
		cancel()
		waitForRollback(t, tx)
	}))
	if err := daemon.Run(ctx, config); err != nil {
		t.Fatalf("daemon stopped during startup = %v, want a clean stop", err)
	}
	if !stopped {
		t.Fatal("daemon never opened a startup transaction")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("data directory mode = %v, want private directory", info.Mode())
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
	defer cancel()
	// Hold the first daemon inside its first startup transaction, with the
	// data directory leased, until the second daemon has been refused.
	inTransaction := make(chan struct{})
	refused := make(chan struct{})
	held := false
	t.Cleanup(sqlite.SetCommitHookForTesting(func(tx *sql.Tx) {
		if held {
			return
		}
		held = true
		close(inTransaction)
		<-refused
		cancel()
		waitForRollback(t, tx)
	}))
	first := make(chan error, 1)
	go func() { first <- daemon.Run(ctx, config) }()
	select {
	case <-inTransaction:
	case err := <-first:
		t.Fatalf("first daemon stopped: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("first daemon never started")
	}
	err := daemon.Run(context.Background(), config)
	close(refused)
	if !errors.Is(err, datadir.ErrInUse) {
		t.Fatalf("second daemon = %v", err)
	}
	if err := <-first; err != nil {
		t.Fatalf("first daemon stopped during startup = %v, want a clean stop", err)
	}
	if _, err := os.Stat(filepath.Join(directory, datadir.LeaseFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lease left after shutdown: %v", err)
	}
}

// waitForRollback waits for database/sql's context watcher to roll tx back,
// so the daemon's Commit always finds the transaction already finished. A
// statement under a live context reports sql.ErrTxDone once it has.
func waitForRollback(t *testing.T, tx *sql.Tx) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var one int
		err := tx.QueryRowContext(context.Background(), "SELECT 1").Scan(&one)
		if errors.Is(err, sql.ErrTxDone) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("transaction still open after its context was cancelled: %v", err)
			return
		}
		time.Sleep(time.Millisecond)
	}
}
