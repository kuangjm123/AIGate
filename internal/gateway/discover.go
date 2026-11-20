package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

const checkLimit = 24

// DetectResult names the protocol a base URL answered with.
// Error is empty when the catalog read succeeded. A rejected key still names
// the protocol and sets Error to auth_failure.
type DetectResult struct {
	Protocol string
	Error    string
}

// FetchResult is the model ids read from a provider catalog.
type FetchResult struct {
	Models []string
	Error  string
}

// ModelCheck is one short request against one model.
type ModelCheck struct {
	Model  string
	OK     bool
	Status int
	Error  string
}

// Detect asks the base URL which protocol it speaks.
// Ollama and Gemini have their own catalog paths. Chat and Anthropic both
// use /v1/models, so a 2xx there is named from the JSON: an OpenAI list
// (object "list", or a model owned_by) is chat. Responses is chosen by the user.
func (s *Server) Detect(ctx context.Context, base, key, consoleListen string) DetectResult {
	if strings.TrimSpace(key) == "" {
		return DetectResult{Error: "credential"}
	}
	switch security.CheckBaseURL(base, consoleListen, "") {
	case security.BaseOK:
	case security.BaseUnknown:
		return DetectResult{Error: CodeUnknown}
	default:
		return DetectResult{Error: CodeBaseURL}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var unreachable bool
	for _, proto := range []Protocol{ProtoOllama, ProtoGemini} {
		status, _, err := s.providerGET(ctx, base, proto, key)
		if err != nil {
			unreachable = true
			continue
		}
		if status >= 200 && status < 300 {
			return DetectResult{Protocol: string(proto)}
		}
	}
	status, body, err := s.providerGET(ctx, base, ProtoChat, key)
	if err != nil {
		if unreachable {
			return DetectResult{Error: string(state.ClassUpstreamUnavailable)}
		}
		return DetectResult{Error: CodeUnknown}
	}
	if status >= 200 && status < 300 {
		if catalogProtocol(body) == ProtoAnthropic {
			return DetectResult{Protocol: string(ProtoAnthropic)}
		}
		return DetectResult{Protocol: string(ProtoChat)}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		again, shaped, againErr := s.providerGET(ctx, base, ProtoAnthropic, key)
		if againErr == nil && again >= 200 && again < 300 && catalogProtocol(shaped) == ProtoAnthropic {
			return DetectResult{Protocol: string(ProtoAnthropic)}
		}
		if againErr == nil && catalogProtocol(shaped) == ProtoAnthropic {
			return DetectResult{Protocol: string(ProtoAnthropic), Error: string(state.ClassAuthFailure)}
		}
		return DetectResult{Protocol: string(ProtoChat), Error: string(state.ClassAuthFailure)}
	}
	return DetectResult{Error: CodeUnknown}
}

// FetchModels reads the catalog for protocol. An empty protocol is read as chat.
func (s *Server) FetchModels(ctx context.Context, base, protocol, key, consoleListen string) FetchResult {
	proto, known := providerProtocol(protocol, ProtoChat)
	if !known {
		return FetchResult{Error: CodeUnknown}
	}
	if strings.TrimSpace(key) == "" {
		return FetchResult{Error: "credential"}
	}
	switch security.CheckBaseURL(base, consoleListen, "") {
	case security.BaseOK:
	case security.BaseUnknown:
		return FetchResult{Error: CodeUnknown}
	default:
		return FetchResult{Error: CodeBaseURL}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	status, body, err := s.providerGET(ctx, base, proto, key)
	if err != nil {
		return FetchResult{Error: string(state.ClassUpstreamUnavailable)}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return FetchResult{Error: string(state.ClassAuthFailure)}
	}
	if status < 200 || status >= 300 {
		code := classifyStatus(status)
		if code == "" {
			code = "http_" + itoa(status)
		}
		return FetchResult{Error: code}
	}
	models := catalogIDs(proto, body)
	if models == nil {
		models = []string{}
	}
	return FetchResult{Models: models}
}

// CheckModels sends one short request for each conversation model, up to checkLimit.
func (s *Server) CheckModels(ctx context.Context, prov state.Provider, consoleListen string) []ModelCheck {
	if _, known := providerProtocol(prov.Protocol, ProtoChat); !known {
		return []ModelCheck{{Error: CodeUnknown}}
	}
	var models []string
	for _, id := range prov.Models {
		if prov.ChatModel(id) {
			models = append(models, id)
		}
	}
	if len(models) == 0 {
		return []ModelCheck{{Error: "model"}}
	}
	if len(models) > checkLimit {
		models = models[:checkLimit]
	}
	out := make([]ModelCheck, 0, len(models))
	for _, model := range models {
		one := prov
		one.Models = []string{model}
		one.Info = nil
		ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		got := s.Probe(ctx, one, consoleListen)
		cancel()
		out = append(out, ModelCheck{Model: model, OK: got.OK, Status: got.Status, Error: got.Error})
	}
	return out
}

func (s *Server) providerGET(ctx context.Context, base string, proto Protocol, key string) (int, []byte, error) {
	target, err := catalogEndpoint(base, proto)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	applyProviderAuth(req, proto, key)
	resp, err := s.httpClient(base).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func catalogEndpoint(base string, proto Protocol) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errBadBody
	}
	suffix := "/v1/models"
	switch proto {
	case ProtoGemini:
		suffix = "/v1beta/models"
	case ProtoOllama:
		suffix = "/api/tags"
	}
	u.Path = joinUnder(u.Path, suffix)
	u.RawQuery = ""
	return u.String(), nil
}

// catalogProtocol reads a /v1/models body. OpenAI's list wins when both
// shapes appear, so a Chat server that ignores Anthropic headers stays chat.
func catalogProtocol(body []byte) Protocol {
	var doc struct {
		Object  string `json:"object"`
		HasMore *bool  `json:"has_more"`
		FirstID string `json:"first_id"`
		Data    []struct {
			Object      string `json:"object"`
			Type        string `json:"type"`
			OwnedBy     string `json:"owned_by"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return ""
	}
	openai := doc.Object == "list"
	anthropic := doc.HasMore != nil || doc.FirstID != ""
	for _, row := range doc.Data {
		if row.Object == "model" || row.OwnedBy != "" {
			openai = true
		}
		if row.Type == "model" || row.DisplayName != "" {
			anthropic = true
		}
	}
	if openai {
		return ProtoChat
	}
	if anthropic {
		return ProtoAnthropic
	}
	return ""
}

func catalogIDs(proto Protocol, body []byte) []string {
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if proto == ProtoGemini {
			id = strings.TrimPrefix(id, "models/")
		}
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, row := range doc.Data {
		add(row.ID)
	}
	for _, row := range doc.Models {
		if row.ID != "" {
			add(row.ID)
		} else {
			add(row.Name)
		}
	}
	return out
}
