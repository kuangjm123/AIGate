package state

import (
	"encoding/json"
	"strings"
)

type modelInfoJSON struct {
	ID               string    `json:"id"`
	DisplayName      string    `json:"displayName,omitempty"`
	Kind             string    `json:"kind,omitempty"`
	Hidden           bool      `json:"hidden,omitempty"`
	Context          int       `json:"context,omitempty"`
	Reasoning        bool      `json:"reasoning,omitempty"`
	ReasoningEfforts *[]string `json:"reasoningEfforts,omitempty"`
	Vision           *bool     `json:"vision,omitempty"`
}

// MarshalJSON keeps an explicitly empty level list in the file.
func (m ModelInfo) MarshalJSON() ([]byte, error) {
	aux := modelInfoJSON{
		ID: m.ID, DisplayName: m.DisplayName, Kind: m.Kind, Hidden: m.Hidden,
		Context: m.Context, Reasoning: m.Reasoning, Vision: m.Vision,
	}
	if m.ReasoningEffortsKnown || m.ReasoningEfforts != nil {
		levels := append([]string(nil), m.ReasoningEfforts...)
		if levels == nil {
			levels = []string{}
		}
		aux.ReasoningEfforts = &levels
	}
	return json.Marshal(aux)
}

// UnmarshalJSON accepts reasoningEfforts, reasoning_efforts, reasoningEffort,
// and levels. The first three are what coding-agent catalogs name the list.
func (m *ModelInfo) UnmarshalJSON(data []byte) error {
	var aux modelInfoJSON
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.ID = aux.ID
	m.DisplayName = aux.DisplayName
	m.Kind = aux.Kind
	m.Hidden = aux.Hidden
	m.Context = aux.Context
	m.Reasoning = aux.Reasoning
	m.Vision = aux.Vision
	m.ReasoningEfforts = nil
	m.ReasoningEffortsKnown = false
	for _, key := range []string{"reasoningEfforts", "reasoning_efforts", "reasoningEffort", "reasoning_effort", "levels"} {
		value, ok := raw[key]
		if !ok || len(value) == 0 || string(value) == "null" {
			continue
		}
		levels, known := decodeEfforts(value)
		if !known {
			continue
		}
		m.ReasoningEfforts = levels
		m.ReasoningEffortsKnown = true
		if len(levels) > 0 {
			m.Reasoning = true
		}
		break
	}
	return nil
}

func decodeEfforts(raw json.RawMessage) ([]string, bool) {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return cleanEfforts(list), true
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		if strings.TrimSpace(one) == "" {
			return []string{}, true
		}
		return cleanEfforts([]string{one}), true
	}
	return nil, false
}

func cleanEfforts(in []string) []string {
	out := make([]string, 0, len(in))
	for _, level := range in {
		level = strings.ToLower(strings.TrimSpace(level))
		if level != "" {
			out = append(out, level)
		}
	}
	return out
}

// ReasoningLevels reports the model's level list. known is false when the
// catalog has not said anything about a selector.
func (m ModelInfo) ReasoningLevels() (levels []string, known bool) {
	if m.ReasoningEffortsKnown {
		return append([]string(nil), m.ReasoningEfforts...), true
	}
	if m.ReasoningEfforts != nil {
		return append([]string(nil), m.ReasoningEfforts...), true
	}
	return nil, false
}
