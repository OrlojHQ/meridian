package app

import (
	"context"

	"github.com/OrlojHQ/meridian/internal/domain"
)

// NotesWakeSectionForTest exposes the lead wake's working-notes section.
func NotesWakeSectionForTest(s *Service, ctx context.Context, grant domain.CoordinationGrant) (string, int64) {
	return s.notesWakeSection(ctx, grant)
}
