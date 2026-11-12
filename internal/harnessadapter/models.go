package harnessadapter

import (
	"slices"
	"unicode"
	"unicode/utf8"

	"github.com/OrlojHQ/meridian/internal/adapterproto"
)

// modelSet collects what a harness reports about its models. Harness output
// is hostile: entries with unusable IDs are dropped, text is folded to one
// bounded line, later duplicates lose, and the list stops at the protocol
// limit while keeping the current model.
type modelSet struct {
	current string
	options []adapterproto.ModelOption
	ids     map[string]bool
	// effort is the current model's reasoning effort levels, reported only
	// when the controller negotiated effort.
	effort *modelSet
	// resolved names the model the current option runs when that option is
	// an alias, reported only when the controller negotiated it.
	resolved string
}

func newModelSet() *modelSet {
	return &modelSet{ids: map[string]bool{}}
}

func (m *modelSet) add(id, label, description string) {
	if !adapterproto.ValidModelID(id) || m.ids[id] {
		return
	}
	label = commandLine(label, adapterproto.MaxModelLabelBytes)
	if label == "" {
		label = commandLine(id, adapterproto.MaxModelLabelBytes)
	}
	if label == "" {
		return
	}
	m.ids[id] = true
	m.options = append(m.options, adapterproto.ModelOption{
		ID: id, Label: label,
		Description: commandLine(description, adapterproto.MaxModelDescriptionBytes),
	})
}

func (m *modelSet) setCurrent(id string) {
	if adapterproto.ValidModelID(id) {
		m.current = id
	} else {
		m.current = ""
	}
}

func (m *modelSet) has(id string) bool {
	return m != nil && m.ids[id]
}

// set returns the frame payload. A list longer than the protocol limit keeps
// its first entries, and the current model replaces the last kept entry when
// it would otherwise fall off.
func (m *modelSet) set() *adapterproto.ModelSet {
	options := append([]adapterproto.ModelOption{}, m.options...)
	if len(options) > adapterproto.MaxModels {
		kept := options[:adapterproto.MaxModels]
		inKept := false
		for _, option := range kept {
			if option.ID == m.current {
				inKept = true
				break
			}
		}
		if !inKept && m.current != "" {
			for _, option := range options[adapterproto.MaxModels:] {
				if option.ID == m.current {
					kept[len(kept)-1] = option
					break
				}
			}
		}
		options = kept
	}
	set := &adapterproto.ModelSet{
		Current: m.current, Options: options,
		Resolved: commandLine(m.resolved, adapterproto.MaxModelLabelBytes),
	}
	if m.effort != nil && len(m.effort.options) != 0 {
		efforts := m.effort.options[:min(len(m.effort.options), adapterproto.MaxEfforts)]
		current := m.effort.current
		if !slices.ContainsFunc(efforts, func(option adapterproto.ModelOption) bool { return option.ID == current }) {
			current = ""
		}
		set.Effort = &adapterproto.EffortSet{Current: current, Options: append([]adapterproto.ModelOption{}, efforts...)}
	}
	return set
}

// effortLevels builds an effort list from bare level names, labelled with
// the name capitalized.
func effortLevels(levels []string, current string) *modelSet {
	efforts := newModelSet()
	for _, level := range levels {
		efforts.add(level, effortLabel(level), "")
	}
	efforts.setCurrent(current)
	return efforts
}

func effortLabel(level string) string {
	switch level {
	case "xhigh":
		return "Extra high"
	case "":
		return ""
	}
	first, size := utf8.DecodeRuneInString(level)
	return string(unicode.ToUpper(first)) + level[size:]
}

// ReportModels sends the harness's model list when the controller negotiated
// models frames, and drops it otherwise.
func (s *Stream) ReportModels(models *modelSet) error {
	if !s.models || models == nil {
		return nil
	}
	set := models.set()
	if !s.effort {
		set.Effort = nil
	}
	if !s.resolved {
		set.Resolved = ""
	}
	return s.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindModels,
		Models: set,
	})
}

// EffortNegotiated reports whether set_effort frames can arrive.
func (s *Stream) EffortNegotiated() bool {
	return s.effort
}

// effortUnavailable reports a refused effort change without ending the
// session.
func (s *Stream) effortUnavailable(summary string) error {
	return s.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindError,
		Code: "effort_unavailable", Summary: defaultString(summary, "The harness did not change its reasoning effort"),
	})
}

// ModelsNegotiated reports whether set_model frames can arrive.
func (s *Stream) ModelsNegotiated() bool {
	return s.models
}

// modelUnavailable reports a refused switch without ending the session.
func (s *Stream) modelUnavailable(summary string) error {
	return s.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindError,
		Code: "model_unavailable", Summary: defaultString(summary, "The harness did not switch models"),
	})
}
