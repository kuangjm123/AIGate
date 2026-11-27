package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/state"
)

type codexCatalogBody struct {
	Models []struct {
		Slug          string   `json:"slug"`
		DisplayName   string   `json:"display_name"`
		Instructions  string   `json:"base_instructions"`
		Context       int      `json:"context_window"`
		Modalities    []string `json:"input_modalities"`
		DefaultEffort *string  `json:"default_reasoning_level"`
		Summaries     bool     `json:"supports_reasoning_summaries"`
	} `json:"models"`
}

func decodeCodexCatalog(t *testing.T, raw []byte) codexCatalogBody {
	t.Helper()
	var doc codexCatalogBody
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("catalog json: %v\n%s", err, raw)
	}
	return doc
}

func TestCodexCatalogUsesItsOwnShapeAndGroupPrefix(t *testing.T) {
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "responses", BaseURL: "http://127.0.0.1:9", Credential: "k",
		Models: []string{"demo", "pic"},
		Info: []state.ModelInfo{
			{ID: "demo", DisplayName: "Demo", Context: 200000, Reasoning: true, Vision: boolPointer(true)},
			{ID: "pic", Kind: "image"},
		},
	}}, []state.Route{{ID: "r1", ModelMatch: "route", ProviderIDs: []string{"p1"}}})
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Groups = []state.Group{{ID: "g", Members: []string{"p1/demo"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodGet, "/v1/codex/models", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("catalog %d %s", rr.Code, rr.Body.String())
	}
	doc := decodeCodexCatalog(t, rr.Body.Bytes())
	if len(doc.Models) != 3 {
		t.Fatalf("models %#v", doc.Models)
	}
	bySlug := map[string]int{}
	for i, model := range doc.Models {
		bySlug[model.Slug] = i
	}
	demo, ok := bySlug["p1/demo"]
	_, hasRoute := bySlug["route"]
	_, hasGroup := bySlug["group/g"]
	if !ok || !hasRoute || !hasGroup {
		t.Fatalf("slugs %#v", bySlug)
	}
	if _, ok := bySlug["pic"]; ok {
		t.Fatalf("image model in chat catalog: %#v", bySlug)
	}
	got := doc.Models[demo]
	if got.DisplayName != "Demo" || got.Context != 200000 || got.Instructions == "" || got.DefaultEffort == nil || *got.DefaultEffort != "medium" || !got.Summaries || !containsString(got.Modalities, "image") {
		t.Fatalf("demo entry %#v", got)
	}
}

func TestCodexCatalogHonoursTheKeyModelListAndAlias(t *testing.T) {
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "responses", BaseURL: "http://127.0.0.1:9", Credential: "k",
		Models: []string{"demo", "other"},
	}}, []state.Route{{ID: "r1", ModelMatch: "route", ProviderIDs: []string{"p1"}}})
	if err := store.Update(func(cfg *state.Config) error {
		cfg.GatewayTokens[0].Models = []string{"p1/demo"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/codex/models", "/backend-api/codex/models"} {
		rr := callGateway(t, &Server{Store: store}, secret, http.MethodGet, path, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("%s %d %s", path, rr.Code, rr.Body.String())
		}
		doc := decodeCodexCatalog(t, rr.Body.Bytes())
		if len(doc.Models) != 1 || doc.Models[0].Slug != "p1/demo" {
			t.Fatalf("%s models %#v", path, doc.Models)
		}
	}
}

func TestCodexCatalogPutsUsedModelsInTheAppsFirstHundred(t *testing.T) {
	models := make([]string, 0, 101)
	for i := 0; i < 101; i++ {
		models = append(models, fmt.Sprintf("m%03d", i))
	}
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "responses", BaseURL: "http://127.0.0.1:9", Credential: "k",
		Models: models,
	}}, nil)
	now := time.Now().UTC()
	if err := store.AppendUsage(state.AttemptRecord{
		RouteID: "used", CallerKeyID: "tok", Model: "p1/m100",
		Outcome: OutcomeSuccess, Status: http.StatusOK,
		Started: now.Add(-time.Minute), Finished: now,
	}); err != nil {
		t.Fatal(err)
	}
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodGet, "/v1/codex/models", "")
	doc := decodeCodexCatalog(t, rr.Body.Bytes())
	if len(doc.Models) != 101 {
		t.Fatalf("models %d", len(doc.Models))
	}
	first := make([]string, 0, 100)
	for _, model := range doc.Models[:100] {
		first = append(first, model.Slug)
	}
	if !containsString(first, "p1/m100") {
		t.Fatalf("used model outside the first hundred: %v", first)
	}
}

func TestBackendCodexResponsesReachesTheProvider(t *testing.T) {
	var path, model string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		model = body.Model
		_, _ = w.Write([]byte(`{"id":"resp","object":"response","status":"completed","model":"demo","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "responses", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/backend-api/codex/responses",
		`{"model":"demo","input":"hi","stream":false}`)
	if rr.Code != http.StatusOK || path != "/v1/responses" || model != "demo" || !strings.Contains(rr.Body.String(), `"status":"completed"`) {
		t.Fatalf("status %d path %q model %q body %s", rr.Code, path, model, rr.Body.String())
	}
}

func TestCodexActorHeaderMarksTheAgent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req.Header.Set("x-openai-actor-authorization", "aigate")
	if got := callContext(req, Parsed{Protocol: ProtoResponses, Model: "demo"}); got.Agent != "codex" {
		t.Fatalf("agent %q", got.Agent)
	}
}

func boolPointer(v bool) *bool { return &v }

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
