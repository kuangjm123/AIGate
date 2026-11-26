package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestSpendLimitZeroIsUncapped(t *testing.T) {
	var dialed atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dialed.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store := loadTemp(t)
	zero := 0.0
	owner, issuer, short := "", "", ""
	if err := store.Update(func(cfg *state.Config) error {
		var err error
		owner, err = state.NewSecret()
		if err != nil {
			return err
		}
		issuer, err = state.NewSecret()
		if err != nil {
			return err
		}
		short, err = state.NewSecret()
		if err != nil {
			return err
		}
		cfg.GatewayTokens = []state.GatewayToken{
			{ID: "owner", Secret: owner, SpendLimit: &zero, SpendPeriod: "today"},
			{ID: "issuer", Secret: issuer, SpendLimit: &zero},
			{ID: "short", Secret: short, IssuerID: "issuer"},
		}
		cfg.Providers = []state.Provider{{
			ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
		}}
		cfg.Routes = []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: store}
	for _, secret := range []string{owner, short} {
		rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions",
			`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("zero spend limit refused: %d %s", rr.Code, rr.Body.String())
		}
	}
	if dialed.Load() != 2 {
		t.Fatalf("dialed %d", dialed.Load())
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

func TestSpendLimitWithoutACapDoesNotReadTheLedger(t *testing.T) {
	code, status := (*Server)(nil).spendBlock(context.Background(), admission{
		tokenID: "short", issuerID: "missing-issuer",
	}, state.Config{})
	if code != "" || status != 0 {
		t.Fatalf("uncapped token: %q %d", code, status)
	}
}

func TestSpendLimitDayFilledWeekNot(t *testing.T) {
	store := loadTemp(t)
	cfg := spendConfig(t, store, nil)
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "demo", CallerKeyID: "tok", In: 1_000_000,
	})
	srv := &Server{Store: store, Prices: price.Fixed{"demo": {Input: 2, Output: 8}}}
	day, week := money(1), money(100)
	code, status := srv.spendBlock(context.Background(), admission{
		tokenID: "tok",
		limits:  []state.TokenLimit{{ID: "day", USD: day, Window: "today"}},
	}, cfg)
	if code != CodeSpendLimit || status != http.StatusTooManyRequests {
		t.Fatalf("filled day: %q %d", code, status)
	}
	code, status = srv.spendBlock(context.Background(), admission{
		tokenID: "tok",
		limits:  []state.TokenLimit{{ID: "week", USD: week, Window: "week"}},
	}, cfg)
	if code != "" || status != 0 {
		t.Fatalf("open week: %q %d", code, status)
	}
}

func TestSpendLimitChargesTheIssuerOfAShortLivedKey(t *testing.T) {
	store := loadTemp(t)
	cfg := spendConfig(t, store, nil)
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "demo",
		CallerKeyID: "short", CallerIssuerID: "issuer", In: 1_000_000,
	})
	cap := money(1)
	limits := []state.TokenLimit{{USD: cap, Window: "today"}}
	srv := &Server{Store: store, Prices: price.Fixed{"demo": {Input: 2, Output: 8}}}
	for _, key := range []string{"issuer", "short"} {
		code, status := srv.spendBlock(context.Background(), admission{tokenID: key, limits: limits}, cfg)
		if code != CodeSpendLimit || status != http.StatusTooManyRequests {
			t.Fatalf("key %q: %q %d", key, code, status)
		}
	}
	code, status := srv.spendBlock(context.Background(), admission{tokenID: "other", limits: limits}, cfg)
	if code != "" || status != 0 {
		t.Fatalf("unrelated key: %q %d", code, status)
	}
}

func TestSpendLimitIssuerCapsApplyToShortLivedKeys(t *testing.T) {
	store := loadTemp(t)
	cap := money(1)
	cfg := spendConfig(t, store, func(cfg *state.Config) {
		cfg.GatewayTokens = []state.GatewayToken{
			{ID: "issuer", Secret: "a", Limits: []state.TokenLimit{{USD: cap, Window: "today"}}},
			{ID: "short-a", Secret: "b", IssuerID: "issuer"},
			{ID: "short-b", Secret: "c", IssuerID: "issuer"},
			{ID: "other-issuer", Secret: "d"},
			{ID: "short-c", Secret: "e", IssuerID: "other-issuer"},
		}
	})
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "demo",
		CallerKeyID: "short-a", CallerIssuerID: "issuer", In: 1_000_000,
	})
	srv := &Server{Store: store, Prices: price.Fixed{"demo": {Input: 2, Output: 8}}}
	code, status := srv.spendBlock(context.Background(), admission{
		tokenID: "short-b", issuerID: "issuer",
	}, cfg)
	if code != CodeSpendLimit || status != http.StatusTooManyRequests {
		t.Fatalf("issuer cap: %q %d", code, status)
	}
	code, status = srv.spendBlock(context.Background(), admission{
		tokenID: "short-c", issuerID: "other-issuer",
	}, cfg)
	if code != "" || status != 0 {
		t.Fatalf("other issuer: %q %d", code, status)
	}
}

func TestSpendLimitChecksUSDAndTokens(t *testing.T) {
	t.Run("tokens", func(t *testing.T) {
		store := loadTemp(t)
		cfg := spendConfig(t, store, nil)
		appendSpend(t, store, state.AttemptRecord{
			ProviderID: "p1", Model: "demo", CallerKeyID: "tok", In: 60, Out: 50,
		})
		usd, count := money(100), intPtr(100)
		srv := &Server{Store: store, Prices: price.Fixed{"demo": {Input: 2, Output: 8}}}
		code, status := srv.spendBlock(context.Background(), admission{
			tokenID: "tok",
			limits:  []state.TokenLimit{{USD: usd, Tokens: count, Window: "all"}},
		}, cfg)
		if code != CodeSpendLimit || status != http.StatusTooManyRequests {
			t.Fatalf("token cap: %q %d", code, status)
		}
	})
	t.Run("usd", func(t *testing.T) {
		store := loadTemp(t)
		cfg := spendConfig(t, store, nil)
		appendSpend(t, store, state.AttemptRecord{
			ProviderID: "p1", Model: "demo", CallerKeyID: "tok", In: 1_000_000,
		})
		usd, count := money(1), intPtr(10_000_000)
		srv := &Server{Store: store, Prices: price.Fixed{"demo": {Input: 2, Output: 8}}}
		code, status := srv.spendBlock(context.Background(), admission{
			tokenID: "tok",
			limits:  []state.TokenLimit{{USD: usd, Tokens: count, Window: "all"}},
		}, cfg)
		if code != CodeSpendLimit || status != http.StatusTooManyRequests {
			t.Fatalf("usd cap: %q %d", code, status)
		}
	})
}

func TestSpendLimitWithoutPricesIsUnknownForNewLimits(t *testing.T) {
	store := loadTemp(t)
	cfg := spendConfig(t, store, nil)
	usd := money(5)
	code, status := (&Server{Store: store}).spendBlock(context.Background(), admission{
		tokenID: "tok",
		limits:  []state.TokenLimit{{USD: usd, Window: "all"}},
	}, cfg)
	if code != CodePriceUnknown || status != http.StatusServiceUnavailable {
		t.Fatalf("usd without prices: %q %d", code, status)
	}
	count := intPtr(100)
	code, status = (&Server{Store: store}).spendBlock(context.Background(), admission{
		tokenID: "tok",
		limits:  []state.TokenLimit{{Tokens: count, Window: "all"}},
	}, cfg)
	if code != "" || status != 0 {
		t.Fatalf("token cap needs no price: %q %d", code, status)
	}
}

func TestSpendLimitZeroUSDIsUncappedWithATokenCap(t *testing.T) {
	store := loadTemp(t)
	cfg := spendConfig(t, store, nil)
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "demo", CallerKeyID: "tok", In: 50,
	})
	usd, count := money(0), intPtr(100)
	code, status := (&Server{Store: store}).spendBlock(context.Background(), admission{
		tokenID: "tok",
		limits:  []state.TokenLimit{{USD: usd, Tokens: count, Window: "all"}},
	}, cfg)
	if code != "" || status != 0 {
		t.Fatalf("zero USD should not need prices: %q %d", code, status)
	}
}

func TestSpendLimitRefusesAnUnpricedRow(t *testing.T) {
	store := loadTemp(t)
	cfg := spendConfig(t, store, nil)
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "missing", CallerKeyID: "tok", In: 1_000_000,
	})
	usd := money(5)
	code, status := (&Server{Store: store, Prices: price.Fixed{}}).spendBlock(context.Background(), admission{
		tokenID: "tok",
		limits:  []state.TokenLimit{{USD: usd, Window: "all"}},
	}, cfg)
	if code != CodePriceUnknown || status != http.StatusServiceUnavailable {
		t.Fatalf("unpriced row: %q %d", code, status)
	}
}

func TestSpendLimitSkipsReportedProbesAndCanceledRows(t *testing.T) {
	store := loadTemp(t)
	cfg := spendConfig(t, store, nil)
	rows := []state.AttemptRecord{
		{ProviderID: "p1", Model: "demo", Source: state.SourceReported, In: 1000},
		{ProviderID: "p1", Model: "demo", Source: state.SourceProbe, In: 1000},
		{ProviderID: "p1", Model: "demo", Source: state.SourceMCP, In: 1000},
		{ProviderID: "p1", Model: "demo", Source: state.SourceSearch, In: 1000},
		{ProviderID: "p1", Model: "demo", Status: 499, Err: state.CanceledText, In: 1000},
		{ProviderID: "p1", Model: "demo", Rejected: true, In: 1000},
		{ProviderID: "p1", Model: "demo", HandedOn: true, In: 60},
		{ProviderID: "p1", Model: "demo", In: 30},
	}
	for i := range rows {
		rows[i].CallerKeyID = "tok"
		appendSpend(t, store, rows[i])
	}
	count := intPtr(100)
	limits := []state.TokenLimit{{Tokens: count, Window: "all"}}
	srv := &Server{Store: store}
	code, status := srv.spendBlock(context.Background(), admission{tokenID: "tok", limits: limits}, cfg)
	if code != "" || status != 0 {
		t.Fatalf("excluded rows counted: %q %d", code, status)
	}
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "demo", CallerKeyID: "tok", In: 10,
	})
	code, status = srv.spendBlock(context.Background(), admission{tokenID: "tok", limits: limits}, cfg)
	if code != CodeSpendLimit || status != http.StatusTooManyRequests {
		t.Fatalf("handed-on or final row not counted: %q %d", code, status)
	}
}

func TestSpendLimitCountsRowsHiddenFromTheUsagePage(t *testing.T) {
	store := loadTemp(t)
	cfg := spendConfig(t, store, func(cfg *state.Config) {
		cfg.UsageHidden = []state.UsageHide{{
			ProviderID: "p1", Model: "demo", Before: time.Now().Add(time.Hour),
		}}
	})
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "demo", CallerKeyID: "tok", In: 1000,
	})
	count := intPtr(100)
	code, status := (&Server{Store: store}).spendBlock(context.Background(), admission{
		tokenID: "tok",
		limits:  []state.TokenLimit{{Tokens: count, Window: "all"}},
	}, cfg)
	if code != CodeSpendLimit || status != http.StatusTooManyRequests {
		t.Fatalf("hidden row was not counted: %q %d", code, status)
	}
}

func TestSpendLimitScopeCountsOnlyTheNamedProviderAndModel(t *testing.T) {
	store := loadTemp(t)
	cfg := spendConfig(t, store, nil)
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "demo", CallerKeyID: "tok", In: 1000,
	})
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p2", Model: "other", CallerKeyID: "tok", In: 100,
	})
	count := intPtr(100)
	srv := &Server{Store: store}
	code, status := srv.spendBlock(context.Background(), admission{
		tokenID: "tok",
		limits:  []state.TokenLimit{{Tokens: count, Window: "all", Provider: "p2", Model: "other"}},
	}, cfg)
	if code != CodeSpendLimit || status != http.StatusTooManyRequests {
		t.Fatalf("scoped cap: %q %d", code, status)
	}
	for _, limit := range []state.TokenLimit{
		{Tokens: count, Window: "all", Provider: "p1", Model: "other"},
		{Tokens: count, Window: "all", Provider: "p3"},
	} {
		code, status = srv.spendBlock(context.Background(), admission{
			tokenID: "tok", limits: []state.TokenLimit{limit},
		}, cfg)
		if code != "" || status != 0 {
			t.Fatalf("out of scope: %q %d", code, status)
		}
	}
}

func TestSpendLimitCountsOnlyItsRollingWindow(t *testing.T) {
	store := loadTemp(t)
	cfg := spendConfig(t, store, nil)
	now := time.Now().UTC()
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "demo", CallerKeyID: "tok", In: 1000,
		Started: now.Add(-2 * time.Hour),
	})
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "demo", CallerKeyID: "tok", In: 50,
		Started: now.Add(-10 * time.Minute),
	})
	count := intPtr(100)
	limits := []state.TokenLimit{{Tokens: count, Window: "hours", Hours: 1}}
	srv := &Server{Store: store}
	code, status := srv.spendBlock(context.Background(), admission{tokenID: "tok", limits: limits}, cfg)
	if code != "" || status != 0 {
		t.Fatalf("old row counted: %q %d", code, status)
	}
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "demo", CallerKeyID: "tok", In: 50,
		Started: now.Add(-5 * time.Minute),
	})
	code, status = srv.spendBlock(context.Background(), admission{tokenID: "tok", limits: limits}, cfg)
	if code != CodeSpendLimit || status != http.StatusTooManyRequests {
		t.Fatalf("window filled: %q %d", code, status)
	}
}

func TestSpendLimitStopsTheNextCallWithNewLimits(t *testing.T) {
	var dialed atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dialed.Add(1)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"no"}}]}`))
	}))
	defer up.Close()
	store := loadTemp(t)
	cap := money(1)
	secret := ""
	if err := store.Update(func(cfg *state.Config) error {
		sec, err := state.NewSecret()
		if err != nil {
			return err
		}
		secret = sec
		cfg.GatewayTokens = []state.GatewayToken{{
			ID: "tok", Secret: sec,
			Limits: []state.TokenLimit{{USD: cap, Window: "today"}},
		}}
		cfg.Providers = []state.Provider{{
			ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
		}}
		cfg.Routes = []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	appendSpend(t, store, state.AttemptRecord{
		ProviderID: "p1", Model: "demo", CallerKeyID: "tok", In: 1_000_000,
	})
	srv := &Server{Store: store, Prices: price.Fixed{"demo": {Input: 2, Output: 8}}}
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusTooManyRequests || !strings.Contains(rr.Body.String(), CodeSpendLimit) {
		t.Fatalf("capped %d %s", rr.Code, rr.Body.String())
	}
	if dialed.Load() != 0 {
		t.Fatalf("capped call dialed %d times", dialed.Load())
	}
}

func spendConfig(t *testing.T, store *state.Store, edit func(*state.Config)) state.Config {
	t.Helper()
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Providers = []state.Provider{
			{ID: "p1", Protocol: "chat", BaseURL: "https://p1.example/v1", Models: []string{"demo"}, Credential: "k1"},
			{ID: "p2", Protocol: "chat", BaseURL: "https://p2.example/v1", Models: []string{"demo", "other"}, Credential: "k2"},
		}
		if edit != nil {
			edit(cfg)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return store.Current().Config
}

func appendSpend(t *testing.T, store *state.Store, rec state.AttemptRecord) {
	t.Helper()
	if rec.Started.IsZero() {
		rec.Started = time.Now().UTC()
	}
	if rec.Finished.IsZero() {
		rec.Finished = rec.Started.Add(time.Second)
	}
	if err := store.AppendUsage(rec); err != nil {
		t.Fatal(err)
	}
}

func money(v float64) *float64 {
	return &v
}

func intPtr(v int) *int {
	return &v
}
