// Package reasoning maps the thinking level a coding agent asked for onto the
// fields one upstream API accepts.
package reasoning

import "strings"

var ranks = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// Normalize lowercases a level and applies the vendor aliases. It does not
// turn "off" into "none": only the Chat and Responses readers do that.
func Normalize(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "light":
		return "low"
	case "extra_high":
		return "xhigh"
	default:
		return level
	}
}

// Rank returns a level's position from none to ultra, or -1 when it is unknown.
func Rank(level string) int {
	level = Normalize(level)
	for i, name := range ranks {
		if name == level {
			return i
		}
	}
	return -1
}

// CleanLevels trims and lowercases a model's own level names. Aliases keep
// their spelling so a model that calls low "light" is sent "light".
func CleanLevels(levels []string) []string {
	out := make([]string, 0, len(levels))
	for _, level := range levels {
		level = strings.ToLower(strings.TrimSpace(level))
		if level != "" {
			out = append(out, level)
		}
	}
	return out
}

// Nearest returns the level in levels closest to want. A tie goes to the
// stronger level. "none" is skipped unless it is the level wanted.
func Nearest(want string, levels []string) string {
	want = Normalize(want)
	wantRank := Rank(want)
	levels = CleanLevels(levels)
	if wantRank < 0 || len(levels) == 0 {
		return want
	}
	best := ""
	bestDistance := 0
	for _, level := range levels {
		levelRank := Rank(level)
		if levelRank < 0 {
			continue
		}
		if levelRank == Rank("none") && wantRank != Rank("none") {
			continue
		}
		if levelRank == wantRank {
			return level
		}
		distance := wantRank - levelRank
		if distance < 0 {
			distance = -distance
		}
		if best == "" || distance < bestDistance ||
			(distance == bestDistance && levelRank > Rank(best)) {
			best = level
			bestDistance = distance
		}
	}
	if best == "" {
		return want
	}
	return best
}

// Fit resolves ultra to max and then fits want onto the model's levels.
func Fit(want string, levels []string) string {
	want = Normalize(want)
	levels = CleanLevels(levels)
	if want == "ultra" {
		want = "max"
		kept := levels[:0]
		for _, level := range levels {
			if Rank(level) != Rank("ultra") {
				kept = append(kept, level)
			}
		}
		levels = kept
	}
	return Nearest(want, levels)
}

// Collapse maps a level onto the names used by budget tables.
func Collapse(level string) string {
	switch Normalize(level) {
	case "none", "minimal":
		return "low"
	case "low", "medium", "high", "xhigh", "max":
		return Normalize(level)
	case "ultra":
		return "max"
	default:
		return ""
	}
}

// Budget maps a collapsed level onto an Anthropic token budget.
func Budget(level string) int {
	switch Collapse(level) {
	case "low":
		return 4096
	case "medium":
		return 10000
	case "high":
		return 24000
	case "xhigh", "max":
		return 32000
	default:
		return 10000
	}
}

// LevelFromBudget reads a level from an Anthropic budget. A budget of 32000
// cannot say xhigh from max, so it reads xhigh.
func LevelFromBudget(n int) string {
	switch {
	case n <= 0:
		return ""
	case n <= 4096:
		return "low"
	case n <= 12000:
		return "medium"
	case n <= 24000:
		return "high"
	default:
		return "xhigh"
	}
}

// FitFor fits a request onto a model's levels. A model known to have no levels
// cannot answer above high.
func FitFor(want string, levels []string, levelless bool) string {
	got := Fit(want, levels)
	if len(CleanLevels(levels)) == 0 && levelless && Rank(got) > Rank("high") {
		return "high"
	}
	return got
}

// Strongest returns the strongest known level in levels.
func Strongest(levels []string) string {
	best := ""
	for _, level := range CleanLevels(levels) {
		if Rank(level) < 0 {
			continue
		}
		if best == "" || Rank(level) > Rank(best) {
			best = level
		}
	}
	return best
}

// WithoutNone drops the off level. Gemini's level field has no none.
func WithoutNone(levels []string) []string {
	out := make([]string, 0, len(levels))
	for _, level := range CleanLevels(levels) {
		if Rank(level) == Rank("none") {
			continue
		}
		out = append(out, level)
	}
	return out
}
