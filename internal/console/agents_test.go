package console

import (
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/state"
)

func TestRecentCodexModelsFollowCodexCalls(t *testing.T) {
	store := loadTemp(t)
	now := time.Now().UTC()
	records := []state.AttemptRecord{
		{RouteID: "old", ProviderID: "p", Outcome: "success", Status: 200, Model: "p/old", Agent: "codex", Started: now.Add(-3 * time.Minute), Finished: now.Add(-3 * time.Minute)},
		{RouteID: "local", ProviderID: "p", Outcome: "success", Status: 200, Model: "p/local", Agent: "aigate-codex", Started: now.Add(-2 * time.Minute), Finished: now.Add(-2 * time.Minute)},
		{RouteID: "other", ProviderID: "p", Outcome: "success", Status: 200, Model: "p/other", Agent: "claude", Started: now.Add(-time.Minute), Finished: now.Add(-time.Minute)},
		{RouteID: "new", ProviderID: "p", Outcome: "success", Status: 200, Model: "p/new", Agent: "codex", Started: now, Finished: now},
	}
	for _, rec := range records {
		if err := store.AppendUsage(rec); err != nil {
			t.Fatal(err)
		}
	}
	got := (&Server{Store: store}).recentModels("codex")
	want := []string{"p/new", "p/local", "p/old"}
	if len(got) != len(want) {
		t.Fatalf("recent %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recent %v", got)
		}
	}
}

func TestGatewayTargetCarriesLiveTokenModelLists(t *testing.T) {
	cfg := state.Config{GatewayTokens: []state.GatewayToken{
		{ID: "live", Secret: " sk-live ", Models: []string{"p1/demo"}},
		{ID: "revoked", Secret: "sk-revoked", Revoked: true, Models: []string{"p1/other"}},
		{ID: "blank", Secret: "  "},
		{ID: "all", Secret: "sk-all"},
	}}
	used := []string{"p1/demo"}
	got := gatewayTarget(cfg, "http://127.0.0.1:3425", used)
	if got.Origin != "http://127.0.0.1:3425" || len(got.Used) != 1 || got.Used[0] != "p1/demo" {
		t.Fatalf("target %#v", got)
	}
	if len(got.Keys) != 2 || got.Keys[0] != "sk-live" || got.Keys[1] != "sk-all" {
		t.Fatalf("keys %#v", got.Keys)
	}
	if models, ok := got.Allowed["sk-live"]; !ok || len(models) != 1 || models[0] != "p1/demo" {
		t.Fatalf("live model list %#v", got.Allowed)
	}
	if models, ok := got.Allowed["sk-all"]; !ok || len(models) != 0 {
		t.Fatalf("unrestricted token %#v", got.Allowed)
	}
	if _, ok := got.Allowed["sk-revoked"]; ok {
		t.Fatalf("revoked token listed %#v", got.Allowed)
	}
}
