package app_test

import (
	"context"

	"github.com/OrlojHQ/meridian/internal/principal"
)

// operatorContext is a request from the single-user mode operator, the
// bootstrap owner, as internal/httpapi attaches it to an authenticated
// request.
func operatorContext() context.Context {
	return principal.With(context.Background(), principal.Owner())
}

// asSystem is ctx as daemon-internal work, which the reconciler and the
// daemon's background loops attach for themselves.
func asSystem(ctx context.Context) context.Context {
	return principal.WithSystem(ctx)
}
