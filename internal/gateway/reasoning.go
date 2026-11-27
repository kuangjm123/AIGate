package gateway

import (
	"bytes"
	"net/url"
	"strings"

	"github.com/kuangjm123/AIGate/internal/reasoning"
	"github.com/kuangjm123/AIGate/internal/state"
)

// reasoningTargetFor reads the target model's own levels and the address that
// decides which vendor field to write.
func reasoningTargetFor(cfg state.Config, seat seatCandidate, dest Protocol, upModel string) reasoning.Target {
	levels := append([]string(nil), seat.ReasoningLevels...)
	levelless := seat.Levelless
	base := ""
	if seat.Login {
		base = loginReasoningBase(seat.Account)
	} else if provider, ok := cfg.Provider(seat.ProviderID); ok {
		base = provider.BaseURL
		if info, found := provider.InfoFor(upModel); found {
			if got, ok := info.ReasoningLevels(); ok {
				levels = got
				levelless = len(got) == 0
			}
		}
	}
	target := reasoning.Target{
		Dialect: string(dest), Model: upModel, BaseURL: base,
		Levels: levels, Levelless: levelless,
		Fixed: seat.ReasoningFixed, Allowed: append([]string(nil), seat.ReasoningAllowed...),
		Mapping: reasoningMapping(seat.ReasoningMap, seat.ReasoningMember),
	}
	host := reasoningHost(base)
	switch {
	case strings.Contains(host, "aiplatform.googleapis.com"):
		target.Vertex = true
		target.GoogleGemini = true
	case host == "generativelanguage.googleapis.com":
		target.GoogleGemini = true
	}
	if seat.Login && loginVendor(seat.Account) == "codex" {
		target.OpenAI = true
	}
	return target
}

func reasoningMapping(in map[string]string, member string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	refs := map[string]bool{member: true}
	if base := reasoningMemberBase(member); base != member {
		refs[base] = true
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		matched := ""
		for ref := range refs {
			if strings.HasPrefix(key, ref+"/") {
				matched = ref
				break
			}
		}
		if matched != "" {
			out[strings.TrimPrefix(key, matched+"/")] = value
		}
	}
	for key, value := range in {
		if strings.Contains(key, "/") {
			continue
		}
		if _, specific := out[key]; !specific {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func reasoningMemberBase(ref string) string {
	at := strings.LastIndex(ref, ":")
	if at <= 0 || at == len(ref)-1 {
		return ref
	}
	for _, part := range strings.Split(ref[at+1:], "+") {
		part = reasoning.Normalize(part)
		if part != "off" && reasoning.Rank(part) < 0 {
			return ref
		}
	}
	return ref[:at]
}

func loginReasoningBase(account string) string {
	switch loginVendor(account) {
	case "claude":
		return "https://api.anthropic.com"
	case "codex":
		return "https://api.openai.com"
	case "gemini", "antigravity":
		return "https://generativelanguage.googleapis.com"
	default:
		return ""
	}
}

func reasoningHost(base string) string {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func effectiveLevels(target reasoning.Target) []string {
	if len(target.Allowed) > 0 {
		return target.Allowed
	}
	return target.Levels
}

// levelNameRejected reports the provider's "this thinking level name does not
// exist" answer. It is used for Gemini's level field, not for a budget.
func levelNameRejected(status int, body []byte) bool {
	if status != 400 && status != 422 {
		return false
	}
	text := strings.ToLower(string(body))
	if !strings.Contains(text, "thinking") || !strings.Contains(text, "level") {
		return false
	}
	for _, marker := range []string{"invalid", "unsupported", "not supported", "not found", "unknown", "does not exist"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func reasoningBodyEqual(a, b []byte) bool {
	return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
}

func dashScopeBase(base string) bool {
	host := reasoningHost(base)
	return strings.Contains(host, "dashscope.aliyuncs.com") ||
		strings.Contains(host, "dashscope-intl.aliyuncs.com")
}

func thinkingRequested(signal reasoning.Signal) bool {
	if signal.ThinkOff {
		return false
	}
	if signal.Effort != "" && reasoning.Rank(signal.Effort) != reasoning.Rank("none") {
		return true
	}
	return signal.ThinkingType == "enabled" || signal.Budget != nil
}

func levelSent(result reasoning.Result) bool {
	level := reasoning.Normalize(result.Effort)
	return result.Sent && level != "" && reasoning.Rank(level) != reasoning.Rank("none")
}

// geminiLevelSent reports whether this attempt wrote a Gemini thinking level.
// Gemini can answer a level it does not take even when the protocol is a relay
// that carries the level in its own field.
func geminiLevelSent(dest Protocol, target reasoning.Target, result reasoning.Result) bool {
	if !levelSent(result) {
		return false
	}
	if dest == ProtoGemini {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(target.Model))
	if at := strings.LastIndexByte(name, '/'); at >= 0 {
		name = name[at+1:]
	}
	return strings.HasPrefix(strings.TrimPrefix(name, "models/"), "gemini")
}
