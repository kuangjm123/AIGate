package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/agent"
	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

func TestPortsSplitAndToken(t *testing.T) {
	gw, cs := freeAddr(t), freeAddr(t)
	var buf bytes.Buffer
	application, store := start(t, state.Config{
		Listeners: state.Listeners{Gateway: gw, Console: cs},
	}, log.New(&buf, "", 0))

	res, body := do(t, http.MethodGet, application.ConsoleURL()+"/config-status", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing session %d %s", res.StatusCode, body)
	}
	session := consoleSession(t, application.ConsoleURL())
	status := getJSON(t, application.ConsoleURL()+"/config-status", session)
	if status["config"] != "ok" {
		t.Fatalf("status %#v", status)
	}

	created := postJSON(t, application.ConsoleURL()+"/tokens", session, nil)
	secret, _ := created["secret"].(string)
	if secret == "" {
		t.Fatalf("token %#v", created)
	}
	res, body = do(t, http.MethodPost, application.GatewayURL()+"/v1/chat/completions", secret, []byte(`{"model":"m"}`))
	if res.StatusCode != http.StatusNotFound || !strings.Contains(body, "no_route") {
		t.Fatalf("gateway %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPost, application.GatewayURL()+"/v1/other", secret, []byte(`{"model":"m"}`))
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "no_dialect") {
		t.Fatalf("unknown path %d %s", res.StatusCode, body)
	}
	for _, class := range []string{"auth_failure", "rate_limit", "empty_balance", "upstream_unavailable"} {
		if strings.Contains(body, class) {
			t.Fatalf("dialect failure classified as %s: %s", class, body)
		}
	}
	res, body = doForwarded(t, application.GatewayURL()+"/v1/chat/completions", session, []byte(`{"model":"m"}`))
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "auth_failure") {
		t.Fatalf("forwarded console session on gateway %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodGet, application.ConsoleURL()+"/providers", secret, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("gateway token on console %d %s", res.StatusCode, body)
	}

	res, body = do(t, http.MethodPost, application.ConsoleURL()+"/v1/chat/completions", session, []byte(`{"model":"m"}`))
	if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "rejected") {
		t.Fatalf("model on console %d %s", res.StatusCode, body)
	}
	for _, path := range []string{"/v1beta/models/gemini-pro:generateContent", "/api/chat", "/api/tags", "/api/hello"} {
		res, body = do(t, http.MethodPost, application.ConsoleURL()+path, session, []byte(`{"model":"m"}`))
		if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "rejected") {
			t.Fatalf("model path %s on console %d %s", path, res.StatusCode, body)
		}
	}
	before, err := os.ReadFile(store.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	res, body = do(t, http.MethodPost, application.GatewayURL()+"/providers", secret, []byte(`{"protocol":"p","baseUrl":"https://example.com/v1","models":[]}`))
	if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "rejected") {
		t.Fatalf("console route on gateway %d %s", res.StatusCode, body)
	}
	after, err := os.ReadFile(store.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("gateway changed the config file")
	}
	if strings.Contains(buf.String(), secret) || strings.Contains(buf.String(), session) || strings.Contains(buf.String(), "password1") {
		t.Fatalf("log leaked a secret: %s", buf.String())
	}
}

func TestSameListenOpensNeither(t *testing.T) {
	addr := freeAddr(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "gate.json")
	cfg := state.Config{Listeners: state.Listeners{Gateway: addr, Console: addr}}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := state.Open(state.Paths{ConfigFile: path, UsageDir: filepath.Join(dir, "usage")})
	application := New(store, nil)
	err = application.Start()
	if !errors.Is(err, ErrSameListen) {
		t.Fatalf("start err = %v", err)
	}
	if application.GatewayURL() != "" || application.ConsoleURL() != "" {
		t.Fatalf("listeners opened gw=%s cs=%s", application.GatewayURL(), application.ConsoleURL())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, got) {
		t.Fatalf("config changed:\n%s", got)
	}
}

func TestShutdownRestoresCodex(t *testing.T) {
	application, _ := start(t, state.Config{
		Listeners: state.Listeners{Gateway: freeAddr(t), Console: freeAddr(t)},
		Routes:    []state.Route{{ID: "route", ModelMatch: "demo"}},
	}, nil)
	home := t.TempDir()
	machine := agent.Machine{
		Home:     home,
		StashDir: filepath.Join(t.TempDir(), "agents"),
		LookPath: func(string) bool { return false },
	}
	application.EnableAgentWiring(machine)
	codexConfig := filepath.Join(home, ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(codexConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	const original = "model = \"mine\"\n"
	if err := os.WriteFile(codexConfig, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	session := consoleSession(t, application.ConsoleURL())
	res, body := do(t, http.MethodPost, application.ConsoleURL()+"/agents/codex/connect", session, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("connect %d %s", res.StatusCode, body)
	}
	if got, err := os.ReadFile(codexConfig); err != nil || string(got) == original {
		t.Fatalf("connect did not write Codex config: %q %v", got, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := application.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(codexConfig)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, `model = "mine"`) {
		t.Fatalf("model was not restored\n%s", text)
	}
	for _, key := range []string{"model_provider =", "model_catalog_json =", "openai_base_url ="} {
		if strings.Contains(text, key) {
			t.Fatalf("%s was left behind\n%s", key, text)
		}
	}
	if !strings.Contains(text, "[model_providers.aigate]") {
		t.Fatalf("provider table was removed\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "aigate-models.json")); !os.IsNotExist(err) {
		t.Fatalf("generated catalog was left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(machine.StashDir, "codex.json")); !os.IsNotExist(err) {
		t.Fatalf("connection note was left behind: %v", err)
	}
}

func TestConsoleNonLoopbackWithoutUserStaysClosed(t *testing.T) {
	gw := freeAddr(t)
	_, port, err := net.SplitHostPort(freeAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	application, _ := start(t, state.Config{Listeners: state.Listeners{
		Gateway: gw,
		Console: net.JoinHostPort("0.0.0.0", port),
	}}, nil)
	if application.GatewayURL() == "" {
		t.Fatal("gateway did not listen")
	}
	if application.ConsoleURL() != "" {
		t.Fatal("console listened without a user")
	}
	st := application.Status()
	if st.Console.Listening || st.Console.Effective != "" {
		t.Fatalf("console status %+v", st.Console)
	}
	if st.Console.Configured != net.JoinHostPort("0.0.0.0", port) {
		t.Fatalf("configured %+v", st.Console)
	}
	if !st.Gateway.Listening {
		t.Fatal("gateway not listening")
	}
}

func TestRebindListen(t *testing.T) {
	application, _ := start(t, state.Config{Listeners: state.Listeners{
		Gateway: freeAddr(t),
		Console: freeAddr(t),
	}}, nil)
	session := consoleSession(t, application.ConsoleURL())
	next := freeAddr(t)
	old := application.GatewayURL()
	res, body := do(t, http.MethodPut, application.ConsoleURL()+"/listeners", session, []byte(`{"gateway":"`+next+`"}`))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("put %d %s", res.StatusCode, body)
	}
	var st struct {
		Gateway struct {
			Effective string `json:"effective"`
			Listening bool   `json:"listening"`
		} `json:"gateway"`
	}
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Gateway.Listening || st.Gateway.Effective != next {
		t.Fatalf("status %s", body)
	}
	res, body = do(t, http.MethodPost, "http://"+next+"/v1/chat/completions", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("new gateway %d %s", res.StatusCode, body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", strings.TrimPrefix(old, "http://"), 200*time.Millisecond)
		if err != nil {
			break
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatal("old gateway still accepts connections")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func start(t *testing.T, cfg state.Config, logger *log.Logger) (*App, *state.Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gate.json")
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	store := state.Open(state.Paths{ConfigFile: path, UsageDir: filepath.Join(dir, "usage")})
	application := New(store, logger)
	if err := application.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = application.Shutdown(ctx)
	})
	return application, store
}

func TestUnsharedGatewayStaysClosed(t *testing.T) {
	cs := freeAddr(t)
	application, _ := start(t, state.Config{
		Listeners: state.Listeners{Gateway: "192.0.2.10:3425", Console: cs},
	}, nil)
	if application.GatewayURL() != "" {
		t.Fatalf("gateway opened at %s", application.GatewayURL())
	}
	if application.ConsoleURL() == "" {
		t.Fatal("console closed")
	}
}

func TestNonLoopbackConsoleOpensForAUser(t *testing.T) {
	gw := freeAddr(t)
	_, port, err := net.SplitHostPort(freeAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	salt, hash, err := security.HashPassword("password1")
	if err != nil {
		t.Fatal(err)
	}
	application, _ := start(t, state.Config{
		Listeners: state.Listeners{Gateway: gw, Console: net.JoinHostPort("0.0.0.0", port)},
		Users: []state.User{{
			ID: "u", Username: "ada", PasswordSalt: salt, PasswordHash: hash,
		}},
	}, nil)
	if application.ConsoleURL() == "" {
		t.Fatal("console stayed closed after a user existed")
	}
	res, body := do(t, http.MethodPost, application.ConsoleURL()+"/register", "", []byte(`{"username":"bob","password":"password1"}`))
	if res.StatusCode != http.StatusForbidden || !strings.Contains(body, "rejected") {
		t.Fatalf("register %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPost, application.ConsoleURL()+"/login", "", []byte(`{"username":"ada","password":"password1"}`))
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "secret") {
		t.Fatalf("login %d %s", res.StatusCode, body)
	}
}

func consoleSession(t *testing.T, base string) string {
	t.Helper()
	res, body := do(t, http.MethodPost, base+"/register", "", []byte(`{"username":"ada","password":"password1"}`))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("register %d %s", res.StatusCode, body)
	}
	res, body = do(t, http.MethodPost, base+"/login", "", []byte(`{"username":"ada","password":"password1"}`))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login %d %s", res.StatusCode, body)
	}
	var logged map[string]string
	if err := json.Unmarshal([]byte(body), &logged); err != nil {
		t.Fatal(err)
	}
	if logged["secret"] == "" {
		t.Fatal("login returned no session")
	}
	return logged["secret"]
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func getJSON(t *testing.T, url, token string) map[string]any {
	t.Helper()
	res, body := do(t, http.MethodGet, url, token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d %s", url, res.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func postJSON(t *testing.T, url, token string, body any) map[string]any {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
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

func doForwarded(t *testing.T, url, token string, body []byte) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Forwarded-For", "203.0.113.8")
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	res, err := client.Do(req)
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
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	res, err := client.Do(req)
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
