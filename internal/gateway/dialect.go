package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/kuangjm123/AIGate/internal/state"
)

// Protocol is the application dialect named by the request path.
type Protocol string

const (
	ProtoChat      Protocol = "chat"
	ProtoResponses Protocol = "responses"
	ProtoAnthropic Protocol = "anthropic"
	ProtoGemini    Protocol = "gemini"
	ProtoOllama    Protocol = "ollama"
)

// Classify reports the dialect of an application request.
// A catalog read is reported as chat, responses' list, ollama, or gemini
// according to the path. Other methods and paths are not dialects.
func Classify(r *http.Request) (Protocol, bool) {
	if r == nil || r.URL == nil {
		return "", false
	}
	path := r.URL.Path
	switch r.Method {
	case http.MethodGet:
		switch path {
		case "/v1/models":
			return ProtoChat, true
		case "/api/tags":
			return ProtoOllama, true
		case "/v1beta/models":
			return ProtoGemini, true
		default:
			return "", false
		}
	case http.MethodPost:
	default:
		return "", false
	}
	switch path {
	case "/v1/chat/completions":
		return ProtoChat, true
	case "/v1/responses":
		return ProtoResponses, true
	case "/v1/messages":
		return ProtoAnthropic, true
	case "/api/chat":
		return ProtoOllama, true
	}
	if model, method, ok := geminiCall(path); ok && model != "" && (method == "generateContent" || method == "streamGenerateContent") {
		return ProtoGemini, true
	}
	return "", false
}

func geminiCall(path string) (model, method string, ok bool) {
	rest, found := strings.CutPrefix(path, "/v1beta/models/")
	if !found || rest == "" || strings.Contains(rest, "/") {
		return "", "", false
	}
	model, method, found = strings.Cut(rest, ":")
	if !found || model == "" || method == "" || strings.Contains(method, "/") {
		return "", "", false
	}
	unescaped, err := url.PathUnescape(model)
	if err != nil || unescaped == "" {
		return "", "", false
	}
	return unescaped, method, true
}

// Parsed is the inbound request the chain selects a provider for.
// Tools is the canonical tool declaration. An empty Tools means the request
// declared none.
type Parsed struct {
	Protocol Protocol
	Model    string
	Stream   bool
	Messages []turn
	Specs    []toolSpec
	Tools    []byte
	Raw      []byte
}

type toolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type toolCall struct {
	ID        string
	Name      string
	Arguments string
}

type toolResult struct {
	ID      string
	Name    string
	Content string
}

type turn struct {
	Role      string
	Text      string
	Reasoning string
	Calls     []toolCall
	Result    *toolResult
}

// Parse reads a dialect body. The Gemini model and stream flag come from the path.
func Parse(proto Protocol, r *http.Request, body []byte) (Parsed, error) {
	p := Parsed{Protocol: proto, Raw: append([]byte(nil), body...)}
	if !json.Valid(body) || len(bytes.TrimSpace(body)) == 0 || body[0] != '{' {
		return Parsed{}, errBadBody
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return Parsed{}, errBadBody
	}
	p.Tools = canonicalTools(doc, proto)
	switch proto {
	case ProtoChat:
		p.Model = jsonString(doc["model"])
		p.Stream = jsonBool(doc["stream"])
		p.Messages = chatTurns(doc["messages"])
	case ProtoResponses:
		p.Model = jsonString(doc["model"])
		p.Stream = jsonBool(doc["stream"])
		p.Messages = responseTurns(doc["input"])
	case ProtoAnthropic:
		p.Model = jsonString(doc["model"])
		p.Stream = jsonBool(doc["stream"])
		p.Messages = append(systemTurn(jsonText(doc["system"])), anthropicTurns(doc["messages"])...)
	case ProtoOllama:
		p.Model = jsonString(doc["model"])
		p.Stream = jsonBool(doc["stream"])
		p.Messages = chatTurns(doc["messages"])
	case ProtoGemini:
		model, method, ok := geminiCall(r.URL.Path)
		if !ok {
			return Parsed{}, errBadBody
		}
		p.Model = model
		p.Stream = method == "streamGenerateContent" || r.URL.Query().Get("alt") == "sse"
		p.Messages = append(systemTurn(geminiText(doc["systemInstruction"])), geminiTurns(doc["contents"])...)
	default:
		return Parsed{}, errBadBody
	}
	if strings.TrimSpace(p.Model) == "" {
		return Parsed{}, errBadBody
	}
	enrichParsed(&p, doc)
	return p, nil
}

// sameShape reports whether next keeps the model, stream flag, and tool declaration.
func sameShape(before, next Parsed) bool {
	return before.Model == next.Model && before.Stream == next.Stream && bytes.Equal(before.Tools, next.Tools)
}

func canonicalTools(doc map[string]json.RawMessage, proto Protocol) []byte {
	keys := []string{"tools", "tool_choice"}
	if proto == ProtoGemini {
		keys = []string{"tools", "toolConfig"}
	}
	var b bytes.Buffer
	for _, key := range keys {
		raw, ok := doc[key]
		if !ok || len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		if key == "tools" && bytes.Equal(bytes.TrimSpace(raw), []byte("[]")) {
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			compact.Write(bytes.TrimSpace(raw))
		}
		b.WriteString(key)
		b.WriteByte('=')
		b.Write(compact.Bytes())
		b.WriteByte(';')
	}
	return b.Bytes()
}

func hasTools(tools []byte) bool {
	return bytes.Contains(tools, []byte("tools="))
}

func jsonString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func jsonBool(raw json.RawMessage) bool {
	var v bool
	_ = json.Unmarshal(raw, &v)
	return v
}

func jsonText(raw json.RawMessage) string {
	if s := jsonString(raw); s != "" {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, block := range blocks {
		if block.Type == "" || block.Type == "text" {
			b.WriteString(block.Text)
		}
	}
	return b.String()
}

func systemTurn(text string) []turn {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return []turn{{Role: "system", Text: text}}
}

func chatTurns(raw json.RawMessage) []turn {
	var rows []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return nil
	}
	out := make([]turn, 0, len(rows))
	for _, row := range rows {
		text := partText(row.Content)
		if text == "" {
			continue
		}
		role := row.Role
		if role == "" {
			role = "user"
		}
		out = append(out, turn{Role: role, Text: text})
	}
	return out
}

func partText(raw json.RawMessage) string {
	if s := jsonString(raw); s != "" {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		if part.Type == "" || part.Type == "text" || part.Type == "input_text" || part.Type == "output_text" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func responseTurns(raw json.RawMessage) []turn {
	if s := jsonString(raw); s != "" {
		return []turn{{Role: "user", Text: s}}
	}
	return chatTurns(raw)
}

func anthropicTurns(raw json.RawMessage) []turn {
	return chatTurns(raw)
}

func geminiText(raw json.RawMessage) string {
	var doc struct {
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range doc.Parts {
		b.WriteString(part.Text)
	}
	return b.String()
}

func geminiTurns(raw json.RawMessage) []turn {
	var rows []struct {
		Role  string `json:"role"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}
	if json.Unmarshal(raw, &rows) != nil {
		return nil
	}
	out := make([]turn, 0, len(rows))
	for _, row := range rows {
		var b strings.Builder
		for _, part := range row.Parts {
			b.WriteString(part.Text)
		}
		if b.Len() == 0 {
			continue
		}
		role := "user"
		if row.Role == "model" || row.Role == "assistant" {
			role = "assistant"
		}
		out = append(out, turn{Role: role, Text: b.String()})
	}
	return out
}

// providerProtocol maps a stored provider protocol onto a dialect.
// An empty protocol means the provider speaks the caller's dialect.
// An unknown name is not a dialect.
func providerProtocol(stored string, inbound Protocol) (Protocol, bool) {
	switch strings.ToLower(strings.TrimSpace(stored)) {
	case "":
		return inbound, true
	case "chat", "openai", "openai-chat", "chat-completions":
		return ProtoChat, true
	case "responses", "openai-responses":
		return ProtoResponses, true
	case "anthropic", "messages":
		return ProtoAnthropic, true
	case "gemini":
		return ProtoGemini, true
	case "ollama":
		return ProtoOllama, true
	default:
		return "", false
	}
}

// outboundBody is the JSON sent to one provider.
// The same dialect keeps the caller's body, with the upstream model name.
// A different dialect is translated, including tool declarations, tool calls, and reasoning text.
// upstream is the model name that provider lists. The caller's catalog id stays on the reply.
func outboundBody(inbound Parsed, raw []byte, dest Protocol, upstream string) ([]byte, error) {
	if upstream == "" {
		upstream = inbound.Model
	}
	if dest == inbound.Protocol {
		return withModel(raw, upstream), nil
	}
	if hasTools(inbound.Tools) && len(inbound.Specs) == 0 {
		return nil, errTools
	}
	return translatedBody(inbound, dest, upstream)
}

func marshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// replyText reads the assistant text from a provider body in its dialect.
func replyText(proto Protocol, body []byte) string {
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	switch proto {
	case ProtoChat:
		var choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Delta struct {
				Content json.RawMessage `json:"content"`
			} `json:"delta"`
		}
		_ = json.Unmarshal(doc["choices"], &choices)
		if len(choices) == 0 {
			return ""
		}
		if text := partText(choices[0].Message.Content); text != "" {
			return text
		}
		return partText(choices[0].Delta.Content)
	case ProtoResponses:
		var output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.Unmarshal(doc["output"], &output)
		var b strings.Builder
		for _, item := range output {
			for _, part := range item.Content {
				if part.Type == "" || part.Type == "output_text" || part.Type == "text" {
					b.WriteString(part.Text)
				}
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
		return jsonString(doc["delta"])
	case ProtoAnthropic:
		return jsonText(doc["content"])
	case ProtoOllama:
		var msg struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(doc["message"], &msg)
		if msg.Content != "" {
			return msg.Content
		}
		return jsonString(doc["response"])
	case ProtoGemini:
		var candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		}
		_ = json.Unmarshal(doc["candidates"], &candidates)
		var b strings.Builder
		for _, cand := range candidates {
			for _, part := range cand.Content.Parts {
				if part.Thought {
					continue
				}
				b.WriteString(part.Text)
			}
		}
		return b.String()
	default:
		return ""
	}
}

// clientBody renders assistant text in the caller's dialect.
func clientBody(proto Protocol, model, text string) []byte {
	switch proto {
	case ProtoAnthropic:
		b, _ := json.Marshal(map[string]any{
			"id": "msg_aigate", "type": "message", "role": "assistant", "model": model,
			"content": []map[string]string{{"type": "text", "text": text}}, "stop_reason": "end_turn",
		})
		return b
	case ProtoResponses:
		b, _ := json.Marshal(map[string]any{
			"id": "resp_aigate", "object": "response", "status": "completed", "model": model,
			"output": []map[string]any{{
				"type": "message", "role": "assistant",
				"content": []map[string]string{{"type": "output_text", "text": text}},
			}},
		})
		return b
	case ProtoOllama:
		b, _ := json.Marshal(map[string]any{
			"model": model, "done": true,
			"message": map[string]string{"role": "assistant", "content": text},
		})
		return b
	case ProtoGemini:
		b, _ := json.Marshal(map[string]any{
			"candidates": []map[string]any{{
				"content": map[string]any{"role": "model", "parts": []map[string]string{{"text": text}}},
			}},
		})
		return b
	default:
		b, _ := json.Marshal(map[string]any{
			"id": "chatcmpl_aigate", "object": "chat.completion", "model": model,
			"choices": []map[string]any{{
				"index": 0, "finish_reason": "stop",
				"message": map[string]string{"role": "assistant", "content": text},
			}},
		})
		return b
	}
}

// deltaText reads one streamed event's new text in the provider dialect.
func deltaText(proto Protocol, data []byte) string {
	text := replyText(proto, data)
	if text != "" {
		return text
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		return ""
	}
	if proto == ProtoResponses {
		return jsonString(doc["delta"])
	}
	if proto == ProtoAnthropic {
		var delta struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(doc["delta"], &delta)
		return delta.Text
	}
	return ""
}

// clientEvent is one SSE data payload in the caller's dialect.
func clientEvent(proto Protocol, model, text string, done bool) []byte {
	if done {
		switch proto {
		case ProtoChat:
			return []byte("[DONE]")
		case ProtoOllama:
			b, _ := json.Marshal(map[string]any{"model": model, "done": true, "message": map[string]string{"role": "assistant", "content": ""}})
			return b
		case ProtoResponses:
			b, _ := json.Marshal(map[string]any{"type": "response.completed", "response": json.RawMessage(clientBody(proto, model, text))})
			return b
		default:
			return nil
		}
	}
	switch proto {
	case ProtoAnthropic:
		b, _ := json.Marshal(map[string]any{"type": "content_block_delta", "delta": map[string]string{"type": "text_delta", "text": text}})
		return b
	case ProtoResponses:
		b, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "delta": text})
		return b
	case ProtoOllama:
		b, _ := json.Marshal(map[string]any{"model": model, "done": false, "message": map[string]string{"role": "assistant", "content": text}})
		return b
	case ProtoGemini:
		b, _ := json.Marshal(map[string]any{"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": []map[string]string{{"text": text}}}}}})
		return b
	default:
		b, _ := json.Marshal(map[string]any{"object": "chat.completion.chunk", "model": model, "choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": text}}}})
		return b
	}
}

func endpoint(base string, proto Protocol, model string, stream bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errBadBody
	}
	suffix := ""
	switch proto {
	case ProtoChat:
		suffix = "/v1/chat/completions"
	case ProtoResponses:
		suffix = "/v1/responses"
	case ProtoAnthropic:
		suffix = "/v1/messages"
	case ProtoOllama:
		suffix = "/api/chat"
	case ProtoGemini:
		method := "generateContent"
		if stream {
			method = "streamGenerateContent"
		}
		suffix = "/v1beta/models/" + url.PathEscape(model) + ":" + method
	default:
		return "", errBadBody
	}
	u.Path = joinUnder(u.Path, suffix)
	if proto == ProtoGemini && stream {
		q := u.Query()
		q.Set("alt", "sse")
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func joinUnder(basePath, suffix string) string {
	basePath = strings.TrimRight(basePath, "/")
	for _, prefix := range []string{"/v1beta", "/v1", "/api"} {
		if strings.HasPrefix(suffix, prefix+"/") && (basePath == prefix || strings.HasSuffix(basePath, prefix)) {
			return basePath + strings.TrimPrefix(suffix, prefix)
		}
	}
	if basePath == "" {
		return suffix
	}
	return basePath + suffix
}

type catalogEntry struct {
	ID        string
	Name      string
	Owner     string
	Bare      string
	Context   int
	Reasoning bool
	Vision    *bool
}

func catalog(cfg state.Config, kind string) []catalogEntry {
	if kind == "" {
		kind = "chat"
	}
	routed := map[string]bool{}
	for _, route := range cfg.Routes {
		if route.ModelMatch != "" {
			routed[route.ModelMatch] = true
		}
	}
	seen := map[string]bool{}
	var out []catalogEntry
	add := func(entry catalogEntry) {
		entry.ID = strings.TrimSpace(entry.ID)
		if entry.ID == "" || seen[entry.ID] {
			return
		}
		if entry.Name == "" {
			entry.Name = entry.ID
		}
		seen[entry.ID] = true
		out = append(out, entry)
	}
	if kind == "chat" {
		for _, route := range cfg.Routes {
			owner := route.ID
			if owner == "" {
				owner = "aigate"
			}
			add(catalogEntry{ID: route.ModelMatch, Owner: owner, Bare: route.ModelMatch})
		}
	}
	for _, provider := range cfg.Providers {
		owner := provider.ID
		if owner == "" {
			owner = "aigate"
		}
		for _, model := range provider.Models {
			info, _ := provider.InfoFor(model)
			if info.Hidden {
				continue
			}
			modelKind := info.Kind
			if modelKind == "" {
				modelKind = "chat"
			}
			if modelKind != kind {
				continue
			}
			if kind == "chat" && routed[model] {
				continue
			}
			name := info.DisplayName
			if name == "" {
				name = model
			}
			add(catalogEntry{
				ID: owner + "/" + model, Name: name, Owner: owner, Bare: model,
				Context: info.Context, Reasoning: info.Reasoning, Vision: info.Vision,
			})
		}
	}
	if out == nil {
		out = []catalogEntry{}
	}
	return out
}

func writeCatalog(w http.ResponseWriter, proto Protocol, cfg state.Config, allow []string, kind string) {
	entries := catalog(cfg, kind)
	filtered := entries[:0]
	for _, entry := range entries {
		if state.AllowsModel(allow, entry.ID, entry.Bare) {
			filtered = append(filtered, entry)
		}
	}
	entries = filtered
	switch proto {
	case ProtoOllama:
		models := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			models = append(models, map[string]any{"name": entry.ID, "model": entry.ID})
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
	case ProtoGemini:
		models := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			models = append(models, map[string]any{"name": "models/" + entry.ID, "displayName": entry.Name})
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
	default:
		data := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			row := map[string]any{"id": entry.ID, "object": "model", "owned_by": entry.Owner, "display_name": entry.Name}
			if entry.Context > 0 {
				row["context_window"] = entry.Context
			}
			if entry.Reasoning {
				row["reasoning"] = true
			}
			if entry.Vision != nil {
				row["vision"] = *entry.Vision
			}
			data = append(data, row)
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	}
}

func writeProtocolError(w http.ResponseWriter, proto Protocol, status int, code, message string) {
	if message == "" {
		message = code
	}
	switch proto {
	case ProtoAnthropic:
		writeJSON(w, status, map[string]any{"type": "error", "error": map[string]string{"type": code, "message": message}})
	case ProtoGemini:
		writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "status": http.StatusText(status)}})
	case ProtoOllama:
		writeJSON(w, status, map[string]string{"error": message})
	default:
		writeJSON(w, status, map[string]any{"error": map[string]string{"message": message, "type": "invalid_request_error", "code": code}})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
