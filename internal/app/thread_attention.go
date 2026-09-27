package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
	"github.com/OrlojHQ/meridian/internal/ports"
	"github.com/OrlojHQ/meridian/internal/transcripts"
)

// threadAttentionWindow bounds how many trailing messages are decrypted to
// find an unanswered request. An adapter blocked on the operator does not
// stream hundreds of further messages, so older requests are treated as stale.
const threadAttentionWindow = 256

type ThreadAwaitingKind string

const (
	ThreadAwaitingPermission ThreadAwaitingKind = "permission"
	ThreadAwaitingInput      ThreadAwaitingKind = "input"
)

// ThreadAwaiting is the content-free signal that an active structured session
// is blocked on the operator. It carries no summary, options, or prompt text.
type ThreadAwaiting struct {
	Kind  ThreadAwaitingKind
	Since time.Time
}

type cachedThreadAttention struct {
	messageCount int64
	awaiting     *ThreadAwaiting
}

// ThreadAwaiting derives, in memory, whether the Thread's live adapter session
// has an unanswered permission or input request. It is never persisted: the
// only durable record of a request remains the encrypted transcript.
func (s *Service) ThreadAwaiting(ctx context.Context, thread domain.Thread) (*ThreadAwaiting, error) {
	if s.transcriptKey == nil || thread.State != domain.ThreadActive ||
		thread.CurrentRunID == "" || thread.MessageCount == 0 {
		return nil, nil
	}
	run, err := s.GetRunStored(ctx, thread.CurrentRunID)
	if err != nil {
		return nil, err
	}
	if run.State.Terminal() {
		return nil, nil
	}
	s.attentionMu.Lock()
	cached, ok := s.attention[thread.ID]
	s.attentionMu.Unlock()
	if ok && cached.messageCount == thread.MessageCount {
		return cached.awaiting, nil
	}

	dek, err := s.unwrapThread(thread)
	if err != nil {
		return nil, err
	}
	defer wipe(dek)
	after := max(thread.MessageCount-threadAttentionWindow, 0)
	var messages []domain.ThreadMessage
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		messages, _, err = reader.ListThreadMessages(ctx, thread.ID, after, threadAttentionWindow)
		return err
	}); err != nil {
		return nil, err
	}

	// Mirror the browser's pending-request rule: each response answers the
	// most recent outstanding request of its kind.
	var permissions, inputs []domain.ThreadMessage
	for _, message := range messages {
		plaintext, err := transcripts.DecryptMessage(dek, message)
		if err != nil {
			return nil, err
		}
		for _, raw := range plaintext {
			var payload persistedThreadBlock
			if err := json.Unmarshal(raw, &payload); err != nil {
				transcripts.ZeroPlaintextBlocks(plaintext)
				return nil, domain.ErrTranscriptCorrupt
			}
			if payload.Event == nil {
				continue
			}
			switch payload.Event.Type {
			case adapterproto.KindPermissionRequest:
				permissions = append(permissions, message)
			case adapterproto.KindInputRequest:
				inputs = append(inputs, message)
			case adapterproto.KindPermissionResponse:
				permissions = dropLast(permissions)
			case adapterproto.KindInputResponse:
				inputs = dropLast(inputs)
			}
		}
		transcripts.ZeroPlaintextBlocks(plaintext)
	}

	var awaiting *ThreadAwaiting
	permission, hasPermission := last(permissions)
	input, hasInput := last(inputs)
	switch {
	case hasPermission && (!hasInput || permission.Sequence > input.Sequence):
		awaiting = &ThreadAwaiting{Kind: ThreadAwaitingPermission, Since: permission.CreatedAt}
	case hasInput:
		awaiting = &ThreadAwaiting{Kind: ThreadAwaitingInput, Since: input.CreatedAt}
	}
	s.attentionMu.Lock()
	s.attention[thread.ID] = cachedThreadAttention{
		messageCount: thread.MessageCount, awaiting: awaiting,
	}
	s.attentionMu.Unlock()
	return awaiting, nil
}

func dropLast(values []domain.ThreadMessage) []domain.ThreadMessage {
	if len(values) == 0 {
		return values
	}
	return values[:len(values)-1]
}

func last(values []domain.ThreadMessage) (domain.ThreadMessage, bool) {
	if len(values) == 0 {
		return domain.ThreadMessage{}, false
	}
	return values[len(values)-1], true
}
