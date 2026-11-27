package login

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	codexClientID   = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexRefreshURL = "https://auth.openai.com/oauth/token"
	codexAPIRoot    = "https://chatgpt.com/backend-api/codex"
	refreshSkew     = 5 * time.Minute
)

var codexWrite sync.Mutex

func (m Machine) refreshURL() string {
	if m.CodexAuth != "" {
		return m.CodexAuth
	}
	return codexRefreshURL
}

func (m Machine) codexRoot() string {
	if m.CodexAPI != "" {
		return strings.TrimRight(m.CodexAPI, "/")
	}
	return codexAPIRoot
}

func (m Machine) openAIRoot() string {
	if m.OpenAI != "" {
		return strings.TrimRight(m.OpenAI, "/")
	}
	return "https://api.openai.com/v1"
}

func codexAuthPath(home string) string {
	return filepath.Join(home, ".codex", "auth.json")
}

func readCodex(m Machine) (Account, bool) {
	path := codexAuthPath(m.Home)
	raw, err := m.read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Account{}, false
		}
		return Account{ID: "codex", Name: "Codex", Readable: false}, true
	}
	if m.plugin("codex") {
		if bytes.Contains(raw, []byte("access_token")) || bytes.Contains(raw, []byte("OPENAI_API_KEY")) {
			return Account{ID: "codex", Name: "Codex", Plugin: true, Readable: true}, true
		}
		return Account{}, false
	}
	if key, ok := codexAPIKey(raw); ok {
		models, known := openAIModels(m, key)
		return Account{ID: "codex", Name: "Codex", Readable: true, ModelsKnown: known, Models: models}, true
	}
	access, accountID, _, ok := codexTokens(raw)
	if !ok || access == "" {
		if !json.Valid(raw) {
			return Account{ID: "codex", Name: "Codex", Readable: false}, true
		}
		return Account{}, false
	}
	models, known := codexModels(m, path, access, accountID)
	return Account{ID: "codex", Name: "Codex", Readable: true, ModelsKnown: known, Models: models}, true
}

func codexAPIKey(raw []byte) (string, bool) {
	var doc struct {
		AuthMode string `json:"auth_mode"`
		Key      string `json:"OPENAI_API_KEY"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.AuthMode != "apikey" {
		return "", false
	}
	key := strings.TrimSpace(doc.Key)
	if key == "" {
		return "", false
	}
	return key, true
}

func openAIModels(m Machine, key string) ([]Model, bool) {
	req, err := http.NewRequest(http.MethodGet, m.openAIRoot()+"/models", nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
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

func codexTokens(raw []byte) (access, accountID, refresh string, ok bool) {
	var doc struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return "", "", "", false
	}
	if doc.AuthMode != "" && doc.AuthMode != "chatgpt" {
		return "", "", "", false
	}
	if strings.TrimSpace(doc.Tokens.AccessToken) == "" {
		return "", "", "", false
	}
	return doc.Tokens.AccessToken, doc.Tokens.AccountID, doc.Tokens.RefreshToken, true
}

func codexModels(m Machine, path, access, accountID string) ([]Model, bool) {
	access, accountID, err := ensureCodex(m, path, access, accountID)
	if err != nil {
		return nil, false
	}
	req, err := http.NewRequest(http.MethodGet, m.codexRoot()+"/models?client_version=1.0.0", nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+access)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
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
		Models []struct {
			Slug           string `json:"slug"`
			Visibility     string `json:"visibility"`
			SupportedInAPI bool   `json:"supported_in_api"`
			Levels         []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &listed) != nil {
		return nil, false
	}
	var out []Model
	for _, model := range listed.Models {
		if model.Slug == "" || !model.SupportedInAPI || model.Visibility != "list" {
			continue
		}
		levels := make([]string, 0, len(model.Levels))
		for _, level := range model.Levels {
			level := strings.ToLower(strings.TrimSpace(level.Effort))
			if level != "" {
				levels = append(levels, level)
			}
		}
		out = append(out, Model{
			ID: model.Slug, Name: model.Slug,
			Reasoning: len(levels) > 0, ReasoningEfforts: levels,
			ReasoningEffortsKnown: model.Levels != nil,
		})
	}
	if out == nil {
		out = []Model{}
	}
	return out, true
}

func ensureCodex(m Machine, path, access, accountID string) (string, string, error) {
	exp, ok := jwtExpiry(access)
	if ok && m.now().Add(refreshSkew).Before(exp) {
		return access, accountID, nil
	}
	return refreshCodex(m, path)
}

func refreshCodex(m Machine, path string) (string, string, error) {
	codexWrite.Lock()
	defer codexWrite.Unlock()
	raw, err := m.read(path)
	if err != nil {
		return "", "", err
	}
	access, accountID, refresh, ok := codexTokens(raw)
	if !ok || refresh == "" {
		return "", "", os.ErrInvalid
	}
	exp, known := jwtExpiry(access)
	if known && m.now().Add(refreshSkew).Before(exp) {
		return access, accountID, nil
	}
	payload, _ := json.Marshal(map[string]string{
		"client_id": codexClientID, "grant_type": "refresh_token", "refresh_token": refresh,
	})
	req, err := http.NewRequest(http.MethodPost, m.refreshURL(), bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", os.ErrInvalid
	}
	var refreshed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if json.Unmarshal(body, &refreshed) != nil || refreshed.AccessToken == "" {
		return "", "", os.ErrInvalid
	}
	if err := writeCodexTokens(path, raw, refreshed.AccessToken, refreshed.RefreshToken, refreshed.IDToken, m.now()); err != nil {
		return "", "", err
	}
	return refreshed.AccessToken, accountID, nil
}

func writeCodexTokens(path string, raw []byte, access, refresh, idToken string, now time.Time) error {
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return os.ErrInvalid
	}
	tokens, _ := doc["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}
	tokens["access_token"] = access
	if refresh != "" {
		tokens["refresh_token"] = refresh
	}
	if idToken != "" {
		tokens["id_token"] = idToken
	}
	doc["tokens"] = tokens
	doc["last_refresh"] = now.UTC().Format(time.RFC3339)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return writePrivate(path, out)
}

func writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".aigate-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		if werr != nil {
			return werr
		}
		return cerr
	}
	_ = os.Chmod(name, 0o600)
	if err := os.Rename(name, path); err == nil {
		return nil
	}
	bak := path + ".aigate-bak"
	_ = os.Remove(bak)
	if err := os.Rename(path, bak); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Rename(bak, path)
		_ = os.Remove(name)
		return err
	}
	_ = os.Remove(bak)
	return nil
}

func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

func dialCodex(ctx context.Context, m Machine, model string, body []byte, stream bool) (*http.Response, error) {
	path := codexAuthPath(m.Home)
	raw, err := m.read(path)
	if err != nil {
		return nil, err
	}
	if key, ok := codexAPIKey(raw); ok {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.openAIRoot()+"/responses", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		return m.client().Do(req)
	}
	access, accountID, _, ok := codexTokens(raw)
	if !ok {
		return nil, os.ErrInvalid
	}
	access, accountID, err = ensureCodex(m, path, access, accountID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.codexRoot()+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("originator", "codex_cli_rs")
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	return m.client().Do(req)
}
