package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuangjm123/AIGate/internal/state"
)

func testMachine(t *testing.T) Machine {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	return Machine{
		Home:     home,
		StashDir: filepath.Join(root, "stash"),
		LookPath: func(string) bool { return false },
	}
}

func gw() Target { return Target{Origin: "http://127.0.0.1:3425"} }

func offers() []Model {
	return []Model{{ID: "p1/demo", Name: "Demo", Bare: "demo"}, {ID: "route", Name: "route", Bare: "route"}}
}

func mustStatus(t *testing.T, m Machine, id string) Status {
	t.Helper()
	return statusFor(t, m, gw(), id)
}

func statusFor(t *testing.T, m Machine, target Target, id string) Status {
	t.Helper()
	for _, st := range Inspect(m, target) {
		if st.ID == id {
			return st
		}
	}
	t.Fatalf("missing %s", id)
	return Status{}
}

func TestOffersSkipHiddenAndNonChat(t *testing.T) {
	cfg := state.Config{
		Routes: []state.Route{
			{ID: "valid", ModelMatch: "routed", ProviderIDs: []string{"p"}},
			{ID: "empty", ModelMatch: "", ProviderIDs: []string{"p"}},
			{ID: "spaced", ModelMatch: " spaced", ProviderIDs: []string{"p"}},
			{ID: "comma", ModelMatch: "one, two", ProviderIDs: []string{"p"}},
		},
		Groups: []state.Group{{ID: "g", Members: []string{"p/chat"}}},
		Providers: []state.Provider{{
			ID: "p", Models: []string{"routed", "chat", "pic", "gone"},
			Info: []state.ModelInfo{
				{ID: "pic", Kind: "image"},
				{ID: "gone", Hidden: true},
			},
		}},
	}
	got := Offers(cfg)
	if len(got) != 3 || got[0].ID != "routed" || got[1].ID != "group/g" || got[1].Bare != "g" || got[2].ID != "p/chat" || got[2].Bare != "chat" {
		t.Fatalf("offers %#v", got)
	}
}

func TestClaudeConnectKeepsOtherFieldsAndRestores(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	orig := "{\n  \"theme\": \"dark\",\n  \"permissions\": {\"defaultMode\": \"acceptEdits\"},\n  \"env\": {\"FOO\": \"bar\"},\n  \"model\": \"sonnet\"\n}\n"
	writeFile(t, path, orig)
	if err := Connect(m, "claude", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	if doc["theme"] != "dark" {
		t.Fatalf("theme %#v", doc["theme"])
	}
	perms, _ := doc["permissions"].(map[string]any)
	if perms["defaultMode"] != "acceptEdits" {
		t.Fatalf("permissions %#v", doc["permissions"])
	}
	env, _ := doc["env"].(map[string]any)
	if env["FOO"] != "bar" || env["ANTHROPIC_BASE_URL"] != gw().Origin || env["ANTHROPIC_AUTH_TOKEN"] != "aigate-claude" {
		t.Fatalf("env %#v", env)
	}
	if doc["model"] != "p1/demo" {
		t.Fatalf("model %#v", doc["model"])
	}
	st := mustStatus(t, m, "claude")
	if st.State != "on" || strings.Contains(st.Note, "sk-") {
		t.Fatalf("status %#v", st)
	}
	doc["hooks"] = map[string]any{"a": json.Number("1")}
	body, err := marshalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body))
	if err := Disconnect(m, "claude"); err != nil {
		t.Fatal(err)
	}
	doc = readDoc(t, path)
	env, _ = doc["env"].(map[string]any)
	if _, ok := env["ANTHROPIC_BASE_URL"]; ok || env["FOO"] != "bar" || doc["model"] != "sonnet" || doc["theme"] != "dark" {
		t.Fatalf("restored %#v env %#v", doc, env)
	}
	if _, ok := doc["hooks"]; !ok {
		t.Fatal("later field was dropped")
	}
	if st := mustStatus(t, m, "claude"); st.State != "off" {
		t.Fatalf("after disconnect %#v", st)
	}
}

func TestClaudeCredentialsAloneAreNotInstalled(t *testing.T) {
	m := testMachine(t)
	writeFile(t, filepath.Join(m.Home, ".claude", ".credentials.json"), "{}\n")
	st := mustStatus(t, m, "claude")
	if st.Installed || st.State != "absent" {
		t.Fatalf("status %#v", st)
	}
}

func TestClaudeUnreadableFileIsNotOverwritten(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, "{ not json\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Connect(m, "claude", gw(), offers()); CodeOf(err) != "unknown" {
		t.Fatalf("connect %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("file changed %q", after)
	}
	if st := mustStatus(t, m, "claude"); st.State != "unknown" {
		t.Fatalf("status %#v", st)
	}
}

func TestClaudeConnectWiresGatewaySettings(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, `{
  "theme": "dark",
  "model": "sonnet",
  "env": {
    "FOO": "bar",
    "ANTHROPIC_MODEL": "claude-opus-4-6",
    "ANTHROPIC_API_KEY": "sk-real",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "old-sonnet",
    "CLAUDE_CODE_USE_BEDROCK": "1"
  }
}
`)
	models := []Model{
		{ID: "p1/main", Name: "Main", Bare: "main", Context: 1_000_000},
		{ID: "p1/opus", Name: "Opus", Bare: "opus"},
		{ID: "p1/sonnet", Name: "Sonnet", Bare: "sonnet"},
		{ID: "p1/haiku", Name: "Haiku", Bare: "haiku", Context: 1_000_000},
		{ID: "p1/fable", Name: "Fable", Bare: "fable"},
	}
	if err := Connect(m, "claude", gw(), models); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	env := claudeEnvOf(t, doc)
	if doc["theme"] != "dark" || doc["model"] != "p1/main[1m]" {
		t.Fatalf("top level %#v", doc)
	}
	want := map[string]string{
		"ANTHROPIC_BASE_URL":                         gw().Origin,
		"ANTHROPIC_AUTH_TOKEN":                       "aigate-claude",
		"ANTHROPIC_API_KEY":                          "",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":               "p1/opus",
		"ANTHROPIC_DEFAULT_SONNET_MODEL":             "p1/sonnet",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":              "p1/haiku[1m]",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":              "p1/fable",
		"ANTHROPIC_SMALL_FAST_MODEL":                 "p1/haiku[1m]",
		"CLAUDE_CODE_SUBAGENT_MODEL":                 "p1/main[1m]",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
		"CLAUDE_CODE_USE_BEDROCK":                    "",
		"CLAUDE_CODE_USE_VERTEX":                     "",
		"CLAUDE_CODE_USE_FOUNDRY":                    "",
		"CLAUDE_CODE_USE_ANTHROPIC_AWS":              "",
		"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD":     "",
		"CLAUDE_CODE_USE_MANTLE":                     "",
	}
	for key, value := range want {
		if env[key] != value {
			t.Fatalf("%s = %#v, want %q", key, env[key], value)
		}
	}
	if _, ok := env["ANTHROPIC_MODEL"]; ok {
		t.Fatalf("ANTHROPIC_MODEL left behind: %#v", env)
	}
	if env["FOO"] != "bar" {
		t.Fatalf("other env %#v", env)
	}
	if st := mustStatus(t, m, "claude"); st.State != "on" {
		t.Fatalf("status %#v", st)
	}

	if err := Disconnect(m, "claude"); err != nil {
		t.Fatal(err)
	}
	doc = readDoc(t, path)
	env = claudeEnvOf(t, doc)
	if doc["model"] != "sonnet" || env["ANTHROPIC_MODEL"] != "claude-opus-4-6" || env["ANTHROPIC_API_KEY"] != "sk-real" || env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "old-sonnet" || env["CLAUDE_CODE_USE_BEDROCK"] != "1" || env["FOO"] != "bar" {
		t.Fatalf("restored %#v env %#v", doc, env)
	}
	for _, key := range []string{
		"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
		"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY", "CLAUDE_CODE_USE_VERTEX",
		"CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS",
		"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD", "CLAUDE_CODE_USE_MANTLE",
	} {
		if _, ok := env[key]; ok {
			t.Fatalf("%s was not removed: %#v", key, env)
		}
	}
	if st := mustStatus(t, m, "claude"); st.State != "off" {
		t.Fatalf("after disconnect %#v", st)
	}
}

func TestClaudeConnectWritesModelPickerAndRestores(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, `{
  "model": "sonnet",
  "modelPicker": {
    "options": [
      {"description": "custom", "label": "Mine", "model": "mine"}
    ]
  }
}`)
	models := []Model{
		{ID: "deepseek-flash", Name: "DeepSeek Flash", Bare: "deepseek-flash"},
		{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", Bare: "deepseek-v4-pro"},
		{ID: "group/smart-split", Name: "Smart split", Bare: "smart-split"},
		{ID: "p1/private", Name: "Provider only", Bare: "private"},
	}
	if err := Connect(m, "claude", gw(), models); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	picker := claudePickerModels(t, doc)
	for _, id := range []string{"deepseek-flash", "deepseek-v4-pro", "group/smart-split", "p1/private", "mine"} {
		if _, ok := picker[id]; !ok {
			t.Fatalf("modelPicker lost %s: %#v", id, picker)
		}
	}
	for _, id := range []string{"deepseek-flash", "deepseek-v4-pro", "group/smart-split", "p1/private"} {
		if got := picker[id]["description"]; got != "via AIGate" {
			t.Fatalf("%s description %#v", id, got)
		}
	}
	if st := mustStatus(t, m, "claude"); st.State != "on" {
		t.Fatalf("status %#v", st)
	}

	if err := Disconnect(m, "claude"); err != nil {
		t.Fatal(err)
	}
	doc = readDoc(t, path)
	picker = claudePickerModels(t, doc)
	if len(picker) != 1 || picker["mine"] == nil {
		t.Fatalf("original modelPicker was not restored: %#v", picker)
	}
}

func TestClaudeDisconnectKeepsAChangedModelPicker(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, `{"model":"sonnet"}`)
	if err := Connect(m, "claude", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	picker, ok := doc["modelPicker"].(map[string]any)
	if !ok {
		t.Fatalf("modelPicker missing %#v", doc)
	}
	picker["options"] = append(picker["options"].([]any), map[string]any{
		"description": "custom", "label": "Mine", "model": "mine",
	})
	body, err := marshalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body))
	if st := mustStatus(t, m, "claude"); st.State != "drift" {
		t.Fatalf("status %#v", st)
	}
	if err := Disconnect(m, "claude"); err != nil {
		t.Fatal(err)
	}
	restored := claudePickerModels(t, readDoc(t, path))
	if restored["mine"] == nil {
		t.Fatalf("changed modelPicker was overwritten: %#v", restored)
	}
}

func TestClaudeDisconnectKeepsUserChanges(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, `{"model":"sonnet","env":{"ANTHROPIC_API_KEY":"sk-original","ANTHROPIC_DEFAULT_SONNET_MODEL":"old-sonnet"}}`)
	if err := Connect(m, "claude", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	env := claudeEnvOf(t, doc)
	env["ANTHROPIC_BASE_URL"] = "http://127.0.0.1:9999"
	env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = "user-sonnet"
	env["CLAUDE_CODE_USE_VERTEX"] = "1"
	env["ANTHROPIC_MODEL"] = "user-model"
	doc["model"] = "user-top-model"
	body, err := marshalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body))
	if st := mustStatus(t, m, "claude"); st.State != "drift" {
		t.Fatalf("status %#v", st)
	}
	if err := Disconnect(m, "claude"); err != nil {
		t.Fatal(err)
	}
	doc = readDoc(t, path)
	env = claudeEnvOf(t, doc)
	if doc["model"] != "user-top-model" || env["ANTHROPIC_BASE_URL"] != "http://127.0.0.1:9999" || env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "user-sonnet" || env["CLAUDE_CODE_USE_VERTEX"] != "1" || env["ANTHROPIC_MODEL"] != "user-model" {
		t.Fatalf("user values lost %#v env %#v", doc, env)
	}
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatalf("unchanged gateway key was not removed: %#v", env)
	}
	if env["ANTHROPIC_API_KEY"] != "sk-original" {
		t.Fatalf("unchanged empty value was not restored: %#v", env)
	}
}

func TestClaudeStatusNoticesAMissingGatewayGuard(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, `{"model":"sonnet"}`)
	for _, key := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_USE_VERTEX"} {
		if err := Connect(m, "claude", gw(), offers()); err != nil {
			t.Fatal(err)
		}
		doc := readDoc(t, path)
		env := claudeEnvOf(t, doc)
		if value, ok := env[key]; !ok || value != "" {
			t.Fatalf("%s after connect: %#v", key, env[key])
		}
		delete(env, key)
		body, err := marshalJSON(doc)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, string(body))
		if st := mustStatus(t, m, "claude"); st.State != "drift" {
			t.Fatalf("%s removed, status %#v", key, st)
		}
	}
}

func TestClaudeTierAliasIsNotDrift(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, `{"model":"sonnet"}`)
	models := []Model{
		{ID: "p1/main", Name: "Main", Bare: "main"},
		{ID: "p1/fable", Name: "Fable", Bare: "fable"},
	}
	if err := Connect(m, "claude", gw(), models); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	doc["model"] = "fable"
	body, err := marshalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body))
	if st := mustStatus(t, m, "claude"); st.State != "on" {
		t.Fatalf("tier alias state %#v", st)
	}
	env := claudeEnvOf(t, doc)
	env["ANTHROPIC_DEFAULT_FABLE_MODEL"] = "p1/other"
	body, err = marshalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body))
	if st := mustStatus(t, m, "claude"); st.State != "drift" {
		t.Fatalf("changed tier state %#v", st)
	}
	env["ANTHROPIC_DEFAULT_FABLE_MODEL"] = "p1/fable"
	body, err = marshalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body))
	if err := Disconnect(m, "claude"); err != nil {
		t.Fatal(err)
	}
	if doc = readDoc(t, path); doc["model"] != "fable" {
		t.Fatalf("user alias was overwritten: %#v", doc)
	}
}

func TestClaudePickerSelectionIsNotDrift(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, `{"model":"sonnet"}`)
	models := []Model{
		{ID: "deepseek-flash", Name: "DeepSeek Flash", Bare: "deepseek-flash"},
		{ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", Bare: "deepseek-v4-pro"},
	}
	if err := Connect(m, "claude", gw(), models); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	doc["model"] = "deepseek-v4-pro"
	body, err := marshalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body))
	st := mustStatus(t, m, "claude")
	if st.State != "on" || st.Model != "deepseek-v4-pro" {
		t.Fatalf("picker selection state %#v", st)
	}
	if err := Disconnect(m, "claude"); err != nil {
		t.Fatal(err)
	}
	if doc = readDoc(t, path); doc["model"] != "deepseek-v4-pro" {
		t.Fatalf("user picker choice was overwritten: %#v", doc)
	}
}

func TestClaudeSettingsWithUTF8BOMAreReadable(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, "\xef\xbb\xbf"+`{"model":"sonnet","env":{"FOO":"bar"}}`)
	if st := mustStatus(t, m, "claude"); st.State != "off" {
		t.Fatalf("status before connect %#v", st)
	}
	if err := Connect(m, "claude", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if st := mustStatus(t, m, "claude"); st.State != "on" {
		t.Fatalf("status after connect %#v", st)
	}
	if err := Disconnect(m, "claude"); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	env := claudeEnvOf(t, doc)
	if doc["model"] != "sonnet" || env["FOO"] != "bar" {
		t.Fatalf("restored %#v env %#v", doc, env)
	}
}

func TestClaudeUsesConfiguredDirectory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	configDir := filepath.Join(root, "claude-alt")
	m := Machine{
		Home:            home,
		StashDir:        filepath.Join(root, "stash"),
		ClaudeConfigDir: configDir,
		LookPath:        func(string) bool { return false },
	}
	altPath := filepath.Join(configDir, "settings.json")
	writeFile(t, altPath, `{"theme":"alt"}`)
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"theme":"home"}`)
	if err := Connect(m, "claude", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	alt := readDoc(t, altPath)
	homeDoc := readDoc(t, filepath.Join(home, ".claude", "settings.json"))
	if alt["theme"] != "alt" || alt["model"] != "p1/demo" {
		t.Fatalf("configured file %#v", alt)
	}
	if homeDoc["theme"] != "home" || homeDoc["model"] != nil {
		t.Fatalf("home file was changed %#v", homeDoc)
	}
	if st := mustStatus(t, m, "claude"); st.State != "on" || st.Where != altPath {
		t.Fatalf("status %#v", st)
	}
}

func TestClaudeLegacyNoteTakesMissingOriginalsFromTheFile(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, `{
  "model": "p1/demo",
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:3425",
    "ANTHROPIC_AUTH_TOKEN": "aigate-claude",
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
    "ANTHROPIC_MODEL": "legacy-model",
    "ANTHROPIC_API_KEY": "sk-old",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "my-sonnet",
    "CLAUDE_CODE_USE_BEDROCK": "1"
  }
}
`)
	writeFile(t, stashPath(m, "claude"), `{
  "fields": {
    "file": {"present": true},
    "env": {"present": true},
    "env.ANTHROPIC_BASE_URL": {"present": true, "value": "http://old.example"},
    "env.ANTHROPIC_AUTH_TOKEN": {"present": true, "value": "old-token"},
    "env.CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": {"present": true, "value": "0"},
    "model": {"present": true, "value": "sonnet"}
  },
  "written": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:3425",
    "ANTHROPIC_AUTH_TOKEN": "aigate-claude",
    "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
    "model": "p1/demo"
  }
}
`)
	if err := Connect(m, "claude", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if err := Disconnect(m, "claude"); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	env := claudeEnvOf(t, doc)
	if doc["model"] != "sonnet" || env["ANTHROPIC_BASE_URL"] != "http://old.example" || env["ANTHROPIC_AUTH_TOKEN"] != "old-token" || env["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] != "0" {
		t.Fatalf("old values lost %#v env %#v", doc, env)
	}
	if env["ANTHROPIC_MODEL"] != "legacy-model" || env["ANTHROPIC_API_KEY"] != "sk-old" || env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "my-sonnet" || env["CLAUDE_CODE_USE_BEDROCK"] != "1" {
		t.Fatalf("newly recorded values lost %#v", env)
	}
}

func TestConnectRefusesWhenThereAreNoModels(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, "{}\n")
	if err := Connect(m, "claude", gw(), nil); CodeOf(err) != "no_models" {
		t.Fatalf("connect %v", err)
	}
	if _, err := os.Stat(stashPath(m, "claude")); !os.IsNotExist(err) {
		t.Fatal("stash was written")
	}
	doc := readDoc(t, path)
	if len(doc) != 0 {
		t.Fatalf("file %#v", doc)
	}
}

func TestConnectRefusesWhenGatewayIsDown(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, "{\"theme\":\"dark\"}\n")
	if err := Connect(m, "claude", Target{}, offers()); CodeOf(err) != "not_listening" {
		t.Fatalf("connect %v", err)
	}
	doc := readDoc(t, path)
	if doc["theme"] != "dark" || doc["model"] != nil {
		t.Fatalf("file %#v", doc)
	}
}

func TestDriftReconnectKeepsTheOriginalValue(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, "{\"model\":\"sonnet\"}\n")
	if err := Connect(m, "claude", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	doc["model"] = "elsewhere"
	body, err := marshalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(body))
	if st := mustStatus(t, m, "claude"); st.State != "drift" {
		t.Fatalf("status %#v", st)
	}
	if err := Connect(m, "claude", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if readDoc(t, path)["model"] != "p1/demo" {
		t.Fatalf("reapplied %#v", readDoc(t, path))
	}
	if err := Disconnect(m, "claude"); err != nil {
		t.Fatal(err)
	}
	if readDoc(t, path)["model"] != "sonnet" {
		t.Fatalf("original lost %#v", readDoc(t, path))
	}
}

func TestUnreadableStashDoesNotChangeTheAgent(t *testing.T) {
	m := testMachine(t)
	path := claudePath(m)
	writeFile(t, path, "{\"model\":\"sonnet\"}\n")
	if err := Connect(m, "claude", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	wired, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, stashPath(m, "claude"), "{")
	if err := Disconnect(m, "claude"); CodeOf(err) != "unknown" {
		t.Fatalf("disconnect %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(wired) {
		t.Fatalf("file changed\n%s", after)
	}
}

func TestCodexConnectLeavesOtherTablesAndRestoresTheLine(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	orig := "# keep\nmodel = \"mine\" # mine\nmodel_provider = \"openai\"\n\n[model_providers.openai]\nname = \"OpenAI\"\nbase_url = \"https://api.openai.com/v1\"\n\n[notice]\nhide = true\n"
	writeFile(t, path, orig)
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	text := readString(t, path)
	if !strings.Contains(text, "# keep") || !strings.Contains(text, "name = \"OpenAI\"") || !strings.Contains(text, "[notice]") || !strings.Contains(text, "hide = true") {
		t.Fatalf("other tables lost\n%s", text)
	}
	if !strings.Contains(text, "model = \"p1/demo\"") || !strings.Contains(text, "[model_providers.aigate]") || !strings.Contains(text, "experimental_bearer_token = \"aigate-codex\"") {
		t.Fatalf("wiring missing\n%s", text)
	}
	wantCatalog := codexCatalogRef(m, codexCatalogPath(m))
	if !strings.Contains(text, "base_url = \"http://127.0.0.1:3425/v1\"") || !strings.Contains(text, "wire_api = \"responses\"") || !strings.Contains(text, "model_catalog_json = \""+wantCatalog+"\"") || strings.Contains(text, "requires_openai_auth") {
		t.Fatalf("provider table\n%s", text)
	}
	if !strings.Contains(text, "http_headers = { \"x-openai-actor-authorization\" = \"aigate\" }") || !strings.Contains(text, "openai_base_url = \"http://127.0.0.1:3425/backend-api/codex\"") {
		t.Fatalf("codex headers\n%s", text)
	}
	if !exists(codexCatalogPath(m)) {
		t.Fatal("catalog was not written")
	}
	text = strings.Replace(text, "[notice]\nhide = true", "[notice]\nhide = true\npersonality = \"pragmatic\"", 1)
	writeFile(t, path, text)
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	text = readString(t, path)
	if !strings.Contains(text, "model = \"mine\" # mine") || !strings.Contains(text, "model_provider = \"openai\"") {
		t.Fatalf("lines not restored\n%s", text)
	}
	if !strings.Contains(text, "[model_providers.aigate]") || !strings.Contains(text, "personality = \"pragmatic\"") || !strings.Contains(text, "# keep") {
		t.Fatalf("disconnect\n%s", text)
	}
	if strings.Contains(text, "openai_base_url") {
		t.Fatalf("gateway base URL left behind\n%s", text)
	}
	if exists(codexCatalogPath(m)) {
		t.Fatal("generated catalog left behind")
	}
}

func TestCodexCatalogAndProviderSettings(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, "model = \"old\"\nmodel_reasoning_effort = \"xhigh\"\n")
	vision := true
	models := []Model{{
		ID: "p1/reasoner", Name: "Reasoner", Context: 128000, Reasoning: true, Vision: &vision,
	}}
	if err := Connect(m, "codex", gw(), models); err != nil {
		t.Fatal(err)
	}
	text := readString(t, path)
	if !strings.Contains(text, "model_provider = \"aigate\"") || !strings.Contains(text, "model = \"p1/reasoner\"") {
		t.Fatalf("top-level\n%s", text)
	}
	catalog := readDoc(t, codexCatalogPath(m))
	list, _ := catalog["models"].([]any)
	if len(list) != 1 {
		t.Fatalf("catalog %#v", catalog)
	}
	entry, _ := list[0].(map[string]any)
	if entry["slug"] != "p1/reasoner" || entry["default_reasoning_level"] != "xhigh" || entry["context_window"] != json.Number("128000") {
		t.Fatalf("entry %#v", entry)
	}
	mods, _ := entry["input_modalities"].([]any)
	if len(mods) != 2 || mods[1] != "image" {
		t.Fatalf("modalities %#v", mods)
	}
}

func TestCodexCatalogCarriesProviderLevels(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, "model = \"old\"\nmodel_reasoning_effort = \"high\"\n")
	models := []Model{{
		ID: "p1/reasoner", Name: "Reasoner", Reasoning: true,
		ReasoningEfforts: []string{"low", "high"}, ReasoningEffortsKnown: true,
	}}
	if err := Connect(m, "codex", gw(), models); err != nil {
		t.Fatal(err)
	}
	catalog := readDoc(t, codexCatalogPath(m))
	list, _ := catalog["models"].([]any)
	entry, _ := list[0].(map[string]any)
	if entry["default_reasoning_level"] != "high" {
		t.Fatalf("default level %#v", entry)
	}
	levels, _ := entry["supported_reasoning_levels"].([]any)
	if len(levels) != 3 {
		t.Fatalf("levels %#v", levels)
	}
	for i, want := range []string{"low", "high", "ultra"} {
		row, _ := levels[i].(map[string]any)
		if row["effort"] != want {
			t.Fatalf("levels %#v", levels)
		}
	}
}

func TestCodexKeepsUserBaseURLTokenAndHeaders(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	orig := `model = "mine"
openai_base_url = "https://example.test/custom"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "aigate-shared"
requires_openai_auth = false
http_headers = { "x-org" = "acme" }

[model_providers.aigate.http_headers]
"x-extra" = "kept"
`
	writeFile(t, path, orig)
	if err := Connect(m, "codex", gw(), offers()); CodeOf(err) != "unknown" {
		t.Fatalf("conflicting header forms %v", err)
	}
	if readString(t, path) != orig {
		t.Fatalf("conflicting file changed\n%s", readString(t, path))
	}
	writeFile(t, path, `model = "mine"
openai_base_url = "https://example.test/custom"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "aigate-shared"
requires_openai_auth = false

[model_providers.aigate.http_headers]
"x-org" = "acme"
`)
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	text := readString(t, path)
	if !strings.Contains(text, "experimental_bearer_token = \"aigate-shared\"") || !strings.Contains(text, "requires_openai_auth = false") {
		t.Fatalf("user provider values lost\n%s", text)
	}
	if !strings.Contains(text, "openai_base_url = \"https://example.test/custom\"") || strings.Contains(text, "backend-api/codex") {
		t.Fatalf("user base URL changed\n%s", text)
	}
	if !strings.Contains(text, `"x-org" = "acme"`) || !strings.Contains(text, `"x-openai-actor-authorization" = "aigate"`) {
		t.Fatalf("user headers lost\n%s", text)
	}
	if strings.Count(text, "[model_providers.aigate.http_headers]") != 1 || strings.Count(text, "http_headers =") != 0 {
		t.Fatalf("duplicate headers\n%s", text)
	}
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	text = readString(t, path)
	if !strings.Contains(text, "openai_base_url = \"https://example.test/custom\"") || !strings.Contains(text, "[model_providers.aigate.http_headers]") {
		t.Fatalf("disconnect changed user values\n%s", text)
	}
}

func TestCodexRestoresAGatewayBaseURLItFound(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	const base = "http://127.0.0.1:3425/backend-api/codex"
	writeFile(t, path, "model = \"mine\"\nopenai_base_url = \""+base+"\"\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `openai_base_url = "`+base+`"`) {
		t.Fatalf("the base URL the user already had was removed\n%s", text)
	}
}

func TestCodexInlineHeadersKeepOtherHeaders(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, `model = "mine"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "aigate-codex"
http_headers = { "x-org" = "acme", "x-second" = "two" }
`)
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	text := readString(t, path)
	if !strings.Contains(text, `"x-org" = "acme"`) || !strings.Contains(text, `"x-second" = "two"`) || !strings.Contains(text, `"x-openai-actor-authorization" = "aigate"`) {
		t.Fatalf("inline headers lost\n%s", text)
	}
}

func TestCodexDottedHeadersKeepOtherHeaders(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, `model = "mine"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "aigate-codex"
http_headers."x-org" = "acme"
`)
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	text := readString(t, path)
	if !strings.Contains(text, `http_headers."x-org" = "acme"`) || !strings.Contains(text, `http_headers.x-openai-actor-authorization = "aigate"`) {
		t.Fatalf("dotted headers lost\n%s", text)
	}
	if strings.Contains(text, "http_headers =") {
		t.Fatalf("second header definition\n%s", text)
	}
}

func TestCodexRemovesAuthTrueAndKeepsFalse(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, `model = "mine"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "aigate-codex"
requires_openai_auth = true
`)
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); strings.Contains(text, "requires_openai_auth") {
		t.Fatalf("requires_openai_auth stayed\n%s", text)
	}
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); strings.Contains(text, "requires_openai_auth") {
		t.Fatalf("requires_openai_auth came back\n%s", text)
	}

	m = testMachine(t)
	path = codexPath(m)
	writeFile(t, path, `model = "mine"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "aigate-codex"
requires_openai_auth = false
`)
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, "requires_openai_auth = false") {
		t.Fatalf("user's false was removed\n%s", text)
	}
}

func TestCodexKeepsOnlyCredentialsTheGatewayTakes(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, `model = "mine"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "sk-issued"
`)
	gwWithKey := gw()
	gwWithKey.Keys = []string{"sk-issued"}
	if err := Connect(m, "codex", gwWithKey, offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `experimental_bearer_token = "sk-issued"`) {
		t.Fatalf("issued key was replaced\n%s", text)
	}

	m = testMachine(t)
	path = codexPath(m)
	writeFile(t, path, `model = "mine"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "sk-user"
`)
	gwWithKey = gw()
	gwWithKey.Keys = []string{"sk-issued"}
	if err := Connect(m, "codex", gwWithKey, offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `experimental_bearer_token = "sk-user"`) {
		t.Fatalf("loopback credential was replaced\n%s", text)
	}

	m = testMachine(t)
	path = codexPath(m)
	writeFile(t, path, `model = "mine"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "aigate-codex"
`)
	remote := gw()
	remote.Token = "shared-secret"
	if err := Connect(m, "codex", remote, offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `experimental_bearer_token = "shared-secret"`) {
		t.Fatalf("remote connection kept a local caller name\n%s", text)
	}

	m = testMachine(t)
	path = codexPath(m)
	writeFile(t, path, `model = "mine"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "sk-issued"
`)
	remote = gw()
	remote.Token = "shared-secret"
	remote.Keys = []string{"sk-issued"}
	if err := Connect(m, "codex", remote, offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `experimental_bearer_token = "sk-issued"`) {
		t.Fatalf("remote issued key was replaced\n%s", text)
	}

	m = testMachine(t)
	path = codexPath(m)
	orig := `model = "mine"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "sk-user"
`
	writeFile(t, path, orig)
	remote = Target{Origin: "http://192.0.2.10:3425"}
	if err := Connect(m, "codex", remote, offers()); CodeOf(err) != "no_credential" {
		t.Fatalf("remote without a token %v", err)
	}
	if text := readString(t, path); text != orig {
		t.Fatalf("refused remote connect changed the file\n%s", text)
	}
	remote.Keys = []string{"sk-shared"}
	if err := Connect(m, "codex", remote, offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `experimental_bearer_token = "sk-shared"`) || strings.Contains(text, `experimental_bearer_token = "aigate-codex"`) {
		t.Fatalf("remote connection kept a local caller name\n%s", text)
	}
}

func TestCodexStashDoesNotCopyARemoteToken(t *testing.T) {
	m := testMachine(t)
	writeFile(t, codexPath(m), "model = \"mine\"\n")
	remote := Target{Origin: "http://192.0.2.10:3425", Token: "shared-secret"}
	if err := Connect(m, "codex", remote, offers()); err != nil {
		t.Fatal(err)
	}
	stash := readString(t, stashPath(m, "codex"))
	if strings.Contains(stash, "shared-secret") {
		t.Fatalf("stash holds the remote token\n%s", stash)
	}
	if !strings.Contains(stash, secretDigest("shared-secret")) {
		t.Fatalf("stash does not hold the token fingerprint\n%s", stash)
	}
}

func TestCodexRemoteCatalogFollowsTheWrittenToken(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, `model = "mine"

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "sk-b"
`)
	remote := Target{
		Origin: "http://192.0.2.10:3425",
		Keys:   []string{"sk-a", "sk-b"},
		Allowed: map[string][]string{
			"sk-a": {"p1/demo"},
			"sk-b": {"route"},
		},
	}
	if err := Connect(m, "codex", remote, offers()); err != nil {
		t.Fatal(err)
	}
	text := readString(t, path)
	if !strings.Contains(text, `experimental_bearer_token = "sk-b"`) {
		t.Fatalf("the table's accepted token was replaced\n%s", text)
	}
	if !strings.Contains(text, `model = "route"`) {
		t.Fatalf("model does not follow the written token\n%s", text)
	}
	catalog := readDoc(t, codexCatalogPath(m))
	list, _ := catalog["models"].([]any)
	if len(list) != 1 {
		t.Fatalf("catalog was not filtered by the written token: %#v", catalog)
	}
	entry, _ := list[0].(map[string]any)
	if entry["slug"] != "route" {
		t.Fatalf("catalog kept a model the key cannot call: %#v", entry)
	}
}

func TestCodexExplicitRemoteTokenFiltersTheCatalog(t *testing.T) {
	m := testMachine(t)
	writeFile(t, codexPath(m), "model = \"mine\"\n")
	remote := Target{
		Origin:  "http://192.0.2.10:3425",
		Token:   "sk-shared",
		Allowed: map[string][]string{"sk-shared": {"p1/demo"}},
	}
	if err := Connect(m, "codex", remote, offers()); err != nil {
		t.Fatal(err)
	}
	catalog := readDoc(t, codexCatalogPath(m))
	list, _ := catalog["models"].([]any)
	if len(list) != 1 {
		t.Fatalf("catalog %#v", catalog)
	}
	entry, _ := list[0].(map[string]any)
	if entry["slug"] != "p1/demo" {
		t.Fatalf("entry %#v", entry)
	}
}

func TestCodexRemoteTokenWithNoModelWritesNothing(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	const orig = "model = \"mine\"\n"
	writeFile(t, path, orig)
	remote := Target{
		Origin:  "http://192.0.2.10:3425",
		Token:   "sk-shared",
		Allowed: map[string][]string{"sk-shared": {"not/offered"}},
	}
	if err := Connect(m, "codex", remote, offers()); CodeOf(err) != "no_models" {
		t.Fatalf("connect %v", err)
	}
	if text := readString(t, path); text != orig {
		t.Fatalf("refused connect changed the config\n%s", text)
	}
	if exists(codexCatalogPath(m)) {
		t.Fatal("refused connect wrote a catalog")
	}
	if exists(stashPath(m, "codex")) {
		t.Fatal("refused connect wrote a note")
	}
}

func TestCodexLeavesReasoningEffortToTheUser(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, "model = \"mine\"\nmodel_reasoning_effort = \"xhigh\"\n")
	models := []Model{{ID: "p1/reasoner", Name: "Reasoner", Reasoning: true}}
	if err := Connect(m, "codex", gw(), models); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, "model_reasoning_effort = \"xhigh\"") {
		t.Fatalf("connect changed the effort\n%s", text)
	}
	text := readString(t, path)
	text = strings.Replace(text, `model_reasoning_effort = "xhigh"`, `model_reasoning_effort = "low"`, 1)
	writeFile(t, path, text)
	if err := Connect(m, "codex", gw(), models); err != nil {
		t.Fatal(err)
	}
	catalog := readDoc(t, codexCatalogPath(m))
	list, _ := catalog["models"].([]any)
	entry, _ := list[0].(map[string]any)
	if entry["default_reasoning_level"] != "low" {
		t.Fatalf("catalog did not follow the current effort %#v", entry)
	}
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, "model_reasoning_effort = \"low\"") {
		t.Fatalf("disconnect restored the earlier effort\n%s", text)
	}

	m = testMachine(t)
	path = codexPath(m)
	writeFile(t, path, "model = \"mine\"\nmodel_reasoning_effort = \"xhigh\"\n")
	if err := Connect(m, "codex", gw(), models); err != nil {
		t.Fatal(err)
	}
	text = readString(t, path)
	text = strings.Replace(text, "model_reasoning_effort = \"xhigh\"\n", "", 1)
	writeFile(t, path, text)
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, "model_reasoning_effort = \"xhigh\"") {
		t.Fatalf("disconnect did not put back the effort the user left empty\n%s", text)
	}
}

func TestCodexRemoteCompactionCapability(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, "model = \"mine\"\n\n[features]\nasync_compaction = true\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `capabilities = { remote_compaction = "v2" }`) {
		t.Fatalf("background compaction has no capability\n%s", text)
	}

	m = testMachine(t)
	path = codexPath(m)
	writeFile(t, path, "model = \"mine\"\n\n[features.async_compaction]\nenabled = true\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `capabilities = { remote_compaction = "v2" }`) {
		t.Fatalf("table form has no capability\n%s", text)
	}

	m = testMachine(t)
	path = codexPath(m)
	writeFile(t, path, `model = "mine"

[features]
async_compaction = true

[model_providers.aigate]
name = "Mine"
base_url = "http://old"
wire_api = "responses"
experimental_bearer_token = "aigate-codex"
capabilities = { remote_compaction = "unsupported" }
`)
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `capabilities = { remote_compaction = "unsupported" }`) {
		t.Fatalf("user capability was replaced\n%s", text)
	}

	m = testMachine(t)
	path = codexPath(m)
	writeFile(t, path, "model = \"mine\"\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); strings.Contains(text, "capabilities") {
		t.Fatalf("capability written without the feature\n%s", text)
	}

	for _, body := range []string{
		"model = \"mine\"\nfeatures.async_compaction = true\n",
		"model = \"mine\"\nfeatures = { async_compaction = true }\n",
		"model = \"mine\"\n[features]\nasync_compaction = { enabled = true }\n",
	} {
		m = testMachine(t)
		path = codexPath(m)
		writeFile(t, path, body)
		if err := Connect(m, "codex", gw(), offers()); err != nil {
			t.Fatal(err)
		}
		if text := readString(t, path); !strings.Contains(text, `capabilities = { remote_compaction = "v2" }`) {
			t.Fatalf("background compaction shape has no capability\n%s\n%s", body, text)
		}
	}
}

func TestCodexRemoteCompactionCapabilityFollowsTheFeature(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, "model = \"mine\"\n\n[features]\nasync_compaction = true\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `capabilities = { remote_compaction = "v2" }`) {
		t.Fatalf("capability was not written\n%s", text)
	}
	text := readString(t, path)
	text = strings.Replace(text, "async_compaction = true", "async_compaction = false", 1)
	writeFile(t, path, text)
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); strings.Contains(text, "capabilities") {
		t.Fatalf("AIGate's capability stayed after the feature went off\n%s", text)
	}

	m = testMachine(t)
	path = codexPath(m)
	writeFile(t, path, "model = \"mine\"\n\n[features]\nasync_compaction = true\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	text = readString(t, path)
	text = strings.Replace(text, `capabilities = { remote_compaction = "v2" }`, `capabilities = { remote_compaction = "v2", custom = true }`, 1)
	text = strings.Replace(text, "async_compaction = true", "async_compaction = false", 1)
	writeFile(t, path, text)
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `capabilities = { remote_compaction = "v2", custom = true }`) {
		t.Fatalf("user capability was removed\n%s", text)
	}

	m = testMachine(t)
	path = codexPath(m)
	writeFile(t, path, "model = \"mine\"\n\n[features]\nasync_compaction = true\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	text = readString(t, path)
	text = strings.Replace(text, "async_compaction = true", "async_compaction = false", 1)
	writeFile(t, path, text)
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); strings.Contains(text, "capabilities") {
		t.Fatalf("disconnect kept AIGate's capability after the feature went off\n%s", text)
	}
}

func TestCodexMissingCatalogIsDriftAndReconnectRepairs(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, "model = \"mine\"\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(codexCatalogPath(m)); err != nil {
		t.Fatal(err)
	}
	if st := mustStatus(t, m, "codex"); st.State != "drift" {
		t.Fatalf("missing catalog state %#v", st)
	}
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if !exists(codexCatalogPath(m)) {
		t.Fatal("reconnect did not restore catalog")
	}
	if st := mustStatus(t, m, "codex"); st.State != "on" {
		t.Fatalf("repaired state %#v", st)
	}
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	if readString(t, path) == "" {
		t.Fatal("config disappeared")
	}
}

func TestCodexSyncRestoresAMissingCatalog(t *testing.T) {
	m := testMachine(t)
	writeFile(t, codexPath(m), "model = \"mine\"\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	catalogPath := codexCatalogPath(m)
	if err := os.Remove(catalogPath); err != nil {
		t.Fatal(err)
	}
	if err := SyncCodex(m); err != nil {
		t.Fatal(err)
	}
	if !exists(catalogPath) {
		t.Fatal("sync did not restore the catalog")
	}
	if st := mustStatus(t, m, "codex"); st.State != "on" {
		t.Fatalf("repaired state %#v", st)
	}
}

func TestCodexSyncRemovesADanglingCatalogKey(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, "model = \"mine\"\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	text := readString(t, path)
	text = strings.Replace(text, `model_provider = "aigate"`, `model_provider = "openai"`, 1)
	writeFile(t, path, text)
	if err := SyncCodex(m); err != nil {
		t.Fatal(err)
	}
	text = readString(t, path)
	if strings.Contains(text, "model_catalog_json") {
		t.Fatalf("dangling catalog key stayed\n%s", text)
	}
	if !strings.Contains(text, "[model_providers.aigate]") {
		t.Fatalf("provider table was removed\n%s", text)
	}
}

func TestCodexSyncDoesNotGuessFromAnUnreadableNote(t *testing.T) {
	m := testMachine(t)
	writeFile(t, codexPath(m), "model = \"mine\"\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	catalogPath := codexCatalogPath(m)
	if err := os.Remove(catalogPath); err != nil {
		t.Fatal(err)
	}
	writeFile(t, stashPath(m, "codex"), "{")
	if err := SyncCodex(m); CodeOf(err) != "unknown" {
		t.Fatalf("sync %v", err)
	}
	if exists(catalogPath) {
		t.Fatal("sync wrote from an unreadable note")
	}
}

func TestCodexKeepsUserCatalogFieldsAcrossReconnect(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, "model = \"mine\"\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	catalogPath := codexCatalogPath(m)
	doc := readDoc(t, catalogPath)
	list, _ := doc["models"].([]any)
	entry, _ := list[0].(map[string]any)
	entry["x_user"] = map[string]any{"keep": true}
	body, err := marshalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, catalogPath, string(body))
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	doc = readDoc(t, catalogPath)
	list, _ = doc["models"].([]any)
	entry, _ = list[0].(map[string]any)
	if entry["x_user"] == nil {
		t.Fatalf("user key lost %#v", entry)
	}
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	if !exists(catalogPath) {
		t.Fatal("catalog with user keys was removed")
	}
}

func TestCodexRestoresTheCatalogItFound(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	catalogPath := codexCatalogPath(m)
	original := `{
  "x_owner": {"name": "user"},
  "models": [
    {"slug": "mine/only", "base_instructions": "mine", "x_note": 9007199254740993}
  ]
}
`
	writeFile(t, path, "model = \"mine\"\nmodel_catalog_json = \"mine-models.json\"\n")
	writeFile(t, catalogPath, original)
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if readString(t, catalogPath) == original {
		t.Fatal("connect did not write its own catalog")
	}
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, catalogPath); text != original {
		t.Fatalf("the catalog the user had was not restored\n%s", text)
	}
	if text := readString(t, path); !strings.Contains(text, `model_catalog_json = "mine-models.json"`) {
		t.Fatalf("catalog reference was not restored\n%s", text)
	}
}

func TestCodexKeepsTopLevelCatalogFieldsAcrossReconnect(t *testing.T) {
	m := testMachine(t)
	writeFile(t, codexPath(m), "model = \"mine\"\n")
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	catalogPath := codexCatalogPath(m)
	doc := readDoc(t, catalogPath)
	doc["x_owner"] = map[string]any{"name": "user"}
	list, _ := doc["models"].([]any)
	entry, _ := list[0].(map[string]any)
	entry["x_entry"] = true
	body, err := marshalJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, catalogPath, string(body))
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	doc = readDoc(t, catalogPath)
	if doc["x_owner"] == nil {
		t.Fatalf("top-level user key lost on reconnect %#v", doc)
	}
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	if !exists(catalogPath) {
		t.Fatal("catalog with a top-level user key was removed")
	}
	if doc = readDoc(t, catalogPath); doc["x_owner"] == nil {
		t.Fatalf("top-level user key lost on disconnect %#v", doc)
	}
}

func TestCodexLegacyRecordKeepsTheCatalogItNeverStashed(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, `model = "p1/demo"
model_provider = "aigate"
model_catalog_json = "mine-models.json"

[model_providers.aigate]
name = "AIGate"
base_url = "http://127.0.0.1:3425/v1"
wire_api = "responses"
experimental_bearer_token = "aigate-codex"
`)
	if err := saveRecord(m, "codex", record{
		Fields: map[string]field{
			"file":           {Present: true},
			"model":          {Present: true, Value: "model = \"mine\""},
			"model_provider": {Present: true, Value: "model_provider = \"openai\""},
			"provider":       {Present: true, Value: "[model_providers.aigate]\nname = \"AIGate\""},
		},
		Written: map[string]string{
			"model":                     "p1/demo",
			"model_provider":            "aigate",
			"base_url":                  "http://127.0.0.1:3425/v1",
			"wire_api":                  "responses",
			"experimental_bearer_token": "aigate-codex",
			"name":                      "AIGate",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if st := mustStatus(t, m, "codex"); st.State != "drift" {
		t.Fatalf("legacy wiring state %#v", st)
	}
	if err := Connect(m, "codex", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if text := readString(t, path); !strings.Contains(text, `model_catalog_json = "`+codexCatalogName+`"`) {
		t.Fatalf("catalog was not wired\n%s", text)
	}
	if err := Disconnect(m, "codex"); err != nil {
		t.Fatal(err)
	}
	text := readString(t, path)
	if !strings.Contains(text, `model_catalog_json = "mine-models.json"`) || !strings.Contains(text, "model = \"mine\"") {
		t.Fatalf("legacy catalog or model was not restored\n%s", text)
	}
}

func TestCodexHandStatusNeedsACredentialTheGatewayTakes(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	writeFile(t, path, `model = "p1/demo"
model_provider = "aigate"

[model_providers.aigate]
name = "AIGate"
base_url = "http://127.0.0.1:3425/v1"
wire_api = "responses"
experimental_bearer_token = "sk-never-issued"
`)
	if st := mustStatus(t, m, "codex"); st.State != "hand" {
		t.Fatalf("loopback credential state %#v", st)
	}
	text := readString(t, path)
	writeFile(t, path, strings.Replace(text, "sk-never-issued", "aigate-codex", 1))
	if st := mustStatus(t, m, "codex"); st.State != "hand" {
		t.Fatalf("loopback token state %#v", st)
	}

	writeFile(t, path, `model = "p1/demo"
model_provider = "aigate"

[model_providers.aigate]
name = "AIGate"
base_url = "http://192.0.2.10:3425/v1"
wire_api = "responses"
experimental_bearer_token = "sk-never-issued"
`)
	remote := Target{Origin: "http://192.0.2.10:3425"}
	if st := statusFor(t, m, remote, "codex"); st.State != "off" {
		t.Fatalf("remote unknown token state %#v", st)
	}
	remote.Keys = []string{"sk-never-issued"}
	if st := statusFor(t, m, remote, "codex"); st.State != "hand" {
		t.Fatalf("remote issued token state %#v", st)
	}
}

func TestCodexConnectedStatusNeedsACredentialTheGatewayTakes(t *testing.T) {
	m := testMachine(t)
	writeFile(t, codexPath(m), "model = \"mine\"\n")
	remote := Target{Origin: "http://192.0.2.10:3425", Keys: []string{"sk-live"}}
	if err := Connect(m, "codex", remote, offers()); err != nil {
		t.Fatal(err)
	}
	if st := statusFor(t, m, remote, "codex"); st.State != "on" {
		t.Fatalf("connected state %#v", st)
	}
	remote.Keys = nil
	if st := statusFor(t, m, remote, "codex"); st.State != "drift" {
		t.Fatalf("revoked token state %#v", st)
	}
}

func TestCodexUnsafeFileIsNotOverwritten(t *testing.T) {
	m := testMachine(t)
	path := codexPath(m)
	orig := "model = \"\"\"\nx\n\"\"\"\n"
	writeFile(t, path, orig)
	if err := Connect(m, "codex", gw(), offers()); CodeOf(err) != "unknown" {
		t.Fatalf("connect %v", err)
	}
	if readString(t, path) != orig {
		t.Fatalf("file changed %q", readString(t, path))
	}
	writeFile(t, path, "model_providers.aigate.base_url = \"http://other\"\n")
	before := readString(t, path)
	if err := Connect(m, "codex", gw(), offers()); CodeOf(err) != "unknown" {
		t.Fatalf("dotted %v", err)
	}
	if readString(t, path) != before {
		t.Fatal("dotted file changed")
	}
}

func TestCodexAuthAloneIsNotInstalled(t *testing.T) {
	m := testMachine(t)
	writeFile(t, filepath.Join(m.Home, ".codex", "auth.json"), "{}\n")
	if st := mustStatus(t, m, "codex"); st.Installed {
		t.Fatalf("status %#v", st)
	}
}

func TestOpenCodeKeepsOtherProviders(t *testing.T) {
	m := testMachine(t)
	path := filepath.Join(m.Home, ".config", "opencode", "opencode.json")
	writeFile(t, path, "{\"theme\":\"dark\",\"model\":\"anthropic/claude\",\"provider\":{\"old\":{\"name\":\"Old\"}}}\n")
	if err := Connect(m, "opencode", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	if doc["theme"] != "dark" {
		t.Fatalf("theme %#v", doc)
	}
	old, _ := doc["provider"].(map[string]any)
	if old["old"] == nil {
		t.Fatalf("old provider %#v", doc["provider"])
	}
	if doc["model"] != "aigate/demo" {
		t.Fatalf("model %#v", doc["model"])
	}
	providers, _ := doc["providers"].(map[string]any)
	block, _ := providers["aigate"].(map[string]any)
	settings, _ := block["settings"].(map[string]any)
	if settings["baseURL"] != "http://127.0.0.1:3425/v1" || settings["apiKey"] != "aigate-opencode" || block["package"] != "@opencode/ai/providers/openai-compatible" {
		t.Fatalf("provider %#v", block)
	}
	models, _ := block["models"].(map[string]any)
	demo, _ := models["demo"].(map[string]any)
	if demo["modelID"] != "p1/demo" {
		t.Fatalf("model id %#v", models)
	}
	if err := Disconnect(m, "opencode"); err != nil {
		t.Fatal(err)
	}
	doc = readDoc(t, path)
	if doc["model"] != "anthropic/claude" || doc["theme"] != "dark" {
		t.Fatalf("restored %#v", doc)
	}
	providers, _ = doc["providers"].(map[string]any)
	if providers["aigate"] != nil {
		t.Fatalf("aigate left %#v", providers)
	}
}

func TestGeminiKeepsOtherEnvAndSettings(t *testing.T) {
	m := testMachine(t)
	m.LookPath = func(name string) bool { return name == "gemini" }
	writeFile(t, geminiEnvPath(m), "# comment\nOTHER=1\nGEMINI_MODEL=old\n")
	writeFile(t, geminiSettingsPath(m), "{\"ui\":{\"theme\":\"dark\"},\"model\":{\"name\":\"old\"}}\n")
	if err := Connect(m, "gemini", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	env := readString(t, geminiEnvPath(m))
	if !strings.Contains(env, "OTHER=1") || !strings.Contains(env, "# comment") || !strings.Contains(env, "GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3425") || !strings.Contains(env, "GEMINI_API_KEY=aigate-gemini") || !strings.Contains(env, "GEMINI_MODEL=demo") {
		t.Fatalf("env\n%s", env)
	}
	doc := readDoc(t, geminiSettingsPath(m))
	ui, _ := doc["ui"].(map[string]any)
	model, _ := doc["model"].(map[string]any)
	if ui["theme"] != "dark" || model["name"] != "demo" {
		t.Fatalf("settings %#v", doc)
	}
	if err := Disconnect(m, "gemini"); err != nil {
		t.Fatal(err)
	}
	env = readString(t, geminiEnvPath(m))
	if strings.Contains(env, "GOOGLE_GEMINI_BASE_URL") || strings.Contains(env, "aigate-gemini") || !strings.Contains(env, "OTHER=1") || !strings.Contains(env, "GEMINI_MODEL=old") {
		t.Fatalf("env restored\n%s", env)
	}
	model, _ = readDoc(t, geminiSettingsPath(m))["model"].(map[string]any)
	if model["name"] != "old" {
		t.Fatalf("model %#v", model)
	}
}

func TestGeminiFolderAloneIsNotInstalled(t *testing.T) {
	m := testMachine(t)
	writeFile(t, geminiSettingsPath(m), "{}\n")
	if st := mustStatus(t, m, "gemini"); st.Installed {
		t.Fatalf("status %#v", st)
	}
}

func TestAiderKeepsOtherKeys(t *testing.T) {
	m := testMachine(t)
	path := aiderPath(m)
	writeFile(t, path, "auto-commits: false\nmodel: gpt-4\n")
	if err := Connect(m, "aider", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	text := readString(t, path)
	if !strings.Contains(text, "auto-commits: false") || !strings.Contains(text, "openai-api-base: \"http://127.0.0.1:3425/v1\"") || !strings.Contains(text, "openai-api-key: \"aigate-aider\"") || !strings.Contains(text, "model: \"openai/p1/demo\"") {
		t.Fatalf("wired\n%s", text)
	}
	text = strings.Replace(text, "auto-commits: false", "auto-commits: false\nlint: true", 1)
	writeFile(t, path, text)
	if err := Disconnect(m, "aider"); err != nil {
		t.Fatal(err)
	}
	text = readString(t, path)
	if strings.Contains(text, "aigate") || strings.Contains(text, "openai-api-base") || !strings.Contains(text, "model: \"gpt-4\"") || !strings.Contains(text, "lint: true") {
		t.Fatalf("restored\n%s", text)
	}
}

func TestWorkBuddyKeepsExistingModels(t *testing.T) {
	m := testMachine(t)
	path := workbuddyPath(m)
	writeFile(t, path, `[
	  {"id":"mine","name":"Mine","vendor":"DeepSeek","url":"https://api.deepseek.com/chat/completions","apiKey":"sk-user-secret","supportsToolCall":true,"maxInputTokens":128000,"extra":{"keep":true}}
	]`)
	models := []Model{{
		ID: "p1/demo", Name: "Demo", Bare: "demo", Context: 32000, Reasoning: true,
		Vision: boolPtr(true),
	}}
	if err := Connect(m, "workbuddy", gw(), models); err != nil {
		t.Fatal(err)
	}
	list := readArray(t, path)
	if len(list) != 2 {
		t.Fatalf("len %d", len(list))
	}
	mine := list[0]
	if mine["apiKey"] != "sk-user-secret" || mine["url"] != "https://api.deepseek.com/chat/completions" {
		t.Fatalf("user model %#v", mine)
	}
	extra, _ := mine["extra"].(map[string]any)
	if extra["keep"] != true || mine["maxInputTokens"] != json.Number("128000") {
		t.Fatalf("user fields %#v", mine)
	}
	added := list[1]
	if added["id"] != "p1/demo" || added["vendor"] != "Custom" || added["apiKey"] != "aigate-workbuddy" || added["url"] != "http://127.0.0.1:3425/v1/chat/completions" || added["useCustomProtocol"] != false || added["supportsToolCall"] != true || added["supportsImages"] != true || added["supportsReasoning"] != true || added["maxInputTokens"] != float64(32000) && added["maxInputTokens"] != json.Number("32000") {
		t.Fatalf("added %#v", added)
	}
	st := mustStatus(t, m, "workbuddy")
	if st.State != "on" || strings.Contains(st.Note, "sk-user-secret") {
		t.Fatalf("status %#v", st)
	}
	if err := Disconnect(m, "workbuddy"); err != nil {
		t.Fatal(err)
	}
	list = readArray(t, path)
	if len(list) != 1 || list[0]["id"] != "mine" || list[0]["apiKey"] != "sk-user-secret" {
		t.Fatalf("restored %#v", list)
	}
}

func TestWorkBuddyObjectFormRestoresAvailableModels(t *testing.T) {
	m := testMachine(t)
	path := workbuddyPath(m)
	writeFile(t, path, `{"theme":"dark","models":[{"id":"mine","name":"Mine","url":"https://example.com/v1/chat/completions","apiKey":"sk-user-secret"}],"availableModels":["mine"]}`)
	if err := Connect(m, "workbuddy", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	doc := readDoc(t, path)
	if doc["theme"] != "dark" {
		t.Fatalf("theme %#v", doc["theme"])
	}
	names := doc["availableModels"].([]any)
	if len(names) != 3 || names[0] != "mine" {
		t.Fatalf("available %#v", names)
	}
	if err := Disconnect(m, "workbuddy"); err != nil {
		t.Fatal(err)
	}
	doc = readDoc(t, path)
	names = doc["availableModels"].([]any)
	if doc["theme"] != "dark" || len(names) != 1 || names[0] != "mine" {
		t.Fatalf("restored %#v", doc)
	}
	models := doc["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models %#v", models)
	}
}

func TestWorkBuddyTakenIDsAreNotOverwritten(t *testing.T) {
	m := testMachine(t)
	path := workbuddyPath(m)
	body := `[{"id":"p1/demo","apiKey":"sk-user-secret","url":"https://example.com/v1/chat/completions"},{"id":"route","apiKey":"sk-user-secret","url":"https://example.com/v1/chat/completions"}]`
	writeFile(t, path, body)
	if err := Connect(m, "workbuddy", gw(), offers()); CodeOf(err) != "id_taken" {
		t.Fatalf("connect %v", err)
	}
	if readString(t, path) != body {
		t.Fatalf("file changed %s", readString(t, path))
	}
}

func TestWorkBuddyBadFileIsNotOverwritten(t *testing.T) {
	m := testMachine(t)
	path := workbuddyPath(m)
	writeFile(t, path, `{"models":"nope"}`)
	if err := Connect(m, "workbuddy", gw(), offers()); CodeOf(err) != "unknown" {
		t.Fatalf("connect %v", err)
	}
	if readString(t, path) != `{"models":"nope"}` {
		t.Fatal("file changed")
	}
}

func TestWorkBuddyExeIsInstalled(t *testing.T) {
	m := testMachine(t)
	dir := filepath.Join(t.TempDir(), "Programs")
	writeFile(t, filepath.Join(dir, "WorkBuddy", "WorkBuddy.exe"), "")
	m.AppDirs = []string{dir}
	st := mustStatus(t, m, "workbuddy")
	if !st.Installed || st.State != "off" {
		t.Fatalf("status %#v", st)
	}
}

func TestCursorIsNotEdited(t *testing.T) {
	m := testMachine(t)
	m.LookPath = func(name string) bool { return name == "cursor" }
	before := listHome(t, m.Home)
	st := mustStatus(t, m, "cursor")
	if !st.Installed || st.State != "manual" || st.Support != "manual" || !strings.Contains(st.Note, "aigate-cursor") || !strings.Contains(st.Note, "http://127.0.0.1:3425/v1") {
		t.Fatalf("status %#v", st)
	}
	if err := Connect(m, "cursor", gw(), offers()); CodeOf(err) != "unsupported" {
		t.Fatalf("connect %v", err)
	}
	if err := Disconnect(m, "cursor"); CodeOf(err) != "unsupported" {
		t.Fatalf("disconnect %v", err)
	}
	if after := listHome(t, m.Home); after != before {
		t.Fatalf("home changed %q -> %q", before, after)
	}
}

func TestCreatedFileIsRemovedOnDisconnect(t *testing.T) {
	m := testMachine(t)
	m.LookPath = func(name string) bool { return name == "claude" }
	if err := Connect(m, "claude", gw(), offers()); err != nil {
		t.Fatal(err)
	}
	if !exists(claudePath(m)) {
		t.Fatal("settings were not created")
	}
	if err := Disconnect(m, "claude"); err != nil {
		t.Fatal(err)
	}
	if exists(claudePath(m)) {
		t.Fatalf("created file left behind: %s", readString(t, claudePath(m)))
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	doc, _, _, err := readJSONFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func claudeEnvOf(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	env, _ := doc["env"].(map[string]any)
	if env == nil {
		t.Fatalf("missing env in %#v", doc)
	}
	return env
}

func claudePickerModels(t *testing.T, doc map[string]any) map[string]map[string]any {
	t.Helper()
	picker, _ := doc["modelPicker"].(map[string]any)
	options, _ := picker["options"].([]any)
	out := map[string]map[string]any{}
	for _, item := range options {
		option, _ := item.(map[string]any)
		if option == nil {
			continue
		}
		if id, _ := option["model"].(string); id != "" {
			out[id] = option
		}
	}
	return out
}

func boolPtr(v bool) *bool { return &v }

func readArray(t *testing.T, path string) []map[string]any {
	t.Helper()
	doc, _, existed, err := loadWorkBuddy(path)
	if err != nil || !existed || doc.form != "array" {
		t.Fatalf("array err %v form %s", err, doc.form)
	}
	return doc.models
}

func listHome(t *testing.T, root string) string {
	t.Helper()
	var names []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(names, "\n")
}
