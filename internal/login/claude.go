package login

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Public Claude model ids from Claude Code's settings reference.
// The login is not sent to Anthropic to fetch this list.
var claudePublic = []Model{
	{ID: "claude-opus-4-8", Name: "Claude Opus 4.8"},
	{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6"},
	{ID: "claude-opus-4-6", Name: "Claude Opus 4.6"},
	{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
}

// ClaudeFunc runs the local Claude Code binary. A test supplies one.
// The production function does not put the subscription token on an HTTP request.
type ClaudeFunc func(ctx context.Context, home string, model string, body []byte) ([]byte, error)

func claudeDir(m Machine) string {
	if dir := strings.TrimSpace(m.ClaudeConfigDir); dir != "" {
		return dir
	}
	return filepath.Join(m.Home, ".claude")
}

func claudeCreds(m Machine) string {
	return filepath.Join(claudeDir(m), ".credentials.json")
}

func claudeSettings(m Machine) string {
	return filepath.Join(claudeDir(m), "settings.json")
}

func readClaude(m Machine) (Account, bool) {
	path := claudeCreds(m)
	raw, err := m.read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Account{}, false
		}
		return Account{ID: "claude", Name: "Claude Code", Readable: false}, true
	}
	raw = withoutUTF8BOM(raw)
	if m.plugin("claude") {
		var doc map[string]json.RawMessage
		if json.Unmarshal(raw, &doc) != nil {
			return Account{ID: "claude", Name: "Claude Code", Readable: false}, true
		}
		oauth := bytes.TrimSpace(doc["claudeAiOauth"])
		if len(oauth) == 0 || string(oauth) == "null" {
			return Account{}, false
		}
		return Account{ID: "claude", Name: "Claude Code", Plugin: true, Readable: true}, true
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return Account{ID: "claude", Name: "Claude Code", Readable: false}, true
	}
	oauth := doc["claudeAiOauth"]
	if len(bytes.TrimSpace(oauth)) == 0 || string(bytes.TrimSpace(oauth)) == "null" {
		return Account{}, false
	}
	var token struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(oauth, &token) != nil || strings.TrimSpace(token.AccessToken) == "" {
		return Account{ID: "claude", Name: "Claude Code", Readable: false}, true
	}
	models := append([]Model{}, claudePublic...)
	extras, known := claudeExtras(m)
	if known {
		seen := map[string]bool{}
		for _, model := range models {
			seen[model.ID] = true
		}
		for _, model := range extras {
			if !seen[model.ID] {
				models = append(models, model)
				seen[model.ID] = true
			}
		}
	}
	return Account{ID: "claude", Name: "Claude Code", Readable: true, ModelsKnown: known, Models: models}, true
}

func claudeExtras(m Machine) ([]Model, bool) {
	raw, err := m.read(claudeSettings(m))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true
		}
		return nil, false
	}
	raw = withoutUTF8BOM(raw)
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, true
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil, false
	}
	var out []Model
	if rawModels := doc["availableModels"]; len(rawModels) > 0 {
		var ids []string
		if json.Unmarshal(rawModels, &ids) != nil {
			return nil, false
		}
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" {
				out = append(out, Model{ID: id, Name: id})
			}
		}
	}
	if rawPicker := doc["modelPicker"]; len(rawPicker) > 0 {
		var picker struct {
			Options []struct {
				Model string `json:"model"`
			} `json:"options"`
		}
		if json.Unmarshal(rawPicker, &picker) != nil {
			return nil, false
		}
		for _, option := range picker.Options {
			if id := strings.TrimSpace(option.Model); id != "" {
				out = append(out, Model{ID: id, Name: id})
			}
		}
	}
	if rawOverrides := doc["modelOverrides"]; len(rawOverrides) > 0 {
		var overrides map[string]json.RawMessage
		if json.Unmarshal(rawOverrides, &overrides) != nil {
			return nil, false
		}
		for id := range overrides {
			if id = strings.TrimSpace(id); id != "" {
				out = append(out, Model{ID: id, Name: id})
			}
		}
	}
	return out, true
}

func withoutUTF8BOM(b []byte) []byte {
	return bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
}

// RunClaude starts the claude binary. The subscription token stays in Claude
// Code's own file. Tool calls in the first assistant message are returned and
// the process is stopped before Claude Code runs them.
func RunClaude(ctx context.Context, home, model string, body []byte) ([]byte, error) {
	return runClaude(ctx, home, os.Getenv("CLAUDE_CONFIG_DIR"), model, body)
}

func runClaude(ctx context.Context, home, configDir, model string, body []byte) ([]byte, error) {
	prompt := claudePrompt(body)
	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", model, "--output-format", "stream-json", "--verbose", "--include-partial-messages")
	cmd.Dir = home
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = claudeEnv(home, configDir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	reply, readErr := readClaudeStream(out)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if readErr != nil {
		return nil, readErr
	}
	return reply, nil
}

func claudeEnv(home, configDir string) []string {
	var env []string
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR":
			continue
		}
		env = append(env, item)
	}
	env = append(env, "HOME="+home)
	if dir := strings.TrimSpace(configDir); dir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+dir)
	}
	return env
}

func claudePrompt(body []byte) string {
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return string(body)
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(doc["messages"], &messages)
	var b strings.Builder
	for _, message := range messages {
		text := contentText(message.Content)
		if text == "" {
			continue
		}
		b.WriteString(message.Role)
		b.WriteString(": ")
		b.WriteString(text)
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return string(body)
	}
	return b.String()
}

func contentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, block := range blocks {
			if block.Type == "" || block.Type == "text" {
				b.WriteString(block.Text)
			}
		}
		return b.String()
	}
	return ""
}

func readClaudeStream(r io.Reader) ([]byte, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Content []json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type != "assistant" {
			continue
		}
		return anthropicFromBlocks(event.Message.Content), nil
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func anthropicFromBlocks(blocks []json.RawMessage) []byte {
	var content []any
	stop := "end_turn"
	for _, raw := range blocks {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &kind) != nil {
			continue
		}
		switch kind.Type {
		case "text":
			var text struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(raw, &text)
			content = append(content, map[string]any{"type": "text", "text": text.Text})
		case "tool_use":
			stop = "tool_use"
			var tool struct {
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(raw, &tool)
			input := tool.Input
			if len(input) == 0 {
				input = []byte("{}")
			}
			content = append(content, map[string]any{"type": "tool_use", "id": tool.ID, "name": tool.Name, "input": json.RawMessage(input)})
		}
	}
	if content == nil {
		content = []any{}
	}
	body, _ := json.Marshal(map[string]any{
		"id": "msg_claude_code", "type": "message", "role": "assistant",
		"model": "", "content": content, "stop_reason": stop,
	})
	return body
}
