package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kuangjm123/AIGate/internal/state"
)

func TestHelloNamesTheGatewayWithoutAToken(t *testing.T) {
	store := loadTemp(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	(&Server{Store: store}).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"name":"aigate"`) {
		t.Fatalf("hello %d %s", rr.Code, rr.Body.String())
	}
}

func TestLocalMagpieKeyIsTheCaller(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store, _ := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`))
	req.RemoteAddr = "127.0.0.1:9"
	req.Header.Set("Authorization", "Bearer magpie-codex")
	rr := httptest.NewRecorder()
	(&Server{Store: store}).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("local %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 || recs[0].Caller != "magpie-codex" || recs[0].TokenID != "" {
		t.Fatalf("usage err %v recs %+v", err, recs)
	}
}

func TestRemoteMagpieKeyIsRejected(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("remote magpie key dialed")
	}))
	defer up.Close()
	store, _ := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k"},
	}, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer magpie-codex")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.RemoteAddr = "127.0.0.1:9"
	rr := httptest.NewRecorder()
	(&Server{Store: store}).ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "auth_failure") {
		t.Fatalf("remote %d %s", rr.Code, rr.Body.String())
	}
}

func TestHiddenModelIsAbsentAndRejected(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"secret"}, Credential: "k",
		Info: []state.ModelInfo{{ID: "secret", Hidden: true}},
	}}, nil)
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodGet, "/v1/models", "")
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("catalog %d %s", rr.Code, rr.Body.String())
	}
	rr = callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", `{"model":"p1/secret","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "hidden") || hits.Load() != 0 {
		t.Fatalf("call %d hits %d %s", rr.Code, hits.Load(), rr.Body.String())
	}
}

func TestCatalogIDReachesTheProviderModel(t *testing.T) {
	var seen string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var doc struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&doc)
		seen = doc.Model
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"gpt"}, Credential: "k"},
	}, nil)
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodGet, "/v1/models", "")
	if !strings.Contains(rr.Body.String(), `"id":"p1/gpt"`) || !strings.Contains(rr.Body.String(), `"owned_by":"p1"`) {
		t.Fatalf("catalog %s", rr.Body.String())
	}
	rr = callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", `{"model":"p1/gpt","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || seen != "gpt" || !strings.Contains(rr.Body.String(), `"content":"ok"`) {
		t.Fatalf("dial model %q status %d body %s", seen, rr.Code, rr.Body.String())
	}
}

func TestTextOnlyModelRejectsMedia(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer up.Close()
	no := false
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
		Info: []state.ModelInfo{{ID: "demo", Vision: &no}},
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	body := `{"model":"demo","messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aa"}}]}]}`
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", body)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "模型不接受图片或视频") || hits.Load() != 0 {
		t.Fatalf("media %d hits %d %s", rr.Code, hits.Load(), rr.Body.String())
	}
}

func TestStreamDoesNotFallThroughAfterTheReplyStarts(t *testing.T) {
	var second atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
	}))
	defer first.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
	}))
	defer other.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: first.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: other.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}}})
	body := `{"model":"demo","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", body)
	if rr.Code != http.StatusOK || second.Load() != 0 || !strings.Contains(rr.Body.String(), `"content":"a"`) {
		t.Fatalf("stream %d second %d %s", rr.Code, second.Load(), rr.Body.String())
	}
}

func TestTokenModelListHidesTheRest(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("denied model was dialed")
	}))
	defer up.Close()
	store := loadTemp(t)
	secret := ""
	if err := store.Update(func(cfg *state.Config) error {
		sec, err := state.NewSecret()
		if err != nil {
			return err
		}
		secret = sec
		cfg.GatewayTokens = []state.GatewayToken{{ID: "tok", Secret: sec, Models: []string{"other"}}}
		cfg.Providers = []state.Provider{{ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k"}}
		cfg.Routes = []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodGet, "/v1/models", "")
	if strings.Contains(rr.Body.String(), "demo") {
		t.Fatalf("catalog showed a denied model %s", rr.Body.String())
	}
	rr = callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "model_denied") {
		t.Fatalf("denied %d %s", rr.Code, rr.Body.String())
	}
}

func TestImageModelsStayOffTheChatList(t *testing.T) {
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: "http://127.0.0.1:9/v1", Models: []string{"pic"}, Credential: "k",
		Info: []state.ModelInfo{{ID: "pic", Kind: "image", DisplayName: "Picture"}},
	}}, nil)
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodGet, "/v1/models", "")
	if strings.Contains(rr.Body.String(), "pic") {
		t.Fatalf("chat list %s", rr.Body.String())
	}
	rr = callGateway(t, &Server{Store: store}, secret, http.MethodGet, "/v1/models?kind=image", "")
	var doc struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil || len(doc.Data) != 1 || doc.Data[0].ID != "p1/pic" || doc.Data[0].Name != "Picture" {
		t.Fatalf("image list %s", rr.Body.String())
	}
}
