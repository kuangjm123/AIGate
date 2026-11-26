package balance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/state"
)

func TestDeepSeekBalanceUsesTheOrigin(t *testing.T) {
	got, ok := Endpoint("https://api.deepseek.com/v1")
	if !ok || got != "https://api.deepseek.com/user/balance" {
		t.Fatalf("endpoint %q ok %v", got, ok)
	}
	if _, ok := Endpoint("https://api.openai.com/v1"); ok {
		t.Fatal("openai was treated as a balance host")
	}
}

func TestParseDeepSeekKeepsTheVendorAmount(t *testing.T) {
	body := []byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"}]}`)
	remaining, available, ok := ParseDeepSeek(body)
	if !ok || remaining != "CNY 110.00" || available == nil || !*available {
		t.Fatalf("remaining %q available %v ok %v", remaining, available, ok)
	}
}

func TestAskDeepSeekReadsTheBalance(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_available":false,"balance_infos":[{"currency":"USD","total_balance":"1.50"}]}`))
	}))
	defer srv.Close()
	body, status, err := get(context.Background(), srv.Client(), srv.URL+"/user/balance", "sk-test")
	if err != nil || status != http.StatusOK {
		t.Fatalf("get %d %v", status, err)
	}
	remaining, available, ok := ParseDeepSeek(body)
	if !ok || remaining != "USD 1.50" || available == nil || *available {
		t.Fatalf("remaining %q available %v", remaining, available)
	}
	if gotAuth != "Bearer sk-test" || gotPath != "/user/balance" {
		t.Fatalf("auth %q path %q", gotAuth, gotPath)
	}
}

func TestUnknownHostIsNotDialed(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("dialed")
		return nil, nil
	})}
	got := Ask(context.Background(), client, "https://api.openai.com/v1", "sk-test", "")
	if got.Status != "unsupported" || got.Remaining != "" {
		t.Fatalf("%+v", got)
	}
}

func TestFailedBalanceIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	body, status, err := get(context.Background(), srv.Client(), srv.URL+"/user/balance", "sk-test")
	if err != nil || status != http.StatusUnauthorized {
		t.Fatalf("get %d %v", status, err)
	}
	if _, _, ok := ParseDeepSeek(body); ok || strings.Contains(string(body), "sk-test") {
		t.Fatal("error body parsed as a balance")
	}
}

func TestOpenRouterCreditsAreTheRemainingBalance(t *testing.T) {
	ResetCache()
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"total_credits":100.0,"total_usage":25.0}}`))
	}))
	defer srv.Close()
	if err := Register(Source{
		URL:    srv.URL + "/api/v1/credits",
		Amount: "data.total_credits", Subtract: "data.total_usage", Currency: "$",
	}); err != nil {
		t.Fatal(err)
	}
	endpoint, ok := Endpoint(srv.URL)
	if !ok || endpoint != srv.URL+"/api/v1/credits" {
		t.Fatalf("endpoint %q ok %v", endpoint, ok)
	}
	got := Ask(context.Background(), srv.Client(), srv.URL+"/v1", "sk-or", "")
	if got.Status != "ok" || got.Remaining != "$75.00" || got.Error != "" || got.Stale {
		t.Fatalf("reading %+v", got)
	}
	if gotAuth != "Bearer sk-or" || gotPath != "/api/v1/credits" {
		t.Fatalf("auth %q path %q", gotAuth, gotPath)
	}
}

func TestBalanceRefreshKeepsTheLastGoodAmount(t *testing.T) {
	ResetCache()
	var fail atomic.Bool
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "no", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"balance":"12.50"}}`))
	}))
	defer srv.Close()
	if err := Register(Source{
		URL: srv.URL + "/balance", Amount: "data.balance", Currency: "USD",
	}); err != nil {
		t.Fatal(err)
	}
	old := clock
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	clock = func() time.Time { return now }
	defer func() { clock = old }()
	read := func() Reading {
		got := ForQueries(context.Background(), srv.Client(), []Query{{
			ProviderID: "p", BaseURL: srv.URL + "/v1", Key: "sk-test",
			Manual: "1.00", Currency: "USD",
		}}, "")
		if len(got) != 1 {
			t.Fatalf("readings %+v", got)
		}
		return got[0]
	}

	first := read()
	if first.Status != "ok" || first.Remaining != "USD 12.50" {
		t.Fatalf("first %+v", first)
	}
	again := read()
	if again.Status != "ok" || hits.Load() != 1 {
		t.Fatalf("cached %+v hits %d", again, hits.Load())
	}

	fail.Store(true)
	now = now.Add(2 * time.Minute)
	stale := read()
	if stale.Status != "unknown" || stale.Remaining != "USD 12.50" || !stale.Stale || stale.Error == "" {
		t.Fatalf("stale %+v", stale)
	}
	if hits.Load() != 2 {
		t.Fatalf("refresh hits %d", hits.Load())
	}
	_ = read()
	if hits.Load() != 2 {
		t.Fatalf("failed reading was not cached: hits %d", hits.Load())
	}

	fail.Store(false)
	now = now.Add(2 * time.Minute)
	fresh := read()
	if fresh.Status != "ok" || fresh.Remaining != "USD 12.50" || fresh.Stale || fresh.Error != "" {
		t.Fatalf("fresh %+v", fresh)
	}
}

func TestForQueriesUsesTheManualAmountWithoutAnEndpoint(t *testing.T) {
	ResetCache()
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("a manual balance dialed")
		return nil, nil
	})}
	got := ForQueries(context.Background(), client, []Query{{
		ProviderID: "p", Name: "Relay", Index: 2,
		BaseURL: "https://unknown.example/v1", Key: "sk-test",
		Manual: "12.5", Currency: "USD",
	}}, "")
	if len(got) != 1 || got[0].ProviderID != "p" || got[0].Index != 2 ||
		got[0].Status != "manual" || got[0].Remaining != "USD 12.5" {
		t.Fatalf("manual %+v", got)
	}
}

func TestForQueriesReadsAUserBalanceURL(t *testing.T) {
	ResetCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"remaining":"3.25","currency":"EUR"}`))
	}))
	defer srv.Close()
	got := ForQueries(context.Background(), srv.Client(), []Query{{
		ProviderID: "p", Name: "Relay", Index: 1,
		BaseURL: "https://unknown.example/v1", Key: "sk-test",
		URL: srv.URL + "/my-balance", Field: "remaining",
	}}, "")
	if len(got) != 1 || got[0].Status != "ok" || got[0].Remaining != "EUR 3.25" {
		t.Fatalf("user URL %+v", got)
	}
}

func TestForQueriesFallsBackToManualWhenTheEndpointFails(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "http", status: http.StatusBadGateway},
		{name: "missing amount", status: http.StatusOK, body: `{"other":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetCache()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			got := ForQueries(context.Background(), srv.Client(), []Query{{
				ProviderID: "p", BaseURL: "https://unknown.example/v1", Key: "sk-test",
				URL: srv.URL + "/balance", Field: "remaining",
				Manual: "4.50", Currency: "USD",
			}}, "")
			if len(got) != 1 || got[0].Status != "manual" || got[0].Remaining != "USD 4.50" || got[0].Error == "" {
				t.Fatalf("manual fallback %+v", got)
			}
		})
	}
}

func TestForProvidersUsesTheSavedBaseURLOrigin(t *testing.T) {
	ResetCache()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"9.00"}]}`))
	}))
	defer srv.Close()
	if err := Register(Source{Host: strings.TrimPrefix(srv.URL, "http://"), Path: "/user/balance", Parse: ParseDeepSeek}); err != nil {
		t.Fatal(err)
	}
	got := ForProviders(context.Background(), srv.Client(), []state.Provider{{
		ID: "p", Name: "DeepSeek", BaseURL: srv.URL + "/v1", Credential: "sk-test",
	}}, "")
	if len(got) != 1 || got[0].Status != "ok" || got[0].Remaining != "CNY 9.00" || gotPath != "/user/balance" {
		t.Fatalf("providers %+v path %q", got, gotPath)
	}
}

func TestForProvidersAsksEachKey(t *testing.T) {
	ResetCache()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"balance":"1.00"}`))
	}))
	defer srv.Close()
	if err := Register(Source{URL: srv.URL + "/balance", Amount: "balance", Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	got := ForProviders(context.Background(), srv.Client(), []state.Provider{{
		ID: "p", Name: "Relay", BaseURL: srv.URL + "/v1",
		Credentials: []string{"key-a", "key-b"},
	}}, "")
	if len(got) != 2 || got[0].Index != 1 || got[1].Index != 2 ||
		got[0].Remaining != "USD 1.00" || got[1].Remaining != "USD 1.00" {
		t.Fatalf("readings %+v", got)
	}
	if hits.Load() != 2 {
		t.Fatalf("asked %d times", hits.Load())
	}
}

func TestForProvidersUsesManualAndUserURLFields(t *testing.T) {
	ResetCache()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"left":"8.25"}}`))
	}))
	defer srv.Close()
	manual := ForProviders(context.Background(), srv.Client(), []state.Provider{{
		ID: "manual", BaseURL: "https://unknown.example/v1", Credential: "sk-manual",
		BalanceManual: "4.50", BalanceCurrency: "USD",
	}}, "")
	if len(manual) != 1 || manual[0].Status != "manual" || manual[0].Remaining != "USD 4.50" {
		t.Fatalf("manual %+v", manual)
	}
	queried := ForProviders(context.Background(), srv.Client(), []state.Provider{{
		ID: "query", BaseURL: "https://unknown.example/v1", Credential: "sk-query",
		BalanceURL: srv.URL + "/balance", BalanceField: "data.left", BalanceCurrency: "EUR",
	}}, "")
	if len(queried) != 1 || queried[0].Status != "ok" || queried[0].Remaining != "EUR 8.25" {
		t.Fatalf("queried %+v", queried)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
