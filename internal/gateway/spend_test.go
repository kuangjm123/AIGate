package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/state"
)

func TestSpendLimitStopsTheNextCall(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a capped token dialed")
	}))
	defer up.Close()
	store := loadTemp(t)
	limit := 1.0
	secret := ""
	if err := store.Update(func(cfg *state.Config) error {
		sec, err := state.NewSecret()
		if err != nil {
			return err
		}
		secret = sec
		cfg.GatewayTokens = []state.GatewayToken{{ID: "tok", Secret: sec, SpendLimit: &limit, SpendPeriod: "all"}}
		cfg.Providers = []state.Provider{{ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k"}}
		cfg.Routes = []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsage(state.AttemptRecord{
		RouteID: "r1", ProviderID: "p1", Outcome: OutcomeSuccess, Model: "demo", TokenID: "tok",
		Started: time.Now().UTC(), Finished: time.Now().UTC(),
		Usage: json.RawMessage(`{"prompt_tokens":1000000,"completion_tokens":0}`),
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: store, Prices: price.Fixed{"demo": {Input: 2, Output: 8}}}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusTooManyRequests || !strings.Contains(rr.Body.String(), "spend_limit") {
		t.Fatalf("capped %d %s", rr.Code, rr.Body.String())
	}
}

func TestSpendLimitWithoutPricesIsUnknown(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("an unpriced cap dialed")
	}))
	defer up.Close()
	store := loadTemp(t)
	limit := 5.0
	secret := ""
	if err := store.Update(func(cfg *state.Config) error {
		sec, err := state.NewSecret()
		if err != nil {
			return err
		}
		secret = sec
		cfg.GatewayTokens = []state.GatewayToken{{ID: "tok", Secret: sec, SpendLimit: &limit}}
		cfg.Providers = []state.Provider{{ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k"}}
		cfg.Routes = []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions", `{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "price_unknown") {
		t.Fatalf("unknown %d %s", rr.Code, rr.Body.String())
	}
}
