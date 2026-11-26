package balance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
