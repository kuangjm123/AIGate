package login

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoveryOffReadsNothing(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret-token"}}`)
	reads := 0
	got := Discover(Machine{Home: home, Off: true, ReadFile: func(string) ([]byte, error) {
		reads++
		return nil, os.ErrNotExist
	}})
	if got.Status != "off" || len(got.Accounts) != 0 || reads != 0 {
		t.Fatalf("off %#v reads %d", got, reads)
	}
}

func TestClaudeListsPublicModelsAndExtrasWithoutAskingAnthropic(t *testing.T) {
	home := t.TempDir()
	const secret = "sk-ant-oat-secret"
	write(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"`+secret+`"}}`)
	write(t, filepath.Join(home, ".claude", "settings.json"), `{"availableModels":["my-extra"],"modelOverrides":{"override-model":"x"}}`)
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked = true }))
	defer srv.Close()
	got := Discover(Machine{Home: home, Client: srv.Client()})
	if got.Status != "ok" || len(got.Accounts) != 1 {
		t.Fatalf("discover %#v", got)
	}
	account := got.Accounts[0]
	if account.ID != "claude" || !account.ModelsKnown || !hasModel(account, "claude-sonnet-4-6") || !hasModel(account, "my-extra") || !hasModel(account, "override-model") {
		t.Fatalf("models %#v", account.Models)
	}
	if asked {
		t.Fatal("claude login was sent to an HTTP server")
	}
	blob, _ := json.Marshal(account)
	if strings.Contains(string(blob), secret) {
		t.Fatal("account value contains the token")
	}
}

func TestMissingLoginIsAbsentAndBadFileIsUnknown(t *testing.T) {
	home := t.TempDir()
	got := Discover(Machine{Home: home})
	if got.Status != "ok" || len(got.Accounts) != 0 {
		t.Fatalf("empty %#v", got)
	}
	write(t, filepath.Join(home, ".codex", "auth.json"), `{`)
	got = Discover(Machine{Home: home})
	if len(got.Accounts) != 1 || got.Accounts[0].ID != "codex" || got.Accounts[0].Readable {
		t.Fatalf("bad %#v", got.Accounts)
	}
}

func TestCodexRefreshWritesBackAndListsOnlyBackendModels(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "auth.json")
	write(t, path, `{"auth_mode":"chatgpt","keep":"yes","tokens":{"access_token":"`+jwtAt(time.Now().Add(-time.Hour))+`","refresh_token":"old-refresh","account_id":"acct-1"}}`)
	var sawAuth, sawAccount bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["refresh_token"] != "old-refresh" || body["client_id"] != codexClientID {
				t.Errorf("refresh body %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": jwtAt(time.Now().Add(2 * time.Hour)), "refresh_token": "new-refresh", "id_token": "id-1"})
		case "/models":
			if r.Header.Get("Authorization") == "" || r.Header.Get("ChatGPT-Account-ID") != "acct-1" {
				t.Errorf("models headers %#v", r.Header)
			}
			sawAuth = r.Header.Get("Authorization") != ""
			sawAccount = true
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-kept","visibility":"list","supported_in_api":true},{"slug":"gpt-hidden","visibility":"hide","supported_in_api":true}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	got := Discover(Machine{Home: home, Client: api.Client(), CodexAuth: api.URL + "/oauth/token", CodexAPI: api.URL})
	if !sawAuth || !sawAccount || len(got.Accounts) != 1 || !hasModel(got.Accounts[0], "gpt-kept") || hasModel(got.Accounts[0], "gpt-hidden") {
		t.Fatalf("codex %#v saw %v %v", got.Accounts, sawAuth, sawAccount)
	}
	saved, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(saved), "new-refresh") || !strings.Contains(string(saved), `"keep": "yes"`) || strings.Contains(string(saved), "old-refresh") {
		t.Fatalf("saved %s %v", saved, err)
	}
}

func TestPluginOccupiesTheSlotAndDialDoesNotReadTheToken(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret-token"}}`)
	got := Discover(Machine{Home: home, PluginIDs: []string{"claude"}})
	if len(got.Accounts) != 1 || !got.Accounts[0].Plugin || got.Accounts[0].ModelsKnown {
		t.Fatalf("plugin %#v", got.Accounts)
	}
	reads := 0
	_, err := Dial(t.Context(), Machine{Home: home, PluginIDs: []string{"claude"}, ReadFile: func(string) ([]byte, error) {
		reads++
		return nil, os.ErrInvalid
	}}, "claude", "claude-sonnet-4-6", []byte(`{}`), false)
	if err != ErrPlugin || reads != 0 {
		t.Fatalf("dial %v reads %d", err, reads)
	}
}

func TestCodexAPIKeyLoginIsListed(t *testing.T) {
	home := t.TempDir()
	const secret = "sk-test-secret"
	write(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"apikey","OPENAI_API_KEY":"`+secret+`"}`)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("auth %s", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer api.Close()
	got := Discover(Machine{Home: home, Client: api.Client(), OpenAI: api.URL + "/v1"})
	if len(got.Accounts) != 1 || got.Accounts[0].ID != "codex" || !hasModel(got.Accounts[0], "gpt-4o") {
		t.Fatalf("accounts %#v", got.Accounts)
	}
	blob, _ := json.Marshal(got)
	if strings.Contains(string(blob), secret) {
		t.Fatal("discovery returned the key")
	}
}

func TestWorkBuddyDesktopLoginIsReadOnly(t *testing.T) {
	home := t.TempDir()
	auth := t.TempDir()
	const access = "desktop-access"
	const refresh = "desktop-refresh"
	body := `{
	  "account":{"uid":"u-1","nickname":"Ann","type":"personal"},
	  "auth":{"accessToken":"` + access + `","refreshToken":"` + refresh + `","domain":"www.codebuddy.cn","tokenType":"Bearer"}
	}`
	path := filepath.Join(auth, "workbuddy-desktop.info")
	write(t, path, body)
	write(t, filepath.Join(home, ".workbuddy", "models.json"), `[{"id":"hunyuan-2.0-instruct","url":"https://api.lkeap.cloud.tencent.com/coding/v3/chat/completions","apiKey":"plan-secret"}]`)
	var calls []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer "+access {
			t.Errorf("auth %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Refresh-Token") != "" {
			t.Errorf("refresh token was sent")
		}
		if r.Header.Get("User-Agent") != "WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0" {
			t.Errorf("ua %s", r.Header.Get("User-Agent"))
		}
		if r.URL.Path == "/v3/config" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"agents":[{"name":"cli","models":["glm-5.3"]}],"models":[{"id":"glm-5.3","name":"GLM-5.3","maxInputTokens":128000,"reasoningEffort":["high"]},{"id":"deepseek-v4-pro","name":"DeepSeek"}]}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	machine := Machine{Home: home, WorkBuddyAuth: auth, WorkBuddyAPI: api.URL, Client: api.Client()}
	got := Discover(machine)
	var account Account
	for _, item := range got.Accounts {
		if item.ID == "workbuddy" {
			account = item
		}
	}
	if account.Origin != "desktop" || !account.ModelsKnown || !hasModel(account, "glm-5.3") || hasModel(account, "deepseek-v4-pro") || hasModel(account, "hunyuan-2.0-instruct") {
		t.Fatalf("account %#v", got.Accounts)
	}
	blob, _ := json.Marshal(got)
	if strings.Contains(string(blob), access) || strings.Contains(string(blob), refresh) || strings.Contains(string(blob), "plan-secret") {
		t.Fatal("discovery returned a token")
	}
	before, _ := os.ReadFile(path)
	resp, err := Dial(t.Context(), machine, "workbuddy", "glm-5.3", []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`), true)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("desktop file was written")
	}
	for _, call := range calls {
		if strings.Contains(call, "refresh") {
			t.Fatalf("refreshed %v", calls)
		}
	}
}

func TestWorkBuddyEncryptedDesktopIsNotALogin(t *testing.T) {
	home := t.TempDir()
	auth := t.TempDir()
	write(t, filepath.Join(auth, "workbuddy-desktop.info"), `{"account":{"uid":"u"},"auth":{"accessToken":{"$wbEncrypted":1},"refreshToken":{"$wbEncrypted":1}}}`)
	write(t, filepath.Join(auth, "workbuddy-desktop-ai.info"), `{`)
	got := Discover(Machine{Home: home, WorkBuddyAuth: auth})
	if len(got.Accounts) != 1 || got.Accounts[0].ID != "workbuddy-ai" || got.Accounts[0].Readable {
		t.Fatalf("accounts %#v", got.Accounts)
	}
}

func TestWorkBuddyEnterpriseUsesItsOwnHostAndModels(t *testing.T) {
	home := t.TempDir()
	auth := t.TempDir()
	write(t, filepath.Join(auth, "workbuddy-desktop.info"), `{
	  "account":{"uid":"u-1","nickname":"Ann","type":"ultimate","enterpriseId":"ent-1","enterpriseName":"学校"},
	  "auth":{"accessToken":"ent-access","refreshToken":"ent-refresh","domain":"acme.sso.copilot.tencent.com"}
	}`)
	var gotURL, enterprise string
	machine := Machine{Home: home, WorkBuddyAuth: auth, Client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		enterprise = r.Header.Get("X-Enterprise-Id")
		if r.Header.Get("X-Tenant-Id") != "ent-1" {
			t.Errorf("tenant %s", r.Header.Get("X-Tenant-Id"))
		}
		body := `{"code":0,"data":{"agents":[{"name":"cli","models":["ent-model"]}],"models":[{"id":"ent-model","name":"Ent"}]}}`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	got := Discover(machine)
	if len(got.Accounts) != 1 || !hasModel(got.Accounts[0], "ent-model") || gotURL != "https://acme.sso.copilot.tencent.com/v3/config" || enterprise != "ent-1" {
		t.Fatalf("discover %#v %s %s", got.Accounts, gotURL, enterprise)
	}
}

func TestWorkBuddyAPIKeyStaysInItsOwnFile(t *testing.T) {
	home := t.TempDir()
	store := filepath.Join(t.TempDir(), "workbuddy-auth.json")
	const key = "ck_test0000000000abcdefgh"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != key || r.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("key headers %v", r.Header)
		}
		if r.Header.Get("X-User-Id") != "anonymous_abcdefgh" || r.Header.Get("X-Domain") != "" {
			t.Errorf("identity %s %s", r.Header.Get("X-User-Id"), r.Header.Get("X-Domain"))
		}
		if r.URL.Path == "/v3/config" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"agents":[{"name":"cli","models":["glm-9"]}],"models":[{"id":"glm-9","name":"GLM-9"}]}}`))
			return
		}
		if r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("encoding %s", r.Header.Get("Content-Encoding"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n"))
	}))
	defer api.Close()
	machine := Machine{Home: home, WorkBuddyStore: store, WorkBuddyAPI: api.URL, Client: api.Client()}
	if err := SaveWorkBuddyKey(machine, "workbuddy", key); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(store)
	if !strings.Contains(string(saved), key) {
		t.Fatal("store lost the key")
	}
	got := Discover(machine)
	if len(got.Accounts) != 1 || got.Accounts[0].ID != "workbuddy#key-abcdefgh" || got.Accounts[0].Origin != "saved" || !hasModel(got.Accounts[0], "glm-9") {
		t.Fatalf("accounts %#v", got.Accounts)
	}
	blob, _ := json.Marshal(got)
	if strings.Contains(string(blob), key) {
		t.Fatal("discovery returned the key")
	}
	resp, err := Dial(t.Context(), machine, "workbuddy#key-abcdefgh", "glm-9", []byte(`{"messages":[]}`), false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(reply, []byte("ok")) {
		t.Fatalf("reply %s", reply)
	}
}

func TestWorkBuddyImportRefreshesIntoItsOwnFile(t *testing.T) {
	home := t.TempDir()
	auth := t.TempDir()
	store := filepath.Join(t.TempDir(), "workbuddy-auth.json")
	desktop := `{
	  "account":{"uid":"u-1","nickname":"Ann","type":"personal"},
	  "auth":{"accessToken":"a-1","refreshToken":"r-1","domain":"www.codebuddy.cn"}
	}`
	path := filepath.Join(auth, "workbuddy-desktop.info")
	write(t, path, desktop)
	before := mustRead(t, path)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/plugin/auth/token/refresh":
			if r.Header.Get("X-Refresh-Token") != "r-1" {
				t.Errorf("refresh %s", r.Header.Get("X-Refresh-Token"))
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"a2-r-1","refreshToken":"r2-r-1","expiresIn":3600}}`))
		case "/v3/config":
			_, _ = w.Write([]byte(`{"code":0,"data":{"agents":[{"name":"cli","models":["glm-5.3"]}],"models":[{"id":"glm-5.3","name":"GLM-5.3"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	machine := Machine{Home: home, WorkBuddyAuth: auth, WorkBuddyStore: store, WorkBuddyAPI: api.URL, Client: api.Client()}
	if err := ImportWorkBuddyDesktop(t.Context(), machine, "workbuddy"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, mustRead(t, path)) {
		t.Fatal("desktop file changed")
	}
	saved := string(mustRead(t, store))
	if !strings.Contains(saved, "r2-r-1") || strings.Contains(saved, `"refresh": "r-1"`) {
		t.Fatalf("store %s", saved)
	}
	got := Discover(machine)
	var savedID bool
	for _, item := range got.Accounts {
		if item.ID == "workbuddy#u-1" && item.Origin == "saved" && hasModel(item, "glm-5.3") {
			savedID = true
		}
	}
	if !savedID {
		t.Fatalf("accounts %#v", got.Accounts)
	}
}

func TestWorkBuddySavedLoginRefreshesOnlyItsOwnFile(t *testing.T) {
	home := t.TempDir()
	store := filepath.Join(t.TempDir(), "workbuddy-auth.json")
	write(t, store, `{
	  "accounts":[{"id":"workbuddy#u-1","build":"workbuddy","kind":"oauth","name":"Ann","uid":"u-1","domain":"www.codebuddy.cn","access":"old-access","refresh":"old-refresh","expires":1}]
	}`)
	var chatAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/plugin/auth/token/refresh":
			if r.Header.Get("X-Refresh-Token") != "old-refresh" {
				t.Errorf("refresh %s", r.Header.Get("X-Refresh-Token"))
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"new-access","refreshToken":"new-refresh","expiresIn":3600}}`))
		case "/v3/config":
			_, _ = w.Write([]byte(`{"code":0,"data":{"agents":[{"name":"cli","models":["glm-5.3"]}],"models":[{"id":"glm-5.3","name":"GLM-5.3"}]}}`))
		case "/v2/chat/completions":
			chatAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	machine := Machine{Home: home, WorkBuddyStore: store, WorkBuddyAPI: api.URL, Client: api.Client()}
	resp, err := Dial(t.Context(), machine, "workbuddy#u-1", "glm-5.3", []byte(`{"messages":[]}`), true)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if chatAuth != "Bearer new-access" {
		t.Fatalf("auth %s", chatAuth)
	}
	saved := string(mustRead(t, store))
	if !strings.Contains(saved, "new-refresh") || strings.Contains(saved, "old-refresh") {
		t.Fatalf("store %s", saved)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (r roundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return r(req) }

func TestClaudeDialUsesTheRunnerNotAnHTTPToken(t *testing.T) {
	m := Machine{Claude: func(_ context.Context, _, model string, _ []byte) ([]byte, error) {
		if model != "claude-sonnet-4-6" {
			t.Fatalf("model %s", model)
		}
		return []byte(`{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"bash"}]}`), nil
	}}
	resp, err := Dial(t.Context(), m, "claude", "claude-sonnet-4-6", []byte(`{"messages":[]}`), false)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("bash")) {
		t.Fatalf("body %s", body)
	}
}

func TestClaudeStreamStopsAtTheToolCall(t *testing.T) {
	stream := strings.NewReader("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"},{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"bash\",\"input\":{}}]}}\n")
	body, err := readClaudeStream(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"stop_reason":"tool_use"`)) || !bytes.Contains(body, []byte(`"name":"bash"`)) {
		t.Fatalf("body %s", body)
	}
}

func hasModel(account Account, id string) bool {
	for _, model := range account.Models {
		if model.ID == id {
			return true
		}
	}
	return false
}

func jwtAt(exp time.Time) string {
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":` + jsonNumber(exp.Unix()) + `}`))
	return head + "." + payload + ".sig"
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
