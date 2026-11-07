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

// ThreadTurn says whether a live structured session is working on a turn or
// idle between turns. Like ThreadAwaiting it carries no transcript content.
type ThreadTurn string

const (
	ThreadTurnWorking ThreadTurn = "working"
	ThreadTurnIdle    ThreadTurn = "idle"
)

// ThreadAttention is the content-free live state of a structured session.
// Turn is empty when no live session or transcript key is available.
type ThreadAttention struct {
	Awaiting *ThreadAwaiting
	Turn     ThreadTurn
}

type cachedThreadAttention struct {
	messageCount int64
	attention    ThreadAttention
}

// ThreadAwaiting derives, in memory, whether the Thread's live adapter session
// has an unanswered permission or input request. It is never persisted: the
// only durable record of a request remains the encrypted transcript.
func (s *Service) ThreadAwaiting(ctx context.Context, thread domain.Thread) (*ThreadAwaiting, error) {
	attention, err := s.ThreadAttention(ctx, thread)
	return attention.Awaiting, err
}

// ThreadAttention derives the live session's unanswered request and turn state
// from the trailing transcript window, in memory and never persisted.
func (s *Service) ThreadAttention(ctx context.Context, thread domain.Thread) (ThreadAttention, error) {
	if s.transcriptKey == nil || thread.State != domain.ThreadActive ||
		thread.CurrentRunID == "" || thread.MessageCount == 0 {
		return ThreadAttention{}, nil
	}
	run, err := s.GetRunStored(ctx, thread.CurrentRunID)
	if err != nil {
		return ThreadAttention{}, err
	}
	if run.State.Terminal() {
		return ThreadAttention{}, nil
	}
	s.attentionMu.Lock()
	cached, ok := s.attention[thread.ID]
	s.attentionMu.Unlock()
	if ok && cached.messageCount == thread.MessageCount {
		return cached.attention, nil
	}

	dek, err := s.unwrapThread(thread)
	if err != nil {
		return ThreadAttention{}, err
	}
	defer wipe(dek)
	after := max(thread.MessageCount-threadAttentionWindow, 0)
	var messages []domain.ThreadMessage
	if err := s.store.View(ctx, func(reader ports.Reader) error {
		var err error
		messages, _, err = reader.ListThreadMessages(ctx, thread.ID, after, threadAttentionWindow)
		return err
	}); err != nil {
		return ThreadAttention{}, err
	}

	// Mirror the browser's pending-request rule: a response answers the
	// request it names, or the most recent outstanding one of its kind.
	var permissions, inputs []pendingThreadRequest
	working := false
	for _, message := range messages {
		if message.Role == domain.ThreadRoleUser && message.Kind == domain.ThreadMessagePrompt {
			working = true
		}
		plaintext, err := transcripts.DecryptMessage(dek, message)
		if err != nil {
			return ThreadAttention{}, err
		}
		for _, raw := range plaintext {
			var payload persistedThreadBlock
			if err := json.Unmarshal(raw, &payload); err != nil {
				transcripts.ZeroPlaintextBlocks(plaintext)
				return ThreadAttention{}, domain.ErrTranscriptCorrupt
			}
			if payload.Event == nil {
				continue
			}
			switch payload.Event.Type {
			case adapterproto.KindStatus:
				if payload.Event.Status != "" {
					working = busyAdapterStatus(payload.Event.Status)
				}
			case adapterproto.KindEnd:
				working = false
			case adapterproto.KindPermissionRequest:
				permissions = append(permissions, pendingThreadRequest{payload.Event.MessageID, message})
			case adapterproto.KindInputRequest:
				inputs = append(inputs, pendingThreadRequest{payload.Event.MessageID, message})
			case adapterproto.KindPermissionResponse:
				permissions = answerRequest(permissions, payload.Event.ResponseTo)
			case adapterproto.KindInputResponse:
				inputs = answerRequest(inputs, payload.Event.ResponseTo)
			}
		}
		transcripts.ZeroPlaintextBlocks(plaintext)
	}

	attention := ThreadAttention{Turn: ThreadTurnIdle}
	if working {
		attention.Turn = ThreadTurnWorking
	}
	permission, hasPermission := last(permissions)
	input, hasInput := last(inputs)
	switch {
	case hasPermission && (!hasInput || permission.message.Sequence > input.message.Sequence):
		attention.Awaiting = &ThreadAwaiting{Kind: ThreadAwaitingPermission, Since: permission.message.CreatedAt}
	case hasInput:
		attention.Awaiting = &ThreadAwaiting{Kind: ThreadAwaitingInput, Since: input.message.CreatedAt}
	}
	s.attentionMu.Lock()
	s.attention[thread.ID] = cachedThreadAttention{
		messageCount: thread.MessageCount, attention: attention,
	}
	s.attentionMu.Unlock()
	return attention, nil
}

func busyAdapterStatus(status adapterproto.Status) bool {
	switch status {
	case adapterproto.StatusStarting, adapterproto.StatusRunning,
		adapterproto.StatusRestarting, adapterproto.StatusWaiting:
		return true
	}
	return false
}

type pendingThreadRequest struct {
	id      string
	message domain.ThreadMessage
}

func answerRequest(pending []pendingThreadRequest, responseTo string) []pendingThreadRequest {
	if len(pending) == 0 {
		return pending
	}
	if responseTo == "" {
		return pending[:len(pending)-1]
	}
	for index := len(pending) - 1; index >= 0; index-- {
		if pending[index].id == responseTo {
			return append(pending[:index:index], pending[index+1:]...)
		}
	}
	return pending
}

func last(values []pendingThreadRequest) (pendingThreadRequest, bool) {
	if len(values) == 0 {
		return pendingThreadRequest{}, false
	}
	return values[len(values)-1], true
}
