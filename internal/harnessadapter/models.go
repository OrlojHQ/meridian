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
	// images is true when the harness accepts image content.
	images bool
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
		Images:   m.images,
	}
	if m.effort != nil && len(m.effort.options) != 0 {
		efforts := m.effort.options[:min(len(m.effort.options), adapterproto.MaxEfforts)]
		// A level this model does not offer is clamped to one it does, so
		// the composer keeps a real level instead of an empty current.
		current := clampEffortID(effortIDs(efforts), m.effort.current, "")
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

// effortScale is the canonical reasoning-effort order. A level the current
// model does not offer is clamped along it.
var effortScale = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

func effortRank(level string) (int, bool) {
	for index, name := range effortScale {
		if name == level {
			return index, true
		}
	}
	return 0, false
}

func effortIDs(options []adapterproto.ModelOption) []string {
	ids := make([]string, len(options))
	for index, option := range options {
		ids[index] = option.ID
	}
	return ids
}

// clampEffortID is the level a session runs. want is the requested or last
// reported level; fallback is the model's own default when the harness
// named one. want wins when this model offers it. Otherwise fallback wins
// when it is offered. A want this model does not offer, and no usable
// fallback, is clamped to the highest offered level that does not exceed
// it, or the lowest offered level when want sits below all of them. An
// empty result means the level is unknown and the model did not name a
// default.
func clampEffortID(ids []string, want, fallback string) string {
	if want != "" && slices.Contains(ids, want) {
		return want
	}
	if fallback != "" && slices.Contains(ids, fallback) {
		return fallback
	}
	if want == "" {
		return ""
	}
	wantRank, ranked := effortRank(want)
	if !ranked {
		if len(ids) > 0 {
			return ids[0]
		}
		return ""
	}
	best := ""
	bestRank := -1
	lowest := ""
	lowestRank := 0
	for _, id := range ids {
		rank, ok := effortRank(id)
		if !ok {
			continue
		}
		if lowest == "" || rank < lowestRank {
			lowest, lowestRank = id, rank
		}
		if rank <= wantRank && rank > bestRank {
			best, bestRank = id, rank
		}
	}
	if best != "" {
		return best
	}
	if lowest != "" {
		return lowest
	}
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
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
	if !s.images {
		set.Images = false
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
