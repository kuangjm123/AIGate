package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

const probeTimeout = 20 * time.Second

// ProbeResult is one saved provider checked with a single short request.
// Error is a fixed code. It does not contain the provider key or the reply body.
type ProbeResult struct {
	OK       bool
	Status   int
	Protocol string
	Model    string
	Error    string
}

// Probe dials prov the way a model request would, with the first conversation
// model and a one-token reply. An empty protocol is tried as chat.
// It does not record usage. A base URL the gateway would not dial is not dialed.
func (s *Server) Probe(ctx context.Context, prov state.Provider, consoleListen string) ProbeResult {
	proto, known := providerProtocol(prov.Protocol, ProtoChat)
	if !known {
		return ProbeResult{Error: CodeUnknown}
	}
	model, ok := probeModel(prov)
	if !ok {
		return ProbeResult{Protocol: string(proto), Error: "model"}
	}
	out := ProbeResult{Protocol: string(proto), Model: model}
	if len(prov.Keys()) == 0 {
		out.Error = "credential"
		return out
	}
	switch security.CheckBaseURL(prov.BaseURL, consoleListen, "") {
	case security.BaseOK:
	case security.BaseUnknown:
		out.Error = CodeUnknown
		return out
	default:
		out.Error = CodeBaseURL
		return out
	}
	body, err := probeBody(proto, model)
	if err != nil {
		out.Error = CodeUnknown
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	resp, err := s.postProvider(ctx, prov, proto, model, false, body)
	if err != nil {
		out.Error = string(state.ClassUpstreamUnavailable)
		return out
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
	out.Status = resp.StatusCode
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		out.OK = true
		return out
	}
	out.Error = classifyStatus(resp.StatusCode)
	if out.Error == "" {
		out.Error = "http_" + itoa(resp.StatusCode)
	}
	return out
}

func probeModel(prov state.Provider) (string, bool) {
	for _, id := range prov.Models {
		if prov.ChatModel(id) {
			return id, true
		}
	}
	return "", false
}

func probeBody(proto Protocol, model string) ([]byte, error) {
	switch proto {
	case ProtoResponses:
		return json.Marshal(map[string]any{"model": model, "input": "hi", "max_output_tokens": 16})
	case ProtoAnthropic:
		return json.Marshal(map[string]any{
			"model": model, "max_tokens": 1,
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		})
	case ProtoGemini:
		return json.Marshal(map[string]any{
			"contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": "hi"}}}},
		})
	case ProtoOllama:
		return json.Marshal(map[string]any{
			"model": model, "stream": false,
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		})
	default:
		return json.Marshal(map[string]any{
			"model": model, "max_tokens": 1,
			"messages": []map[string]string{{"role": "user", "content": "hi"}},
		})
	}
}

func applyProviderAuth(req *http.Request, proto Protocol, credential string) {
	req.Header.Set("Authorization", "Bearer "+credential)
	switch proto {
	case ProtoAnthropic:
		req.Header.Set("x-api-key", credential)
		req.Header.Set("anthropic-version", "2023-06-01")
	case ProtoGemini:
		req.Header.Set("x-goog-api-key", credential)
	}
}
