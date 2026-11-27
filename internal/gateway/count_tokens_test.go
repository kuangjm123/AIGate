package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kuangjm123/AIGate/internal/state"
)

func TestCountTokensAnswersLocally(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "count_tokens dialed", http.StatusInternalServerError)
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	body := `{
	  "model": "demo[1m]",
	  "system": "You are helpful.",
	  "messages": [{"role": "user", "content": [{"type": "text", "text": "hello"}]}],
	  "tools": [{"name": "lookup", "description": "look up", "input_schema": {"type": "object"}}]
	}`
	for _, path := range []string{"/v1/messages/count_tokens", "/messages/count_tokens"} {
		rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, path, body)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
		var got struct {
			InputTokens int `json:"input_tokens"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || got.InputTokens <= 0 {
			t.Fatalf("%s: %s", path, rr.Body.String())
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("count_tokens dialed the provider %d times", hits.Load())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 0 {
		t.Fatalf("count_tokens wrote usage: %#v %v", recs, err)
	}
}

func TestCountTokensHonoursGatewayTokenModelList(t *testing.T) {
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: "http://127.0.0.1:9", Models: []string{"demo"}, Credential: "k",
	}}, nil)
	if err := store.Update(func(cfg *state.Config) error {
		cfg.GatewayTokens[0].Models = []string{"other"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/messages/count_tokens", `{"model":"p1/demo[1m]","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), CodeModelDenied) {
		t.Fatalf("denied %d %s", rr.Code, rr.Body.String())
	}
	rr = callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/messages/count_tokens", `{"model":"login/claude/claude-sonnet-4-6[1m]","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), CodeModelDenied) {
		t.Fatalf("login denied %d %s", rr.Code, rr.Body.String())
	}
}

func TestCountTokensRejectsAHiddenModel(t *testing.T) {
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: "http://127.0.0.1:9", Models: []string{"demo"}, Credential: "k",
		Info: []state.ModelInfo{{ID: "demo", Hidden: true}},
	}}, nil)
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/messages/count_tokens", `{"model":"p1/demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), CodeHidden) {
		t.Fatalf("hidden %d %s", rr.Code, rr.Body.String())
	}
}

func TestCountTokensCountsAnImageAtAFixedCost(t *testing.T) {
	doc := map[string]json.RawMessage{
		"messages": json.RawMessage(`[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]`),
	}
	if got := estimateAnthropicInput(doc); got != claudeImageTokens {
		t.Fatalf("tokens %d", got)
	}
}

func TestCountTokensAcceptsLeadingWhitespace(t *testing.T) {
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: "http://127.0.0.1:9", Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/messages/count_tokens", "\n\t {\"model\":\"demo\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}")
	if rr.Code != http.StatusOK {
		t.Fatalf("whitespace %d %s", rr.Code, rr.Body.String())
	}
}

func TestAnthropicOneMillionSuffixDoesNotReachTheProvider(t *testing.T) {
	var gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var doc map[string]any
		_ = json.NewDecoder(r.Body).Decode(&doc)
		gotModel, _ = doc["model"].(string)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/messages", `{"model":"demo[1m]","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || gotModel != "demo" || !strings.Contains(rr.Body.String(), "pong") {
		t.Fatalf("forwarded %d model %q body %s", rr.Code, gotModel, rr.Body.String())
	}
}

func TestAnthropicOneMillionSuffixLeavesTheSameDialectBody(t *testing.T) {
	raw := []byte(`{"model":"demo[1m]","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw))
	parsed, err := Parse(ProtoAnthropic, req, raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := outboundBody(parsed, raw, ProtoAnthropic, parsed.Model)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if got := jsonString(doc["model"]); parsed.Model != "demo" || got != "demo" {
		t.Fatalf("parsed %q forwarded %q body %s", parsed.Model, got, out)
	}
}
