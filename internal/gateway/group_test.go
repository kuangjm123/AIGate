package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/routes"
	"github.com/kuangjm123/AIGate/internal/state"
)

func storeWithGroups(t *testing.T, providers []state.Provider, routes []state.Route, groups []state.Group) (*state.Store, string) {
	t.Helper()
	store, secret := dialStore(t, providers, routes)
	if len(groups) > 0 {
		if err := store.Update(func(cfg *state.Config) error {
			cfg.Groups = groups
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return store, secret
}

func TestGroupFallsThroughBeforeTheReplyStarts(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits["p1"]++
		mu.Unlock()
		http.Error(w, "busy", http.StatusBadGateway)
	}))
	defer down.Close()
	up := httptest.NewServer(hitCounter(&mu, hits, "p2"))
	defer up.Close()
	store, secret := storeWithGroups(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: down.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k2"},
	}, nil, []state.Group{{
		ID: "group-demo", Members: []string{"p1/demo", "p2/demo"}, Routing: "order",
	}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"group-demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"content":"ok"`) {
		t.Fatalf("group reply %d %s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["p1"] != 1 || hits["p2"] != 1 {
		t.Fatalf("group hits %v", hits)
	}
}

func TestInnerGroupFinishesItsOwnOrderBeforeTheOuterMember(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits["p1"]++
		mu.Unlock()
		http.Error(w, "busy", http.StatusBadGateway)
	}))
	defer down.Close()
	inner := httptest.NewServer(hitCounter(&mu, hits, "p2"))
	defer inner.Close()
	outer := httptest.NewServer(hitCounter(&mu, hits, "p3"))
	defer outer.Close()
	store, secret := storeWithGroups(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: down.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: inner.URL, Models: []string{"demo"}, Credential: "k2"},
		{ID: "p3", Protocol: "chat", BaseURL: outer.URL, Models: []string{"demo"}, Credential: "k3"},
	}, nil, []state.Group{
		{ID: "inner", Members: []string{"p1/demo", "p2/demo"}, Routing: "order"},
		{ID: "outer", Members: []string{"inner", "p3/demo"}},
	})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"outer","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("outer reply %d %s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["p1"] != 1 || hits["p2"] != 1 || hits["p3"] != 0 {
		t.Fatalf("nested hits %v", hits)
	}
}

func TestGroupMemberTriesItsSecondKey(t *testing.T) {
	var mu sync.Mutex
	seen := []string{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		seen = append(seen, key)
		mu.Unlock()
		if key == "k1" {
			http.Error(w, "busy", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store, secret := storeWithGroups(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"},
		Credentials: []string{"k1", "k2"},
	}}, nil, []state.Group{{ID: "group-demo", Members: []string{"p1/demo"}, Routing: "order"}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"group-demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("group key reply %d %s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "k1" || seen[1] != "k2" {
		t.Fatalf("keys seen %v", seen)
	}
}

func TestAutoGroupAnswersABareSharedModel(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits["p1"]++
		mu.Unlock()
		http.Error(w, "busy", http.StatusBadGateway)
	}))
	defer down.Close()
	up := httptest.NewServer(hitCounter(&mu, hits, "p2"))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: down.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k2"},
	}, nil)
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("auto group reply %d %s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["p1"] != 1 || hits["p2"] != 1 {
		t.Fatalf("auto group hits %v", hits)
	}
}

func TestAutoGroupIncludesASubscriptionAccount(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"oat-test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var p1Hits, p2Hits atomic.Int32
	down := func(hits *atomic.Int32) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			http.Error(w, "busy", http.StatusBadGateway)
		})
	}
	p1 := httptest.NewServer(down(&p1Hits))
	defer p1.Close()
	p2 := httptest.NewServer(down(&p2Hits))
	defer p2.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: p1.URL, Models: []string{"claude-sonnet-4-6"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: p2.URL, Models: []string{"claude-sonnet-4-6"}, Credential: "k2"},
	}, nil)
	var seenModel string
	srv := &Server{Store: store, Login: func() login.Machine {
		return login.Machine{Home: home, Claude: func(_ context.Context, _ string, model string, _ []byte) ([]byte, error) {
			seenModel = model
			return []byte(`{"id":"msg_local","type":"message","role":"assistant","content":[{"type":"text","text":"pong"}]}`), nil
		}}
	}}
	srv.quotaMu.Lock()
	srv.quotaAt = time.Now()
	srv.quotaByID = map[string]quotaView{"claude": {Account: "claude", Status: "unknown"}}
	srv.quotaMu.Unlock()

	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "pong") {
		t.Fatalf("auto group account reply %d %s", rr.Code, rr.Body.String())
	}
	if p1Hits.Load() != 1 || p2Hits.Load() != 1 {
		t.Fatalf("provider hits %d %d", p1Hits.Load(), p2Hits.Load())
	}
	if seenModel != "claude-sonnet-4-6" {
		t.Fatalf("subscription model %q", seenModel)
	}
}

func TestAutoGroupsOffRefusesToPickOneProvider(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k2"},
	}, nil)
	if err := store.Update(func(cfg *state.Config) error {
		cfg.NoAutoGroups = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusNotFound || hits.Load() != 0 {
		t.Fatalf("no auto group %d hits %d %s", rr.Code, hits.Load(), rr.Body.String())
	}
}

func TestGroupStrategyInheritance(t *testing.T) {
	for _, outer := range []string{routes.Smart, routes.Rotate, routes.Usage, routes.Pace, routes.Free, routes.Fast, routes.Drain} {
		if got := groupStrategy("", outer); got != outer {
			t.Fatalf("inner inherited %q as %q", outer, got)
		}
	}
	for _, outer := range []string{routes.Order, routes.Weight} {
		if got := groupStrategy("", outer); got != routes.Smart {
			t.Fatalf("inner under %q = %q", outer, got)
		}
	}
	if got := groupStrategy(routes.Order, routes.Usage); got != routes.Order {
		t.Fatalf("own strategy was replaced by %q", got)
	}
}
