package login

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func copilotHosts(home string) string {
	return filepath.Join(home, ".config", "github-copilot", "hosts.json")
}

func readCopilot(m Machine) (Account, bool) {
	return readOAuthFile(m, "copilot", "GitHub Copilot", copilotHosts(m.Home), copilotSignedIn, copilotModels)
}

func copilotSignedIn(raw []byte) bool {
	var doc map[string]map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	for _, host := range doc {
		if token, _ := host["oauth_token"].(string); strings.TrimSpace(token) != "" {
			return true
		}
	}
	return false
}

func copilotModels(m Machine, raw []byte) ([]Model, bool) {
	token := copilotOAuth(raw)
	if token == "" {
		return nil, false
	}
	session, err := copilotSession(m, token)
	if err != nil {
		return nil, false
	}
	req, err := http.NewRequest(http.MethodGet, "https://api.individual.githubcopilot.com/models", nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+session)
	resp, err := m.client().Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &listed) != nil {
		return nil, false
	}
	var out []Model
	for _, model := range listed.Data {
		if model.ID != "" {
			out = append(out, Model{ID: model.ID, Name: model.ID})
		}
	}
	if out == nil {
		out = []Model{}
	}
	return out, true
}

func copilotOAuth(raw []byte) string {
	var doc map[string]map[string]string
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	for _, host := range doc {
		if token := strings.TrimSpace(host["oauth_token"]); token != "" {
			return token
		}
	}
	return ""
}

func copilotSession(m Machine, oauth string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+oauth)
	resp, err := m.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", os.ErrInvalid
	}
	var doc struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &doc) != nil || doc.Token == "" {
		return "", os.ErrInvalid
	}
	return doc.Token, nil
}

func geminiCreds(home string) string {
	return filepath.Join(home, ".gemini", "oauth_creds.json")
}

func antigravityCreds(home string) string {
	return filepath.Join(home, ".gemini", "antigravity", "oauth_creds.json")
}

func readGemini(m Machine) (Account, bool) {
	return readGoogle(m, "gemini", "Gemini CLI", geminiCreds(m.Home))
}

func readAntigravity(m Machine) (Account, bool) {
	return readGoogle(m, "antigravity", "Antigravity", antigravityCreds(m.Home))
}

func readGoogle(m Machine, id, name, path string) (Account, bool) {
	raw, err := m.read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Account{}, false
		}
		return Account{ID: id, Name: name, Readable: false}, true
	}
	if m.plugin(id) {
		if !bytes.Contains(raw, []byte("access_token")) {
			return Account{}, false
		}
		return Account{ID: id, Name: name, Plugin: true, Readable: true}, true
	}
	var doc struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return Account{ID: id, Name: name, Readable: false}, true
	}
	if strings.TrimSpace(doc.AccessToken) == "" {
		return Account{}, false
	}
	return Account{ID: id, Name: name, Readable: true, ModelsKnown: true, Models: geminiPublic()}, true
}

func geminiPublic() []Model {
	return []Model{
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
		{ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash"},
	}
}

type signedInFunc func([]byte) bool
type modelsFunc func(Machine, []byte) ([]Model, bool)

func readOAuthFile(m Machine, id, name, path string, signed signedInFunc, models modelsFunc) (Account, bool) {
	raw, err := m.read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Account{}, false
		}
		return Account{ID: id, Name: name, Readable: false}, true
	}
	if m.plugin(id) {
		if !signed(raw) && json.Valid(raw) {
			return Account{}, false
		}
		if !json.Valid(raw) {
			return Account{ID: id, Name: name, Readable: false}, true
		}
		return Account{ID: id, Name: name, Plugin: true, Readable: true}, true
	}
	if !json.Valid(raw) {
		return Account{ID: id, Name: name, Readable: false}, true
	}
	if !signed(raw) {
		return Account{}, false
	}
	listed, known := models(m, raw)
	return Account{ID: id, Name: name, Readable: true, ModelsKnown: known, Models: listed}, true
}

func dialCopilot(ctx context.Context, m Machine, body []byte, stream bool) (*http.Response, error) {
	raw, err := m.read(copilotHosts(m.Home))
	if err != nil {
		return nil, err
	}
	session, err := copilotSession(m, copilotOAuth(raw))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.individual.githubcopilot.com/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+session)
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	return m.client().Do(req)
}

func dialGoogle(ctx context.Context, m Machine, path, model string, body []byte) (*http.Response, error) {
	raw, err := m.read(path)
	if err != nil {
		return nil, err
	}
	var creds struct {
		AccessToken string `json:"access_token"`
		ProjectID   string `json:"project_id"`
	}
	if json.Unmarshal(raw, &creds) != nil || creds.AccessToken == "" {
		return nil, os.ErrInvalid
	}
	if creds.ProjectID == "" {
		return nil, errNoProject
	}
	envelope, _ := json.Marshal(map[string]any{
		"model": "models/" + model, "project": creds.ProjectID,
		"request": json.RawMessage(body),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://cloudcode-pa.googleapis.com/v1internal:generateContent", bytes.NewReader(envelope))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client().Do(req)
	if err != nil || resp == nil {
		return resp, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var wrapped struct {
		Response json.RawMessage `json:"response"`
	}
	out := payload
	if json.Unmarshal(payload, &wrapped) == nil && len(wrapped.Response) > 0 {
		out = wrapped.Response
	}
	return &http.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(out)),
	}, nil
}

var errNoProject = os.ErrInvalid
