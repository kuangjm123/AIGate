package reasoning

import (
	"encoding/json"
	"strings"
)

// Signal is the thinking request read from an inbound body.
// Endpoint keeps the normalized effort even though thinkOff can remain set:
// a model that cannot switch thinking off still needs the nearest level.
type Signal struct {
	Effort       string
	Named        bool
	FromBudget   bool
	ThinkOff     bool
	Budget       *int
	ThinkingType string
	Thinking     map[string]json.RawMessage
	MaxTokens    int
	HasMaxTokens bool
}

// Read reads the thinking request from an inbound body in its dialect.
func Read(dialect string, body []byte) Signal {
	doc, ok := readObject(body)
	if !ok {
		return Signal{}
	}
	var got Signal
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "chat":
		got = readChat(doc)
	case "responses", "codex":
		got = readResponses(doc)
	case "anthropic":
		got = readAnthropic(doc)
	case "gemini":
		got = readGemini(doc)
	case "ollama":
		got = readOllama(doc)
	}
	if got.Effort == "" && got.Budget != nil {
		got.Effort = LevelFromBudget(*got.Budget)
		got.FromBudget = got.Effort != ""
	}
	return got
}

func readChat(doc map[string]json.RawMessage) Signal {
	var got Signal
	if raw, ok := doc["reasoning_effort"]; ok {
		got = setNamedEffort(got, stringValue(raw), true)
	}
	if got.Effort == "" {
		if raw, ok := doc["reasoning"]; ok {
			if reasoning, ok := readObject(raw); ok {
				if effort, ok := stringField(reasoning, "effort"); ok {
					got = setNamedEffort(got, effort, true)
				}
			}
		}
	}
	if got.Effort == "" {
		got = readChatGoogle(doc, got)
	}
	got = readThinking(doc, got)
	if maxTokens, ok := intField(doc, "max_tokens"); ok {
		got.MaxTokens, got.HasMaxTokens = maxTokens, true
	} else if maxTokens, ok := intField(doc, "max_completion_tokens"); ok {
		got.MaxTokens, got.HasMaxTokens = maxTokens, true
	}
	return got
}

func readChatGoogle(doc map[string]json.RawMessage, got Signal) Signal {
	extra, ok := objectField(doc, "extra_body")
	if !ok {
		extra, ok = objectField(doc, "extraBody")
	}
	if !ok {
		return got
	}
	google, ok := objectField(extra, "google")
	if !ok {
		return got
	}
	thinking, ok := objectField(google, "thinking_config")
	if !ok {
		thinking, ok = objectField(google, "thinkingConfig")
	}
	if !ok {
		return got
	}
	if level, ok := stringField(thinking, "thinking_level"); ok {
		return setNamedEffort(got, level, false)
	}
	if level, ok := stringField(thinking, "thinkingLevel"); ok {
		return setNamedEffort(got, level, false)
	}
	for _, key := range []string{"thinking_budget", "thinkingBudget"} {
		budget, ok := intField(thinking, key)
		if !ok {
			continue
		}
		switch {
		case budget < 0:
			got = setNamedEffort(got, "none", true)
			got.ThinkOff = true
		case budget == 0:
			got = setNamedEffort(got, "low", false)
		default:
			got.Budget = &budget
		}
		break
	}
	return got
}

func readResponses(doc map[string]json.RawMessage) Signal {
	var got Signal
	if reasoning, ok := objectField(doc, "reasoning"); ok {
		if effort, ok := stringField(reasoning, "effort"); ok {
			got = setNamedEffort(got, effort, true)
		}
		if budget, ok := intField(reasoning, "budget_tokens"); ok {
			got.Budget = &budget
		}
	} else if raw, ok := doc["reasoning_effort"]; ok {
		got = setNamedEffort(got, stringValue(raw), true)
	}
	got = readThinking(doc, got)
	if maxTokens, ok := intField(doc, "max_output_tokens"); ok {
		got.MaxTokens, got.HasMaxTokens = maxTokens, true
	}
	return got
}

func readAnthropic(doc map[string]json.RawMessage) Signal {
	var got Signal
	if output, ok := objectField(doc, "output_config"); ok {
		if effort, ok := stringField(output, "effort"); ok {
			got = setNamedEffort(got, effort, false)
		}
	}
	if messages, ok := rawArray(doc["messages"]); ok {
		for _, raw := range messages {
			message, ok := readObject(raw)
			if !ok {
				continue
			}
			output, ok := objectField(message, "output_config")
			if !ok {
				continue
			}
			if effort, ok := stringField(output, "effort"); ok && strings.TrimSpace(effort) != "" {
				got = setNamedEffort(got, effort, false)
			}
		}
	}
	got = readThinking(doc, got)
	if maxTokens, ok := intField(doc, "max_tokens"); ok {
		got.MaxTokens, got.HasMaxTokens = maxTokens, true
	}
	return got
}

func readGemini(doc map[string]json.RawMessage) Signal {
	var got Signal
	config, ok := objectField(doc, "generationConfig")
	if !ok {
		config, ok = objectField(doc, "generation_config")
	}
	if !ok {
		return got
	}
	thinking, ok := objectField(config, "thinkingConfig")
	if !ok {
		thinking, ok = objectField(config, "thinking_config")
	}
	if !ok {
		return got
	}
	if level, ok := stringField(thinking, "thinkingLevel"); ok {
		got = setNamedEffort(got, level, false)
	} else if level, ok := stringField(thinking, "thinking_level"); ok {
		got = setNamedEffort(got, level, false)
	}
	if budget, ok := intField(thinking, "thinkingBudget"); ok {
		got.Budget = &budget
		if budget == 0 {
			got.ThinkOff = true
		}
	} else if budget, ok := intField(thinking, "thinking_budget"); ok {
		got.Budget = &budget
		if budget == 0 {
			got.ThinkOff = true
		}
	}
	if Rank(got.Effort) == Rank("none") {
		got.ThinkOff = true
	}
	return got
}

func readOllama(doc map[string]json.RawMessage) Signal {
	var got Signal
	raw, ok := doc["think"]
	if !ok {
		return got
	}
	var enabled bool
	if json.Unmarshal(raw, &enabled) == nil {
		got.ThinkOff = !enabled
		return got
	}
	var level string
	if json.Unmarshal(raw, &level) == nil {
		got = setNamedEffort(got, level, false)
		if Rank(level) == Rank("none") {
			got.ThinkOff = true
		}
	}
	return got
}

func readThinking(doc map[string]json.RawMessage, got Signal) Signal {
	thinking, ok := objectField(doc, "thinking")
	if !ok {
		return got
	}
	got.Thinking = thinking
	kind, _ := stringField(thinking, "type")
	got.ThinkingType = strings.ToLower(strings.TrimSpace(kind))
	if got.ThinkingType == "disabled" {
		got.ThinkOff = true
	}
	if budget, ok := intField(thinking, "budget_tokens"); ok {
		got.Budget = &budget
	}
	return got
}

func setNamedEffort(got Signal, value string, off bool) Signal {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return got
	}
	if off && value == "off" {
		value = "none"
		got.ThinkOff = true
	}
	got.Effort = value
	got.Named = true
	if value == "none" {
		got.ThinkOff = true
	}
	return got
}

func stringValue(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func rawArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var out []json.RawMessage
	if json.Unmarshal(raw, &out) != nil {
		return nil, false
	}
	return out, true
}
