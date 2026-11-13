package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/OrlojHQ/meridian/internal/ports"
)

// cancelInsideTransaction returns a commit hook that cancels the context on
// the first commit and waits until database/sql has rolled the transaction
// back, so the following Commit always finds it already finished.
func cancelInsideTransaction(t *testing.T, cancel context.CancelFunc) (hook func(*sql.Tx), fired func() bool) {
	t.Helper()
	called := false
	return func(tx *sql.Tx) {
		if called {
			return
		}
		called = true
		cancel()
		waitForRollback(t, tx)
	}, func() bool { return called }
}

// waitForRollback waits for database/sql's context watcher to roll tx back.
// A statement under a live context reports sql.ErrTxDone once it has.
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

func TestOpenCancelledDuringMigrationReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook, fired := cancelInsideTransaction(t, cancel)
	t.Cleanup(SetCommitHookForTesting(hook))

	store, err := Open(ctx, t.TempDir())
	if err == nil {
		_ = store.Close()
	}
	if !fired() {
		t.Fatal("no migration transaction committed")
	}
	if !errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("Open = %v, want context.Canceled", err)
	}
}

func TestTransactCancelledBeforeCommitReportsCancellation(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook, fired := cancelInsideTransaction(t, cancel)
	t.Cleanup(SetCommitHookForTesting(hook))

	err = store.Transact(ctx, func(ports.Transaction) error { return nil })
	if !fired() {
		t.Fatal("transaction never reached commit")
	}
	if !errors.Is(err, context.Canceled) || errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("Transact = %v, want context.Canceled", err)
	}
}

// A finished transaction under a live context is a bug in the caller, not a
// cancellation, and keeps reporting sql.ErrTxDone.
func TestTransactKeepsErrTxDoneWhileContextIsLive(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t.Cleanup(SetCommitHookForTesting(func(tx *sql.Tx) { _ = tx.Rollback() }))

	err = store.Transact(context.Background(), func(ports.Transaction) error { return nil })
	if !errors.Is(err, sql.ErrTxDone) || errors.Is(err, context.Canceled) {
		t.Fatalf("Transact = %v, want sql.ErrTxDone", err)
	}
}
