package datadir

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fast() Options {
	return Options{Interval: 20 * time.Millisecond, TTL: time.Second, Settle: time.Millisecond}
}

func writeForeign(t *testing.T, dir string, record Record) {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LeaseFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSecondOwnerOnTheSameHostIsRefused(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(context.Background(), dir, "meridiand", fast())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background(), dir, "meridian admin restore", fast()); !errors.Is(err, ErrInUse) {
		t.Fatalf("second owner = %v", err)
	}
	first.Release()
	first.Release()
	if _, err := os.Stat(filepath.Join(dir, LeaseFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lease left after release: %v", err)
	}
	second, err := Acquire(context.Background(), dir, "meridiand", fast())
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	second.Release()
}

func TestOwnerOnAnotherHostHoldsUntilItsLeaseExpires(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	options := fast()
	options.Now = func() time.Time { return now }
	foreign := Record{Instance: "vm-daemon", Owner: "meridiand", PID: 1, Host: "docker-desktop",
		StartedAt: now.Add(-time.Hour), HeartbeatAt: now.Add(-200 * time.Millisecond)}
	writeForeign(t, dir, foreign)
	_, err := Acquire(context.Background(), dir, "meridiand", options)
	if !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "meridiand (pid 1 on docker-desktop)") ||
		!strings.Contains(err.Error(), LeaseFile) {
		t.Fatalf("live foreign owner = %v", err)
	}
	foreign.HeartbeatAt = now.Add(-2 * time.Second)
	writeForeign(t, dir, foreign)
	lease, err := Acquire(context.Background(), dir, "meridiand", options)
	if err != nil {
		t.Fatalf("expired foreign owner = %v", err)
	}
	lease.Release()
}

func TestCrashedOwnerOnThisHostIsReplacedAtOnce(t *testing.T) {
	dir := t.TempDir()
	host, _ := os.Hostname()
	writeForeign(t, dir, Record{Instance: "crashed", Owner: "meridiand", PID: 999999, Host: host,
		StartedAt: time.Now(), HeartbeatAt: time.Now()})
	lease, err := Acquire(context.Background(), dir, "meridiand", fast())
	if err != nil {
		t.Fatalf("crashed same-host owner = %v", err)
	}
	defer lease.Release()
	if lease.Record().Instance == "crashed" {
		t.Fatal("lease was not replaced")
	}
}

func TestUnreadableLeaseIsReplaced(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, LeaseFile), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	lease, err := Acquire(context.Background(), dir, "meridiand", fast())
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
}

func TestHeartbeatRefreshesAndReportsTakeover(t *testing.T) {
	dir := t.TempDir()
	lease, err := Acquire(context.Background(), dir, "meridiand", fast())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	started := lease.Record().HeartbeatAt
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, err := readRecord(dir)
		if err == nil && current.HeartbeatAt.After(started) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("heartbeat never refreshed the lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
	writeForeign(t, dir, Record{Instance: "intruder", Owner: "meridiand", PID: 7, Host: "docker-desktop",
		HeartbeatAt: time.Now()})
	select {
	case err := <-lease.Lost():
		if !errors.Is(err, ErrLost) || !strings.Contains(err.Error(), "pid 7 on docker-desktop") {
			t.Fatalf("lost = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("takeover was not reported")
	}
	lease.Release()
	if current, err := readRecord(dir); err != nil || current.Instance != "intruder" {
		t.Fatalf("release removed another owner's lease: %#v, %v", current, err)
	}
}

func TestAcquireRequiresAnExistingDirectory(t *testing.T) {
	if _, err := Acquire(context.Background(), filepath.Join(t.TempDir(), "missing"), "meridiand", fast()); err == nil {
		t.Fatal("missing directory accepted")
	}
}
