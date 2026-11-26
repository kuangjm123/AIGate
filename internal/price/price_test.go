package price

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/state"
)

func TestCostUsesTheListPrice(t *testing.T) {
	cacheRead := 0.5
	rate := Rate{Input: 2, Output: 8, CacheRead: cacheRead, CacheReadSet: true}
	got := rate.Cost(state.UsageCounts{In: 1_000_000, Out: 500_000, CacheRead: 2_000_000, CacheWrite: 1_000_000})
	// 1*2 + 0.5*8 + 2*0.5 + 1*0 = 7
	if math.Abs(got-7) > 1e-9 {
		t.Fatalf("cost %v", got)
	}
}

func TestTallySkipsEmptyAndCountsUnpriced(t *testing.T) {
	recs := []state.AttemptRecord{
		{Model: "demo", Usage: json.RawMessage(`{"prompt_tokens":1000000,"completion_tokens":0}`)},
		{Model: "missing", Usage: json.RawMessage(`{"prompt_tokens":10,"completion_tokens":1}`)},
		{Model: "demo"},
	}
	cost, unpriced := Tally(recs, func(model string) (Rate, bool) {
		if model == "demo" {
			return Rate{Input: 3, Output: 9}, true
		}
		return Rate{}, false
	})
	if math.Abs(cost-3) > 1e-9 || unpriced != 1 {
		t.Fatalf("cost %v unpriced %d", cost, unpriced)
	}
}

func TestTallyUsesAnExternalCostWithoutACatalogRate(t *testing.T) {
	cost := 0.125
	got, unpriced := Tally([]state.AttemptRecord{{
		Model: "demo", In: 1000, Cost: &cost,
	}}, func(string) (Rate, bool) {
		return Rate{}, false
	})
	if math.Abs(got-cost) > 1e-9 || unpriced != 0 {
		t.Fatalf("cost %v unpriced %d", got, unpriced)
	}
}

func TestParseCatalogPrefersTheLab(t *testing.T) {
	body := []byte(`{
		"relay": {"models": {"demo": {"cost": {"input": 9, "output": 9}}}},
		"openai": {"models": {"demo": {"cost": {"input": 2, "output": 8, "cache_read": 0.5}}, "bare": {}}}
	}`)
	got, err := ParseCatalog(body)
	if err != nil {
		t.Fatal(err)
	}
	rate, ok := got["demo"]
	if !ok || rate.Input != 2 || rate.Output != 8 || !rate.CacheReadSet || rate.CacheRead != 0.5 {
		t.Fatalf("rate %+v ok %v", rate, ok)
	}
	named, ok := got["openai/demo"]
	if !ok || named.Input != 2 {
		t.Fatalf("named %+v", named)
	}
	if _, ok := got["bare"]; ok {
		t.Fatal("model without a cost was priced")
	}
	if _, ok := got["openai/bare"]; ok {
		t.Fatal("model without a cost was priced")
	}
}

func TestParseCatalogBareModelPrefersMakerOverReseller(t *testing.T) {
	body := []byte(`{
		"deepseek": {"models": {"deepseek-chat": {"cost": {"input": 1, "output": 2}}}},
		"relay": {"models": {"deepseek-chat": {"cost": {"input": 99, "output": 99}}}}
	}`)
	for i := 0; i < 100; i++ {
		got, err := ParseCatalog(body)
		if err != nil {
			t.Fatal(err)
		}
		if rate := got["deepseek-chat"]; rate.Input != 1 || rate.Output != 2 {
			t.Fatalf("bare model used a reseller price: %+v", rate)
		}
		if rate := got["deepseek/deepseek-chat"]; rate.Input != 1 {
			t.Fatalf("maker-qualified price: %+v", rate)
		}
		if rate := got["relay/deepseek-chat"]; rate.Input != 99 {
			t.Fatalf("reseller-qualified price: %+v", rate)
		}
	}
}

func TestParseCatalogDoesNotPriceABareModelFromAReseller(t *testing.T) {
	got, err := ParseCatalog([]byte(`{
		"alpha": {"models": {"m": {"cost": {"input": 1, "output": 2}}}},
		"zeta": {"models": {"m": {"cost": {"input": 9, "output": 9}}}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["m"]; ok {
		t.Fatalf("bare reseller price: %+v", got["m"])
	}
	if got["alpha/m"].Input != 1 || got["zeta/m"].Input != 9 {
		t.Fatalf("provider-qualified prices: %+v", got)
	}
}

func TestParseCatalogBareMakerTieIsDeterministic(t *testing.T) {
	body := []byte(`{
		"openai": {"models": {"shared": {"cost": {"input": 2, "output": 2}}}},
		"anthropic": {"models": {"shared": {"cost": {"input": 1, "output": 1}}}}
	}`)
	for i := 0; i < 100; i++ {
		got, err := ParseCatalog(body)
		if err != nil {
			t.Fatal(err)
		}
		if rate := got["shared"]; rate.Input != 1 {
			t.Fatalf("tie chose %+v", rate)
		}
	}
}

func TestEngineBareCatalogFallbackUsesMakerPrice(t *testing.T) {
	rates, err := ParseCatalog([]byte(`{
		"deepseek": {"models": {"deepseek-chat": {"cost": {"input": 1, "output": 2}}}},
		"relay": {"models": {"deepseek-chat": {"cost": {"input": 99, "output": 99}}}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	engine := Engine{Catalog: Fixed(rates)}
	cost, ok := engine.Cost(context.Background(), state.AttemptRecord{
		ProviderID: "custom", Model: "deepseek-chat", In: 1_000_000,
	})
	if !ok || cost != 1 {
		t.Fatalf("cost %v ok %v", cost, ok)
	}
}

func TestCostUsesOneHourCachePriceAndClamps(t *testing.T) {
	cacheWrite, oneHour := 2.0, 6.0
	rate := Rate{
		Input: 2, Output: 8,
		CacheWrite: cacheWrite, CacheWriteSet: true,
		CacheWrite1H: oneHour, CacheWrite1HSet: true,
	}
	got := rate.Cost(state.UsageCounts{CacheWrite: 1_000_000, CacheWrite1H: 1_500_000})
	if math.Abs(got-6) > 1e-9 {
		t.Fatalf("1h cache above cache write: %v", got)
	}
	got = rate.Cost(state.UsageCounts{CacheWrite: 1_000_000, CacheWrite1H: 250_000})
	// 0.25 * 6 + 0.75 * 2 = 3
	if math.Abs(got-3) > 1e-9 {
		t.Fatalf("1h split: %v", got)
	}
	mixed := Rate{Input: 2, CacheWrite: 3, CacheWriteSet: true}
	got = mixed.Cost(state.UsageCounts{CacheWrite: 1_000_000, CacheWrite1H: 1_000_000})
	if math.Abs(got-3) > 1e-9 {
		t.Fatalf("missing 1h price falls back to the cache write price: %v", got)
	}
}

func TestEngineUserPriceZeroWinsOverCatalog(t *testing.T) {
	zero := 0.0
	engine := Engine{
		Catalog: Fixed{"demo": {Input: 2, Output: 8}},
		Config: state.Config{Prices: []state.PriceRule{{
			Provider: "p1", Model: "demo", Input: &zero, Output: &zero,
		}}},
	}
	rec := state.AttemptRecord{ProviderID: "p1", Model: "demo", In: 1_000_000, Out: 1_000_000}
	cost, ok := engine.Cost(context.Background(), rec)
	if !ok || cost != 0 {
		t.Fatalf("zero user price: cost %v ok %v", cost, ok)
	}
}

func TestEnginePeakUsesTheRecordTime(t *testing.T) {
	engine := Engine{
		Catalog: Fixed{"demo": {Input: 1, Output: 1}},
		Config: state.Config{Peaks: []state.PeakWindow{{
			Provider: "p1", Model: "demo", Start: "08:00", End: "10:00", Factor: 2,
		}}},
		Location: time.UTC,
	}
	peak := state.AttemptRecord{
		ProviderID: "p1", Model: "demo", In: 1_000_000,
		Started: time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC),
	}
	off := peak
	off.Started = time.Date(2026, 1, 2, 11, 0, 0, 0, time.UTC)
	cost, ok := engine.Cost(context.Background(), peak)
	if !ok || math.Abs(cost-2) > 1e-9 {
		t.Fatalf("peak cost %v ok %v", cost, ok)
	}
	cost, ok = engine.Cost(context.Background(), off)
	if !ok || math.Abs(cost-1) > 1e-9 {
		t.Fatalf("off-peak cost %v ok %v", cost, ok)
	}
}

func TestEngineMultipliersApplyToCatalogOnly(t *testing.T) {
	providerMultiplier, keyMultiplier := 0.5, 3.0
	key := "sk-test"
	cfg := state.Config{Providers: []state.Provider{{
		ID: "p1", Name: "Provider", BaseURL: "https://p1.example/v1",
		Credentials: []string{key}, Multiplier: &providerMultiplier,
		KeyMultipliers: []float64{keyMultiplier},
	}}}
	engine := Engine{Catalog: Fixed{"demo": {Input: 2, Output: 8}}, Config: cfg}
	rec := state.AttemptRecord{
		ProviderID: "p1", Model: "demo", ProviderKeyID: state.KeyFingerprint(key),
		In: 1_000_000,
	}
	cost, ok := engine.Cost(context.Background(), rec)
	if !ok || math.Abs(cost-6) > 1e-9 {
		t.Fatalf("key multiplier cost %v ok %v", cost, ok)
	}
	rec.ProviderKeyID = ""
	cost, ok = engine.Cost(context.Background(), rec)
	if !ok || math.Abs(cost-1) > 1e-9 {
		t.Fatalf("provider multiplier cost %v ok %v", cost, ok)
	}
	one := 1.0
	engine.Config.Prices = []state.PriceRule{{Provider: "p1", Model: "demo", Input: &one}}
	cost, ok = engine.Cost(context.Background(), rec)
	if !ok || math.Abs(cost-1) > 1e-9 {
		t.Fatalf("user price keeps its own rate %v ok %v", cost, ok)
	}
}

func TestEngineMatchesGlobalAndProviderWildcardPrices(t *testing.T) {
	one := 1.0
	for _, provider := range []string{"", "*"} {
		engine := Engine{
			Catalog: Fixed{"demo": {Input: 2, Output: 8}},
			Config:  state.Config{Prices: []state.PriceRule{{Provider: provider, Model: "demo", Input: &one}}},
		}
		rec := state.AttemptRecord{ProviderID: "p1", Model: "demo", In: 1_000_000}
		cost, ok := engine.Cost(context.Background(), rec)
		if !ok || math.Abs(cost-1) > 1e-9 {
			t.Fatalf("provider %q cost %v ok %v", provider, cost, ok)
		}
	}
	zero := 0.0
	engine := Engine{Config: state.Config{Prices: []state.PriceRule{{Model: "*", Output: &zero}}}}
	cost, ok := engine.Cost(context.Background(), state.AttemptRecord{Model: "unknown", In: 2_000_000})
	if !ok || cost != 0 {
		t.Fatalf("wildcard model cost %v ok %v", cost, ok)
	}
}

func TestEngineMultipliesOnlyTheCatalogFieldsOfAPartialUserPrice(t *testing.T) {
	half, input := 0.5, 3.0
	engine := Engine{
		Catalog: Fixed{"demo": {Input: 2, Output: 8}},
		Config: state.Config{
			Providers: []state.Provider{{ID: "p1", Multiplier: &half}},
			Prices:    []state.PriceRule{{Provider: "p1", Model: "demo", Input: &input}},
		},
	}
	rec := state.AttemptRecord{ProviderID: "p1", Model: "demo", In: 1_000_000, Out: 1_000_000}
	cost, ok := engine.Cost(context.Background(), rec)
	// Input is the user's 3; output is the catalog's 8 * 0.5.
	if !ok || math.Abs(cost-7) > 1e-9 {
		t.Fatalf("partial user price cost %v ok %v", cost, ok)
	}
}

func TestEnginePrefersTheProviderCatalogKey(t *testing.T) {
	engine := Engine{Catalog: Fixed{
		"p1/demo": {Input: 5, Output: 5},
		"demo":    {Input: 1, Output: 1},
	}}
	rec := state.AttemptRecord{ProviderID: "p1", Model: "demo", In: 1_000_000}
	cost, ok := engine.Cost(context.Background(), rec)
	if !ok || cost != 5 {
		t.Fatalf("provider key cost %v ok %v", cost, ok)
	}
}

func TestEngineExternalCostWins(t *testing.T) {
	cost := 1.25
	engine := Engine{}
	got, ok := engine.Cost(context.Background(), state.AttemptRecord{
		Model: "demo", In: 1_000_000, Cost: &cost,
	})
	if !ok || got != cost {
		t.Fatalf("reported cost %v ok %v", got, ok)
	}
}

func TestEngineDoesNotChargeCacheWithoutACachePrice(t *testing.T) {
	engine := Engine{Catalog: Fixed{"demo": {Input: 2, Output: 8}}}
	rec := state.AttemptRecord{
		ProviderID: "p1", Model: "demo", In: 1_000_000, CacheRead: 2_000_000,
	}
	cost, ok := engine.Cost(context.Background(), rec)
	if !ok || cost != 2 {
		t.Fatalf("cache without a cache price cost %v ok %v", cost, ok)
	}
}

func TestEngineDoesNotPriceOrCountCacheOnlyRows(t *testing.T) {
	engine := Engine{Catalog: Fixed{"demo": {Input: 2, Output: 8}}}
	if cost, ok := engine.Cost(context.Background(), state.AttemptRecord{Model: "demo", CacheRead: 1_000_000}); ok || cost != 0 {
		t.Fatalf("cache-only row cost %v ok %v", cost, ok)
	}
	if cost, ok := engine.Cost(context.Background(), state.AttemptRecord{Model: "missing", In: 1_000_000}); ok || cost != 0 {
		t.Fatalf("unpriced row cost %v ok %v", cost, ok)
	}
}
