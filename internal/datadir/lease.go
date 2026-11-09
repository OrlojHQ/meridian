// Package datadir keeps a Meridian data directory to one owner at a time.
//
// SQLite stays consistent between processes only while they share POSIX
// locks and a shared-memory index. Neither crosses a VM boundary such as
// Docker Desktop's bind mounts, so two daemons that open one data directory
// from either side of it corrupt the database. A lease guards against that:
// an exclusive flock excludes a second owner on the same host, and a
// heartbeat lease file, whose contents do cross a bind mount, excludes an
// owner elsewhere. meridiand holds the lease while it runs; offline
// maintenance commands hold it for their duration.
package datadir

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	// LockFile is the same-host exclusive lock.
	LockFile = "meridiand.lock"
	// LeaseFile records the current owner and its heartbeat.
	LeaseFile = "meridiand.lease"

	defaultInterval = 5 * time.Second
	defaultTTL      = 30 * time.Second
	defaultSettle   = 250 * time.Millisecond
	maxLeaseBytes   = 4 << 10
)

// ErrInUse means another live owner holds the data directory.
var ErrInUse = errors.New("data directory is in use")

// ErrLost means another owner replaced this lease while it was held.
var ErrLost = errors.New("data directory lease lost")

// Options tunes the lease timing. Zero values use the defaults: a 5 second
// heartbeat, a 30 second expiry, and a 250 millisecond settle before the
// lease is confirmed.
type Options struct {
	Interval time.Duration
	TTL      time.Duration
	Settle   time.Duration
	Now      func() time.Time
}

func (o Options) withDefaults() Options {
	if o.Interval <= 0 {
		o.Interval = defaultInterval
	}
	if o.TTL <= 0 {
		o.TTL = defaultTTL
	}
	if o.Settle < 0 {
		o.Settle = 0
	} else if o.Settle == 0 {
		o.Settle = defaultSettle
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Record is the lease file's content. It names the owner for an operator
// and carries no secret.
type Record struct {
	Instance    string    `json:"instance"`
	Owner       string    `json:"owner"`
	PID         int       `json:"pid"`
	Host        string    `json:"host"`
	StartedAt   time.Time `json:"startedAt"`
	HeartbeatAt time.Time `json:"heartbeatAt"`
}

// Lease is a held data directory.
type Lease struct {
	dir     string
	mu      sync.Mutex
	record  Record
	options Options
	lock    *os.File

	lost    chan error
	stop    chan struct{}
	done    chan struct{}
	release sync.Once
}

// Acquire takes the data directory for owner, a short description such as
// "meridiand" or "meridian admin restore". It fails with ErrInUse when
// another process on this host holds the lock, or when the lease file names
// another owner whose heartbeat is younger than the expiry. A lease whose
// owner stopped without releasing it expires after the TTL.
func Acquire(ctx context.Context, dir, owner string, options Options) (*Lease, error) {
	options = options.withDefaults()
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect data directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("data directory must be a directory")
	}
	lock, err := os.OpenFile(filepath.Join(dir, LockFile), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open data directory lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: another Meridian process on this host holds %s", ErrInUse, dir)
		}
		return nil, fmt.Errorf("lock data directory: %w", err)
	}
	fail := func(err error) (*Lease, error) {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
		return nil, err
	}
	now := options.Now().UTC()
	host, _ := os.Hostname()
	// Holding the flock proves that any earlier owner on this host has
	// exited, because the kernel drops a process's locks when it dies, so a
	// crashed daemon restarts at once. Only an owner on another host, which
	// the flock cannot see, is honoured until its lease expires.
	if current, err := readRecord(dir); err == nil && fresh(current, now, options.TTL) &&
		(host == "" || current.Host != host) {
		return fail(inUse(dir, current, options.TTL))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errUnreadable) {
		return fail(err)
	}
	instance := make([]byte, 16)
	if _, err := rand.Read(instance); err != nil {
		return fail(err)
	}
	record := Record{
		Instance: hex.EncodeToString(instance), Owner: owner, PID: os.Getpid(), Host: host,
		StartedAt: now, HeartbeatAt: now,
	}
	if err := writeRecord(dir, record); err != nil {
		return fail(err)
	}
	// An owner across a VM boundary may have written at the same moment;
	// whichever record survives the settle wins.
	select {
	case <-ctx.Done():
		_ = removeIfOwned(dir, record.Instance)
		return fail(ctx.Err())
	case <-time.After(options.Settle):
	}
	if current, err := readRecord(dir); err != nil || current.Instance != record.Instance {
		if err == nil {
			return fail(inUse(dir, current, options.TTL))
		}
		return fail(fmt.Errorf("%w: lease at %s was replaced while it was taken", ErrInUse, dir))
	}
	lease := &Lease{
		dir: dir, record: record, options: options, lock: lock,
		lost: make(chan error, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go lease.heartbeat()
	return lease, nil
}

// Lost reports, once, that the lease was taken over or could not be
// refreshed before it would expire. The owner must stop using the data
// directory.
func (l *Lease) Lost() <-chan error {
	return l.lost
}

// Record returns the lease as written.
func (l *Lease) Record() Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.record
}

// Release stops the heartbeat, removes the lease file if it is still this
// lease's, and drops the lock. It is safe to call more than once.
func (l *Lease) Release() {
	l.release.Do(func() {
		close(l.stop)
		<-l.done
		_ = removeIfOwned(l.dir, l.record.Instance)
		_ = syscall.Flock(int(l.lock.Fd()), syscall.LOCK_UN)
		_ = l.lock.Close()
	})
}

func (l *Lease) heartbeat() {
	defer close(l.done)
	ticker := time.NewTicker(l.options.Interval)
	defer ticker.Stop()
	refreshed := l.options.Now()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
		}
		now := l.options.Now()
		current, err := readRecord(l.dir)
		if err == nil && current.Instance != l.record.Instance {
			l.lost <- fmt.Errorf("%w: %s now holds %s", ErrLost, describe(current), l.dir)
			return
		}
		if err == nil {
			l.mu.Lock()
			l.record.HeartbeatAt = now.UTC()
			record := l.record
			l.mu.Unlock()
			err = writeRecord(l.dir, record)
		}
		if err == nil {
			refreshed = now
			continue
		}
		// A lease that cannot be refreshed may expire and pass to another
		// owner, so stop before that can happen.
		if now.Sub(refreshed) > l.options.TTL/2 {
			l.lost <- fmt.Errorf("%w: could not refresh %s: %v", ErrLost, filepath.Join(l.dir, LeaseFile), err)
			return
		}
	}
}

var errUnreadable = errors.New("lease file is unreadable")

func readRecord(dir string) (Record, error) {
	file, err := os.OpenFile(filepath.Join(dir, LeaseFile), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Record{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxLeaseBytes+1))
	if err != nil {
		return Record{}, err
	}
	var record Record
	if len(raw) > maxLeaseBytes || json.Unmarshal(raw, &record) != nil || record.Instance == "" {
		return Record{}, errUnreadable
	}
	return record, nil
}

// writeRecord replaces the lease file atomically, so a reader on either side
// of a bind mount sees a whole record.
func writeRecord(dir string, record Record) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+LeaseFile+"-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, LeaseFile))
}

func removeIfOwned(dir, instance string) error {
	if current, err := readRecord(dir); err == nil && current.Instance == instance {
		return os.Remove(filepath.Join(dir, LeaseFile))
	}
	return nil
}

// fresh treats a heartbeat within the TTL as live. A heartbeat from the
// future also counts, so clock skew cannot hand the directory to a second
// owner; an operator can remove a lease that will not expire.
func fresh(record Record, now time.Time, ttl time.Duration) bool {
	return now.Sub(record.HeartbeatAt) < ttl
}

func inUse(dir string, record Record, ttl time.Duration) error {
	return fmt.Errorf(
		"%w: %s holds %s (heartbeat %s); stop it, or if it is gone wait %s for the lease to expire or remove %s",
		ErrInUse, describe(record), dir, record.HeartbeatAt.Format(time.RFC3339), ttl,
		filepath.Join(dir, LeaseFile),
	)
}

func describe(record Record) string {
	owner := record.Owner
	if owner == "" {
		owner = "another process"
	}
	return fmt.Sprintf("%s (pid %d on %s)", owner, record.PID, record.Host)
}
