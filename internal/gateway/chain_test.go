package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/state"
)

func TestChatIsForwardedWithTheProviderKey(t *testing.T) {
	var gotAuth, gotModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var doc map[string]any
		_ = json.NewDecoder(r.Body).Decode(&doc)
		gotModel, _ = doc["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"pong"}}],"usage":{"total_tokens":3}}`))
	}))
	defer up.Close()

	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "openai", BaseURL: up.URL + "/v1", Models: []string{"demo"}, Credential: "provider-key",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	var logBuf bytes.Buffer
	srv := &Server{Store: store, Log: log.New(&logBuf, "", 0)}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "pong") {
		t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
	}
	if gotAuth != "Bearer provider-key" || strings.Contains(gotAuth, secret) {
		t.Fatalf("upstream auth %q", gotAuth)
	}
	if gotModel != "demo" {
		t.Fatalf("model %q", gotModel)
	}
	if strings.Contains(rr.Body.String(), "provider-key") || strings.Contains(logBuf.String(), secret) || strings.Contains(logBuf.String(), "provider-key") {
		t.Fatal("secret leaked")
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 || recs[0].Outcome != OutcomeSuccess || recs[0].Model != "demo" || recs[0].ProviderID != "p1" || recs[0].TokenID != "tok" {
		t.Fatalf("usage %v %v", recs, err)
	}
	if !bytes.Contains(recs[0].Usage, []byte("total_tokens")) {
		t.Fatalf("usage object %s", recs[0].Usage)
	}
}

func TestUnavailableFallsThroughAndAuthDoesNot(t *testing.T) {
	var first, second atomic.Int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Add(1)
		http.Error(w, "no", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"second"}}]}`))
	}))
	defer okSrv.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: down.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: okSrv.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}}})
	srv := &Server{Store: store}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "second") || first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("fallback %d %s hits %d %d", rr.Code, rr.Body.String(), first.Load(), second.Load())
	}

	var blocked atomic.Int32
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer denied.Close()
	later := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blocked.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"nope"}}]}`))
	}))
	defer later.Close()
	store, secret = dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: denied.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: later.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}}})
	rr = callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "auth_failure") || blocked.Load() != 0 {
		t.Fatalf("auth stop %d %s blocked %d", rr.Code, rr.Body.String(), blocked.Load())
	}
}

func TestHookKeepsModelAndMiddlewareCanReject(t *testing.T) {
	var seen string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var doc struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&doc)
		seen = doc.Model
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	hook := &scriptHook{replace: `{"model":"other","messages":[{"role":"user","content":"hi"}]}`}
	srv := &Server{Store: store, Hooks: []RequestHook{hook}}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || seen != "demo" || hook.responses != 0 {
		t.Fatalf("rollback status %d model %q responses %d", rr.Code, seen, hook.responses)
	}

	hook.replace = ""
	hook.err = errHook
	rr = callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || seen != "demo" || hook.responses != 0 {
		t.Fatalf("hook failure still dials: %d model %q responses %d", rr.Code, seen, hook.responses)
	}

	var hits atomic.Int32
	up.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	})
	srv.Hooks = nil
	srv.Middleware = []Middleware{rejectMW{status: http.StatusForbidden, message: "nope"}}
	rr = callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "nope") || hits.Load() != 0 {
		t.Fatalf("reject %d %s hits %d", rr.Code, rr.Body.String(), hits.Load())
	}
}

func TestTryRewriteDoesNotReachTheNextProvider(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var doc struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&doc)
		mu.Lock()
		seen["p1"] = doc.Model
		mu.Unlock()
		http.Error(w, "no", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var doc struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&doc)
		mu.Lock()
		seen["p2"] = doc.Model
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer okSrv.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: down.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: okSrv.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}}})
	srv := &Server{Store: store, Middleware: []Middleware{tryMW{}}}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || seen["p1"] != "p1-only" || seen["p2"] != "demo" {
		t.Fatalf("rewrite %d p1 %q p2 %q", rr.Code, seen["p1"], seen["p2"])
	}
}

func TestStreamReachesTheClientBeforeTheUpstreamFinishes(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		<-release
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	gw := httptest.NewServer(&Server{Store: store})
	defer gw.Close()
	req, err := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"demo","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 128)
		n, _ := res.Body.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream did not start")
	}
	select {
	case chunk := <-got:
		if !strings.Contains(chunk, "hi") {
			t.Fatalf("chunk %q", chunk)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client did not see the first event before the upstream finished")
	}
	close(release)
}

func TestCatalogAndTranslatedAnthropic(t *testing.T) {
	var shape string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var doc struct {
			Messages []map[string]string `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&doc)
		if len(doc.Messages) > 0 {
			shape = doc.Messages[0]["content"]
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"claude"}, Credential: "k",
	}}, nil)
	srv := &Server{Store: store}
	rr := callGateway(t, srv, secret, http.MethodGet, "/v1/models", "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"id":"p1/claude"`) {
		t.Fatalf("catalog %d %s", rr.Code, rr.Body.String())
	}
	rr = callGateway(t, srv, secret, http.MethodPost, "/v1/messages", `{"model":"claude","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	if rr.Code != http.StatusOK || shape != "hello" || !strings.Contains(rr.Body.String(), "pong") || !strings.Contains(rr.Body.String(), `"type":"message"`) {
		t.Fatalf("translated %d shape %q body %s", rr.Code, shape, rr.Body.String())
	}
}

func TestSessionSticksToTheProviderThatAnswered(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	count := func(id string, status int, body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[id]++
			mu.Unlock()
			if status != http.StatusOK {
				http.Error(w, "no", status)
				return
			}
			_, _ = w.Write([]byte(body))
		})
	}
	down := httptest.NewServer(count("p1", http.StatusServiceUnavailable, ""))
	defer down.Close()
	okSrv := httptest.NewServer(count("p2", http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`))
	defer okSrv.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: down.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: okSrv.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}}})
	srv := &Server{Store: store}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("x-session-id", "sess-1")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first %d %s", rr.Code, rr.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"again"}]}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("x-session-id", "sess-1")
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || hits["p1"] != 1 || hits["p2"] != 2 {
		t.Fatalf("sticky p1 %d p2 %d status %d", hits["p1"], hits["p2"], rr.Code)
	}
}

func TestToolsAreTranslatedOntoTheOtherProtocol(t *testing.T) {
	var gotName string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var doc struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&doc)
		if len(doc.Tools) > 0 {
			gotName = doc.Tools[0].Name
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"thinking","thinking":"look"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}}]}`))
	}))
	defer other.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "anth", Protocol: "anthropic", BaseURL: other.URL, Models: []string{"demo"}, Credential: "k1"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"anth"}}})
	body := `{"model":"demo","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", body)
	if rr.Code != http.StatusOK || gotName != "lookup" || !strings.Contains(rr.Body.String(), `"tool_calls"`) || !strings.Contains(rr.Body.String(), `"reasoning_content":"look"`) {
		t.Fatalf("translated tools name %q status %d body %s", gotName, rr.Code, rr.Body.String())
	}
}

func dialStore(t *testing.T, providers []state.Provider, routes []state.Route) (*state.Store, string) {
	t.Helper()
	store := loadTemp(t)
	secret := ""
	if err := store.Update(func(cfg *state.Config) error {
		sec, err := state.NewSecret()
		if err != nil {
			return err
		}
		secret = sec
		cfg.GatewayTokens = []state.GatewayToken{{ID: "tok", Secret: sec}}
		cfg.Providers = providers
		if routes != nil {
			cfg.Routes = routes
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return store, secret
}

func callGateway(t *testing.T, srv *Server, secret, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+secret)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

type scriptHook struct {
	replace   string
	err       error
	responses int
}

func (h *scriptHook) OnRequest(context.Context, json.RawMessage, CallContext) (json.RawMessage, error) {
	if h.err != nil {
		return nil, h.err
	}
	if h.replace == "" {
		return nil, nil
	}
	return json.RawMessage(h.replace), nil
}

func (h *scriptHook) OnResponse(context.Context, json.RawMessage, CallContext, bool) {
	h.responses++
}

type rejectMW struct {
	status  int
	message string
}

func (m rejectMW) OnRequest(context.Context, json.RawMessage, CallContext) (json.RawMessage, *Reject) {
	return nil, &Reject{Status: m.status, Message: m.message}
}
func (rejectMW) OnTry(context.Context, json.RawMessage, CallContext, string, Protocol) json.RawMessage {
	return nil
}
func (rejectMW) OnReply(context.Context, int, json.RawMessage, CallContext) (int, json.RawMessage) {
	return 0, nil
}

type tryMW struct{}

func (tryMW) OnRequest(_ context.Context, body json.RawMessage, _ CallContext) (json.RawMessage, *Reject) {
	return body, nil
}
func (tryMW) OnTry(_ context.Context, body json.RawMessage, _ CallContext, providerID string, _ Protocol) json.RawMessage {
	if providerID != "p1" {
		return body
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return body
	}
	doc["model"] = "p1-only"
	next, _ := json.Marshal(doc)
	return next
}
func (tryMW) OnReply(_ context.Context, status int, body json.RawMessage, _ CallContext) (int, json.RawMessage) {
	return status, body
}
