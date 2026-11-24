package app

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
	"github.com/OrlojHQ/meridian/internal/domain"
)

// ThreadModelSummary names the model and reasoning effort a structured
// session last reported, for views that list many Threads, such as a lead's
// subagents. Labels are the harness's own, already bounded by adapterproto;
// Effort is empty when the harness reports no effort or uses its default.
type ThreadModelSummary struct {
	Model  string
	Effort string
	// ModelID is the harness's identifier for the current model, the value a
	// set_model or a coordination spawn takes. Model is only its label.
	ModelID string
	// Images is true when the harness accepts image content.
	Images bool
}

// summarizeThreadModels picks the labels of the current model and effort.
// The model the harness resolved the current option to wins over the
// option's own label; a current model the harness does not list is named by
// its ID.
func summarizeThreadModels(set *adapterproto.ModelSet) ThreadModelSummary {
	summary := ThreadModelSummary{
		Model: optionLabel(set.Current, set.Options), ModelID: set.Current, Images: set.Images,
	}
	if resolved := boundedLabel(set.Resolved); resolved != "" && summary.Model != "" {
		summary.Model = resolved
	}
	if set.Effort != nil {
		summary.Effort = optionLabel(set.Effort.Current, set.Effort.Options)
	}
	return summary
}

// threadAcceptsImages reports whether the live session's latest models event
// said the harness accepts image content. A session that has not said so does not.
func (s *Service) threadAcceptsImages(ctx context.Context, thread domain.Thread) bool {
	s.modelsMu.Lock()
	summary, ok := s.models[thread.ID]
	s.modelsMu.Unlock()
	if ok {
		return summary.Images
	}
	if _, err := s.ThreadAttention(ctx, thread); err != nil {
		return false
	}
	s.attentionMu.Lock()
	cached, found := s.attention[thread.ID]
	s.attentionMu.Unlock()
	return found && cached.models != nil && cached.models.Images
}

func optionLabel(current string, options []adapterproto.ModelOption) string {
	if current == "" {
		return ""
	}
	label := current
	for _, option := range options {
		if option.ID == current {
			label = option.Label
			break
		}
	}
	return boundedLabel(label)
}

// boundedLabel keeps a label to one line of at most 128 bytes.
func boundedLabel(label string) string {
	label = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(label, ""))
	label = strings.TrimSpace(label)
	for len(label) > adapterproto.MaxModelLabelBytes {
		_, size := utf8.DecodeLastRuneInString(label)
		label = label[:len(label)-size]
	}
	return label
}

// rememberThreadModels records the latest models report the daemon persisted
// for a Thread. Like ThreadAttention it lives only in memory; the durable
// record stays in the encrypted transcript.
//
// It reports whether the report changed what a lead's coordination mailbox
// shows: the current model or the set of offered model IDs.
func (s *Service) rememberThreadModels(id domain.ThreadID, set *adapterproto.ModelSet) bool {
	summary := summarizeThreadModels(set)
	fingerprint := modelSetFingerprint(set)
	s.modelsMu.Lock()
	defer s.modelsMu.Unlock()
	s.models[id] = summary
	if s.modelFingerprints == nil {
		s.modelFingerprints = make(map[domain.ThreadID]string)
	}
	if s.modelFingerprints[id] == fingerprint {
		return false
	}
	s.modelFingerprints[id] = fingerprint
	return true
}

// modelSetFingerprint names the parts of a models report a lead's worker
// defaults and known-model list depend on.
func modelSetFingerprint(set *adapterproto.ModelSet) string {
	var builder strings.Builder
	builder.WriteString(set.Current)
	for _, option := range set.Options {
		builder.WriteByte(0)
		builder.WriteString(option.ID)
		builder.WriteByte(1)
		builder.WriteString(option.Label)
	}
	return builder.String()
}

// ThreadModels returns the model and effort a live structured session last
// reported. After a daemon restart, before the session reports again, it
// falls back to the latest report in the trailing transcript window that
// ThreadAttention already decrypts. It reports false when neither has one.
func (s *Service) ThreadModels(ctx context.Context, thread domain.Thread) (ThreadModelSummary, bool) {
	if requirePrincipal(ctx) != nil {
		return ThreadModelSummary{}, false
	}
	return s.threadModels(ctx, thread)
}

// threadModels is ThreadModels for daemon-internal callers, such as the
// coordination mailbox, that act for the grant rather than a principal.
func (s *Service) threadModels(ctx context.Context, thread domain.Thread) (ThreadModelSummary, bool) {
	if thread.State != domain.ThreadActive || thread.CurrentRunID == "" {
		return ThreadModelSummary{}, false
	}
	s.modelsMu.Lock()
	summary, ok := s.models[thread.ID]
	s.modelsMu.Unlock()
	if ok {
		return summary, summary.Model != ""
	}
	if _, err := s.ThreadAttention(ctx, thread); err != nil {
		return ThreadModelSummary{}, false
	}
	s.attentionMu.Lock()
	cached, ok := s.attention[thread.ID]
	s.attentionMu.Unlock()
	if !ok || cached.models == nil {
		return ThreadModelSummary{}, false
	}
	return *cached.models, cached.models.Model != ""
}

// ThreadLinkModels is ThreadModels for a link's child Thread.
func (s *Service) ThreadLinkModels(ctx context.Context, link domain.ThreadLink) (ThreadModelSummary, bool) {
	if requirePrincipal(ctx) != nil {
		return ThreadModelSummary{}, false
	}
	if link.ChildThreadID == "" {
		return ThreadModelSummary{}, false
	}
	thread, err := s.GetThread(ctx, link.ChildThreadID)
	if err != nil {
		return ThreadModelSummary{}, false
	}
	return s.ThreadModels(ctx, thread)
}
