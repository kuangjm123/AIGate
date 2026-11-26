package price

import (
	"encoding/json"
	"math"
	"testing"

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
