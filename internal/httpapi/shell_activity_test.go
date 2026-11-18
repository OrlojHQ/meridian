package httpapi

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type recordingAttachment struct{ writes int }

func (r *recordingAttachment) Read(context.Context) (bool, []byte, error) { return false, nil, nil }
func (r *recordingAttachment) Write(context.Context, bool, []byte) error {
	r.writes++
	return nil
}
func (r *recordingAttachment) Close() error { return nil }

func TestShellInputIsCapsuleActivity(t *testing.T) {
	upstream := &recordingAttachment{}
	var touched atomic.Int32
	done := make(chan struct{}, 4)
	activity := newShellActivity(context.Background(), upstream, func(context.Context) {
		touched.Add(1)
		done <- struct{}{}
	})
	ctx := context.Background()
	// Opening the shell already counted as activity; typing right away does
	// not touch again.
	_ = activity.Write(ctx, true, []byte("ls\n"))
	if touched.Load() != 0 {
		t.Fatal("touched inside the interval")
	}
	activity.last.Store(time.Now().Add(-shellActivityInterval).UnixNano())
	// A resize is not typing.
	_ = activity.Write(ctx, false, []byte(`{"type":"resize","columns":80,"rows":24}`))
	if touched.Load() != 0 {
		t.Fatal("a resize counted as activity")
	}
	_ = activity.Write(ctx, true, []byte("pwd\n"))
	_ = activity.Write(ctx, true, []byte("pwd\n"))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("typing after the interval was not recorded")
	}
	time.Sleep(50 * time.Millisecond)
	if touched.Load() != 1 {
		t.Fatalf("touches = %d, want 1", touched.Load())
	}
	if upstream.writes != 4 {
		t.Fatalf("forwarded writes = %d, want 4", upstream.writes)
	}
}
