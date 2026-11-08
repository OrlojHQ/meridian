package harnessadapter

import (
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
	return &adapterproto.ModelSet{Current: m.current, Options: options}
}

// ReportModels sends the harness's model list when the controller negotiated
// models frames, and drops it otherwise.
func (s *Stream) ReportModels(models *modelSet) error {
	if !s.models || models == nil {
		return nil
	}
	return s.Write(adapterproto.Frame{
		Protocol: adapterproto.Version, Type: adapterproto.KindModels,
		Models: models.set(),
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
