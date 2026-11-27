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

func TestOriginBasePathUsesTheClientProtocol(t *testing.T) {
	var seen string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RequestURI()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer up.Close()
	cases := []struct {
		path     string
		body     string
		protocol string
		want     string
	}{
		{"/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`, "chat", "/v1/chat/completions"},
		{"/responses", `{"model":"demo","input":"hi"}`, "responses", "/v1/responses"},
		{"/messages", `{"model":"demo","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, "anthropic", "/v1/messages"},
		{"/models/demo:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, "gemini", "/v1beta/models/demo:generateContent"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			seen = ""
			store, secret := dialStore(t, []state.Provider{{
				ID: "p1", Protocol: tc.protocol, BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
			}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
			rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, tc.path, tc.body)
			if rr.Code != http.StatusOK || !strings.Contains(seen, tc.want) {
				t.Fatalf("status %d upstream %q body %s", rr.Code, seen, rr.Body.String())
			}
		})
	}
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodGet, "/models", "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "demo") {
		t.Fatalf("catalog %d %s", rr.Code, rr.Body.String())
	}
}

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
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 || recs[0].Status != http.StatusOK || recs[0].Outcome != OutcomeSuccess || recs[0].ProviderID != "p2" || recs[0].Tries != 2 || recs[0].RouteID == "" {
		t.Fatalf("fallback usage %+v %v", recs, err)
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
	deniedRecs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(deniedRecs) != 1 || deniedRecs[0].Outcome != string(state.ClassAuthFailure) || deniedRecs[0].Status != http.StatusUnauthorized {
		t.Fatalf("auth usage %+v %v", deniedRecs, err)
	}
}

func TestFallbackWithBilledFailureWritesHandedOn(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"busy","usage":{"prompt_tokens":10,"completion_tokens":3}}`))
	}))
	defer down.Close()
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"second"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer okSrv.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: down.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: okSrv.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "second") {
		t.Fatalf("fallback %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 2 {
		t.Fatalf("usage %+v %v", recs, err)
	}
	if recs[0].RouteID == "" || recs[0].RouteID != recs[1].RouteID {
		t.Fatalf("route ids %q %q", recs[0].RouteID, recs[1].RouteID)
	}
	if !recs[0].HandedOn || recs[0].Outcome != string(state.ClassUpstreamUnavailable) {
		t.Fatalf("handed on %+v", recs[0])
	}
	if recs[1].HandedOn || recs[1].ProviderID != "p2" || recs[1].Tries != 2 || recs[1].Outcome != OutcomeSuccess {
		t.Fatalf("final %+v", recs[1])
	}
	summary := state.SummarizeUsage(recs)
	if summary.Calls != 1 || summary.Failed != 0 || summary.In != 15 || summary.Out != 5 {
		t.Fatalf("summary %+v", summary)
	}
}

func TestCanceledRequestIsNotAFailure(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	(&Server{Store: store}).ServeHTTP(rr, req)
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 || !recs[0].Canceled() || recs[0].Status != 499 {
		t.Fatalf("canceled usage %+v %v", recs, err)
	}
	summary := state.SummarizeUsage(recs)
	if summary.Calls != 1 || summary.Canceled != 1 || summary.Failed != 0 {
		t.Fatalf("canceled summary %+v", summary)
	}
}

func TestBilledFailureOnTheLastCandidateIsTheFinalRow(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"busy","usage":{"prompt_tokens":10,"completion_tokens":3}}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 || recs[0].HandedOn || !recs[0].Failed() {
		t.Fatalf("usage %+v %v", recs, err)
	}
	if got := recs[0].Counts(); got.In != 10 || got.Out != 3 {
		t.Fatalf("counts %+v", got)
	}
	summary := state.SummarizeUsage(recs)
	if summary.Calls != 1 || summary.Failed != 1 || summary.In != 10 || summary.Out != 3 {
		t.Fatalf("summary %+v", summary)
	}
}

func TestUsageLineStoresCounts(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}],"usage":{"prompt_tokens":10,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":1}}}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("usage %v %v", recs, err)
	}
	got := recs[0].Counts()
	if recs[0].Status != http.StatusOK || got.In != 6 || got.Out != 3 || got.CacheRead != 4 || got.CacheWrite != 0 || got.Reasoning != 1 {
		t.Fatalf("line %+v counts %+v", recs[0], got)
	}
	if recs[0].BytesIn == 0 || recs[0].BytesOut == 0 || recs[0].TTFTMS != 0 {
		t.Fatalf("line bytes %+v", recs[0])
	}
	b, err := json.Marshal(recs[0])
	if err != nil || !bytes.Contains(b, []byte(`"cacheWrite":0`)) {
		t.Fatalf("zero cache write omitted: %s", b)
	}
}

func TestUsageLineStoresTheModelSentToTheProvider(t *testing.T) {
	var sent string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var doc struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&doc)
		sent = doc.Model
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, nil)
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"p1/demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || sent != "demo" {
		t.Fatalf("reply %d sent %q body %s", rr.Code, sent, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 || recs[0].Model != "demo" {
		t.Fatalf("usage %+v %v", recs, err)
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"busy","usage":{"prompt_tokens":7,"completion_tokens":2}}`))
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
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 2 {
		t.Fatalf("usage %+v %v", recs, err)
	}
	if !recs[0].HandedOn || recs[0].Model != "p1-only" {
		t.Fatalf("handed on %+v", recs[0])
	}
	if recs[1].HandedOn || recs[1].Model != "demo" || recs[1].Outcome != OutcomeSuccess {
		t.Fatalf("final %+v", recs[1])
	}
}

func TestTryRewriteIsTheModelOnTheFinalUsageLine(t *testing.T) {
	var sent string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var doc struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&doc)
		sent = doc.Model
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	srv := &Server{Store: store, Middleware: []Middleware{tryMW{}}}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || sent != "p1-only" {
		t.Fatalf("rewrite %d sent %q body %s", rr.Code, sent, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 || recs[0].Model != "p1-only" {
		t.Fatalf("usage %+v %v", recs, err)
	}
}

func TestCanceledTryRewriteIsTheModelOnTheCanceledUsageLine(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer up.Close()
	defer close(release)
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	srv := &Server{Store: store, Middleware: []Middleware{tryMW{}}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gateway did not finish the canceled request")
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 || !recs[0].Canceled() || recs[0].Model != "p1-only" {
		t.Fatalf("usage %+v %v", recs, err)
	}
}

func TestCanceledBeforeRoutingWritesOneCanceledLine(t *testing.T) {
	tests := []struct {
		name  string
		start func(*syncHook, chan struct{}) *Server
	}{
		{
			name: "hook",
			start: func(hook *syncHook, started chan struct{}) *Server {
				hook.started = started
				return &Server{Hooks: []RequestHook{hook}}
			},
		},
		{
			name: "middleware",
			start: func(_ *syncHook, started chan struct{}) *Server {
				return &Server{Middleware: []Middleware{&syncMiddleware{started: started}}}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := loadTemp(t)
			hook := &syncHook{}
			started := make(chan struct{})
			srv := tc.start(hook, started)
			srv.Store = store
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`))
			ctx, cancel := context.WithCancel(req.Context())
			req = req.WithContext(ctx)
			done := make(chan int, 1)
			go func() {
				done <- srv.serveModel(httptest.NewRecorder(), req, store.Current(), ProtoChat, admission{caller: "tok", tokenID: "tok"})
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("request stage did not start")
			}
			cancel()
			select {
			case status := <-done:
				if status != 499 {
					t.Fatalf("status %d", status)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("gateway did not finish the canceled request")
			}
			recs, err := store.ReadUsage(state.UsageFilter{})
			if err != nil || len(recs) != 1 || !recs[0].Canceled() || recs[0].ProviderID != "" || recs[0].Model != "demo" {
				t.Fatalf("usage %+v %v", recs, err)
			}
		})
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
	var maxTokens int
	var seenPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		var doc struct {
			MaxTokens int                 `json:"max_tokens"`
			Messages  []map[string]string `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&doc)
		maxTokens = doc.MaxTokens
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
	if rr.Code != http.StatusOK || shape != "hello" || maxTokens != 16 || seenPath != "/v1/chat/completions" || !strings.Contains(rr.Body.String(), "pong") || !strings.Contains(rr.Body.String(), `"type":"message"`) {
		t.Fatalf("translated %d path %q shape %q max_tokens %d body %s", rr.Code, seenPath, shape, maxTokens, rr.Body.String())
	}
}

func TestClaudeCodeSystemMessageReachesAChatProvider(t *testing.T) {
	var got []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var doc struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&doc)
		got = doc.Messages
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"claude"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "claude", ProviderIDs: []string{"p1"}}})
	srv := &Server{Store: store}
	body := `{"model":"claude","max_tokens":16,"system":[{"type":"text","text":"top system"}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"system","content":[{"type":"text","text":"environment context"}]}]}`
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/messages", body)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "pong") {
		t.Fatalf("translated %d %s", rr.Code, rr.Body.String())
	}
	if len(got) != 3 || got[0].Role != "system" || got[0].Content != "top system" ||
		got[1].Role != "system" || got[1].Content != "environment context" ||
		got[2].Role != "user" || got[2].Content != "hi" {
		t.Fatalf("messages %#v", got)
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

func TestStreamUsageComesFromTheLastChunk(t *testing.T) {
	chat := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"usage\":null}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2,\"total_tokens\":11}}\n\n" +
		"data: [DONE]\n\n"
	anthropic := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":25,\"cache_read_input_tokens\":4,\"output_tokens\":1}}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":15}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	gemini := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"he\"}]}}],\"usageMetadata\":{\"promptTokenCount\":4,\"candidatesTokenCount\":1,\"totalTokenCount\":5}}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"llo\"}]}}],\"usageMetadata\":{\"promptTokenCount\":4,\"candidatesTokenCount\":2,\"totalTokenCount\":6}}\n\n"
	responses := "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"input_tokens\":8,\"output_tokens\":2,\"total_tokens\":10}}}\n\n"

	cases := []struct {
		name     string
		protocol string
		path     string
		body     string
		stream   string
		keep     string
		want     map[string]float64
	}{
		{
			name: "chat", protocol: "chat", path: "/v1/chat/completions",
			body:   `{"model":"demo","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			stream: chat, keep: "hi",
			want: map[string]float64{"prompt_tokens": 9, "completion_tokens": 2, "total_tokens": 11},
		},
		{
			name: "anthropic", protocol: "anthropic", path: "/v1/messages",
			body:   `{"model":"demo","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			stream: anthropic, keep: "event: message_delta",
			want: map[string]float64{"input_tokens": 25, "cache_read_input_tokens": 4, "output_tokens": 15},
		},
		{
			name: "gemini", protocol: "gemini", path: "/v1beta/models/demo:streamGenerateContent",
			body:   `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			stream: gemini, keep: "llo",
			want: map[string]float64{"promptTokenCount": 4, "candidatesTokenCount": 2, "totalTokenCount": 6},
		},
		{
			name: "responses", protocol: "responses", path: "/v1/responses",
			body:   `{"model":"demo","stream":true,"input":"hi"}`,
			stream: responses, keep: "hi",
			want: map[string]float64{"input_tokens": 8, "output_tokens": 2, "total_tokens": 10},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.stream)
			}))
			defer up.Close()
			store, secret := dialStore(t, []state.Provider{{
				ID: "p1", Protocol: tc.protocol, BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
			}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
			rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, tc.path, tc.body)
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), tc.keep) {
				t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
			}
			recs, err := store.ReadUsage(state.UsageFilter{})
			if err != nil || len(recs) != 1 {
				t.Fatalf("records %v %v", err, recs)
			}
			got := usageNumbers(t, recs[0].Usage)
			for key, want := range tc.want {
				if got[key] != want {
					t.Fatalf("usage %s: got %v want %v in %s", key, got[key], want, recs[0].Usage)
				}
			}
		})
	}
}

func TestStreamWithoutUsageOmitsTheField(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"usage\":null}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 || len(recs[0].Usage) != 0 {
		t.Fatalf("usage %v %+v", err, recs)
	}
}

func TestGeminiReplyRecordsUsageMetadata(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"pong"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "gemini", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1beta/models/demo:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "pong") {
		t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("records %v %v", err, recs)
	}
	got := usageNumbers(t, recs[0].Usage)
	if got["promptTokenCount"] != 3 || got["totalTokenCount"] != 4 {
		t.Fatalf("usage %s", recs[0].Usage)
	}
}

func TestTranslatedStreamKeepsTheProviderUsage(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":6,\"completion_tokens\":1,\"total_tokens\":7}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	body := `{"model":"demo","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/messages", body)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "pong") {
		t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("records %v %v", err, recs)
	}
	got := usageNumbers(t, recs[0].Usage)
	if got["prompt_tokens"] != 6 || got["total_tokens"] != 7 {
		t.Fatalf("usage %s", recs[0].Usage)
	}
}

func TestUsageOnTheLastEventSurvivesASplitRead(t *testing.T) {
	var scan usageScan
	scan.proto = ProtoChat
	if _, err := scan.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":")); err != nil {
		t.Fatal(err)
	}
	if _, err := scan.Write([]byte("9,\"completion_tokens\":2,\"total_tokens\":11}}\n\n")); err != nil {
		t.Fatal(err)
	}
	scan.finish()
	got := usageNumbers(t, scan.object())
	if got["prompt_tokens"] != 9 || got["completion_tokens"] != 2 || got["total_tokens"] != 11 {
		t.Fatalf("usage %s", scan.object())
	}
}

func usageNumbers(t *testing.T, raw []byte) map[string]float64 {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("missing usage")
	}
	var doc map[string]float64
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("usage %s: %v", raw, err)
	}
	return doc
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

type syncHook struct {
	started chan struct{}
}

func (h *syncHook) OnRequest(ctx context.Context, _ json.RawMessage, _ CallContext) (json.RawMessage, error) {
	close(h.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*syncHook) OnResponse(context.Context, json.RawMessage, CallContext, bool) {}

type syncMiddleware struct {
	started chan struct{}
}

func (m *syncMiddleware) OnRequest(ctx context.Context, _ json.RawMessage, _ CallContext) (json.RawMessage, *Reject) {
	close(m.started)
	<-ctx.Done()
	return nil, nil
}

func (*syncMiddleware) OnTry(context.Context, json.RawMessage, CallContext, string, Protocol) json.RawMessage {
	return nil
}

func (*syncMiddleware) OnReply(_ context.Context, status int, body json.RawMessage, _ CallContext) (int, json.RawMessage) {
	return status, body
}
