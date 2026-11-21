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
	for _, st := range Inspect(m, gw()) {
		if st.ID == id {
			return st
		}
	}
	t.Fatalf("missing %s", id)
	return Status{}
}

func TestOffersSkipHiddenAndNonChat(t *testing.T) {
	cfg := state.Config{
		Routes: []state.Route{{ID: "r", ModelMatch: "routed"}},
		Providers: []state.Provider{{
			ID: "p", Models: []string{"routed", "chat", "pic", "gone"},
			Info: []state.ModelInfo{
				{ID: "pic", Kind: "image"},
				{ID: "gone", Hidden: true},
			},
		}},
	}
	got := Offers(cfg)
	if len(got) != 2 || got[0].ID != "routed" || got[1].ID != "p/chat" || got[1].Bare != "chat" {
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
	if !strings.Contains(text, "base_url = \"http://127.0.0.1:3425/v1\"") || !strings.Contains(text, "wire_api = \"responses\"") {
		t.Fatalf("provider table\n%s", text)
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
	if strings.Contains(text, "model_providers.aigate") || !strings.Contains(text, "personality = \"pragmatic\"") || !strings.Contains(text, "# keep") {
		t.Fatalf("disconnect\n%s", text)
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
