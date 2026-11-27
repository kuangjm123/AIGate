// Package codexcat renders the model catalog Codex reads from
// model_catalog_json and from its model_catalog_url.
package codexcat

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// Prompt is the base instruction Codex sends for a model whose entry has no
// instructions of its own. A catalog entry must carry instructions, or Codex
// refuses the whole catalog.
const Prompt = "You are a coding agent working in the user's workspace. Inspect the files and tools you need, keep edits focused, verify the result, and explain what changed."

// AppList is how many listed entries the Codex desktop app reads.
const AppList = 100

// Model is one conversation model AIGate can serve.
// Vision nil means its image support is unknown; only an explicit true offers
// images to Codex.
type Model struct {
	ID        string
	Name      string
	Context   int
	Reasoning bool
	Vision    *bool
}

type reasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type truncationPolicy struct {
	Mode  string `json:"mode"`
	Limit int    `json:"limit"`
}

type entry struct {
	Slug                string           `json:"slug"`
	DisplayName         string           `json:"display_name"`
	Description         string           `json:"description"`
	Instructions        string           `json:"base_instructions"`
	DefaultEffort       *string          `json:"default_reasoning_level"`
	Efforts             []reasoningLevel `json:"supported_reasoning_levels"`
	Shell               string           `json:"shell_type"`
	Visibility          string           `json:"visibility"`
	InAPI               bool             `json:"supported_in_api"`
	Priority            int              `json:"priority"`
	Verbosity           bool             `json:"support_verbosity"`
	DefaultVerbosity    *string          `json:"default_verbosity"`
	ApplyPatch          string           `json:"apply_patch_tool_type"`
	Truncation          truncationPolicy `json:"truncation_policy"`
	Tools               []string         `json:"experimental_supported_tools"`
	Modalities          []string         `json:"input_modalities"`
	ContextWindow       *int             `json:"context_window,omitempty"`
	EffectiveContextPct int              `json:"effective_context_window_percent"`
	AutoCompact         *int             `json:"auto_compact_token_limit,omitempty"`
	Tiers               []any            `json:"service_tiers"`
	SearchTool          bool             `json:"supports_search_tool"`
	ParallelToolCalls   bool             `json:"supports_parallel_tool_calls"`
	ReasoningSummaries  bool             `json:"supports_reasoning_summaries"`
}

// Catalog renders the complete {"models":[...]} document. effort is the
// config's model_reasoning_effort and used is ordered most recent first.
func Catalog(models []Model, effort string, used []string) []byte {
	entries := entriesFor(models, effort)
	fitted := fit(entries, used)
	out := struct {
		Models []entry `json:"models"`
	}{Models: fitted}
	if out.Models == nil {
		out.Models = []entry{}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return []byte(`{"models":[]}` + "\n")
	}
	return append(b, '\n')
}

// Valid reports whether b is one JSON object whose models field is an array
// of entry objects. A file with extra top-level fields is valid.
func Valid(b []byte) bool {
	if len(bytes.TrimSpace(b)) == 0 {
		return false
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(b, &doc) != nil || doc == nil {
		return false
	}
	raw, ok := doc["models"]
	if !ok || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	var entries []map[string]any
	return json.Unmarshal(raw, &entries) == nil
}

// Keep carries the fields the user added to an existing catalog over the
// generated one. A current file that does not parse is ignored. Two known
// user overrides also survive: supports_reasoning_summaries and
// supports_image_detail_original are kept when the user set them true and
// the generated entry does not.
func Keep(current, generated []byte) []byte {
	if len(bytes.TrimSpace(current)) == 0 || !Valid(current) {
		return generated
	}
	var oldDoc map[string]json.RawMessage
	if decodeCatalog(current, &oldDoc) != nil || oldDoc == nil {
		return generated
	}
	var nextDoc map[string]json.RawMessage
	if decodeCatalog(generated, &nextDoc) != nil || nextDoc == nil {
		return generated
	}
	oldModels, err := decodeModels(oldDoc["models"])
	if err != nil {
		return generated
	}
	nextModels, err := decodeModels(nextDoc["models"])
	if err != nil {
		return generated
	}
	old := make(map[string]map[string]any, len(oldModels))
	for _, row := range oldModels {
		slug, _ := row["slug"].(string)
		if slug != "" {
			old[slug] = row
		}
	}
	changed := false
	for i := range nextModels {
		slug, _ := nextModels[i]["slug"].(string)
		was := old[slug]
		if was == nil {
			continue
		}
		for key, value := range was {
			if owned(key) {
				if keepTrue(key) && value == true && nextModels[i][key] != true {
					nextModels[i][key] = true
					changed = true
				}
				continue
			}
			if _, exists := nextModels[i][key]; exists {
				continue
			}
			nextModels[i][key] = value
			changed = true
		}
	}
	for key, value := range oldDoc {
		if key == "models" {
			continue
		}
		if _, exists := nextDoc[key]; exists {
			continue
		}
		nextDoc[key] = value
		changed = true
	}
	if !changed {
		return generated
	}
	models, err := json.Marshal(nextModels)
	if err != nil {
		return generated
	}
	nextDoc["models"] = models
	b, err := json.MarshalIndent(nextDoc, "", "  ")
	if err != nil {
		return generated
	}
	return append(b, '\n')
}

func decodeModels(raw json.RawMessage) ([]map[string]any, error) {
	var models []map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	err := dec.Decode(&models)
	return models, err
}

func decodeCatalog(b []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(out)
}

func owned(key string) bool {
	switch key {
	case "slug", "display_name", "description", "base_instructions", "model_messages",
		"default_reasoning_level", "supported_reasoning_levels", "shell_type",
		"visibility", "supported_in_api", "priority", "support_verbosity",
		"default_verbosity", "apply_patch_tool_type", "truncation_policy",
		"experimental_supported_tools", "input_modalities", "context_window",
		"effective_context_window_percent", "auto_compact_token_limit",
		"max_context_window", "service_tiers", "supports_search_tool",
		"supports_parallel_tool_calls", "supports_reasoning_summaries",
		"availability_nux", "upgrade":
		return true
	default:
		return false
	}
}

func keepTrue(key string) bool {
	return key == "supports_reasoning_summaries" || key == "supports_image_detail_original"
}

func entriesFor(models []Model, effort string) []entry {
	seen := map[string]bool{}
	out := make([]entry, 0, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		name := model.Name
		if name == "" {
			name = id
		}
		levels, def := reasoning(model.Reasoning, effort)
		e := entry{
			Slug:                id,
			DisplayName:         name,
			Description:         name + " via AIGate",
			Instructions:        Prompt,
			DefaultEffort:       def,
			Efforts:             levels,
			Shell:               "unified_exec",
			Visibility:          "list",
			InAPI:               true,
			Priority:            len(out) + 1,
			ApplyPatch:          "freeform",
			Truncation:          truncationPolicy{Mode: "tokens", Limit: 10000},
			Tools:               []string{},
			Modalities:          []string{"text"},
			EffectiveContextPct: 95,
			Tiers:               []any{},
			SearchTool:          false,
			ParallelToolCalls:   true,
			ReasoningSummaries:  model.Reasoning,
		}
		if model.Vision != nil && *model.Vision {
			e.Modalities = append(e.Modalities, "image")
		}
		if model.Context > 0 {
			window := model.Context
			e.ContextWindow = &window
			e.AutoCompact = &window
		}
		if len(levels) == 0 {
			e.DefaultEffort = nil
		}
		out = append(out, e)
	}
	return out
}

func reasoning(on bool, chosen string) ([]reasoningLevel, *string) {
	if !on {
		return []reasoningLevel{}, nil
	}
	names := []string{"low", "medium", "high", "xhigh"}
	chosen = strings.ToLower(strings.TrimSpace(chosen))
	if chosen != "" && !contains(names, chosen) {
		names = append(names, chosen)
	}
	levels := make([]reasoningLevel, 0, len(names))
	for _, name := range names {
		levels = append(levels, reasoningLevel{Effort: name, Description: effortLabel(name)})
	}
	def := "medium"
	if chosen != "" {
		def = chosen
	}
	return levels, &def
}

func effortLabel(effort string) string {
	switch effort {
	case "none":
		return "None"
	case "minimal":
		return "Minimal"
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "xhigh":
		return "Extra High"
	case "max":
		return "Max"
	case "ultra":
		return "Ultra"
	default:
		return effort
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// fit keeps hidden entries last and, when the app's first hundred cannot hold
// every listed model, moves the models Codex used into it without dropping
// anything. Priorities are renumbered after the order is chosen.
func fit(entries []entry, used []string) []entry {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Visibility != "hide" && entries[j].Visibility == "hide"
	})
	listed := 0
	for _, e := range entries {
		if e.Visibility != "hide" {
			listed++
		}
	}
	if listed > AppList && len(used) > 0 {
		want := map[string]bool{}
		for _, id := range used {
			if id != "" && len(want) < AppList {
				want[id] = true
			}
		}
		first := make([]entry, 0, AppList)
		rest := make([]entry, 0, len(entries))
		for _, e := range entries {
			if len(first) < AppList && e.Visibility != "hide" && want[e.Slug] {
				first = append(first, e)
			} else {
				rest = append(rest, e)
			}
		}
		filled := make([]entry, 0, len(entries))
		for _, e := range entries {
			if len(filled) >= AppList {
				break
			}
			if e.Visibility != "hide" && !want[e.Slug] {
				filled = append(filled, e)
			}
		}
		chosen := make(map[int]bool, len(first)+len(filled))
		for _, e := range first {
			for i := range entries {
				if entries[i].Slug == e.Slug && !chosen[i] {
					chosen[i] = true
					break
				}
			}
		}
		for _, e := range filled {
			if len(chosen) >= AppList {
				break
			}
			for i := range entries {
				if entries[i].Slug == e.Slug && !chosen[i] {
					chosen[i] = true
					break
				}
			}
		}
		ordered := make([]entry, 0, len(entries))
		for i, e := range entries {
			if chosen[i] {
				ordered = append(ordered, e)
			}
		}
		for i, e := range entries {
			if !chosen[i] {
				ordered = append(ordered, e)
			}
		}
		entries = ordered
	}
	for i := range entries {
		entries[i].Priority = i + 1
	}
	return entries
}
