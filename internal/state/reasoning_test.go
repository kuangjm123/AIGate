package state

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestModelInfoKeepsKnownEmptyEfforts(t *testing.T) {
	empty, err := json.Marshal(ModelInfo{ID: "m", ReasoningEffortsKnown: true})
	if err != nil || !strings.Contains(string(empty), `"reasoningEfforts":[]`) {
		t.Fatalf("empty levels = %s, %v", empty, err)
	}
	unknown, err := json.Marshal(ModelInfo{ID: "m"})
	if err != nil || strings.Contains(string(unknown), "reasoningEfforts") {
		t.Fatalf("unknown levels = %s, %v", unknown, err)
	}
	var got ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"m","reasoningEfforts":[]}`), &got); err != nil {
		t.Fatal(err)
	}
	if levels, known := got.ReasoningLevels(); !known || len(levels) != 0 {
		t.Fatalf("decoded levels = %v, %v", levels, known)
	}
	var alias ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"m","levels":["light","extra_high"]}`), &alias); err != nil {
		t.Fatal(err)
	}
	levels, known := alias.ReasoningLevels()
	if !known || len(levels) != 2 || levels[0] != "light" || levels[1] != "extra_high" {
		t.Fatalf("alias levels = %v, %v", levels, known)
	}
}

func TestGroupMemberEffortSuffix(t *testing.T) {
	cfg := emptyConfig()
	cfg.Providers = []Provider{{
		ID: "p", Models: []string{"m"}, Credential: "k", Protocol: "chat", BaseURL: "https://example.com",
	}}
	member, ok := cfg.ResolveGroupMember("p/m:high")
	if !ok || member.ProviderID != "p" || member.Model != "m" || member.Fixed != "high" {
		t.Fatalf("fixed member = %+v, %v", member, ok)
	}
	member, ok = cfg.ResolveGroupMember("p/m:low+extra_high")
	if !ok || len(member.Allowed) != 2 || member.Allowed[0] != "low" || member.Allowed[1] != "extra_high" {
		t.Fatalf("allowed member = %+v, %v", member, ok)
	}
	if _, ok := cfg.ResolveGroupMember("p/m:latest"); ok {
		t.Fatal("a non-level suffix was accepted")
	}
}
