package console

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/agent"
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
