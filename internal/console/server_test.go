package console

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/agent"
	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

func TestConsoleCRUDAndSecrets(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	const credential = "super-secret-value"
	var buf bytes.Buffer
	srv := &Server{Store: store, Host: memHost{store: store}, Log: log.New(&buf, "", 0)}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	res, body := do(t, http.MethodPost, ts.URL+"/providers", "", []byte(`{"protocol":"openai","baseUrl":"http://127.0.0.1:11434/v1","models":["m"],"credential":"x"}`))
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("open providers %d %s", res.StatusCode, body)
	}
	provider := postJSON(t, ts.URL+"/providers", token, map[string]any{
		"name":       "local",
		"protocol":   "openai",
		"baseUrl":    "http://127.0.0.1:11434/v1",
		"models":     []string{"m"},
		"credential": credential,
	})
	if provider["credentialSet"] != true {
		t.Fatalf("provider %#v", provider)
	}
	if strings.Contains(mustGET(t, ts.URL+"/providers", token), credential) {
		t.Fatal("provider read returned the credential")
	}
	if strings.Contains(buf.String(), credential) {
		t.Fatalf("log leaked credential: %s", buf.String())
	}

	route := postJSON(t, ts.URL+"/routes", token, map[string]any{
		"modelMatch":  "m",
		"providerIds": []string{provider["id"].(string)},
	})
	res, body = do(t, http.MethodDelete, ts.URL+"/providers/"+provider["id"].(string), token, nil)
	if res.StatusCode != http.StatusConflict || !strings.Contains(body, "in_use") {
		t.Fatalf("delete provider: %d %s", res.StatusCode, body)
	}
	res, _ = do(t, http.MethodDelete, ts.URL+"/routes/"+route["id"].(string), token, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete route %d", res.StatusCode)
	}
	res, _ = do(t, http.MethodDelete, ts.URL+"/providers/"+provider["id"].(string), token, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete provider after route %d", res.StatusCode)
	}

	created := postJSON(t, ts.URL+"/tokens", token, nil)
	secret, _ := created["secret"].(string)
	if secret == "" || strings.Contains(secret, "••••") {
		t.Fatalf("create token %#v", created)
	}
	listed := mustGET(t, ts.URL+"/tokens", token)
	if strings.Contains(listed, secret) {
		t.Fatal("token list returned the secret")
	}
	if !strings.Contains(listed, "••••") {
		t.Fatalf("token list %s", listed)
	}

	consoleOrigin := postStatus(t, ts.URL+"/providers", token, map[string]any{
		"name":       "loop",
		"protocol":   "openai",
		"baseUrl":    "http://127.0.0.1:8081/v1",
		"models":     []string{},
		"credential": "k",
	})
	if consoleOrigin != http.StatusBadRequest {
		t.Fatalf("console origin status %d", consoleOrigin)
	}
}

func TestExplicitEmptyEligible(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Providers = []state.Provider{{ID: "p", Protocol: "p", BaseURL: "https://example.com/v1", Models: []string{}, Credential: "k"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	res, body := do(t, http.MethodPost, srv.URL+"/routes", token, []byte(`{"modelMatch":"m","providerIds":["p"],"eligible":[]}`))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create %d %s", res.StatusCode, body)
	}
	file, err := osRead(store.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(file), `"eligible": []`) && !strings.Contains(string(file), `"eligible":[]`) {
		t.Fatalf("file = %s", file)
	}
}

func TestRouteCarriesRoutingAndWeights(t *testing.T) {
	store := loadTemp(t)
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Providers = []state.Provider{{ID: "p", Name: "P", Protocol: "chat", BaseURL: "https://api.example.com", Models: []string{"m"}, Credential: "k"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	token := withSession(t, store)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	route := postJSON(t, srv.URL+"/routes", token, map[string]any{
		"modelMatch": "m", "providerIds": []string{"p"}, "routing": "weight", "weights": map[string]int{"p": 2},
	})
	if route["routing"] != "weight" {
		t.Fatalf("created %#v", route)
	}
	listed := mustGET(t, srv.URL+"/routes", token)
	if !strings.Contains(listed, `"routing":"weight"`) || !strings.Contains(listed, `"weights":{"p":2}`) {
		t.Fatalf("list %s", listed)
	}
	res, body := do(t, http.MethodPut, srv.URL+"/routes/"+route["id"].(string), token, []byte(`{"modelMatch":"m","providerIds":["p"],"routing":"rotate"}`))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"routing":"rotate"`) || strings.Contains(body, "weights") {
		t.Fatalf("put %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPost, srv.URL+"/routes", token, []byte(`{"modelMatch":"x","providerIds":["p"],"routing":"bogus"}`))
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "routing") {
		t.Fatalf("bad routing %d %s", res.StatusCode, body)
	}
}

func TestGroupCRUDAndReferences(t *testing.T) {
	store := loadTemp(t)
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Providers = []state.Provider{
			{ID: "p1", Name: "One", Protocol: "chat", BaseURL: "https://one.example.com", Models: []string{"m"}, Credential: "k1"},
			{ID: "p2", Name: "Two", Protocol: "chat", BaseURL: "https://two.example.com", Models: []string{"m"}, Credential: "k2"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	token := withSession(t, store)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()

	group := postJSON(t, srv.URL+"/groups", token, map[string]any{
		"id": "strong", "members": []string{"p1/m", "p2/m"}, "routing": "cheap",
	})
	if group["id"] != "strong" || group["routing"] != "cheap" {
		t.Fatalf("created %#v", group)
	}
	listed := mustGET(t, srv.URL+"/groups", token)
	if !strings.Contains(listed, `"id":"strong"`) || !strings.Contains(listed, `"members":["p1/m","p2/m"]`) {
		t.Fatalf("list %s", listed)
	}
	res, body := do(t, http.MethodPut, srv.URL+"/groups/strong", token, []byte(`{"members":["p1/m","p2/m"],"routing":"weight","weights":{"p1/m":3,"p2/m":1}}`))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"routing":"weight"`) || !strings.Contains(body, `"p1/m":3`) {
		t.Fatalf("put %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodDelete, srv.URL+"/providers/p1", token, nil)
	if res.StatusCode != http.StatusConflict || !strings.Contains(body, "in_use") {
		t.Fatalf("provider in group %d %s", res.StatusCode, body)
	}

	nested := postJSON(t, srv.URL+"/groups", token, map[string]any{
		"id": "team/outer", "members": []string{"strong"}, "routing": "order",
	})
	if nested["id"] != "team/outer" {
		t.Fatalf("nested %#v", nested)
	}
	res, body = do(t, http.MethodDelete, srv.URL+"/groups/"+url.PathEscape("team/outer"), token, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete nested %d %s", res.StatusCode, body)
	}

	res, body = do(t, http.MethodDelete, srv.URL+"/groups/strong", token, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete group %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPost, srv.URL+"/routes", token, []byte(`{"modelMatch":"m","providerIds":["p1"],"routing":"cheap"}`))
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "routing") {
		t.Fatalf("route cheap %d %s", res.StatusCode, body)
	}
}

func TestProviderSeatLimitsAndReset(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	var gotScope, gotSeat string
	srv := httptest.NewServer(&Server{
		Store: store, Host: memHost{store: store},
		ResetSeat: func(scope, seat string) {
			gotScope, gotSeat = scope, seat
		},
	})
	defer srv.Close()

	provider := postJSON(t, srv.URL+"/providers", token, map[string]any{
		"name": "limited", "protocol": "chat", "baseUrl": "https://api.example.com",
		"models": []string{"m"}, "credentials": []string{"secret-one", "secret-two"},
		"maxConcurrent": 2, "maxRPM": 60, "queueSlots": 3, "queueWaitMS": 500,
	})
	for key, want := range map[string]float64{"maxConcurrent": 2, "maxRPM": 60, "queueSlots": 3, "queueWaitMS": 500} {
		if provider[key] != want {
			t.Fatalf("%s = %v, want %v (%#v)", key, provider[key], want, provider)
		}
	}
	seats := mustGET(t, srv.URL+"/seats", token)
	if strings.Contains(seats, "secret-one") || strings.Contains(seats, "secret-two") {
		t.Fatalf("seat list leaked a key: %s", seats)
	}
	if !strings.Contains(seats, `"kind":"key"`) || !strings.Contains(seats, `"keyIndex":1`) {
		t.Fatalf("seats %s", seats)
	}
	id := provider["id"].(string)
	res, body := do(t, http.MethodPost, srv.URL+"/seats/reset", token, []byte(`{"scope":"r1","seat":"provider/`+id+`/key/abc"}`))
	if res.StatusCode != http.StatusNoContent || gotScope != "r1" || gotSeat != "provider/"+id+"/key/abc" {
		t.Fatalf("reset %d %s scope=%q seat=%q", res.StatusCode, body, gotScope, gotSeat)
	}
	res, body = do(t, http.MethodPost, srv.URL+"/seats/reset", token, []byte(`{"seat":"nope"}`))
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "seat") {
		t.Fatalf("bad seat %d %s", res.StatusCode, body)
	}

	res, body = do(t, http.MethodPut, srv.URL+"/providers/"+id, token, []byte(`{"name":"limited","protocol":"chat","baseUrl":"https://api.example.com","models":["m"],"maxConcurrent":0,"maxRPM":0,"queueSlots":0,"queueWaitMS":0}`))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("put %d %s", res.StatusCode, body)
	}
	snap := store.Current()
	saved, ok := snap.Config.Provider(id)
	if !ok || saved.MaxConcurrent != 0 || saved.MaxRPM != 0 || saved.QueueSlots != 0 || saved.QueueWaitMS != 0 {
		t.Fatalf("limits not cleared: %#v", saved)
	}
}

func TestAutoGroupsSwitch(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	if body := mustGET(t, srv.URL+"/auto-groups", token); !strings.Contains(body, `"on":true`) {
		t.Fatalf("default %s", body)
	}
	res, body := do(t, http.MethodPut, srv.URL+"/auto-groups", token, []byte(`{"on":false}`))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"on":false`) {
		t.Fatalf("off %d %s", res.StatusCode, body)
	}
	if !store.Current().Config.NoAutoGroups {
		t.Fatal("auto groups not stored as off")
	}
}

func TestUsageReadDoesNotAppend(t *testing.T) {
	store := loadTemp(t)
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := store.AppendUsage(state.AttemptRecord{
		RouteID: "r", ProviderID: "p", Outcome: "success",
		Started: start, Finished: start,
	}); err != nil {
		t.Fatal(err)
	}
	token := withSession(t, store)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	body := mustGET(t, srv.URL+"/usage?routeId=r&accountId=ignored", token)
	if !strings.Contains(body, `"status":"ok"`) || !strings.Contains(body, `"r"`) {
		t.Fatalf("usage %s", body)
	}
	before := mustGET(t, srv.URL+"/usage", token)
	res, _ := do(t, http.MethodPost, srv.URL+"/usage", token, []byte(`{}`))
	if res.StatusCode == http.StatusOK || res.StatusCode == http.StatusCreated {
		t.Fatalf("usage write status %d", res.StatusCode)
	}
	after := mustGET(t, srv.URL+"/usage", token)
	if before != after {
		t.Fatal("console appended usage")
	}
}

func TestTokenSpendLimitIsStored(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	created := postJSON(t, srv.URL+"/tokens", token, map[string]any{"spendLimit": 2.5, "spendPeriod": "today"})
	if created["spendPeriod"] != "today" || created["spendLimit"] != 2.5 {
		t.Fatalf("created %#v", created)
	}
	listed := mustGET(t, srv.URL+"/tokens", token)
	if !strings.Contains(listed, `"spendLimit":2.5`) || !strings.Contains(listed, `"spendPeriod":"today"`) {
		t.Fatalf("list %s", listed)
	}
	res, body := do(t, http.MethodPost, srv.URL+"/tokens", token, []byte(`{"spendLimit":-1}`))
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "spend_limit") {
		t.Fatalf("negative %d %s", res.StatusCode, body)
	}
}

func TestCreatedTokenReturnsModels(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	created := postJSON(t, srv.URL+"/tokens", token, map[string]any{"models": []string{"m1", "m2"}})
	models, _ := created["models"].([]any)
	if len(models) != 2 || models[0] != "m1" || models[1] != "m2" {
		t.Fatalf("created %#v", created)
	}
	listed := mustGET(t, srv.URL+"/tokens", token)
	if !strings.Contains(listed, `"models":["m1","m2"]`) {
		t.Fatalf("list %s", listed)
	}
}

func TestUsageSummaryPricesTheWindow(t *testing.T) {
	store := loadTemp(t)
	if err := store.AppendUsage(state.AttemptRecord{
		RouteID: "r", ProviderID: "p", Outcome: "success", Model: "demo",
		Started: time.Now().UTC(), Finished: time.Now().UTC(),
		Usage: json.RawMessage(`{"prompt_tokens":1000000,"completion_tokens":500000}`),
	}); err != nil {
		t.Fatal(err)
	}
	token := withSession(t, store)
	srv := httptest.NewServer(&Server{
		Store: store, Host: memHost{store: store},
		Prices: price.Fixed{"demo": {Input: 2, Output: 8}},
	})
	defer srv.Close()
	body := mustGET(t, srv.URL+"/usage?period=all", token)
	if !strings.Contains(body, `"cost":6`) || !strings.Contains(body, `"unpriced":0`) {
		t.Fatalf("priced %s", body)
	}
}

func TestUsageBalanceDoesNotDialAnUnknownHost(t *testing.T) {
	store := loadTemp(t)
	key := "sk-test-secret"
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Providers = []state.Provider{{
			ID: "p", Name: "OpenAI", Protocol: "chat", BaseURL: "https://api.openai.com/v1",
			Models: []string{"gpt-4o"}, Credential: key,
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	session := withSession(t, store)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	body := mustGET(t, srv.URL+"/usage", session)
	if strings.Contains(body, key) || !strings.Contains(body, `"providerId":"p"`) || !strings.Contains(body, `"status":"unsupported"`) || strings.Contains(body, `"remaining"`) {
		t.Fatalf("balance %s", body)
	}
}

func TestUsageQuotasReadTheSubscriptionSignIns(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "secret-token"
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"`+secret+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}, Logins: func() login.Machine {
		return login.Machine{Home: home, ClaudeUsage: func(context.Context, string) ([]byte, error) {
			return []byte("Current session: 13% used\nResets 4pm\n"), nil
		}}
	}})
	defer srv.Close()
	body := mustGET(t, srv.URL+"/usage", token)
	if !strings.Contains(body, `"quotas":[{"account":"claude"`) || !strings.Contains(body, `"usedPercent":13`) || !strings.Contains(body, `"resetText":"4pm"`) {
		t.Fatalf("quotas %s", body)
	}
	if strings.Contains(body, secret) {
		t.Fatal("usage response contains the token")
	}
}

func TestUsageQuotasWithoutLoginsIsEmpty(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	body := mustGET(t, srv.URL+"/usage", token)
	if !strings.Contains(body, `"quotas":[]`) {
		t.Fatalf("quotas %s", body)
	}
}

func TestUsageSummaryUsesLocalToday(t *testing.T) {
	store := loadTemp(t)
	now := time.Now()
	start, ok := state.PeriodStart("today", now)
	if !ok {
		t.Fatal("today")
	}
	if err := store.AppendUsage(state.AttemptRecord{
		RouteID: "old", ProviderID: "p", Outcome: "success",
		Started: start.Add(-time.Minute), Finished: start,
		Usage: json.RawMessage(`{"prompt_tokens":100,"completion_tokens":1}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendUsage(state.AttemptRecord{
		RouteID: "new", ProviderID: "p", Outcome: "success",
		Started: start, Finished: start.Add(time.Second),
		Usage: json.RawMessage(`{"prompt_tokens":10,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4}}`),
	}); err != nil {
		t.Fatal(err)
	}
	token := withSession(t, store)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	body := mustGET(t, srv.URL+"/usage?period=today", token)
	if strings.Contains(body, `"routeId":"old"`) || !strings.Contains(body, `"routeId":"new"`) || !strings.Contains(body, `"calls":1`) || !strings.Contains(body, `"in":6`) || !strings.Contains(body, `"cacheRead":4`) {
		t.Fatalf("today %s", body)
	}
	res, bad := do(t, http.MethodGet, srv.URL+"/usage?period=year", token, nil)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(bad, "invalid") {
		t.Fatalf("bad period %d %s", res.StatusCode, bad)
	}
	allBody := mustGET(t, srv.URL+"/usage", token)
	if !strings.Contains(allBody, `"summary"`) || !strings.Contains(allBody, `"calls":2`) {
		t.Fatalf("all %s", allBody)
	}
}

func TestUsageUnknown(t *testing.T) {
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
	body := mustGET(t, srv.URL+"/usage", token)
	if !strings.Contains(body, `"status":"unknown"`) || strings.Contains(body, "records") || strings.Contains(body, "total") {
		t.Fatalf("usage %s", body)
	}
}

func TestConfigStatusAndModelRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gate.json")
	if err := writeFile(path, []byte("{")); err != nil {
		t.Fatal(err)
	}
	store := state.Open(state.Paths{ConfigFile: path, UsageDir: filepath.Join(dir, "usage")})
	if err := store.Load(); err == nil {
		t.Fatal("expected unreadable config")
	}
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	body := mustGET(t, srv.URL+"/config-status", "")
	if !strings.Contains(body, "unreadable") {
		t.Fatalf("status %s", body)
	}
	res, got := do(t, http.MethodGet, srv.URL+"/providers", "", nil)
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(got, "config_unreadable") {
		t.Fatalf("providers %d %s", res.StatusCode, got)
	}
	res, got = do(t, http.MethodPost, srv.URL+"/v1/chat/completions", "", []byte(`{"model":"m"}`))
	if res.StatusCode != http.StatusForbidden || !strings.Contains(got, "rejected") {
		t.Fatalf("model %d %s", res.StatusCode, got)
	}
	res, got = do(t, http.MethodPost, srv.URL+"/chat/completions", "", []byte(`{"model":"m"}`))
	if res.StatusCode != http.StatusForbidden || !strings.Contains(got, "rejected") {
		t.Fatalf("short model path %d %s", res.StatusCode, got)
	}
}

func TestSessionSeparatesGatewayTokens(t *testing.T) {
	store := loadTemp(t)
	session := withSession(t, store)
	if err := store.Update(func(cfg *state.Config) error {
		cfg.GatewayTokens = []state.GatewayToken{{ID: "tok", Secret: "gateway-secret"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	res, _ := do(t, http.MethodGet, srv.URL+"/providers", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("open console %d", res.StatusCode)
	}
	res, _ = do(t, http.MethodGet, srv.URL+"/providers", "gateway-secret", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("gateway token opened console %d", res.StatusCode)
	}
	res, body := do(t, http.MethodGet, srv.URL+"/providers", session, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("session %d %s", res.StatusCode, body)
	}
	if strings.Contains(body, session) || strings.Contains(body, "gateway-secret") {
		t.Fatal("response included a secret")
	}
}

func TestRegisterLoginAndNonLoopback(t *testing.T) {
	store := loadTemp(t)
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	res, body := do(t, http.MethodPost, srv.URL+"/register", "", []byte(`{"username":"ada","password":"short"}`))
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "password") {
		t.Fatalf("short password %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPost, srv.URL+"/register", "", []byte(`{"username":"ada","password":"password1"}`))
	if res.StatusCode != http.StatusCreated || strings.Contains(body, "password1") {
		t.Fatalf("register %d %s", res.StatusCode, body)
	}
	file, err := osRead(store.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(file), "password1") {
		t.Fatal("config stored the password")
	}
	res, body = do(t, http.MethodPost, srv.URL+"/login", "", []byte(`{"username":"ada","password":"nope-nope"}`))
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPost, srv.URL+"/login", "", []byte(`{"username":"missing","password":"password1"}`))
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing user %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPost, srv.URL+"/login", "", []byte(`{"username":"ada","password":"password1"}`))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login %d %s", res.StatusCode, body)
	}
	var logged map[string]string
	if err := json.Unmarshal([]byte(body), &logged); err != nil {
		t.Fatal(err)
	}
	secret := logged["secret"]
	file, err = osRead(store.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || strings.Contains(string(file), secret) {
		t.Fatal("session secret missing or stored raw")
	}
	if res, _ := do(t, http.MethodGet, srv.URL+"/providers", secret, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("authed providers %d", res.StatusCode)
	}
	res, _ = do(t, http.MethodPost, srv.URL+"/logout", secret, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout %d", res.StatusCode)
	}
	if res, _ := do(t, http.MethodGet, srv.URL+"/providers", secret, nil); res.StatusCode != http.StatusUnauthorized {
		t.Fatal("logged out session still worked")
	}

	public := httptest.NewServer(&Server{Store: store, Host: fixedHost{store: store, status: ListenStatus{
		Console: PortStatus{Effective: "192.0.2.10:8081", Listening: true},
	}}})
	defer public.Close()
	res, body = do(t, http.MethodPost, public.URL+"/register", "", []byte(`{"username":"bob","password":"password1"}`))
	if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "rejected") {
		t.Fatalf("public register %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPost, public.URL+"/login", "", []byte(`{"username":"ada","password":"password1"}`))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("public login %d %s", res.StatusCode, body)
	}
}

func TestListenersKeepTheOtherUserValue(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Listeners.Gateway = "127.0.0.1:19090"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	host := &recordingHost{store: store, status: ListenStatus{
		Gateway: PortStatus{Configured: "127.0.0.1:19090", Effective: "127.0.0.1:19090", Listening: true},
		Console: PortStatus{Effective: "127.0.0.1:19091", Listening: true},
	}}
	srv := httptest.NewServer(&Server{Store: store, Host: host})
	defer srv.Close()
	res, body := do(t, http.MethodPut, srv.URL+"/listeners", token, []byte(`{"console":"127.0.0.1:19091"}`))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("put %d %s", res.StatusCode, body)
	}
	snap := store.Current()
	if snap.Config.Listeners.Gateway != "127.0.0.1:19090" || snap.Config.Listeners.Console != "127.0.0.1:19091" {
		t.Fatalf("listeners %+v", snap.Config.Listeners)
	}
	file, err := osRead(store.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(file), "127.0.0.1:3425") || strings.Contains(string(file), "127.0.0.1:8080") || strings.Contains(string(file), "127.0.0.1:8081") {
		t.Fatalf("defaults written: %s", file)
	}
}

func TestProviderProbeReturnsTheResultWithoutTheKey(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	const credential = "super-secret-value"
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Providers = []state.Provider{{
			ID: "p1", Protocol: "chat", BaseURL: "http://127.0.0.1:9", Models: []string{"demo"}, Credential: credential,
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var saw state.Provider
	srv := &Server{Store: store, Host: memHost{store: store}, Probe: func(_ context.Context, prov state.Provider, consoleListen string) ProbeResult {
		saw = prov
		if consoleListen != "127.0.0.1:8081" {
			t.Fatalf("console %q", consoleListen)
		}
		return ProbeResult{OK: false, Status: http.StatusBadGateway, Protocol: "chat", Model: "demo", Error: "upstream_unavailable"}
	}}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	res, body := do(t, http.MethodPost, ts.URL+"/providers/p1/test", token, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "upstream_unavailable") {
		t.Fatalf("probe %d %s", res.StatusCode, body)
	}
	if strings.Contains(body, credential) || saw.Credential != credential {
		t.Fatalf("body %s saw key %q", body, saw.Credential)
	}
	res, body = do(t, http.MethodPost, ts.URL+"/providers/missing/test", token, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing %d %s", res.StatusCode, body)
	}
}

type memHost struct{ store *state.Store }

func (m memHost) Status() ListenStatus {
	return ListenStatus{
		Gateway: PortStatus{Effective: "127.0.0.1:8080", Listening: true},
		Console: PortStatus{Effective: "127.0.0.1:8081", Listening: true},
	}
}

func (m memHost) Update(fn func(*state.Config) error) error { return m.store.Update(fn) }

type fixedHost struct {
	store  *state.Store
	status ListenStatus
}

func (h fixedHost) Status() ListenStatus { return h.status }

func (h fixedHost) Update(fn func(*state.Config) error) error { return h.store.Update(fn) }

type recordingHost struct {
	store  *state.Store
	status ListenStatus
}

func (h *recordingHost) Status() ListenStatus { return h.status }

func (h *recordingHost) Update(fn func(*state.Config) error) error {
	err := h.store.Update(fn)
	if err != nil {
		return err
	}
	snap := h.store.Current()
	h.status.Gateway.Configured = snap.Config.Listeners.Gateway
	h.status.Console.Configured = snap.Config.Listeners.Console
	if snap.Config.Listeners.Console != "" {
		h.status.Console.Effective = snap.Config.Listeners.Console
	}
	return nil
}

func withSession(t *testing.T, store *state.Store) string {
	t.Helper()
	const secret = "session-secret-value"
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Users = append(cfg.Users, state.User{
			ID:           "user1",
			Username:     "ada",
			PasswordSalt: "salt",
			PasswordHash: "hash",
			Sessions:     []state.Session{{ID: "sess1", SecretHash: security.SecretHash(secret)}},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return secret
}

func loadTemp(t *testing.T) *state.Store {
	t.Helper()
	dir := t.TempDir()
	paths, err := state.ResolvePaths(filepath.Join(dir, "gate.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	store := state.Open(paths)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	return store
}

func postJSON(t *testing.T, url, token string, body any) map[string]any {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	res, text := do(t, http.MethodPost, url, token, raw)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST %s = %d %s", url, res.StatusCode, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func postStatus(t *testing.T, url, token string, body any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := do(t, http.MethodPost, url, token, raw)
	return res.StatusCode
}

func mustGET(t *testing.T, url, token string) string {
	t.Helper()
	res, body := do(t, http.MethodGet, url, token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %s", url, res.StatusCode, body)
	}
	return body
}

func TestControlPage(t *testing.T) {
	if !IsConsolePath("/") {
		t.Fatal("control page is not a console path")
	}
	store := loadTemp(t)
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Users = []state.User{{
			ID: "user1", Username: "ada", PasswordSalt: "salt", PasswordHash: "password-hash-not-in-page",
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}})
	defer srv.Close()
	res, body := do(t, http.MethodGet, srv.URL+"/", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("page %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content type %s", ct)
	}
	if !strings.Contains(body, `id="root"`) {
		t.Fatal("page has no react root")
	}
	script := assetSrc(body, ".js")
	if script == "" {
		t.Fatal("page did not load a script")
	}
	js := mustGET(t, srv.URL+script, "")
	if !strings.Contains(js, "/login") || !strings.Contains(js, "/providers") {
		t.Fatal("page does not call the console API")
	}
	if strings.Contains(body, "password-hash-not-in-page") || strings.Contains(js, "password-hash-not-in-page") {
		t.Fatal("page included the password hash")
	}
	style := assetSrc(body, ".css")
	if style == "" {
		t.Fatal("page did not load a stylesheet")
	}
	res, body = do(t, http.MethodGet, srv.URL+style, "", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "text/css") {
		t.Fatalf("stylesheet %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	res, body = do(t, http.MethodGet, srv.URL+"/favicon.ico", "", nil)
	if res.StatusCode != http.StatusNoContent || body != "" {
		t.Fatalf("favicon %d %q", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPost, srv.URL+"/", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post page %d %s", res.StatusCode, body)
	}
}

func assetSrc(html, ext string) string {
	for _, part := range strings.Split(html, `"`) {
		if strings.HasPrefix(part, "/assets/") && strings.HasSuffix(part, ext) {
			return part
		}
	}
	return ""
}

func do(t *testing.T, method, url, token string, body []byte) (*http.Response, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, string(b)
}

func TestAccountsAreNotCopiedIntoConfig(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	home := t.TempDir()
	const secret = "sk-ant-oat-secret"
	path := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"`+secret+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}, Logins: func() login.Machine {
		off := false
		if snap := store.Current(); snap != nil {
			off = snap.Config.NoAccountDiscovery
		}
		return login.Machine{Home: home, Off: off}
	}})
	defer srv.Close()
	body := mustGET(t, srv.URL+"/accounts", token)
	if strings.Contains(body, secret) || !strings.Contains(body, "login/claude/claude-sonnet-4-6") {
		t.Fatalf("accounts %s", body)
	}
	saved, err := os.ReadFile(store.Paths().ConfigFile)
	if err != nil || strings.Contains(string(saved), secret) {
		t.Fatalf("config copied the token %s %v", saved, err)
	}
	res, got := do(t, http.MethodPut, srv.URL+"/accounts/discovery", token, []byte(`{"on":false}`))
	if res.StatusCode != http.StatusOK || !strings.Contains(got, `"status":"off"`) {
		t.Fatalf("off %d %s", res.StatusCode, got)
	}
}

func TestWorkBuddyKeyIsNotCopiedIntoConfig(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	const key = "ck_test0000000000abcdefgh"
	authFile := filepath.Join(t.TempDir(), "workbuddy-auth.json")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"agents":[{"name":"cli","models":["glm-9"]}],"models":[{"id":"glm-9","name":"GLM-9"}]}}`))
	}))
	defer api.Close()
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}, Logins: func() login.Machine {
		return login.Machine{Home: t.TempDir(), WorkBuddyStore: authFile, WorkBuddyAPI: api.URL, Client: api.Client()}
	}})
	defer srv.Close()
	res, body := do(t, http.MethodPost, srv.URL+"/accounts/workbuddy", token, []byte(`{"build":"workbuddy","apiKey":"`+key+`"}`))
	if res.StatusCode != http.StatusOK || strings.Contains(body, key) || !strings.Contains(body, "login/workbuddy#key-abcdefgh/glm-9") {
		t.Fatalf("accounts %d %s", res.StatusCode, body)
	}
	saved, err := os.ReadFile(store.Paths().ConfigFile)
	if err != nil || strings.Contains(string(saved), key) {
		t.Fatalf("config %s %v", saved, err)
	}
	own, err := os.ReadFile(authFile)
	if err != nil || !strings.Contains(string(own), key) {
		t.Fatalf("store %s %v", own, err)
	}
}

func TestAgentConnectRestoresThePreviousKey(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Providers = []state.Provider{{
			ID: "p", Protocol: "chat", BaseURL: "https://example.com/v1", Models: []string{"demo"}, Credential: "k",
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	machine := agent.Machine{
		Home:     home,
		StashDir: filepath.Join(t.TempDir(), "stash"),
		LookPath: func(string) bool { return false },
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	const previous = "sk-user-secret"
	if err := os.WriteFile(settings, []byte(`{"env":{"ANTHROPIC_AUTH_TOKEN":"`+previous+`","FOO":"bar"},"model":"sonnet"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}, Agents: &machine})
	defer srv.Close()

	res, body := do(t, http.MethodGet, srv.URL+"/agents", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("open agents %d %s", res.StatusCode, body)
	}
	listed := mustGET(t, srv.URL+"/agents", token)
	if strings.Contains(listed, previous) || !strings.Contains(listed, `"id":"claude"`) || !strings.Contains(listed, `"installed":true`) {
		t.Fatalf("list %s", listed)
	}
	res, body = do(t, http.MethodPost, srv.URL+"/agents/claude/connect", token, nil)
	if res.StatusCode != http.StatusOK || strings.Contains(body, previous) {
		t.Fatalf("connect %d %s", res.StatusCode, body)
	}
	wired, err := os.ReadFile(settings)
	if err != nil || !strings.Contains(string(wired), "aigate-claude") || !strings.Contains(string(wired), `"FOO": "bar"`) || strings.Contains(string(wired), previous) {
		t.Fatalf("wired %s", wired)
	}
	res, body = do(t, http.MethodPost, srv.URL+"/agents/claude/disconnect", token, nil)
	if res.StatusCode != http.StatusOK || strings.Contains(body, previous) {
		t.Fatalf("disconnect %d %s", res.StatusCode, body)
	}
	back, err := os.ReadFile(settings)
	if err != nil || !strings.Contains(string(back), previous) || !strings.Contains(string(back), `"FOO": "bar"`) || strings.Contains(string(back), "aigate-claude") {
		t.Fatalf("restored %s", back)
	}
}

func TestAgentConnectRefusesAnEmptyCatalog(t *testing.T) {
	store := loadTemp(t)
	token := withSession(t, store)
	home := t.TempDir()
	machine := agent.Machine{Home: home, StashDir: filepath.Join(t.TempDir(), "stash"), LookPath: func(string) bool { return false }}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	down := fixedHost{store: store, status: ListenStatus{Gateway: PortStatus{Listening: false}}}
	srv := httptest.NewServer(&Server{Store: store, Host: down, Agents: &machine})
	defer srv.Close()
	res, body := do(t, http.MethodPost, srv.URL+"/agents/claude/connect", token, nil)
	if res.StatusCode != http.StatusConflict || !strings.Contains(body, "not_listening") {
		t.Fatalf("down %d %s", res.StatusCode, body)
	}
	up := httptest.NewServer(&Server{Store: store, Host: memHost{store: store}, Agents: &machine})
	defer up.Close()
	res, body = do(t, http.MethodPost, up.URL+"/agents/claude/connect", token, nil)
	if res.StatusCode != http.StatusConflict || !strings.Contains(body, "no_models") {
		t.Fatalf("empty %d %s", res.StatusCode, body)
	}
	if raw, err := os.ReadFile(settings); err != nil || string(raw) != "{}\n" {
		t.Fatalf("file %s", raw)
	}
}

func osRead(path string) ([]byte, error) { return os.ReadFile(path) }

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
