package app

import (
	"context"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

// ThreadContextUsage is how full a live structured session's context window
// is, as its harness last reported (ADR 0059). Tokens is the input size of
// the most recent model call and Window the model's context window. Both are
// counts with no model name or transcript text.
type ThreadContextUsage struct {
	Tokens int64
	Window int64
}

type threadContextEntry struct {
	run   domain.RunID
	usage ThreadContextUsage
}

// rememberContextUsage keeps the latest context_usage report for a Thread's
// Run. Like ThreadAttention it lives only in memory and is never persisted,
// logged, or emitted; a restart forgets it until the harness reports again.
// A report that does not say how full the window is, such as a compaction
// whose new size the harness does not know yet, forgets the old value.
func (s *Service) rememberContextUsage(id domain.ThreadID, run domain.RunID, usage adapterproto.ContextUsage) {
	s.contextMu.Lock()
	defer s.contextMu.Unlock()
	if !usage.Known() {
		delete(s.contextUsage, id)
		return
	}
	s.contextUsage[id] = threadContextEntry{
		run: run, usage: ThreadContextUsage{Tokens: usage.Tokens, Window: usage.Window},
	}
}

// ThreadContextUsage returns the context fill the Thread's current Run last
// reported. It reports false for a Thread without a live session, a Run that
// has not reported, or a value from an earlier Run.
func (s *Service) ThreadContextUsage(ctx context.Context, thread domain.Thread) (ThreadContextUsage, bool) {
	if requirePrincipal(ctx) != nil {
		return ThreadContextUsage{}, false
	}
	if thread.State != domain.ThreadActive || thread.CurrentRunID == "" {
		return ThreadContextUsage{}, false
	}
	s.contextMu.Lock()
	entry, ok := s.contextUsage[thread.ID]
	s.contextMu.Unlock()
	if !ok || entry.run != thread.CurrentRunID {
		return ThreadContextUsage{}, false
	}
	run, err := s.GetRunStored(ctx, thread.CurrentRunID)
	if err != nil || run.State.Terminal() {
		return ThreadContextUsage{}, false
	}
	return entry.usage, true
}
