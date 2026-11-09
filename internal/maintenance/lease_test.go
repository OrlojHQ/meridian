package maintenance

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/OrlojHQ/meridian/internal/datadir"
)

func TestMaintenanceRefusesADataDirectoryInUse(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	lease, err := datadir.Acquire(ctx, dataDir, "meridiand", datadir.Options{Settle: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if _, err := CreateBackup(ctx, dataDir, filepath.Join(t.TempDir(), "backup")); !errors.Is(err, datadir.ErrInUse) {
		t.Fatalf("backup beside a running daemon = %v", err)
	}
	if _, err := RotateTranscriptKey(ctx, dataDir, "", filepath.Join(t.TempDir(), "new.key")); !errors.Is(err, datadir.ErrInUse) {
		t.Fatalf("rotation beside a running daemon = %v", err)
	}
	if _, err := VerifyCAS(ctx, dataDir, true); !errors.Is(err, datadir.ErrInUse) {
		t.Fatalf("CAS deletion beside a running daemon = %v", err)
	}
}
