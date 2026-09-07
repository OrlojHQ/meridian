package app

import (
	"context"
)

func (r *Reconciler) setupCacheEnabled(ctx context.Context) bool {
	if r.snapshotter == nil || r.artifacts == nil {
		return false
	}
	capabilities, err := r.provider.Capabilities(ctx)
	return err == nil && capabilities.Snapshot
}
