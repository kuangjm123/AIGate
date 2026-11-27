package reasoning

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// Target describes one upstream request. Body is already encoded in Dialect.
// Levels is the model's own level list. Levelless reports that the catalog
// explicitly said the model has no level selector.
// Fixed, Allowed, and Mapping are the routing group's request overrides.
type Target struct {
	Dialect      string
	Model        string
	BaseURL      string
	Levels       []string
	Levelless    bool
	Fixed        string
	Allowed      []string
	Mapping      map[string]string
	Force        string
	OpenAI       bool
	GeminiRelay  bool
	Vertex       bool
	GoogleGemini bool
}

// Result is the level actually written and whether the body was changed.
// Effort is the vendor field's level name after all adjustments, or the
// collapsed level when the vendor writes a number instead.
type Result struct {
	Effort string
	Sent   bool
}

// Apply rewrites body and returns only the bytes.
func Apply(body []byte, signal Signal, target Target) []byte {
	out, _ := Rewrite(body, signal, target)
	return out
}

// Rewrite maps the request's thinking request onto one upstream body. body
// must already be in target.Dialect. signal is read from the caller's original
// body so an alias survives a cross-protocol conversion.
func Rewrite(body []byte, signal Signal, target Target) ([]byte, Result) {
	doc, ok := readObject(body)
	if !ok {
		return append([]byte(nil), body...), Result{}
	}
	levels, levelless := targetLevels(target)
	effort, thinkOff := effective(signal, target, levels, levelless)
	out := cloneObject(doc)
	var result Result
	switch strings.ToLower(strings.TrimSpace(target.Dialect)) {
	case "chat":
		result = writeChat(out, signal, effort, thinkOff, target, levels)
	case "responses", "codex":
		result = writeResponses(out, signal, effort, thinkOff, target, levels)
	case "anthropic":
		result = writeAnthropic(out, signal, effort, thinkOff, target, levels)
	case "gemini":
		result = writeGemini(out, signal, effort, thinkOff, target, levels)
	case "ollama":
		result = writeOllama(out, signal, effort, thinkOff, target, levels)
	default:
		return append([]byte(nil), body...), Result{}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return append([]byte(nil), body...), Result{}
	}
	return encoded, result
}

func targetLevels(t Target) ([]string, bool) {
	if len(t.Allowed) > 0 {
		return CleanLevels(t.Allowed), false
	}
	levels := CleanLevels(t.Levels)
	return levels, t.Levelless
}

func effective(signal Signal, target Target, levels []string, levelless bool) (string, bool) {
	effort := Normalize(signal.Effort)
	thinkOff := signal.ThinkOff
	if fixed := Normalize(target.Fixed); fixed != "" {
		effort = fixed
		thinkOff = Rank(fixed) == Rank("none")
	}
	if effort == "" && thinkOff {
		effort = "none"
	}
	if effort != "" {
		wasOff := Rank(effort) == Rank("none")
		effort = mapEffort(effort, target.Mapping)
		if wasOff && Rank(effort) != Rank("none") {
			thinkOff = false
		} else if Rank(effort) == Rank("none") {
			thinkOff = true
		}
	}
	if forced := Normalize(target.Force); forced != "" {
		effort = forced
		thinkOff = Rank(forced) == Rank("none")
	}
	if effort != "" {
		effort = FitFor(effort, levels, levelless)
	}
	return effort, thinkOff || Rank(effort) == Rank("none")
}

func mapEffort(effort string, mapping map[string]string) string {
	if len(mapping) == 0 {
		return effort
	}
	try := func(key string) (string, bool) {
		if value, ok := mapping[key]; ok {
			if value = Normalize(value); value != "" {
				return value, true
			}
		}
		for key2, value := range mapping {
			if Normalize(key2) == key {
				if value = Normalize(value); value != "" {
					return value, true
				}
			}
		}
		return "", false
	}
	if value, ok := try(effort); ok {
		return value
	}
	if Normalize(effort) == "ultra" {
		if value, ok := try("max"); ok {
			return value
		}
	}
	return effort
}

// LowestAfterReject returns the level to send after an upstream rejected the
// current level by name. When the catalog's lowest is the rejected level, the
// next stronger one is used so the retry really names another level.
func LowestAfterReject(current string, levels []string) string {
	levels = WithoutNone(CleanLevels(levels))
	current = Normalize(current)
	if len(levels) == 0 {
		return "low"
	}
	lowest := ""
	next := ""
	for _, level := range levels {
		rank := Rank(level)
		if rank < 0 {
			continue
		}
		if lowest == "" || rank < Rank(lowest) {
			lowest = level
		}
	}
	if Normalize(lowest) != current {
		return lowest
	}
	for _, level := range levels {
		rank := Rank(level)
		if rank > Rank(lowest) && (next == "" || rank < Rank(next)) {
			next = level
		}
	}
	if next != "" {
		return next
	}
	return "low"
}

func writeChat(doc map[string]json.RawMessage, signal Signal, effort string, thinkOff bool, target Target, levels []string) Result {
	if officialOpenAI(target) {
		deleteField(doc, "enable_thinking")
		deleteField(doc, "thinking")
	}
	if effort == "" && !thinkOff {
		return Result{}
	}
	if isGeminiModel(target.Model) && !directGemini(target) && !target.GeminiRelay {
		// A Gemini body on Chat is a relay even when the address is empty.
		target.GeminiRelay = true
	}
	written := effort
	if isGeminiModel(target.Model) && target.GeminiRelay {
		written = geminiLevel(target.Model, effort, levels)
		if written == "" {
			written = "low"
		}
		if writeGeminiChatExtra(doc, written) {
			setString(doc, "reasoning_effort", written)
			return Result{Effort: written, Sent: true}
		}
		if writeGeminiChatBudget(doc, written) {
			setString(doc, "reasoning_effort", written)
			return Result{Effort: written, Sent: true}
		}
	}
	setString(doc, "reasoning_effort", written)
	return Result{Effort: written, Sent: true}
}

func writeResponses(doc map[string]json.RawMessage, signal Signal, effort string, thinkOff bool, target Target, levels []string) Result {
	if officialOpenAI(target) {
		deleteField(doc, "enable_thinking")
		deleteField(doc, "thinking")
	}
	if effort == "" && !thinkOff {
		return Result{}
	}
	written := effort
	if isGeminiModel(target.Model) && target.GeminiRelay {
		written = geminiLevel(target.Model, effort, levels)
		if written == "" {
			written = "low"
		}
	}
	reasoning, _ := objectField(doc, "reasoning")
	if reasoning == nil {
		reasoning = map[string]json.RawMessage{}
	} else {
		reasoning = cloneObject(reasoning)
	}
	setString(reasoning, "effort", written)
	deleteField(reasoning, "budget_tokens")
	setObject(doc, "reasoning", reasoning)
	deleteField(doc, "reasoning_effort")
	return Result{Effort: written, Sent: true}
}

func writeAnthropic(doc map[string]json.RawMessage, signal Signal, effort string, thinkOff bool, target Target, levels []string) Result {
	if !hasThinkingRequest(signal) && effort == "" && !thinkOff {
		return Result{}
	}
	if isGeminiModel(target.Model) {
		return writeGeminiAnthropic(doc, signal, effort, thinkOff, target, levels)
	}
	if isDeepSeekOfficial(target) {
		candidates := levels
		if len(candidates) == 0 {
			candidates = []string{"low", "high", "max"}
		}
		written := deepSeekLevel(effort, candidates)
		writeOutputEffort(doc, written)
		if thinkOff {
			setThinkingType(doc, "disabled")
		}
		return Result{Effort: written, Sent: written != ""}
	}
	if isGLM(target.Model) {
		written := effort
		if thinkOff {
			setThinkingType(doc, "disabled")
		} else if written != "" || signal.Named || signal.Budget != nil {
			setAdaptive(doc, signal)
		}
		if written == "none" {
			written = ""
		}
		writeOutputEffort(doc, written)
		return Result{Effort: written, Sent: written != "" || thinkOff}
	}
	if isMiniMax(target.Model) || isGPTNumber(target.Model) {
		written := effort
		if written == "none" && !thinkOff {
			written = "low"
		}
		if thinkOff {
			setThinkingType(doc, "disabled")
		}
		writeOutputEffort(doc, written)
		return Result{Effort: written, Sent: written != "" || thinkOff}
	}
	if claude, ok := parseClaude(target.Model); ok {
		return writeClaude(doc, signal, effort, thinkOff, claude)
	}
	if effort == "none" {
		effort = ""
	}
	if _, ok := objectField(doc, "output_config"); ok && effort != "" {
		writeOutputEffort(doc, effort)
	} else if effort != "" && (target.Fixed != "" || target.Force != "") {
		writeOutputEffort(doc, effort)
	}
	return Result{Effort: effort, Sent: effort != ""}
}

func writeGeminiAnthropic(doc map[string]json.RawMessage, signal Signal, effort string, thinkOff bool, target Target, levels []string) Result {
	if effort == "" && !thinkOff && !signal.Named && signal.Budget == nil && signal.ThinkingType == "" {
		return Result{}
	}
	if thinkOff {
		setThinkingType(doc, "disabled")
		return Result{Effort: "low", Sent: true}
	}
	written := geminiLevel(target.Model, effort, levels)
	if written == "" {
		written = "low"
	}
	if signal.ThinkingType == "enabled" || signal.ThinkingType == "adaptive" || signal.Budget != nil ||
		target.Fixed != "" || target.Force != "" {
		setAdaptive(doc, signal)
	}
	writeOutputEffort(doc, written)
	return Result{Effort: written, Sent: true}
}

func writeClaude(doc map[string]json.RawMessage, signal Signal, effort string, thinkOff bool, c claudeModel) Result {
	if thinkOff {
		if written, handled := closeClaude(doc, signal, effort, c); handled {
			return Result{Effort: written, Sent: true}
		}
		if effort != "" && effort != "none" {
			if _, ok := objectField(doc, "output_config"); ok {
				writeOutputEffort(doc, effort)
			}
		}
		return Result{Effort: effort, Sent: thinkOff}
	}
	if c.adaptive() {
		setAdaptive(doc, signal)
		written := effort
		if signal.FromBudget && written == "xhigh" {
			written = "max"
		}
		if c.major == 4 && c.minor == 6 && written == "xhigh" {
			written = "max"
		}
		if written == "none" {
			written = ""
		}
		writeOutputEffort(doc, written)
		return Result{Effort: written, Sent: written != ""}
	}
	return writeLegacyClaude(doc, signal, effort, c)
}

func closeClaude(doc map[string]json.RawMessage, signal Signal, effort string, c claudeModel) (string, bool) {
	switch {
	case c.family == "sonnet" && c.major == 5 && c.minor == 5:
		setThinkingOnly(doc, "between_tools")
		written := capHigh(effort)
		writeOutputEffort(doc, written)
		return written, true
	case (c.family == "opus" && c.major == 5 && c.minor == 5) ||
		((c.family == "fable" || c.family == "mythos") && c.major == 5):
		deleteField(doc, "thinking")
		written := effort
		if written == "" || written == "none" {
			written = "low"
		}
		writeOutputEffort(doc, written)
		if maxTokens, ok := intField(doc, "max_tokens"); ok && maxTokens > 0 && maxTokens < 2048 {
			setInt(doc, "max_tokens", maxTokens+2048)
		}
		return written, true
	case (c.family == "opus" && c.major == 5) ||
		(c.family == "haiku" && c.major == 5 && c.minor == 5):
		setThinkingType(doc, "disabled")
		written := capHigh(effort)
		writeOutputEffort(doc, written)
		return written, true
	case (c.family == "sonnet" && c.major == 5) ||
		(c.family == "haiku" && c.major == 5) ||
		c.major < 5:
		setThinkingType(doc, "disabled")
		return effort, true
	default:
		return effort, false
	}
}

func writeLegacyClaude(doc map[string]json.RawMessage, signal Signal, effort string, c claudeModel) Result {
	on := signal.ThinkingType == "enabled" || signal.Budget != nil || signal.Named
	if !on {
		return Result{Effort: effort, Sent: false}
	}
	maxTokens, hasMax := intField(doc, "max_tokens")
	if hasMax && maxTokens > 0 && maxTokens <= 2048 {
		deleteField(doc, "thinking")
		return Result{Effort: effort, Sent: true}
	}
	budget := Budget(Collapse(effort))
	if hasMax && maxTokens > 1 && budget > maxTokens-1024 {
		budget = maxTokens - 1024
		if budget < 1 {
			budget = 1
		}
	}
	if !hasMax {
		fallback := claudeDefaultOutput(c)
		if fallback < budget+4096 {
			setInt(doc, "max_tokens", budget+4096)
		}
	}
	thinking, _ := objectField(doc, "thinking")
	if thinking == nil {
		thinking = map[string]json.RawMessage{}
	} else {
		thinking = cloneObject(thinking)
	}
	setString(thinking, "type", "enabled")
	setInt(thinking, "budget_tokens", budget)
	setObject(doc, "thinking", thinking)
	return Result{Effort: effort, Sent: true}
}

func writeGemini(doc map[string]json.RawMessage, signal Signal, effort string, thinkOff bool, target Target, levels []string) Result {
	generation, minor, _, ok := parseGemini(target.Model)
	if !ok {
		return Result{}
	}
	if generation < 2 || (generation == 2 && minor == 0) {
		cfg, camel := geminiConfig(doc, false)
		removeThinkingConfig(cfg, camel)
		setGeminiConfig(doc, cfg, camel)
		return Result{}
	}
	if !hasThinkingRequest(signal) && effort == "" && !thinkOff {
		return Result{}
	}
	cfg, camel := geminiConfig(doc, true)
	if generation == 2 && minor == 5 {
		written := writeGemini25(cfg, camel, target.Model, effort, thinkOff)
		setGeminiConfig(doc, cfg, camel)
		return Result{Effort: written, Sent: true}
	}
	if generation < 3 && !(generation == 2 && minor >= 5) {
		removeThinkingConfig(cfg, camel)
		setGeminiConfig(doc, cfg, camel)
		return Result{}
	}
	if generation == 2 {
		written := writeGemini25(cfg, camel, target.Model, effort, thinkOff)
		setGeminiConfig(doc, cfg, camel)
		return Result{Effort: written, Sent: true}
	}
	var written string
	if !directGemini(target) {
		written = geminiLevel(target.Model, effort, levels)
		if written == "" {
			if thinkOff {
				written = "low"
			}
		}
	} else {
		written = directGeminiLevel(effort, thinkOff, target.Vertex, levels)
	}
	if written == "" {
		return Result{}
	}
	thinking := thinkingConfig(cfg, camel, true)
	delete(thinking, budgetKey(camel))
	delete(thinking, budgetKey(!camel))
	delete(thinking, levelKey(!camel))
	setString(thinking, levelKey(camel), written)
	setThinkingConfig(cfg, camel, thinking)
	setGeminiConfig(doc, cfg, camel)
	return Result{Effort: written, Sent: true}
}

func writeGemini25(cfg map[string]json.RawMessage, camel bool, model, effort string, thinkOff bool) string {
	closed, low, high := gemini25Range(model)
	thinking := thinkingConfig(cfg, camel, true)
	delete(thinking, levelKey(camel))
	delete(thinking, levelKey(!camel))
	if thinkOff || Rank(effort) == Rank("none") {
		setInt(thinking, budgetKey(camel), closed)
		delete(thinking, thoughtsKey(camel))
		delete(thinking, thoughtsKey(!camel))
		setThinkingConfig(cfg, camel, thinking)
		return "low"
	}
	budget := Budget(Collapse(effort))
	if budget < low {
		budget = low
	}
	if budget > high {
		budget = high
	}
	setInt(thinking, budgetKey(camel), budget)
	setThinkingConfig(cfg, camel, thinking)
	return Collapse(effort)
}

func writeOllama(doc map[string]json.RawMessage, signal Signal, effort string, thinkOff bool, target Target, levels []string) Result {
	if effort == "" && !thinkOff {
		return Result{}
	}
	if thinkOff && len(WithoutNone(levels)) == 0 {
		raw, _ := json.Marshal(false)
		doc["think"] = raw
		return Result{Sent: true}
	}
	if effort == "" {
		return Result{}
	}
	setString(doc, "think", effort)
	return Result{Effort: effort, Sent: true}
}

func writeGeminiChatExtra(doc map[string]json.RawMessage, level string) bool {
	extra, ok := objectField(doc, "extra_body")
	if !ok {
		extra, ok = objectField(doc, "extraBody")
	}
	if !ok {
		return false
	}
	extra = cloneObject(extra)
	google, ok := objectField(extra, "google")
	if !ok {
		return false
	}
	google = cloneObject(google)
	key := "thinking_config"
	thinking, ok := objectField(google, key)
	if !ok {
		key = "thinkingConfig"
		thinking, ok = objectField(google, key)
	}
	if !ok {
		thinking = map[string]json.RawMessage{}
	}
	thinking = cloneObject(thinking)
	for _, name := range []string{"thinkingBudget", "thinking_budget", "thinkingLevel", "thinking_level"} {
		delete(thinking, name)
	}
	setString(thinking, "thinking_level", level)
	setObject(google, key, thinking)
	setObject(extra, "google", google)
	if _, ok := doc["extra_body"]; ok {
		setObject(doc, "extra_body", extra)
	} else {
		setObject(doc, "extraBody", extra)
	}
	return true
}

// writeGeminiChatBudget moves a Chat thinking budget that a relay would
// otherwise send to Gemini into the google.thinking_config level field.
func writeGeminiChatBudget(doc map[string]json.RawMessage, level string) bool {
	thinking, ok := objectField(doc, "thinking")
	if !ok {
		return false
	}
	if _, ok := intField(thinking, "budget_tokens"); !ok {
		if _, ok := intField(thinking, "budgetTokens"); !ok {
			return false
		}
	}
	extraKey := "extra_body"
	extra, ok := objectField(doc, extraKey)
	if !ok {
		if alternate, found := objectField(doc, "extraBody"); found {
			extraKey = "extraBody"
			extra = alternate
		}
	}
	if extra == nil {
		extra = map[string]json.RawMessage{}
	} else {
		extra = cloneObject(extra)
	}
	google, _ := objectField(extra, "google")
	if google == nil {
		google = map[string]json.RawMessage{}
	} else {
		google = cloneObject(google)
	}
	key := "thinking_config"
	config, ok := objectField(google, key)
	if !ok {
		if alternate, found := objectField(google, "thinkingConfig"); found {
			key = "thinkingConfig"
			config = alternate
			ok = true
		}
	}
	if !ok {
		config = map[string]json.RawMessage{}
	} else {
		config = cloneObject(config)
	}
	for _, name := range []string{"thinkingBudget", "thinking_budget", "thinkingLevel", "thinking_level"} {
		delete(config, name)
	}
	setString(config, "thinking_level", level)
	setObject(google, key, config)
	setObject(extra, "google", google)
	setObject(doc, extraKey, extra)
	deleteField(doc, "thinking")
	return true
}

func setAdaptive(doc map[string]json.RawMessage, signal Signal) {
	thinking, _ := objectField(doc, "thinking")
	if thinking == nil {
		thinking = map[string]json.RawMessage{}
	} else {
		thinking = cloneObject(thinking)
	}
	setString(thinking, "type", "adaptive")
	display := ""
	if signal.Thinking != nil {
		display, _ = stringField(signal.Thinking, "display")
	}
	if display == "" {
		display, _ = stringField(thinking, "display")
	}
	if display == "" {
		display = "summarized"
	}
	setString(thinking, "display", display)
	delete(thinking, "budget_tokens")
	setObject(doc, "thinking", thinking)
}

func setThinkingType(doc map[string]json.RawMessage, kind string) {
	thinking, _ := objectField(doc, "thinking")
	if thinking == nil {
		thinking = map[string]json.RawMessage{}
	} else {
		thinking = cloneObject(thinking)
	}
	setString(thinking, "type", kind)
	delete(thinking, "budget_tokens")
	setObject(doc, "thinking", thinking)
}

func setThinkingOnly(doc map[string]json.RawMessage, kind string) {
	thinking := map[string]json.RawMessage{}
	setString(thinking, "type", kind)
	setObject(doc, "thinking", thinking)
}

func writeOutputEffort(doc map[string]json.RawMessage, effort string) {
	if effort == "" || effort == "none" {
		return
	}
	output, _ := objectField(doc, "output_config")
	if output == nil {
		output = map[string]json.RawMessage{}
	} else {
		output = cloneObject(output)
	}
	setString(output, "effort", effort)
	setObject(doc, "output_config", output)
}

func fitForVendor(effort string, levels []string) string {
	if effort == "" {
		return ""
	}
	return FitFor(effort, levels, false)
}

func deepSeekLevel(effort string, levels []string) string {
	switch Normalize(effort) {
	case "minimal":
		effort = "low"
	case "medium", "xhigh":
		effort = "high"
	case "ultra":
		effort = "max"
	}
	return fitForVendor(effort, levels)
}

func capHigh(effort string) string {
	switch Normalize(effort) {
	case "xhigh", "max", "ultra":
		return "high"
	default:
		return effort
	}
}

func directGeminiLevel(effort string, thinkOff bool, vertex bool, levels []string) string {
	levels = WithoutNone(CleanLevels(levels))
	if thinkOff {
		if vertex {
			if len(levels) == 0 {
				return "low"
			}
			return Fit("minimal", levels)
		}
		return "low"
	}
	effort = Normalize(effort)
	switch {
	case effort == "":
		return ""
	case effort == "minimal":
		return "minimal"
	case Rank(effort) < Rank("low"):
		return "low"
	case Rank(effort) > Rank("high"):
		if len(levels) > 0 && Rank(Strongest(levels)) > Rank("high") {
			return Nearest(effort, levels)
		}
		return "high"
	default:
		return effort
	}
}

// geminiLevel is the relay rule. It is not used for Google's own Gemini API.
func geminiLevel(model, effort string, levels []string) string {
	effort = Normalize(effort)
	if effort == "none" {
		return "low"
	}
	if effort == "minimal" {
		return "minimal"
	}
	effort = Collapse(effort)
	if effort == "" {
		return ""
	}
	clean := WithoutNone(CleanLevels(levels))
	if len(clean) > 0 {
		return Nearest(effort, clean)
	}
	if Rank(effort) > Rank("high") {
		return "high"
	}
	generation, _, variant, ok := parseGemini(model)
	if ok && generation == 3 && !strings.Contains(variant, "flash") && effort == "medium" {
		return "high"
	}
	return effort
}

func gemini25Range(model string) (closed, low, high int) {
	name := geminiName(model)
	switch {
	case strings.HasPrefix(name, "gemini-2.5-pro"):
		return 128, 128, 32768
	case strings.HasPrefix(name, "gemini-2.5-flash-lite"):
		return 0, 512, 24576
	default:
		return 0, 1, 24576
	}
}

func geminiConfig(doc map[string]json.RawMessage, create bool) (map[string]json.RawMessage, bool) {
	if cfg, ok := objectField(doc, "generationConfig"); ok {
		return cfg, true
	}
	if cfg, ok := objectField(doc, "generation_config"); ok {
		return cfg, false
	}
	if !create {
		return nil, true
	}
	cfg := map[string]json.RawMessage{}
	setObject(doc, "generationConfig", cfg)
	return cfg, true
}

func hasThinkingRequest(signal Signal) bool {
	return signal.Effort != "" || signal.Named || signal.ThinkOff ||
		signal.Budget != nil || signal.ThinkingType != ""
}

func thinkingConfig(cfg map[string]json.RawMessage, camel bool, create bool) map[string]json.RawMessage {
	key := "thinkingConfig"
	if !camel {
		key = "thinking_config"
	}
	if thinking, ok := objectField(cfg, key); ok {
		return thinking
	}
	if !create {
		return nil
	}
	thinking := map[string]json.RawMessage{}
	setObject(cfg, key, thinking)
	return thinking
}

func setGeminiConfig(doc map[string]json.RawMessage, cfg map[string]json.RawMessage, camel bool) {
	if cfg == nil {
		return
	}
	if camel {
		setObject(doc, "generationConfig", cfg)
	} else {
		setObject(doc, "generation_config", cfg)
	}
}

func setThinkingConfig(cfg map[string]json.RawMessage, camel bool, thinking map[string]json.RawMessage) {
	key := "thinkingConfig"
	if !camel {
		key = "thinking_config"
	}
	setObject(cfg, key, thinking)
}

func removeThinkingConfig(cfg map[string]json.RawMessage, camel bool) {
	if cfg == nil {
		return
	}
	deleteField(cfg, "thinkingConfig")
	deleteField(cfg, "thinking_config")
}

func budgetKey(camel bool) string {
	if camel {
		return "thinkingBudget"
	}
	return "thinking_budget"
}

func levelKey(camel bool) string {
	if camel {
		return "thinkingLevel"
	}
	return "thinking_level"
}

func thoughtsKey(camel bool) string {
	if camel {
		return "includeThoughts"
	}
	return "include_thoughts"
}

func officialOpenAI(t Target) bool {
	if t.OpenAI {
		return true
	}
	host := targetHost(t.BaseURL)
	switch {
	case host == "api.openai.com":
		return true
	case strings.HasSuffix(host, ".openai.azure.com"):
		return true
	case strings.HasSuffix(host, ".cognitiveservices.azure.com"):
		return true
	case strings.HasSuffix(host, ".azure-api.net"):
		return true
	default:
		return false
	}
}

func isDeepSeekOfficial(t Target) bool {
	host := targetHost(t.BaseURL)
	if host != "" {
		return host == "api.deepseek.com" || strings.HasSuffix(host, ".deepseek.com")
	}
	return strings.Contains(strings.ToLower(t.Model), "deepseek")
}

func directGemini(t Target) bool {
	if t.GeminiRelay {
		return false
	}
	if t.GoogleGemini {
		return true
	}
	host := targetHost(t.BaseURL)
	if host == "" {
		return true
	}
	return host == "generativelanguage.googleapis.com" ||
		strings.HasSuffix(host, ".generativelanguage.googleapis.com") ||
		strings.Contains(host, "aiplatform.googleapis.com") ||
		t.Vertex
}

func targetHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return strings.ToLower(raw)
	}
	return strings.ToLower(u.Hostname())
}

func isGeminiModel(model string) bool {
	name := geminiName(model)
	return strings.HasPrefix(name, "gemini-")
}

func geminiName(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimPrefix(name, "models/")
}

func parseGemini(model string) (generation, minor int, variant string, ok bool) {
	name := geminiName(model)
	if !strings.HasPrefix(name, "gemini") {
		return 0, 0, "", false
	}
	rest := strings.TrimPrefix(name, "gemini")
	tokens := splitAlnum(rest)
	if len(tokens) == 0 {
		return 0, 0, "", false
	}
	generation, err := strconv.Atoi(tokens[0])
	if err != nil {
		return 0, 0, "", false
	}
	at := 1
	if len(tokens) > 1 {
		if n, err := strconv.Atoi(tokens[1]); err == nil && n >= 0 && n < 100 {
			minor = n
			at = 2
		}
	}
	return generation, minor, strings.Join(tokens[at:], "-"), true
}

func splitAlnum(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func isGLM(model string) bool {
	name := strings.ToLower(model)
	for _, prefix := range []string{"glm-5.2", "glm-5.3"} {
		at := strings.Index(name, prefix)
		if at < 0 {
			continue
		}
		end := at + len(prefix)
		if end == len(name) {
			return true
		}
		switch name[end] {
		case '-', '.', ':', '/', '[':
			return true
		}
	}
	return false
}

func isMiniMax(model string) bool {
	name := strings.ToLower(model)
	at := strings.Index(name, "minimax-m3.1-flash")
	if at < 0 {
		return false
	}
	end := at + len("minimax-m3.1-flash")
	return end == len(name) || strings.ContainsRune("-.:[/", rune(name[end]))
}

func isGPTNumber(model string) bool {
	name := strings.ToLower(model)
	at := strings.Index(name, "gpt-")
	if at < 0 || at+4 >= len(name) {
		return false
	}
	return name[at+4] >= '0' && name[at+4] <= '9'
}

type claudeModel struct {
	family string
	major  int
	minor  int
}

func (c claudeModel) adaptive() bool {
	return c.major > 4 || (c.major == 4 && c.minor >= 6)
}

func parseClaude(model string) (claudeModel, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimPrefix(name, "models/")
	name = strings.TrimPrefix(name, "anthropic.")
	if !strings.Contains(name, "claude") && !strings.Contains(name, "opus") &&
		!strings.Contains(name, "sonnet") && !strings.Contains(name, "haiku") &&
		!strings.Contains(name, "fable") && !strings.Contains(name, "mythos") {
		return claudeModel{}, false
	}
	tokens := splitAlnum(name)
	familyAt := -1
	for i, token := range tokens {
		switch token {
		case "opus", "sonnet", "haiku", "fable", "mythos":
			familyAt = i
			break
		}
		if familyAt >= 0 {
			break
		}
	}
	c := claudeModel{}
	if familyAt >= 0 {
		c.family = tokens[familyAt]
	}
	if numbers := leadingNumbers(tokens[familyAt+1:]); len(numbers) > 0 && numbers[0] <= 20 {
		c.major = numbers[0]
		if len(numbers) > 1 && numbers[1] <= 20 {
			c.minor = numbers[1]
		}
	} else if familyAt >= 0 {
		if numbers := trailingNumbers(tokens[:familyAt]); len(numbers) > 0 {
			c.major = numbers[len(numbers)-1]
			if len(numbers) > 1 {
				c.major = numbers[len(numbers)-2]
				c.minor = numbers[len(numbers)-1]
			}
		}
	} else {
		numbers := leadingNumbers(tokens)
		if len(numbers) == 0 || numbers[0] > 20 {
			return claudeModel{}, false
		}
		c.major = numbers[0]
		if len(numbers) > 1 && numbers[1] <= 20 {
			c.minor = numbers[1]
		}
	}
	if c.major == 0 {
		return claudeModel{}, false
	}
	return c, true
}

func leadingNumbers(tokens []string) []int {
	var out []int
	for _, token := range tokens {
		n, err := strconv.Atoi(token)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}

func trailingNumbers(tokens []string) []int {
	var out []int
	for i := len(tokens) - 1; i >= 0; i-- {
		n, err := strconv.Atoi(tokens[i])
		if err != nil {
			break
		}
		out = append([]int{n}, out...)
	}
	return out
}

func claudeDefaultOutput(c claudeModel) int {
	if c.major >= 4 {
		return 8192
	}
	return 4096
}
