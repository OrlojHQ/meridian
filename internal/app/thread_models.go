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
}

// summarizeThreadModels picks the labels of the current model and effort.
// The model the harness resolved the current option to wins over the
// option's own label; a current model the harness does not list is named by
// its ID.
func summarizeThreadModels(set *adapterproto.ModelSet) ThreadModelSummary {
	summary := ThreadModelSummary{Model: optionLabel(set.Current, set.Options)}
	if resolved := boundedLabel(set.Resolved); resolved != "" && summary.Model != "" {
		summary.Model = resolved
	}
	if set.Effort != nil {
		summary.Effort = optionLabel(set.Effort.Current, set.Effort.Options)
	}
	return summary
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
func (s *Service) rememberThreadModels(id domain.ThreadID, set *adapterproto.ModelSet) {
	summary := summarizeThreadModels(set)
	s.modelsMu.Lock()
	s.models[id] = summary
	s.modelsMu.Unlock()
}

// ThreadModels returns the model and effort a live structured session last
// reported. After a daemon restart, before the session reports again, it
// falls back to the latest report in the trailing transcript window that
// ThreadAttention already decrypts. It reports false when neither has one.
func (s *Service) ThreadModels(ctx context.Context, thread domain.Thread) (ThreadModelSummary, bool) {
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
	if link.ChildThreadID == "" {
		return ThreadModelSummary{}, false
	}
	thread, err := s.GetThread(ctx, link.ChildThreadID)
	if err != nil {
		return ThreadModelSummary{}, false
	}
	return s.ThreadModels(ctx, thread)
}
