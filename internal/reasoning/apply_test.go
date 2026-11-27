package reasoning

import (
	"encoding/json"
	"testing"
)

func TestNormalizeAndFit(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		levels []string
		got    string
	}{
		{"alias light to low", "light", nil, "low"},
		{"alias extra high", "extra_high", nil, "xhigh"},
		{"nearest up on a tie", "xhigh", []string{"low", "high", "max"}, "max"},
		{"nearest skips none", "low", []string{"none", "high"}, "high"},
		{"none stays when listed", "none", []string{"none", "low"}, "none"},
		{"ultra becomes max", "ultra", []string{"low", "high", "max", "ultra"}, "max"},
		{"ultra falls back", "ultra", []string{"low", "high"}, "high"},
		{"model spelling wins", "low", []string{"light", "extra_high"}, "light"},
	}
	for _, test := range tests {
		if got := Fit(test.want, test.levels); got != test.got {
			t.Errorf("%s: Fit(%q, %v) = %q, want %q", test.name, test.want, test.levels, got, test.got)
		}
	}
}

func TestFitForLevellessAndUnknown(t *testing.T) {
	if got := FitFor("xhigh", nil, true); got != "high" {
		t.Fatalf("levelless xhigh = %q, want high", got)
	}
	if got := FitFor("xhigh", nil, false); got != "xhigh" {
		t.Fatalf("unknown xhigh = %q, want xhigh", got)
	}
	if got := FitFor("max", nil, true); got != "high" {
		t.Fatalf("levelless max = %q, want high", got)
	}
}

func TestBudgetAndLevelFromBudget(t *testing.T) {
	tests := map[string]int{"low": 4096, "medium": 10000, "high": 24000, "xhigh": 32000, "max": 32000, "none": 4096, "": 10000}
	for level, want := range tests {
		if got := Budget(level); got != want {
			t.Errorf("Budget(%q) = %d, want %d", level, got, want)
		}
	}
	budgets := map[int]string{0: "", 100: "low", 4096: "low", 5000: "medium", 12000: "medium", 24000: "high", 32000: "xhigh"}
	for budget, want := range budgets {
		if got := LevelFromBudget(budget); got != want {
			t.Errorf("LevelFromBudget(%d) = %q, want %q", budget, got, want)
		}
	}
}

func TestReadSignals(t *testing.T) {
	chat := Read("chat", []byte(`{"model":"m","reasoning_effort":"off"}`))
	if chat.Effort != "none" || !chat.Named || !chat.ThinkOff {
		t.Fatalf("chat off = %+v", chat)
	}
	responses := Read("responses", []byte(`{"model":"m","reasoning":{"effort":"ultra","budget_tokens":32000}}`))
	if responses.Effort != "ultra" || !responses.Named || responses.Budget == nil || *responses.Budget != 32000 {
		t.Fatalf("responses signal = %+v", responses)
	}
	anthropic := Read("anthropic", []byte(`{
		"model":"m","output_config":{"effort":"low"},
		"messages":[{"role":"user","content":"a","output_config":{"effort":"high"}}],
		"thinking":{"type":"enabled","budget_tokens":32000}
	}`))
	if anthropic.Effort != "high" || !anthropic.Named || anthropic.Budget == nil || *anthropic.Budget != 32000 {
		t.Fatalf("anthropic signal = %+v", anthropic)
	}
	noEffort := Read("anthropic", []byte(`{"model":"m","thinking":{"type":"enabled","budget_tokens":32000}}`))
	if noEffort.Effort != "xhigh" || !noEffort.FromBudget {
		t.Fatalf("budget signal = %+v", noEffort)
	}
	gemini := Read("gemini", []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":0,"includeThoughts":true}}}`))
	if !gemini.ThinkOff || gemini.Budget == nil || *gemini.Budget != 0 {
		t.Fatalf("gemini signal = %+v", gemini)
	}
	relay := Read("chat", []byte(`{"model":"gemini-3-pro","extra_body":{"google":{"thinking_config":{"thinking_budget":0}}}}`))
	if relay.Effort != "low" || !relay.Named || relay.ThinkOff {
		t.Fatalf("chat google signal = %+v", relay)
	}
}

func TestApplyAcceptance(t *testing.T) {
	type test struct {
		name    string
		dialect string
		body    string
		signal  Signal
		target  Target
		check   func(*testing.T, map[string]any, Result)
	}
	tests := []test{
		{
			name:    "chat off fits low",
			dialect: "chat",
			body:    `{"model":"m","reasoning_effort":"off"}`,
			signal:  Signal{Effort: "none", Named: true, ThinkOff: true},
			target:  Target{Dialect: "chat", Model: "m", Levels: []string{"low", "medium", "high"}},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if doc["reasoning_effort"] != "low" {
					t.Fatalf("reasoning_effort = %v", doc["reasoning_effort"])
				}
			},
		},
		{
			name:    "chat none stays off",
			dialect: "chat",
			body:    `{"model":"m","reasoning_effort":"none"}`,
			signal:  Signal{Effort: "none", Named: true, ThinkOff: true},
			target:  Target{Dialect: "chat", Model: "m", Levels: []string{"none", "low", "high"}},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if doc["reasoning_effort"] != "none" {
					t.Fatalf("reasoning_effort = %v", doc["reasoning_effort"])
				}
			},
		},
		{
			name:    "responses ultra max",
			dialect: "responses",
			body:    `{"model":"m","reasoning":{"effort":"ultra"}}`,
			signal:  Signal{Effort: "ultra", Named: true},
			target:  Target{Dialect: "responses", Model: "m", Levels: []string{"low", "high", "max", "ultra"}},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkReasoningEffort(t, doc, "max")
			},
		},
		{
			name:    "responses ultra falls to high",
			dialect: "responses",
			body:    `{"model":"m","reasoning":{"effort":"ultra"}}`,
			signal:  Signal{Effort: "ultra", Named: true},
			target:  Target{Dialect: "responses", Model: "m", Levels: []string{"low", "high"}},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkReasoningEffort(t, doc, "high")
			},
		},
		{
			name:    "responses xhigh tie goes up",
			dialect: "responses",
			body:    `{"model":"m","reasoning":{"effort":"xhigh"}}`,
			signal:  Signal{Effort: "xhigh", Named: true},
			target:  Target{Dialect: "responses", Model: "m", Levels: []string{"low", "high", "max"}},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkReasoningEffort(t, doc, "max")
			},
		},
		{
			name:    "responses xhigh levelless",
			dialect: "responses",
			body:    `{"model":"m","reasoning":{"effort":"xhigh"}}`,
			signal:  Signal{Effort: "xhigh", Named: true},
			target:  Target{Dialect: "responses", Model: "m", Levelless: true},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkReasoningEffort(t, doc, "high")
			},
		},
		{
			name:    "responses xhigh unknown",
			dialect: "responses",
			body:    `{"model":"m","reasoning":{"effort":"xhigh"}}`,
			signal:  Signal{Effort: "xhigh", Named: true},
			target:  Target{Dialect: "responses", Model: "m"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkReasoningEffort(t, doc, "xhigh")
			},
		},
		{
			name:    "claude 4.6 budget only",
			dialect: "anthropic",
			body:    `{"model":"claude-opus-4-6","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":32000},"messages":[]}`,
			signal:  Read("anthropic", []byte(`{"model":"claude-opus-4-6","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":32000},"messages":[]}`)),
			target:  Target{Dialect: "anthropic", Model: "claude-opus-4-6"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkAdaptiveEffort(t, doc, "max")
			},
		},
		{
			name:    "claude 4.6 named xhigh",
			dialect: "anthropic",
			body:    `{"model":"claude-opus-4-6","max_tokens":8192,"output_config":{"effort":"xhigh"},"messages":[]}`,
			signal:  Signal{Effort: "xhigh", Named: true},
			target:  Target{Dialect: "anthropic", Model: "claude-opus-4-6"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkAdaptiveEffort(t, doc, "max")
			},
		},
		{
			name:    "claude 4.7 keeps xhigh",
			dialect: "anthropic",
			body:    `{"model":"claude-opus-4-7","max_tokens":8192,"output_config":{"effort":"xhigh"},"messages":[]}`,
			signal:  Signal{Effort: "xhigh", Named: true},
			target:  Target{Dialect: "anthropic", Model: "claude-opus-4-7"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkAdaptiveEffort(t, doc, "xhigh")
			},
		},
		{
			name:    "sonnet 5.5 close",
			dialect: "anthropic",
			body:    `{"model":"claude-sonnet-5-5","max_tokens":8192,"thinking":{"type":"disabled"},"output_config":{"effort":"max"},"messages":[]}`,
			signal:  Signal{Effort: "max", Named: true, ThinkOff: true, ThinkingType: "disabled"},
			target:  Target{Dialect: "anthropic", Model: "claude-sonnet-5-5"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				thinking := doc["thinking"].(map[string]any)
				if thinking["type"] != "between_tools" {
					t.Fatalf("thinking = %v", thinking)
				}
				if effort := outputEffort(doc); effort != "high" {
					t.Fatalf("effort = %q", effort)
				}
			},
		},
		{
			name:    "opus 5.5 close",
			dialect: "anthropic",
			body:    `{"model":"opus-5.5","max_tokens":1024,"thinking":{"type":"disabled"},"messages":[]}`,
			signal:  Signal{ThinkOff: true, ThinkingType: "disabled"},
			target:  Target{Dialect: "anthropic", Model: "opus-5.5"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if _, ok := doc["thinking"]; ok {
					t.Fatalf("thinking = %v", doc["thinking"])
				}
				if effort := outputEffort(doc); effort != "low" {
					t.Fatalf("effort = %q", effort)
				}
				if doc["max_tokens"] != float64(3072) {
					t.Fatalf("max_tokens = %v", doc["max_tokens"])
				}
			},
		},
		{
			name:    "deepseek official medium",
			dialect: "anthropic",
			body:    `{"model":"deepseek-v4","max_tokens":8192,"output_config":{"effort":"medium"},"messages":[]}`,
			signal:  Signal{Effort: "medium", Named: true},
			target:  Target{Dialect: "anthropic", Model: "deepseek-v4", BaseURL: "https://api.deepseek.com"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if effort := outputEffort(doc); effort != "high" {
					t.Fatalf("effort = %q", effort)
				}
			},
		},
		{
			name:    "deepseek official xhigh folds to high",
			dialect: "anthropic",
			body:    `{"model":"deepseek-v4","max_tokens":8192,"output_config":{"effort":"xhigh"},"messages":[]}`,
			signal:  Signal{Effort: "xhigh", Named: true},
			target:  Target{Dialect: "anthropic", Model: "deepseek-v4", BaseURL: "https://api.deepseek.com"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if effort := outputEffort(doc); effort != "high" {
					t.Fatalf("effort = %q", effort)
				}
			},
		},
		{
			name:    "glm 5.3 max",
			dialect: "anthropic",
			body:    `{"model":"glm-5.3","max_tokens":8192,"output_config":{"effort":"max"},"messages":[]}`,
			signal:  Signal{Effort: "max", Named: true},
			target:  Target{Dialect: "anthropic", Model: "glm-5.3"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkAdaptiveEffort(t, doc, "max")
			},
		},
		{
			name:    "gemini 2.5 flash high",
			dialect: "gemini",
			body:    `{"generationConfig":{}}`,
			signal:  Signal{Effort: "high", Named: true},
			target:  Target{Dialect: "gemini", Model: "gemini-2.5-flash"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if budget := thinkingBudget(t, doc); budget != 24000 {
					t.Fatalf("budget = %d", budget)
				}
			},
		},
		{
			name:    "gemini 2.5 flash none",
			dialect: "gemini",
			body:    `{"generationConfig":{}}`,
			signal:  Signal{Effort: "none", Named: true, ThinkOff: true},
			target:  Target{Dialect: "gemini", Model: "gemini-2.5-flash"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if budget := thinkingBudget(t, doc); budget != 0 {
					t.Fatalf("budget = %d", budget)
				}
			},
		},
		{
			name:    "gemini 3 pro direct medium",
			dialect: "gemini",
			body:    `{"generationConfig":{}}`,
			signal:  Signal{Effort: "medium", Named: true},
			target:  Target{Dialect: "gemini", Model: "gemini-3-pro-preview", GoogleGemini: true},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if level := thinkingLevel(t, doc); level != "medium" {
					t.Fatalf("level = %q", level)
				}
			},
		},
		{
			name:    "gemini 3 pro relay medium",
			dialect: "gemini",
			body:    `{"generationConfig":{}}`,
			signal:  Signal{Effort: "medium", Named: true},
			target:  Target{Dialect: "gemini", Model: "gemini-3-pro-preview", BaseURL: "https://relay.example.com", GeminiRelay: true},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if level := thinkingLevel(t, doc); level != "high" {
					t.Fatalf("level = %q", level)
				}
			},
		},
		{
			name:    "gemini 3 flash minimal",
			dialect: "gemini",
			body:    `{"generationConfig":{}}`,
			signal:  Signal{Effort: "minimal", Named: true},
			target:  Target{Dialect: "gemini", Model: "gemini-3-flash", GoogleGemini: true},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if level := thinkingLevel(t, doc); level != "minimal" {
					t.Fatalf("level = %q", level)
				}
			},
		},
		{
			name:    "gemini 4 relay anthropic budget",
			dialect: "anthropic",
			body:    `{"model":"gemini-4-hydrogen","max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":32000},"messages":[]}`,
			signal:  Signal{Effort: "xhigh", FromBudget: true, Budget: intPointer(32000), ThinkingType: "enabled"},
			target:  Target{Dialect: "anthropic", Model: "gemini-4-hydrogen", BaseURL: "https://relay.example.com"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				thinking := doc["thinking"].(map[string]any)
				if thinking["type"] != "adaptive" {
					t.Fatalf("thinking = %v", thinking)
				}
				if _, ok := thinking["budget_tokens"]; ok {
					t.Fatalf("budget still present: %v", thinking)
				}
				if effort := outputEffort(doc); effort == "" {
					t.Fatal("missing effort")
				}
			},
		},
		{
			name:    "gemini vertex off fits minimal",
			dialect: "gemini",
			body:    `{"generationConfig":{}}`,
			signal:  Signal{Effort: "none", Named: true, ThinkOff: true},
			target:  Target{Dialect: "gemini", Model: "gemini-3-pro", Vertex: true, GoogleGemini: true, Levels: []string{"minimal", "low", "high"}},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if level := thinkingLevel(t, doc); level != "minimal" {
					t.Fatalf("level = %q", level)
				}
			},
		},
		{
			name:    "gemini relay chat budget becomes level",
			dialect: "chat",
			body:    `{"model":"gemini-4","thinking":{"type":"enabled","budget_tokens":32000}}`,
			signal:  Read("anthropic", []byte(`{"model":"gemini-4","thinking":{"type":"enabled","budget_tokens":32000}}`)),
			target:  Target{Dialect: "chat", Model: "gemini-4", BaseURL: "https://relay.example.com", GeminiRelay: true},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if _, ok := doc["thinking"]; ok {
					t.Fatalf("thinking kept: %v", doc["thinking"])
				}
				extra, _ := doc["extra_body"].(map[string]any)
				google, _ := extra["google"].(map[string]any)
				config, _ := google["thinking_config"].(map[string]any)
				if config["thinking_level"] == nil {
					t.Fatalf("config = %v", doc)
				}
				if _, ok := config["thinking_budget"]; ok {
					t.Fatalf("budget kept: %v", config)
				}
			},
		},
		{
			name:    "alias light",
			dialect: "chat",
			body:    `{"model":"m","reasoning_effort":"low"}`,
			signal:  Signal{Effort: "low", Named: true},
			target:  Target{Dialect: "chat", Model: "m", Levels: []string{"light", "extra_high"}},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if doc["reasoning_effort"] != "light" {
					t.Fatalf("reasoning_effort = %v", doc["reasoning_effort"])
				}
			},
		},
		{
			name:    "official openai drops foreign fields",
			dialect: "chat",
			body:    `{"model":"gpt-5","enable_thinking":true,"thinking":{"type":"enabled"},"reasoning_effort":"high"}`,
			signal:  Signal{Effort: "high", Named: true},
			target:  Target{Dialect: "chat", Model: "gpt-5", BaseURL: "https://api.openai.com"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if _, ok := doc["enable_thinking"]; ok {
					t.Fatal("enable_thinking kept")
				}
				if _, ok := doc["thinking"]; ok {
					t.Fatal("thinking kept")
				}
			},
		},
		{
			name:    "fixed Claude level starts thinking",
			dialect: "anthropic",
			body:    `{"model":"claude-opus-4-8","max_tokens":8192,"messages":[]}`,
			target:  Target{Dialect: "anthropic", Model: "claude-opus-4-8", Fixed: "high"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkAdaptiveEffort(t, doc, "high")
			},
		},
		{
			name:    "fixed Gemini level starts thinking",
			dialect: "gemini",
			body:    `{"generationConfig":{}}`,
			target:  Target{Dialect: "gemini", Model: "gemini-3-pro", GoogleGemini: true, Fixed: "medium"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				if level := thinkingLevel(t, doc); level != "medium" {
					t.Fatalf("level = %q", level)
				}
			},
		},
		{
			name:    "fixed Gemini level starts adaptive thinking on Anthropic relay",
			dialect: "anthropic",
			body:    `{"model":"gemini-4","max_tokens":8192,"messages":[]}`,
			target:  Target{Dialect: "anthropic", Model: "gemini-4", BaseURL: "https://relay.example.com", Fixed: "high"},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkAdaptiveEffort(t, doc, "high")
			},
		},
		{
			name:    "mapping off to a level starts Claude thinking",
			dialect: "anthropic",
			body:    `{"model":"claude-opus-4-8","max_tokens":8192,"messages":[]}`,
			signal:  Signal{Effort: "none", Named: true, ThinkOff: true},
			target:  Target{Dialect: "anthropic", Model: "claude-opus-4-8", Mapping: map[string]string{"none": "high"}},
			check: func(t *testing.T, doc map[string]any, got Result) {
				checkAdaptiveEffort(t, doc, "high")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, got := Rewrite([]byte(test.body), test.signal, test.target)
			var doc map[string]any
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("decode: %v\n%s", err, out)
			}
			test.check(t, doc, got)
		})
	}
}

func TestLowestAfterReject(t *testing.T) {
	if got := LowestAfterReject("minimal", []string{"minimal", "low", "high"}); got != "low" {
		t.Fatalf("minimal retry = %q", got)
	}
	if got := LowestAfterReject("high", []string{"minimal", "low", "high"}); got != "minimal" {
		t.Fatalf("high retry = %q", got)
	}
	if got := LowestAfterReject("xhigh", nil); got != "low" {
		t.Fatalf("unknown retry = %q", got)
	}
}

func checkReasoningEffort(t *testing.T, doc map[string]any, want string) {
	t.Helper()
	reasoning, _ := doc["reasoning"].(map[string]any)
	if reasoning == nil || reasoning["effort"] != want {
		t.Fatalf("reasoning = %v, want effort %q", doc["reasoning"], want)
	}
}

func checkAdaptiveEffort(t *testing.T, doc map[string]any, want string) {
	t.Helper()
	thinking, _ := doc["thinking"].(map[string]any)
	if thinking == nil || thinking["type"] != "adaptive" {
		t.Fatalf("thinking = %v", doc["thinking"])
	}
	if effort := outputEffort(doc); effort != want {
		t.Fatalf("effort = %q, want %q", effort, want)
	}
}

func outputEffort(doc map[string]any) string {
	output, _ := doc["output_config"].(map[string]any)
	if output == nil {
		return ""
	}
	value, _ := output["effort"].(string)
	return value
}

func thinkingBudget(t *testing.T, doc map[string]any) int {
	t.Helper()
	config, _ := doc["generationConfig"].(map[string]any)
	thinking, _ := config["thinkingConfig"].(map[string]any)
	value, _ := thinking["thinkingBudget"].(float64)
	return int(value)
}

func thinkingLevel(t *testing.T, doc map[string]any) string {
	t.Helper()
	config, _ := doc["generationConfig"].(map[string]any)
	thinking, _ := config["thinkingConfig"].(map[string]any)
	value, _ := thinking["thinkingLevel"].(string)
	return value
}

func intPointer(v int) *int { return &v }
