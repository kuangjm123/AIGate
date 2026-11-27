package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kuangjm123/AIGate/internal/state"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReasoningFitsChatBeforeTheProvider(t *testing.T) {
	var got map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
		Info: []state.ModelInfo{{
			ID: "demo", Reasoning: true,
			ReasoningEfforts: []string{"low", "medium", "high"}, ReasoningEffortsKnown: true,
		}},
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"demo","reasoning_effort":"off","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	if got["reasoning_effort"] != "low" {
		t.Fatalf("upstream reasoning_effort = %v\n%v", got["reasoning_effort"], got)
	}
}

func TestReasoningCrossProtocolWritesAdaptiveClaude(t *testing.T) {
	var got map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "anthropic", BaseURL: up.URL, Models: []string{"claude-opus-4-6"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "claude-opus-4-6", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"claude-opus-4-6","reasoning_effort":"xhigh","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	thinking, _ := got["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" {
		t.Fatalf("thinking = %v", got["thinking"])
	}
	if _, ok := thinking["budget_tokens"]; ok {
		t.Fatalf("budget kept: %v", thinking)
	}
	output, _ := got["output_config"].(map[string]any)
	if output["effort"] != "max" {
		t.Fatalf("effort = %v", got["output_config"])
	}
}

func TestReasoningResponsesUltraBecomesMax(t *testing.T) {
	var got map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"resp","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "responses", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
		Info: []state.ModelInfo{{
			ID: "demo", Reasoning: true,
			ReasoningEfforts: []string{"low", "high", "max", "ultra"}, ReasoningEffortsKnown: true,
		}},
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/responses",
		`{"model":"demo","reasoning":{"effort":"ultra"},"input":"hi"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	reasoning, _ := got["reasoning"].(map[string]any)
	if reasoning["effort"] != "max" {
		t.Fatalf("effort = %v", got["reasoning"])
	}
}

func TestReasoningGeminiBudgetCrossProtocol(t *testing.T) {
	var got map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "gemini", BaseURL: up.URL, Models: []string{"gemini-2.5-flash"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "gemini-2.5-flash", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"gemini-2.5-flash","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	config, _ := got["generationConfig"].(map[string]any)
	thinking, _ := config["thinkingConfig"].(map[string]any)
	if thinking["thinkingBudget"] != float64(24000) {
		t.Fatalf("thinking = %v", config)
	}
}

func TestReasoningGeminiLevelRejectionRetriesLowest(t *testing.T) {
	var calls atomic.Int32
	var levels []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var doc map[string]any
		_ = json.NewDecoder(r.Body).Decode(&doc)
		config, _ := doc["generationConfig"].(map[string]any)
		thinking, _ := config["thinkingConfig"].(map[string]any)
		level, _ := thinking["thinkingLevel"].(string)
		levels = append(levels, level)
		if level != "low" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"THINKING_LEVEL is not supported"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "gemini", BaseURL: up.URL, Models: []string{"gemini-3-flash"}, Credential: "k",
		Info: []state.ModelInfo{{
			ID: "gemini-3-flash", Reasoning: true,
			ReasoningEfforts: []string{"low", "medium", "high"}, ReasoningEffortsKnown: true,
		}},
	}}, []state.Route{{ID: "r1", ModelMatch: "gemini-3-flash", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost,
		"/v1beta/models/gemini-3-flash:generateContent",
		`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"medium"}}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s levels %v", rr.Code, rr.Body.String(), levels)
	}
	if calls.Load() != 2 || len(levels) != 2 || levels[0] != "medium" || levels[1] != "low" {
		t.Fatalf("calls %d levels %v", calls.Load(), levels)
	}
}

func TestReasoningGeminiLevelRejectionRetriesOnChatRelay(t *testing.T) {
	var calls atomic.Int32
	var levels []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var doc map[string]any
		_ = json.NewDecoder(r.Body).Decode(&doc)
		level, _ := doc["reasoning_effort"].(string)
		levels = append(levels, level)
		if level != "low" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"THINKING_LEVEL is not supported"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"gemini-3-flash"}, Credential: "k",
		Info: []state.ModelInfo{{
			ID: "gemini-3-flash", Reasoning: true,
			ReasoningEfforts: []string{"low", "medium", "high"}, ReasoningEffortsKnown: true,
		}},
	}}, []state.Route{{ID: "r1", ModelMatch: "gemini-3-flash", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions",
		`{"model":"gemini-3-flash","reasoning_effort":"medium","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s levels %v", rr.Code, rr.Body.String(), levels)
	}
	if calls.Load() != 2 || len(levels) != 2 || levels[0] != "medium" || levels[1] != "low" {
		t.Fatalf("calls %d levels %v", calls.Load(), levels)
	}
}

func TestReasoningGroupFixedGeminiAnthropicStartsThinking(t *testing.T) {
	var got map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "anthropic", BaseURL: up.URL, Models: []string{"gemini-4-hydrogen"}, Credential: "k",
	}}, nil)
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Groups = []state.Group{{ID: "g", Members: []string{"p1/gemini-4-hydrogen:high"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/messages",
		`{"model":"g","max_tokens":8192,"messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	thinking, _ := got["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" {
		t.Fatalf("thinking = %v", got["thinking"])
	}
	output, _ := got["output_config"].(map[string]any)
	if output["effort"] != "high" {
		t.Fatalf("effort = %v", got["output_config"])
	}
}

func TestReasoningGroupOverrides(t *testing.T) {
	var got map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	tests := []struct {
		name    string
		group   state.Group
		request string
		want    string
	}{
		{
			name:    "member fixed",
			group:   state.Group{ID: "g", Members: []string{"p1/demo:high"}},
			request: "low",
			want:    "high",
		},
		{
			name:    "member allowed",
			group:   state.Group{ID: "g", Members: []string{"p1/demo:low+high"}},
			request: "medium",
			want:    "high",
		},
		{
			name:    "group levels",
			group:   state.Group{ID: "g", Members: []string{"p1/demo"}, Levels: []string{"low", "high"}},
			request: "xhigh",
			want:    "high",
		},
		{
			name:    "member mapping",
			group:   state.Group{ID: "g", Members: []string{"p1/demo"}, ReasoningMap: map[string]string{"low": "high"}},
			request: "low",
			want:    "high",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, secret := dialStore(t, []state.Provider{{
				ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
				Info: []state.ModelInfo{{
					ID: "demo", Reasoning: true,
					ReasoningEfforts: []string{"low", "medium", "high"}, ReasoningEffortsKnown: true,
				}},
			}}, nil)
			if err := store.Update(func(cfg *state.Config) error {
				cfg.Groups = []state.Group{test.group}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/chat/completions",
				`{"model":"g","reasoning_effort":"`+test.request+`","messages":[{"role":"user","content":"hi"}]}`)
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d %s", rr.Code, rr.Body.String())
			}
			if got["reasoning_effort"] != test.want {
				t.Fatalf("reasoning_effort = %v, want %s", got["reasoning_effort"], test.want)
			}
		})
	}
}

func TestReasoningMappingUsesTheCurrentMember(t *testing.T) {
	got := reasoningMapping(map[string]string{
		"p1/demo/low":   "medium",
		"p2/other/high": "max",
		"high":          "max",
	}, "p1/demo:high")
	if got["low"] != "medium" || got["high"] != "max" {
		t.Fatalf("mapping = %v", got)
	}
	if _, ok := got["p2/other"]; ok {
		t.Fatalf("another member leaked: %v", got)
	}
	got = reasoningMapping(map[string]string{
		"p1/demo/high": "max",
		"high":         "low",
	}, "p1/demo:high")
	if got["high"] != "max" {
		t.Fatalf("member mapping did not override global mapping: %v", got)
	}
}

func TestReasoningDashScopeNonStreamUsesUpstreamStream(t *testing.T) {
	var upstream map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&upstream)
		body := strings.Join([]string{
			`data: {"choices":[{"delta":{"role":"assistant","content":"he"}}]}`,
			`data: {"choices":[{"delta":{"content":"llo"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
			`data: [DONE]`,
			``,
		}, "\n\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(bytes.NewBufferString(body)),
			Request:    r,
		}, nil
	})
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Models: []string{"qwen3"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "qwen3", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store, Transport: transport}, secret, http.MethodPost,
		"/v1/chat/completions",
		`{"model":"qwen3","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	if upstream["stream"] != true {
		t.Fatalf("upstream stream = %v", upstream["stream"])
	}
	if upstream["reasoning_effort"] != "high" {
		t.Fatalf("upstream effort = %v", upstream["reasoning_effort"])
	}
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &reply); err != nil {
		t.Fatalf("reply %s: %v", rr.Body.String(), err)
	}
	if len(reply.Choices) == 0 || reply.Choices[0].Message.Content != "hello" {
		t.Fatalf("reply = %s", rr.Body.String())
	}
}

func TestReasoningDashScopeAggregatedReplyCrossesDialects(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := strings.Join([]string{
			`data: {"choices":[{"delta":{"role":"assistant","content":"ok"}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
			`data: [DONE]`,
			``,
		}, "\n\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(bytes.NewBufferString(body)),
			Request:    r,
		}, nil
	})
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Models: []string{"qwen3"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "qwen3", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store, Transport: transport}, secret, http.MethodPost,
		"/v1/responses",
		`{"model":"qwen3","reasoning":{"effort":"high"},"input":"hi"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	var reply map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &reply); err != nil {
		t.Fatalf("reply %s: %v", rr.Body.String(), err)
	}
	if reply["object"] != "response" || !strings.Contains(rr.Body.String(), `"text":"ok"`) {
		t.Fatalf("reply = %s", rr.Body.String())
	}
}

func TestReasoningDashScopeAggregatedToolCalls(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := strings.Join([]string{
			`data: {"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`data: [DONE]`,
			``,
		}, "\n\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(bytes.NewBufferString(body)),
			Request:    r,
		}, nil
	})
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Models: []string{"qwen3"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "qwen3", ProviderIDs: []string{"p1"}}})
	rr := callGateway(t, &Server{Store: store, Transport: transport}, secret, http.MethodPost,
		"/v1/chat/completions",
		`{"model":"qwen3","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	var reply struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &reply); err != nil {
		t.Fatalf("reply %s: %v", rr.Body.String(), err)
	}
	if len(reply.Choices) != 1 || reply.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("reply = %s", rr.Body.String())
	}
	calls := reply.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Function.Name != "lookup" || calls[0].Function.Arguments != `{"q":"x"}` {
		t.Fatalf("tool calls = %+v", calls)
	}
}
