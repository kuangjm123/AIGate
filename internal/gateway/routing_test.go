package gateway

import (
	"context"
	"encoding/json"
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
	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/state"
)

func hitCounter(mu *sync.Mutex, hits map[string]int, id string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[id]++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`))
	})
}

func twoProviders(mu *sync.Mutex, hits map[string]int) (*httptest.Server, *httptest.Server) {
	first := httptest.NewServer(hitCounter(mu, hits, "p1"))
	second := httptest.NewServer(hitCounter(mu, hits, "p2"))
	return first, second
}

func TestRotateAlternatesProviders(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	first, second := twoProviders(&mu, hits)
	defer first.Close()
	defer second.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: first.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: second.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}, Routing: "rotate"}})
	srv := &Server{Store: store}
	for i := 0; i < 4; i++ {
		rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status %d", i, rr.Code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["p1"] != 2 || hits["p2"] != 2 {
		t.Fatalf("rotate hits %v", hits)
	}
}

func TestRateLimitRestsTheProvider(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits["p1"]++
		mu.Unlock()
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer limited.Close()
	okSrv := httptest.NewServer(hitCounter(&mu, hits, "p2"))
	defer okSrv.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: limited.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: okSrv.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}, EligibleSet: true, Eligible: []state.ErrorClass{state.ClassRateLimit}}})
	srv := &Server{Store: store}
	for i := 0; i < 3; i++ {
		rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status %d body %s", i, rr.Code, rr.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["p1"] != 1 || hits["p2"] != 3 {
		t.Fatalf("resting hits %v", hits)
	}
}

func TestSecondKeyOfOneProviderAnswersAfterRateLimit(t *testing.T) {
	var mu sync.Mutex
	seen := []string{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		seen = append(seen, key)
		mu.Unlock()
		if key == "k1" {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"second-key"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"},
		Credentials: []string{"k1", "k2"},
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	srv := &Server{Store: store}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "second-key") {
		t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "k1" || seen[1] != "k2" {
		t.Fatalf("keys %v", seen)
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("usage %+v %v", recs, err)
	}
	rec := recs[0]
	if rec.ProviderID != "p1" || rec.ProviderKeyID != state.KeyFingerprint("k2") || rec.Tries != 2 || rec.Outcome != OutcomeSuccess {
		t.Fatalf("usage %+v", rec)
	}
	blob, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "k2") {
		t.Fatal("upstream key leaked into usage")
	}
}

func TestSpentKeyStaysBehindTheOtherKey(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		hits[key]++
		mu.Unlock()
		if key == "k1" {
			http.Error(w, "no credit", http.StatusPaymentRequired)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"},
		Credentials: []string{"k1", "k2"},
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	srv := &Server{Store: store}
	for i := 0; i < 2; i++ {
		rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status %d body %s", i, rr.Code, rr.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["k1"] != 1 || hits["k2"] != 2 {
		t.Fatalf("hits %v", hits)
	}
}

func TestResetSeatClearsSpentAndDrained(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		hits[key]++
		count := hits[key]
		mu.Unlock()
		if key == "k1" && count == 1 {
			http.Error(w, "no credit", http.StatusPaymentRequired)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"},
		Credentials: []string{"k1", "k2"},
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	srv := &Server{Store: store}
	for i := 0; i < 2; i++ {
		rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status %d body %s", i, rr.Code, rr.Body.String())
		}
	}
	key1 := providerSeatKey("p1", "k1")
	if !srv.strategies().Drained("r1", key1) {
		t.Fatal("quota refusal was not drained")
	}
	srv.ResetSeat("r1", key1)
	if srv.strategies().Drained("r1", key1) {
		t.Fatal("reset left the seat drained")
	}
	mu.Lock()
	before := hits["k1"]
	mu.Unlock()
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("reset request %d %s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["k1"] != before+1 {
		t.Fatalf("reset did not return to k1: hits %v", hits)
	}
}

func TestExpiredRestReturnsToTheStrategyOrderAndSpentDoesNot(t *testing.T) {
	srv := &Server{}
	mem := srv.routingState()
	key1 := providerSeatKey("p1", "k1")
	key2 := providerSeatKey("p1", "k2")
	mem.rests[key1] = time.Now().Add(-time.Second)
	mem.spent[key2] = true
	candidates := []seatCandidate{
		{Key: key1, ProviderID: "p1", Model: "demo", KeyIndex: 0},
		{Key: key2, ProviderID: "p1", Model: "demo", KeyIndex: 1},
	}
	ordered, _, blocked, _, _ := srv.orderSeats(context.Background(), state.Route{ID: "r1", Routing: "order"}, "demo", candidates)
	if ordered[0].Key != key1 || !blocked[key2] {
		t.Fatalf("order %+v blocked %v", ordered, blocked)
	}
	mem.rests[key1] = time.Now().Add(time.Minute)
	ordered, _, blocked, _, _ = srv.orderSeats(context.Background(), state.Route{ID: "r1", Routing: "order"}, "demo", candidates)
	if ordered[0].Key != key1 || !blocked[key1] || ordered[len(ordered)-1].Key != key2 {
		t.Fatalf("resting order %+v blocked %v", ordered, blocked)
	}
}

func TestFullSeatLetsTheRequestGoToTheNextSeat(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	hits := map[string]int{}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits["p1"]++
		mu.Unlock()
		once.Do(func() { close(started) })
		<-release
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"slow"}}]}`))
	}))
	defer slow.Close()
	fast := httptest.NewServer(hitCounter(&mu, hits, "p2"))
	defer fast.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: slow.URL, Models: []string{"demo"}, Credential: "k1", MaxConcurrent: 1, QueueWaitMS: 2000},
		{ID: "p2", Protocol: "chat", BaseURL: fast.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}}})
	srv := &Server{Store: store}
	first := make(chan int, 1)
	go func() {
		rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"one"}]}`)
		first <- rr.Code
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not reach p1")
	}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"two"}]}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"content":"ok"`) {
		t.Fatalf("second reply %d %s", rr.Code, rr.Body.String())
	}
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("first status %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["p1"] != 1 || hits["p2"] != 1 {
		t.Fatalf("hits %v", hits)
	}
}

func TestFullSeatQueueTimeoutIsRateLimit(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-release
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"slow"}}]}`))
	}))
	defer slow.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: slow.URL, Models: []string{"demo"},
		Credential: "k", MaxConcurrent: 1, QueueWaitMS: 40,
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	srv := &Server{Store: store}
	first := make(chan int, 1)
	go func() {
		rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"one"}]}`)
		first <- rr.Code
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not reach p1")
	}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"two"}]}`)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("queued reply %d %s", rr.Code, rr.Body.String())
	}
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("first status %d", code)
	}
}

func TestFullSeatDoesNotJumpToARestingSeat(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-release
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"slow"}}]}`))
	}))
	defer slow.Close()
	var restingHits atomic.Int32
	resting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		restingHits.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"resting"}}]}`))
	}))
	defer resting.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: slow.URL, Models: []string{"demo"}, Credential: "k1", MaxConcurrent: 1, QueueWaitMS: 40},
		{ID: "p2", Protocol: "chat", BaseURL: resting.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}}})
	srv := &Server{Store: store}
	mem := srv.routingState()
	mem.rests[providerSeatKey("p2", "k2")] = time.Now().Add(time.Minute)
	first := make(chan int, 1)
	go func() {
		rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"one"}]}`)
		first <- rr.Code
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not reach p1")
	}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"two"}]}`)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("busy seat reply %d %s", rr.Code, rr.Body.String())
	}
	if restingHits.Load() != 0 {
		t.Fatalf("queue jumped to a resting seat %d times", restingHits.Load())
	}
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("first status %d", code)
	}
}

func TestEmptyBalanceDrainsTheProvider(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	broke := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits["p1"]++
		mu.Unlock()
		http.Error(w, "no credit", http.StatusPaymentRequired)
	}))
	defer broke.Close()
	okSrv := httptest.NewServer(hitCounter(&mu, hits, "p2"))
	defer okSrv.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: broke.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: okSrv.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}, Routing: "drain", EligibleSet: true, Eligible: []state.ErrorClass{state.ClassEmptyBalance}}})
	srv := &Server{Store: store}
	for i := 0; i < 3; i++ {
		rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status %d body %s", i, rr.Code, rr.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	// Drained once, the pool sits on p2 and does not return to p1.
	if hits["p1"] != 1 || hits["p2"] != 3 {
		t.Fatalf("drain hits %v", hits)
	}
}

func TestWeightPrefersTheHeavierProvider(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	first, second := twoProviders(&mu, hits)
	defer first.Close()
	defer second.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: first.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: second.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}, Routing: "weight", Weights: map[string]int{"p1": 3, "p2": 1}}})
	srv := &Server{Store: store}
	for i := 0; i < 4; i++ {
		rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status %d", i, rr.Code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["p1"] != 3 || hits["p2"] != 1 {
		t.Fatalf("weight hits %v", hits)
	}
}

func TestCheapPrefersThePricedProvider(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	first, second := twoProviders(&mu, hits)
	defer first.Close()
	defer second.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: first.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: second.URL, Models: []string{"demo"}, Credential: "k2"},
	}, nil)
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Groups = []state.Group{{ID: "group-demo", Members: []string{"p1/demo", "p2/demo"}, Routing: "cheap"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: store, Prices: price.Fixed{"p2/demo": {Input: 0.5, Output: 0.5}}}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"group-demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["p2"] != 1 || hits["p1"] != 0 {
		t.Fatalf("cheap hits %v", hits)
	}
}

func TestUnknownRoutingKeepsTheListedOrder(t *testing.T) {
	// A hand-edited config can hold an id this build does not serve;
	// validation refuses new ones, and the gateway falls back to the list.
	srv := &Server{}
	candidates := []seatCandidate{{Key: "p1"}, {Key: "p2"}}
	ids, picked, _, _, strategyID := srv.orderSeats(context.Background(), state.Route{ID: "r1", Routing: "bogus"}, "demo", candidates)
	if len(ids) != 2 || ids[0].Key != "p1" || picked != "p1" || strategyID != "bogus" {
		t.Fatalf("unknown strategy ids %v picked %q id %q", ids, picked, strategyID)
	}
}

func TestBareModelFindsASubscriptionAccount(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"oat-test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, secret := dialStore(t, nil, nil)
	var seenModel string
	srv := &Server{Store: store, Login: func() login.Machine {
		return login.Machine{Home: home, Claude: func(_ context.Context, _ string, model string, _ []byte) ([]byte, error) {
			seenModel = model
			return []byte(`{"id":"msg_local","type":"message","role":"assistant","content":[{"type":"text","text":"pong"}]}`), nil
		}}
	}}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "pong") {
		t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
	}
	if seenModel != "claude-sonnet-4-6" {
		t.Fatalf("upstream model %q", seenModel)
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("usage %+v %v", recs, err)
	}
	if recs[0].ProviderID != "login/claude" || recs[0].ProviderAccount != "claude" || recs[0].Outcome != OutcomeSuccess {
		t.Fatalf("usage %+v", recs[0])
	}
}

func TestStickFirstSkipsABlockedSeat(t *testing.T) {
	ids := stickFirst([]string{"p1", "p2"}, "p2", map[string]bool{"p2": true})
	if ids[0] != "p1" {
		t.Fatalf("blocked sticky moved %v", ids)
	}
	ids = stickFirst([]string{"p1", "p2"}, "p2", nil)
	if ids[0] != "p2" {
		t.Fatalf("sticky not moved %v", ids)
	}
}

func TestUsageStrategyPrefersTheLessUsedProvider(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	first, second := twoProviders(&mu, hits)
	defer first.Close()
	defer second.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: first.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: second.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}, Routing: "usage"}})
	srv := &Server{Store: store}
	// The first call answers on p1 (listed order, neither reported a share).
	// Its send volume then outranks p2, so the next call goes to p2.
	for i := 0; i < 2; i++ {
		rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d status %d body %s", i, rr.Code, rr.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["p1"] != 1 || hits["p2"] != 1 {
		t.Fatalf("usage hits %v", hits)
	}
}

func TestStreamRecordsFirstTokenTime(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	srv := &Server{Store: store}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
	}
	mem := srv.routingState()
	srv.routingMu.Lock()
	stat := mem.speed[providerSeatKey("p1", "k")+"\ndemo"]
	srv.routingMu.Unlock()
	if stat == nil || stat.ttft.weight == 0 || stat.ttft.avg() < 0 {
		t.Fatalf("ttft not recorded: %+v", stat)
	}
	if stat.tps.weight == 0 || stat.tps.avg() <= 0 {
		t.Fatalf("tps not recorded: %+v", stat)
	}
}

func TestNonStreamRecordsNoFirstTokenTime(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	srv := &Server{Store: store}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
	}
	mem := srv.routingState()
	srv.routingMu.Lock()
	stat := mem.speed[providerSeatKey("p1", "k")+"\ndemo"]
	srv.routingMu.Unlock()
	if stat == nil || stat.ttft.weight != 0 {
		t.Fatalf("whole reply recorded a first token: %+v", stat)
	}
	if stat.tps.weight == 0 {
		t.Fatalf("tps not recorded: %+v", stat)
	}
}

func TestStickyDoesNotHoldARestingProvider(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits["p1"]++
		count := hits["p1"]
		mu.Unlock()
		if count == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			return
		}
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer flaky.Close()
	okSrv := httptest.NewServer(hitCounter(&mu, hits, "p2"))
	defer okSrv.Close()
	store, secret := dialStore(t, []state.Provider{
		{ID: "p1", Protocol: "chat", BaseURL: flaky.URL, Models: []string{"demo"}, Credential: "k1"},
		{ID: "p2", Protocol: "chat", BaseURL: okSrv.URL, Models: []string{"demo"}, Credential: "k2"},
	}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1", "p2"}, EligibleSet: true, Eligible: []state.ErrorClass{state.ClassRateLimit}}})
	srv := &Server{Store: store}
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("x-session-id", "sess-rest")
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	if rr := call(); rr.Code != http.StatusOK {
		t.Fatalf("first %d", rr.Code)
	}
	// The session sticks to p1. p1 then answers 429 once and rests; the next
	// call must not stick to it.
	if rr := call(); rr.Code != http.StatusOK {
		t.Fatalf("second %d body %s", rr.Code, rr.Body.String())
	}
	if rr := call(); rr.Code != http.StatusOK {
		t.Fatalf("third %d body %s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if hits["p1"] != 2 || hits["p2"] != 2 {
		t.Fatalf("sticky rest hits %v", hits)
	}
}
