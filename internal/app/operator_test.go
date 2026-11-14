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
