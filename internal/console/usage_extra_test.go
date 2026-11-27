package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/state"
)

func usageServer(t *testing.T, prices price.Pricer) (*state.Store, *httptest.Server, string) {
	t.Helper()
	store := loadTemp(t)
	token := withSession(t, store)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}, Prices: prices})
	t.Cleanup(srv.Close)
	return store, srv, token
}

func appendRecord(t *testing.T, store *state.Store, rec state.AttemptRecord) {
	t.Helper()
	if rec.Finished.IsZero() {
		rec.Finished = rec.Started.Add(10 * time.Millisecond)
	}
	if err := store.AppendUsage(rec); err != nil {
		t.Fatal(err)
	}
}

func decodeUsage(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}

func recordsOf(t *testing.T, body map[string]any) []any {
	t.Helper()
	list, _ := body["records"].([]any)
	return list
}

func recordField(t *testing.T, body map[string]any, index int, field string) any {
	t.Helper()
	list := recordsOf(t, body)
	if index >= len(list) {
		t.Fatalf("record %d missing in %v", index, body)
	}
	row, _ := list[index].(map[string]any)
	return row[field]
}

func summaryNumber(t *testing.T, body map[string]any, field string) float64 {
	t.Helper()
	summary, _ := body["summary"].(map[string]any)
	value, ok := summary[field].(float64)
	if !ok {
		t.Fatalf("summary %s missing in %v", field, body["summary"])
	}
	return value
}

func TestUsagePaginationFiltersAndWholeWindowSummary(t *testing.T) {
	store, srv, token := usageServer(t, nil)
	base := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "r1", ProviderID: "p1", Outcome: "success", Agent: "a1", Model: "m1",
		CallerKeyID: "k1", Started: base,
		Usage: json.RawMessage(`{"prompt_tokens":10,"completion_tokens":2}`),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "r2", ProviderID: "p1", Outcome: "success", Agent: "a2", Model: "m2",
		CallerKeyID: "k2", Started: base.Add(time.Minute),
		Usage: json.RawMessage(`{"prompt_tokens":20,"completion_tokens":4}`),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "r3", ProviderID: "p1", Outcome: "success", Agent: "a1", Model: "m1",
		CallerKeyID: "k1", Source: state.SourceReported, Started: base.Add(2 * time.Minute),
		Usage: json.RawMessage(`{"prompt_tokens":30,"completion_tokens":6}`),
	})

	body := decodeUsage(t, mustGET(t, srv.URL+"/usage?offset=0&limit=2", token))
	if body["status"] != "ok" || body["total"].(float64) != 3 || len(recordsOf(t, body)) != 2 {
		t.Fatalf("page one %v", body)
	}
	if got := recordField(t, body, 0, "routeId"); got != "r3" {
		t.Fatalf("newest first %v", got)
	}
	if got := recordField(t, body, 1, "routeId"); got != "r2" {
		t.Fatalf("second row %v", got)
	}
	if got := summaryNumber(t, body, "calls"); got != 3 {
		t.Fatalf("summary covers the whole window, got calls %v", got)
	}

	body = decodeUsage(t, mustGET(t, srv.URL+"/usage?offset=2&limit=2", token))
	if len(recordsOf(t, body)) != 1 || recordField(t, body, 0, "routeId") != "r1" {
		t.Fatalf("page two %v", body)
	}
	body = decodeUsage(t, mustGET(t, srv.URL+"/usage?offset=20&limit=2", token))
	if list, ok := body["records"].([]any); !ok || len(list) != 0 || body["total"].(float64) != 3 {
		t.Fatalf("past the end %v", body)
	}

	cases := []struct {
		query string
		want  int
		first string
	}{
		{"agent=a1", 2, "r3"},
		{"model=m2", 1, "r2"},
		{"callerKeyId=k1", 2, "r3"},
		{"tokenId=k2", 1, "r2"},
		{"source=reported", 1, "r3"},
	}
	for _, tc := range cases {
		body = decodeUsage(t, mustGET(t, srv.URL+"/usage?"+tc.query, token))
		if body["total"].(float64) != float64(tc.want) || recordField(t, body, 0, "routeId") != tc.first {
			t.Fatalf("%s -> %v", tc.query, body)
		}
	}
	if res, _ := do(t, http.MethodGet, srv.URL+"/usage?offset=-1", token, nil); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative offset %d", res.StatusCode)
	}
}

func TestUsageResponseCarriesTimezone(t *testing.T) {
	_, srv, token := usageServer(t, nil)
	body := decodeUsage(t, mustGET(t, srv.URL+"/usage", token))
	if tz, _ := body["timezone"].(string); strings.TrimSpace(tz) == "" {
		t.Fatalf("timezone missing %v", body)
	}
}

func TestUsageSummaryCountsOnceAndExcludesProbes(t *testing.T) {
	store, srv, token := usageServer(t, nil)
	now := time.Now().UTC()
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "retry", ProviderID: "p1", Outcome: "upstream_unavailable", Status: 503,
		Started: now.Add(-time.Minute), HandedOn: true,
		Usage: json.RawMessage(`{"prompt_tokens":100,"completion_tokens":10}`),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "retry", ProviderID: "p2", Outcome: "success", Status: 200,
		Started: now.Add(-30 * time.Second), Tries: 2,
		Usage: json.RawMessage(`{"prompt_tokens":7,"completion_tokens":3}`),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "probe", ProviderID: "p1", Outcome: "success", Status: 200,
		Source: state.SourceProbe, Started: now.Add(-20 * time.Second),
		Usage: json.RawMessage(`{"prompt_tokens":999,"completion_tokens":999}`),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "cancel", ProviderID: "p1", Outcome: "canceled", Status: 499,
		Err: state.CanceledText, Started: now.Add(-10 * time.Second),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "fail", ProviderID: "p1", Outcome: "auth_failure", Status: 401,
		Started: now.Add(-5 * time.Second),
	})

	body := decodeUsage(t, mustGET(t, srv.URL+"/usage?period=all", token))
	if got := summaryNumber(t, body, "calls"); got != 3 {
		t.Fatalf("calls %v in %v", got, body)
	}
	if got := summaryNumber(t, body, "failed"); got != 1 {
		t.Fatalf("failed %v", got)
	}
	if got := summaryNumber(t, body, "canceled"); got != 1 {
		t.Fatalf("canceled %v", got)
	}
	if got := summaryNumber(t, body, "retried"); got != 1 {
		t.Fatalf("retried %v", got)
	}
	if got := summaryNumber(t, body, "in"); got != 107 {
		t.Fatalf("in %v", got)
	}
	if got := summaryNumber(t, body, "out"); got != 13 {
		t.Fatalf("out %v", got)
	}
}

func TestUsageHeatmapUsesFiltersAndFiftyThreeWeeks(t *testing.T) {
	store, srv, token := usageServer(t, nil)
	local := time.Now().In(time.Local)
	today := time.Date(local.Year(), local.Month(), local.Day(), 12, 0, 0, 0, time.Local)
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "today", ProviderID: "p1", Outcome: "success", Agent: "a1", Started: today,
		Usage: json.RawMessage(`{"prompt_tokens":10,"completion_tokens":1}`),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "handed", ProviderID: "p1", Outcome: "upstream_unavailable", HandedOn: true,
		Agent: "a1", Started: today.Add(-time.Hour),
		Usage: json.RawMessage(`{"prompt_tokens":20,"completion_tokens":2}`),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "probe", ProviderID: "p1", Outcome: "success", Agent: "a1",
		Source: state.SourceProbe, Started: today,
		Usage: json.RawMessage(`{"prompt_tokens":500,"completion_tokens":500}`),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "outside", ProviderID: "p1", Outcome: "success", Agent: "a1",
		Started: today.AddDate(0, 0, -400),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "other-agent", ProviderID: "p1", Outcome: "success", Agent: "a2", Started: today,
	})

	body := decodeUsage(t, mustGET(t, srv.URL+"/usage/heatmap?agent=a1", token))
	days, _ := body["days"].([]any)
	if len(days) != 371 {
		t.Fatalf("days %d", len(days))
	}
	key := today.Format("2006-01-02")
	var todayCell map[string]any
	total := 0.0
	for _, raw := range days {
		cell, _ := raw.(map[string]any)
		total += cell["calls"].(float64)
		if cell["date"] == key {
			todayCell = cell
		}
	}
	if total != 1 || todayCell == nil || todayCell["calls"].(float64) != 1 {
		t.Fatalf("heatmap total %v today %v", total, todayCell)
	}
	if todayCell["in"].(float64) != 30 || todayCell["out"].(float64) != 3 {
		t.Fatalf("handed-on tokens missing from heatmap %v", todayCell)
	}
	body = decodeUsage(t, mustGET(t, srv.URL+"/usage/heatmap?agent=nobody", token))
	for _, raw := range body["days"].([]any) {
		if calls := raw.(map[string]any)["calls"].(float64); calls != 0 {
			t.Fatalf("unexpected filtered call %v", calls)
		}
	}
}

func TestUsageCSVUsesTheFilteredPageAndReadTimePrice(t *testing.T) {
	store, srv, token := usageServer(t, price.Fixed{"demo": {Input: 2, Output: 8}})
	base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "old", ProviderID: "p1", ProviderKeyID: "pk1", Outcome: "success",
		Agent: "a1", Model: "demo", CallerKeyID: "k1", Session: "s1", Started: base,
		TTFTMS: 120, BytesIn: 100, BytesOut: 200,
		Usage: json.RawMessage(`{"prompt_tokens":1000000,"completion_tokens":500000}`),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "new", ProviderID: "p1", Outcome: "success", Agent: "a2",
		Model: "unknown", CallerKeyID: "k2", Started: base.Add(time.Minute),
		Usage: json.RawMessage(`{"prompt_tokens":3,"completion_tokens":1}`),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "hidden", ProviderID: "p1", Outcome: "success", Model: "demo",
		Started: base.Add(2 * time.Minute), Usage: json.RawMessage(`{"prompt_tokens":100,"completion_tokens":1}`),
	})

	res, body := do(t, http.MethodGet, srv.URL+"/usage/requests.csv?agent=a1&limit=10", token, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("csv %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 2 {
		t.Fatalf("csv lines %d %s", len(lines), body)
	}
	header := lines[0]
	for _, column := range []string{"providerKeyId", "callerKeyId", "session", "ttft_ms", "bytes_in", "cost"} {
		if !strings.Contains(header, column) {
			t.Fatalf("csv header missing %s: %s", column, header)
		}
	}
	if !strings.Contains(lines[1], "old") || !strings.Contains(lines[1], "6.000000") || !strings.Contains(lines[1], "pk1") {
		t.Fatalf("priced row %s", lines[1])
	}

	if err := store.Update(func(cfg *state.Config) error {
		cfg.UsageHidden = []state.UsageHide{{
			ProviderID: "p1", Model: "demo", Before: base.Add(10 * time.Minute),
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, body = do(t, http.MethodGet, srv.URL+"/usage/requests.csv?limit=10", token, nil)
	lines = strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 2 || strings.Contains(body, "hidden") {
		t.Fatalf("hidden row %s", body)
	}
	columns := strings.Split(lines[0], ",")
	unpricedAt := -1
	for i, column := range columns {
		if column == "unpriced" {
			unpricedAt = i
		}
	}
	row := strings.Split(lines[1], ",")
	if unpricedAt < 0 || unpricedAt >= len(row) || row[unpricedAt] != "1" {
		t.Fatalf("unpriced marker %s", lines[1])
	}
}

func TestUsageClearHidesPageRowsButKeepsThemInTheStore(t *testing.T) {
	store, srv, token := usageServer(t, nil)
	now := time.Now().UTC()
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "old", ProviderID: "p1", Outcome: "success", Model: "m",
		Started: now.Add(-2 * time.Hour), Usage: json.RawMessage(`{"prompt_tokens":10,"completion_tokens":1}`),
	})
	appendRecord(t, store, state.AttemptRecord{
		RouteID: "new", ProviderID: "p1", Outcome: "success", Model: "m",
		Started: now.Add(-time.Minute), Usage: json.RawMessage(`{"prompt_tokens":20,"completion_tokens":2}`),
	})
	before := now.Add(-time.Hour).Format(time.RFC3339)
	res, body := do(t, http.MethodPost, srv.URL+"/usage/clear", token, []byte(`{"providerId":"p1","model":"m","before":"`+before+`"}`))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("clear %d %s", res.StatusCode, body)
	}
	usage := decodeUsage(t, mustGET(t, srv.URL+"/usage?period=all", token))
	if usage["total"].(float64) != 1 || recordField(t, usage, 0, "routeId") != "new" {
		t.Fatalf("page after clear %v", usage)
	}
	all, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("store changed %d %v", len(all), err)
	}
	cfg := store.Current().Config
	if !cfg.HiddenOnUsagePage(all[0]) || cfg.HiddenOnUsagePage(all[1]) {
		t.Fatalf("hide rule %+v rows %+v", cfg.UsageHidden, all)
	}
	if all[0].InLimit() != true || all[1].InLimit() != true {
		t.Fatal("hidden rows no longer count toward limits")
	}
	if res, _ := do(t, http.MethodPost, srv.URL+"/usage/clear", token, []byte(`{"providerId":"p1"}`)); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("incomplete clear %d", res.StatusCode)
	}
}

func TestPricingRoundTripAndValidation(t *testing.T) {
	_, srv, token := usageServer(t, nil)
	empty := decodeUsage(t, mustGET(t, srv.URL+"/pricing", token))
	if len(empty["prices"].([]any)) != 0 || len(empty["peaks"].([]any)) != 0 {
		t.Fatalf("initial pricing %v", empty)
	}
	payload := `{"prices":[{"provider":"p1","model":"*","input":0,"output":2.5,"cacheRead":0.1,"cacheWrite":0.2,"cacheWrite1h":0.3}],"peaks":[{"provider":"p1","model":"m","start":"09:00","end":"18:00","factor":1.5}]}`
	res, body := do(t, http.MethodPut, srv.URL+"/pricing", token, []byte(payload))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"cacheWrite1h":0.3`) || !strings.Contains(body, `"factor":1.5`) {
		t.Fatalf("put pricing %d %s", res.StatusCode, body)
	}
	got := decodeUsage(t, mustGET(t, srv.URL+"/pricing", token))
	rule := got["prices"].([]any)[0].(map[string]any)
	if rule["input"].(float64) != 0 || rule["model"] != "*" {
		t.Fatalf("zero price lost %v", got)
	}
	if res, body := do(t, http.MethodPut, srv.URL+"/pricing", token, []byte(`{"prices":[{"model":"m","input":-1}]}`)); res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "price") {
		t.Fatalf("negative price %d %s", res.StatusCode, body)
	}
	if res, _ := do(t, http.MethodPut, srv.URL+"/pricing", token, []byte(`{"prices":[{"model":"m"}]}`)); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty price %d", res.StatusCode)
	}
}

func TestTokenLimitsAreFiveAndRoundTrip(t *testing.T) {
	_, srv, token := usageServer(t, nil)
	limits := []map[string]any{
		{"usd": 1.5, "window": "today"},
		{"tokens": 1000, "window": "week"},
		{"usd": 2, "tokens": 2000, "window": "month"},
		{"usd": 3, "window": "hours", "hours": 4},
		{"tokens": 3000, "window": "days", "days": 7, "provider": "p1", "model": "m"},
	}
	created := postJSON(t, srv.URL+"/tokens", token, map[string]any{"limits": limits})
	if got, _ := created["limits"].([]any); len(got) != 5 {
		t.Fatalf("created limits %#v", created)
	}
	id := created["id"].(string)
	listed := mustGET(t, srv.URL+"/tokens", token)
	if !strings.Contains(listed, `"tokens":3000`) || !strings.Contains(listed, `"hours":4`) {
		t.Fatalf("listed %s", listed)
	}
	tooMany := append(limits, map[string]any{"usd": 4, "window": "all"})
	res, body := do(t, http.MethodPut, srv.URL+"/tokens/"+id, token, mustJSON(t, map[string]any{"limits": tooMany}))
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "limit") {
		t.Fatalf("six limits %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPut, srv.URL+"/tokens/"+id, token, mustJSON(t, map[string]any{"limits": limits[:2]}))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"usd":1.5`) || strings.Contains(body, `"tokens":3000`) {
		t.Fatalf("replace limits %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPut, srv.URL+"/tokens/"+id, token, []byte(`{"limits":[]}`))
	if res.StatusCode != http.StatusOK || strings.Contains(body, `"limits"`) {
		t.Fatalf("clear limits %d %s", res.StatusCode, body)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestUsageTrafficShapeAndLiveHook(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	plain := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer plain.Close()
	body := decodeUsage(t, mustGET(t, plain.URL+"/usage/traffic", token))
	if body["status"] != "ok" || body["live"] != false || body["activeRequests"].(float64) != 0 {
		t.Fatalf("empty traffic %v", body)
	}
	if _, ok := body["at"].(string); !ok {
		t.Fatalf("missing sample time %v", body)
	}
	live := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}, Traffic: func() TrafficSnapshot {
		return TrafficSnapshot{ActiveRequests: 3, BytesIn: 1000, BytesOut: 2000, At: time.Unix(10, 0).UTC()}
	}})
	defer live.Close()
	body = decodeUsage(t, mustGET(t, live.URL+"/usage/traffic", token))
	if body["live"] != true || body["activeRequests"].(float64) != 3 || body["bytesIn"].(float64) != 1000 || body["bytesOut"].(float64) != 2000 {
		t.Fatalf("live traffic %v", body)
	}
}

func TestUsageContentReadsArchivedTurnsByTime(t *testing.T) {
	store, srv, token := usageServer(t, nil)
	base := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	for _, turn := range []state.TurnRecord{
		{Session: "s1", At: base, Model: "m", Caller: "a1", Input: "first in", Output: "first out"},
		{Session: "s1", At: base.Add(10 * time.Minute), Model: "m", Caller: "a1", Input: "second in", Output: "second out"},
		{Session: "s2", At: base, Model: "m", Caller: "a1", Output: "other session"},
	} {
		if err := store.AppendTurn(turn); err != nil {
			t.Fatal(err)
		}
	}
	body := decodeUsage(t, mustGET(t, srv.URL+"/usage/requests/content?agent=a1&session=s1", token))
	turns, _ := body["turns"].([]any)
	if body["status"] != "ok" || body["agent"] != "a1" || body["session"] != "s1" || len(turns) != 2 {
		t.Fatalf("content %v", body)
	}
	from := base.Add(time.Minute).Format(time.RFC3339)
	body = decodeUsage(t, mustGET(t, srv.URL+"/usage/requests/content?session=s1&from="+from, token))
	if turns, _ := body["turns"].([]any); len(turns) != 1 || turns[0].(map[string]any)["input"] != "second in" {
		t.Fatalf("from filter %v", body)
	}
	to := base.Add(time.Minute).Format(time.RFC3339)
	body = decodeUsage(t, mustGET(t, srv.URL+"/usage/requests/content?session=s1&to="+to, token))
	if turns, _ := body["turns"].([]any); len(turns) != 1 || turns[0].(map[string]any)["input"] != "first in" {
		t.Fatalf("to filter %v", body)
	}
	body = decodeUsage(t, mustGET(t, srv.URL+"/usage/requests/content?session=missing", token))
	if body["status"] != "ok" || len(body["turns"].([]any)) != 0 {
		t.Fatalf("missing session %v", body)
	}
	if res, _ := do(t, http.MethodGet, srv.URL+"/usage/requests/content", token, nil); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("session required %d", res.StatusCode)
	}
	if res, _ := do(t, http.MethodGet, srv.URL+"/usage/requests/content?session=s1&from=nope", token, nil); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad from %d", res.StatusCode)
	}
}

func TestProviderKeyMultipliersRoundTrip(t *testing.T) {
	_, srv, token := usageServer(t, nil)
	created := postJSON(t, srv.URL+"/providers", token, map[string]any{
		"name": "relay", "protocol": "chat", "baseUrl": "https://example.com/v1",
		"models": []string{"m"}, "credentials": []string{"k1", "k2"},
		"multiplier": 0.5, "keyMultipliers": []float64{1, 2},
	})
	if created["multiplier"] != 0.5 {
		t.Fatalf("provider multiplier %#v", created)
	}
	keys, _ := created["keyMultipliers"].([]any)
	if len(keys) != 2 || keys[1] != 2.0 {
		t.Fatalf("key multipliers %#v", created)
	}
	id := created["id"].(string)
	res, body := do(t, http.MethodPut, srv.URL+"/providers/"+id, token, mustJSON(t, map[string]any{
		"name": "relay", "protocol": "chat", "baseUrl": "https://example.com/v1",
		"models": []string{"m"}, "keyMultipliers": []float64{3, 4},
	}))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"keyMultipliers":[3,4]`) {
		t.Fatalf("update key multipliers %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPut, srv.URL+"/providers/"+id, token, mustJSON(t, map[string]any{
		"name": "relay", "protocol": "chat", "baseUrl": "https://example.com/v1",
		"models": []string{"m"}, "keyMultipliers": []float64{3},
	}))
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "multiplier") {
		t.Fatalf("mismatched key multipliers %d %s", res.StatusCode, body)
	}
}

func TestProviderKeyMultipliersFollowKeyChanges(t *testing.T) {
	_, srv, token := usageServer(t, nil)
	created := postJSON(t, srv.URL+"/providers", token, map[string]any{
		"name": "relay", "protocol": "chat", "baseUrl": "https://example.com/v1",
		"models": []string{"m"}, "credentials": []string{"k1", "k2"},
		"multiplier": 0.5, "keyMultipliers": []float64{1, 2},
	})
	id := created["id"].(string)
	check := func(row map[string]any, want ...float64) {
		t.Helper()
		got, _ := row["keyMultipliers"].([]any)
		if len(got) != len(want) {
			t.Fatalf("key multipliers %v, want %v", row["keyMultipliers"], want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("key multipliers %v, want %v", row["keyMultipliers"], want)
			}
		}
	}
	put := func(extra map[string]any) map[string]any {
		t.Helper()
		body := map[string]any{
			"name": "relay", "protocol": "chat", "baseUrl": "https://example.com/v1",
			"models": []string{"m"},
		}
		for key, value := range extra {
			body[key] = value
		}
		res, text := do(t, http.MethodPut, srv.URL+"/providers/"+id, token, mustJSON(t, body))
		if res.StatusCode != http.StatusOK {
			t.Fatalf("put %d %s", res.StatusCode, text)
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(text), &row); err != nil {
			t.Fatal(err)
		}
		return row
	}

	row := put(map[string]any{"addCredentials": []string{"k3"}})
	if row["keyCount"] != 3.0 {
		t.Fatalf("key count after add %v", row)
	}
	check(row, 1, 2, 0.5)

	res, text := do(t, http.MethodDelete, srv.URL+"/providers/"+id+"/keys/0", token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete key %d %s", res.StatusCode, text)
	}
	var afterDelete map[string]any
	if err := json.Unmarshal([]byte(text), &afterDelete); err != nil {
		t.Fatal(err)
	}
	if afterDelete["keyCount"] != 2.0 {
		t.Fatalf("key count after delete %v", afterDelete)
	}
	check(afterDelete, 2, 0.5)

	row = put(map[string]any{"credentials": []string{"k9"}})
	if row["keyCount"] != 1.0 {
		t.Fatalf("key count after replace %v", row)
	}
	check(row, 2)

	row = put(map[string]any{"keyMultipliers": []float64{}})
	check(row)

	row = put(map[string]any{"addCredentials": []string{"k10"}})
	if row["keyCount"] != 2.0 {
		t.Fatalf("key count after second add %v", row)
	}
	check(row)
}

func TestProviderBalanceFieldsRoundTrip(t *testing.T) {
	_, srv, token := usageServer(t, nil)
	created := postJSON(t, srv.URL+"/providers", token, map[string]any{
		"name": "relay", "protocol": "chat", "baseUrl": "https://example.com/v1",
		"models": []string{"m"}, "credentials": []string{"k1"},
		"balanceUrl": "https://example.com/balance", "balanceField": "data.remaining",
		"balanceManual": "12.50", "balanceCurrency": "USD",
	})
	for key, want := range map[string]string{
		"balanceUrl": "https://example.com/balance", "balanceField": "data.remaining",
		"balanceManual": "12.50", "balanceCurrency": "USD",
	} {
		if created[key] != want {
			t.Fatalf("%s = %#v in %#v", key, created[key], created)
		}
	}
	id := created["id"].(string)
	res, body := do(t, http.MethodPut, srv.URL+"/providers/"+id, token, mustJSON(t, map[string]any{
		"name": "relay", "protocol": "chat", "baseUrl": "https://example.com/v1",
		"models": []string{"m"}, "balanceManual": "7", "balanceCurrency": "CNY",
		"balanceUrl": "", "balanceField": "",
	}))
	if res.StatusCode != http.StatusOK || strings.Contains(body, "balanceUrl") || strings.Contains(body, "balanceField") {
		t.Fatalf("clear balance endpoint %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, `"balanceManual":"7"`) || !strings.Contains(body, `"balanceCurrency":"CNY"`) {
		t.Fatalf("manual balance %s", body)
	}
	res, body = do(t, http.MethodPut, srv.URL+"/providers/"+id, token, mustJSON(t, map[string]any{
		"name": "relay", "protocol": "chat", "baseUrl": "https://example.com/v1",
		"models": []string{"m"}, "balanceUrl": "ftp://example.com/balance",
	}))
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "balance") {
		t.Fatalf("bad balance url %d %s", res.StatusCode, body)
	}
}

func TestUsageCSVRefusesAnUnreadableLedger(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := writeFile(file, []byte("x")); err != nil {
		t.Fatal(err)
	}
	store := state.Open(state.Paths{ConfigFile: filepath.Join(dir, "gate.json"), UsageDir: file})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	token := withSession(t, store)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	res, body := do(t, http.MethodGet, srv.URL+"/usage/requests.csv", token, nil)
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "usage_unknown") {
		t.Fatalf("csv unknown %d %s", res.StatusCode, body)
	}
}

func TestUsageConsolePathsAreReserved(t *testing.T) {
	for _, path := range []string{
		"/pricing", "/usage", "/usage/heatmap", "/usage/requests.csv",
		"/usage/traffic", "/usage/requests/content", "/usage/clear",
	} {
		if !IsConsolePath(path) {
			t.Fatalf("%s is not reserved for the console", path)
		}
	}
}
