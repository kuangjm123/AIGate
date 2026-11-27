package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"unicode/utf8"

	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/state"
)

const claudeImageTokens = 1600

func isCountTokensPath(path string) bool {
	return path == "/v1/messages/count_tokens" || path == "/messages/count_tokens"
}

// countTokens answers Claude Code without dialing an upstream. The count is an
// estimate used for context and compaction decisions, not billing.
func (s *Server) countTokens(w http.ResponseWriter, r *http.Request, snap *state.Snapshot, admitted admission) int {
	body, tooBig := readBody(r.Body)
	if tooBig {
		writeProtocolError(w, ProtoAnthropic, http.StatusRequestEntityTooLarge, "too_large", "too_large")
		return http.StatusRequestEntityTooLarge
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil || doc == nil {
		writeProtocolError(w, ProtoAnthropic, http.StatusBadRequest, "bad_request", "bad_request")
		return http.StatusBadRequest
	}
	model := stripClaudeContextSuffix(jsonString(doc["model"]))
	if model == "" {
		writeProtocolError(w, ProtoAnthropic, http.StatusBadRequest, "bad_request", "bad_request")
		return http.StatusBadRequest
	}
	if code := gateModel(snap.Config, model, admitted.allow); code != "" {
		status := http.StatusNotFound
		switch code {
		case CodeModelDenied:
			status = http.StatusForbidden
		case CodeNotChat:
			status = http.StatusBadRequest
		}
		writeProtocolError(w, ProtoAnthropic, status, code, code)
		return status
	}
	if _, loginModel, ok := login.ParseID(model); ok && !state.AllowsModel(admitted.allow, model, loginModel) {
		writeProtocolError(w, ProtoAnthropic, http.StatusForbidden, CodeModelDenied, CodeModelDenied)
		return http.StatusForbidden
	}
	writeJSON(w, http.StatusOK, map[string]int{"input_tokens": estimateAnthropicInput(doc)})
	return http.StatusOK
}

func estimateAnthropicInput(doc map[string]json.RawMessage) int {
	chars, images := 0, 0
	var count func(json.RawMessage)
	count = func(raw json.RawMessage) {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return
		}
		if raw[0] == '"' {
			var text string
			if json.Unmarshal(raw, &text) == nil {
				chars += utf8.RuneCountInString(text)
			}
			return
		}
		var blocks []json.RawMessage
		if raw[0] == '[' && json.Unmarshal(raw, &blocks) == nil {
			for _, block := range blocks {
				count(block)
			}
			return
		}
		if raw[0] != '{' {
			return
		}
		var block struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			Name     string          `json:"name"`
			Content  json.RawMessage `json:"content"`
			Input    json.RawMessage `json:"input"`
		}
		if json.Unmarshal(raw, &block) != nil {
			return
		}
		switch block.Type {
		case "image":
			images++
		case "text":
			chars += utf8.RuneCountInString(block.Text)
		case "thinking":
			chars += utf8.RuneCountInString(block.Thinking)
		case "tool_use":
			chars += utf8.RuneCountInString(block.Name) + rawChars(block.Input)
		case "tool_result":
			count(block.Content)
		default:
			if block.Text != "" {
				chars += utf8.RuneCountInString(block.Text)
			}
			if len(block.Content) > 0 {
				count(block.Content)
			}
		}
	}
	count(doc["system"])
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(doc["messages"], &messages) == nil {
		for _, message := range messages {
			count(message.Content)
		}
	}
	chars += rawChars(doc["tools"])
	chars += rawChars(doc["tool_choice"])
	tokens := (chars + 3) / 4
	return tokens + images*claudeImageTokens
}

func rawChars(raw json.RawMessage) int {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) == nil {
		return utf8.RuneCount(compact.Bytes())
	}
	return utf8.RuneCount(raw)
}
