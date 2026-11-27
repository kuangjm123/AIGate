package login

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	workBuddyVersion = "5.9.3"
	workBuddyCLI     = "2.165.0"
	workBuddySkew    = 5 * time.Minute
)

// ErrWorkBuddyEncrypted means the desktop file's token is an encrypted object.
// That file is not a sign-in.
var ErrWorkBuddyEncrypted = errors.New("encrypted")

// ErrWorkBuddyRejected means the bytes are not a WorkBuddy sign-in.
var ErrWorkBuddyRejected = errors.New("rejected")

// ErrWorkBuddyRefresh means WorkBuddy refused to renew a saved sign-in.
var ErrWorkBuddyRefresh = errors.New("refresh")

// ErrWorkBuddyStore means AIGate has no file for its own WorkBuddy sign-ins.
var ErrWorkBuddyStore = errors.New("no store")

// DesktopAuthDir is the CodeBuddy directory that holds the desktop sign-in files.
// The files are only read. An empty result means this system has no such directory.
func DesktopAuthDir() string {
	var root string
	switch runtime.GOOS {
	case "windows":
		root = os.Getenv("LOCALAPPDATA")
		if root == "" {
			return ""
		}
		return filepath.Join(root, "CodeBuddyExtension", "Data", "Public", "auth")
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "CodeBuddyExtension", "Data", "Public", "auth")
	default:
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		return filepath.Join(home, ".local", "share", "CodeBuddyExtension", "Data", "Public", "auth")
	}
}

type wbAccountInfo struct {
	UID            string `json:"uid"`
	Nickname       string `json:"nickname"`
	Type           string `json:"type"`
	EnterpriseID   string `json:"enterpriseId"`
	EnterpriseName string `json:"enterpriseName"`
	LastLogin      bool   `json:"lastLogin"`
}

type wbSaved struct {
	ID             string `json:"id"`
	Build          string `json:"build"`
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	UID            string `json:"uid,omitempty"`
	EnterpriseID   string `json:"enterpriseId,omitempty"`
	EnterpriseName string `json:"enterpriseName,omitempty"`
	Domain         string `json:"domain,omitempty"`
	Access         string `json:"access,omitempty"`
	Refresh        string `json:"refresh,omitempty"`
	Expires        int64  `json:"expires,omitempty"`
	Key            string `json:"key,omitempty"`
}

type wbStoreFile struct {
	Accounts []wbSaved `json:"accounts"`
}

type wbCred struct {
	build      string
	kind       string
	uid        string
	name       string
	domain     string
	enterprise string
	access     string
	refresh    string
	expires    int64
	key        string
	savedID    string
}

var workBuddyMu sync.Mutex

func readWorkBuddyAccounts(m Machine) []Account {
	var out []Account
	for _, build := range []string{"workbuddy", "workbuddy-ai"} {
		if account, ok := readWorkBuddyDesktop(m, build); ok {
			out = append(out, account)
		}
	}
	out = append(out, readWorkBuddySaved(m)...)
	return out
}

func readWorkBuddyDesktop(m Machine, build string) (Account, bool) {
	if strings.TrimSpace(m.WorkBuddyAuth) == "" {
		return Account{}, false
	}
	path := filepath.Join(m.WorkBuddyAuth, desktopInfoName(build))
	raw, err := m.read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Account{}, false
		}
		return desktopUnknown(build), true
	}
	cred, state, _ := credFromDesktop(raw, build)
	switch state {
	case wbEncrypted, wbAbsent:
		return Account{}, false
	case wbPlain:
	default:
		return desktopUnknown(build), true
	}
	account := Account{
		ID: cred.catalogID(), Name: cred.display(), Origin: "desktop", Readable: true,
	}
	if m.plugin(account.ID) {
		account.Plugin = true
		return account, true
	}
	models, known := workBuddyModels(m, cred)
	account.ModelsKnown = known
	account.Models = models
	return account, true
}

func desktopUnknown(build string) Account {
	return Account{ID: build, Name: workBuddyTitle(build), Origin: "desktop", Readable: false}
}

func readWorkBuddySaved(m Machine) []Account {
	if strings.TrimSpace(m.WorkBuddyStore) == "" {
		return nil
	}
	doc, err := loadWorkBuddyStore(m)
	if err != nil {
		if errors.Is(err, ErrWorkBuddyStore) || os.IsNotExist(err) {
			return nil
		}
		return []Account{{ID: "workbuddy-auth", Name: "WorkBuddy", Origin: "saved", Readable: false}}
	}
	var out []Account
	for _, item := range doc.Accounts {
		cred := item.cred()
		if cred.access == "" && cred.key == "" {
			continue
		}
		account := Account{ID: cred.catalogID(), Name: cred.display(), Origin: "saved", Readable: true}
		if m.plugin(account.ID) {
			account.Plugin = true
			out = append(out, account)
			continue
		}
		models, known := workBuddyModels(m, cred)
		account.ModelsKnown = known
		account.Models = models
		out = append(out, account)
	}
	return out
}

const (
	wbAbsent tokenState = iota
	wbPlain
	wbEncrypted
	wbBad
)

type tokenState int

func credFromDesktop(raw []byte, build string) (wbCred, tokenState, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return wbCred{}, wbBad, ErrWorkBuddyRejected
	}
	var doc struct {
		Account wbAccountInfo              `json:"account"`
		Auth    map[string]json.RawMessage `json:"auth"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Auth == nil {
		return wbCred{}, wbBad, ErrWorkBuddyRejected
	}
	access, state := jsonString(doc.Auth["accessToken"])
	if state == wbEncrypted {
		return wbCred{}, wbEncrypted, ErrWorkBuddyEncrypted
	}
	if state != wbPlain || access == "" {
		return wbCred{}, wbAbsent, nil
	}
	domain, _ := jsonString(doc.Auth["domain"])
	cred := wbCred{
		build: build, kind: "oauth", uid: doc.Account.UID, name: doc.Account.Nickname,
		domain: domain, access: access,
	}
	if doc.Account.Type != "personal" && doc.Account.EnterpriseID != "" {
		cred.enterprise = doc.Account.EnterpriseID
		cred.name = enterpriseLabel(doc.Account.Nickname, doc.Account.EnterpriseName, doc.Account.EnterpriseID)
	}
	return cred, wbPlain, nil
}

func jsonString(raw json.RawMessage) (string, tokenState) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", wbAbsent
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text), wbPlain
	}
	return "", wbEncrypted
}

func (c wbCred) catalogID() string {
	if c.savedID != "" {
		return c.savedID
	}
	if c.kind == "api" {
		return ""
	}
	return c.build
}

func (c wbCred) display() string {
	title := workBuddyTitle(c.build)
	if c.name == "" {
		return title
	}
	return title + "（" + c.name + "）"
}

func workBuddyTitle(build string) string {
	if build == "workbuddy-ai" {
		return "WorkBuddy AI"
	}
	return "WorkBuddy"
}

func desktopInfoName(build string) string {
	if build == "workbuddy-ai" {
		return "workbuddy-desktop-ai.info"
	}
	return "workbuddy-desktop.info"
}

func enterpriseLabel(nickname, enterpriseName, enterpriseID string) string {
	ent := strings.TrimSpace(enterpriseName)
	if ent == "" {
		tail := enterpriseID
		if len(tail) > 6 {
			tail = tail[len(tail)-6:]
		}
		ent = "enterprise " + tail
	}
	if nickname == "" {
		return ent
	}
	return nickname + " · " + ent
}

func (s wbSaved) cred() wbCred {
	return wbCred{
		build: s.Build, kind: s.Kind, uid: s.UID, name: s.Name, domain: s.Domain,
		enterprise: s.EnterpriseID, access: s.Access, refresh: s.Refresh, expires: s.Expires,
		key: s.Key, savedID: s.ID,
	}
}

func workBuddyModels(m Machine, cred wbCred) ([]Model, bool) {
	if cred.key != "" && !workBuddyConfigKey(cred.key) {
		return nil, false
	}
	origin := workBuddyOrigin(m, cred)
	req, err := http.NewRequest(http.MethodGet, origin+"/v3/config", nil)
	if err != nil {
		return nil, false
	}
	workBuddyHeaders(req, cred)
	resp, err := m.client().Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	return parseWorkBuddyConfig(raw)
}

func workBuddyConfigKey(key string) bool {
	return strings.HasPrefix(key, "ck_") || strings.HasPrefix(key, "pt_")
}

func parseWorkBuddyConfig(raw []byte) ([]Model, bool) {
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	body := raw
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Data) > 0 {
		if envelope.Code != 0 {
			return nil, false
		}
		body = envelope.Data
	}
	var doc struct {
		Agents []struct {
			Name   string          `json:"name"`
			Models json.RawMessage `json:"models"`
		} `json:"agents"`
		Models []struct {
			ID              string          `json:"id"`
			Name            string          `json:"name"`
			MaxInputTokens  int             `json:"maxInputTokens"`
			ReasoningEffort json.RawMessage `json:"reasoningEffort"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil, false
	}
	var ids []string
	found := false
	for _, agent := range doc.Agents {
		if agent.Name != "cli" {
			continue
		}
		found = true
		ids = cliModelIDs(agent.Models)
	}
	if !found {
		return nil, false
	}
	detail := map[string]Model{}
	for _, model := range doc.Models {
		reasoning, levels, known := reasoningInfo(model.ReasoningEffort)
		detail[model.ID] = Model{
			ID: model.ID, Name: model.Name, Context: model.MaxInputTokens,
			Reasoning: reasoning, ReasoningEfforts: levels, ReasoningEffortsKnown: known,
		}
	}
	out := make([]Model, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		model, ok := detail[id]
		if !ok {
			model = Model{ID: id, Name: id}
		}
		if model.Name == "" {
			model.Name = model.ID
		}
		out = append(out, model)
	}
	return out, true
}

func cliModelIDs(raw json.RawMessage) []string {
	var ids []string
	if json.Unmarshal(raw, &ids) == nil {
		return ids
	}
	var objects []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &objects) == nil {
		for _, item := range objects {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func reasoningInfo(raw json.RawMessage) (bool, []string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false, nil, false
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return len(list) > 0, cleanLevels(list), true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return false, []string{}, true
		}
		return true, []string{text}, true
	}
	var enabled bool
	if json.Unmarshal(raw, &enabled) == nil {
		return enabled, nil, false
	}
	return true, nil, false
}

func cleanLevels(levels []string) []string {
	out := make([]string, 0, len(levels))
	for _, level := range levels {
		level = strings.ToLower(strings.TrimSpace(level))
		if level != "" {
			out = append(out, level)
		}
	}
	return out
}

func workBuddyOrigin(m Machine, cred wbCred) string {
	if ent := enterpriseOrigin(cred.domain); ent != "" {
		return ent
	}
	if cred.build == "workbuddy-ai" {
		if strings.TrimSpace(m.WorkBuddyAI) != "" {
			return strings.TrimRight(m.WorkBuddyAI, "/")
		}
		return "https://www.workbuddy.ai"
	}
	if strings.TrimSpace(m.WorkBuddyAPI) != "" {
		return strings.TrimRight(m.WorkBuddyAPI, "/")
	}
	return "https://copilot.tencent.com"
}

func enterpriseOrigin(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || domain == "tencent.sso.copilot.tencent.com" {
		return ""
	}
	if strings.HasSuffix(domain, ".sso.copilot.tencent.com") || strings.HasSuffix(domain, ".copilot.qq.com") || strings.HasSuffix(domain, ".sso.codebuddy.cn") {
		return "https://" + domain
	}
	return ""
}

func workBuddyUA(build string) string {
	if build == "workbuddy-ai" {
		return "WorkBuddy/" + workBuddyVersion + " workbuddy-ai/" + workBuddyVersion + " CLI/" + workBuddyCLI
	}
	return "WorkBuddy/" + workBuddyVersion + " WorkBuddy/" + workBuddyVersion + " CLI/" + workBuddyCLI
}

func workBuddyHeaders(req *http.Request, cred wbCred) {
	req.Header.Set("User-Agent", workBuddyUA(cred.build))
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("X-IDE-Type", "WorkBuddy")
	req.Header.Set("X-IDE-Name", "WorkBuddy")
	req.Header.Set("X-IDE-Version", workBuddyVersion)
	req.Header.Set("X-Agent-Purpose", "conversation")
	req.Header.Set("X-Agent-Intent", "craft")
	req.Header.Set("X-Agent-Type", "main")
	req.Header.Set("X-Agent-Runtime", "local-agent")
	req.Header.Set("Accept", "application/json")
	if cred.key != "" {
		req.Header.Set("Authorization", "Bearer "+cred.key)
		req.Header.Set("X-API-Key", cred.key)
		req.Header.Set("X-User-Id", "anonymous_"+last8(cred.key))
		return
	}
	req.Header.Set("Authorization", "Bearer "+cred.access)
	if cred.uid != "" {
		req.Header.Set("X-User-Id", cred.uid)
	}
	domain := cred.domain
	if domain == "" && req.URL != nil {
		domain = req.URL.Host
	}
	if domain != "" {
		req.Header.Set("X-Domain", domain)
	}
	if cred.enterprise != "" {
		req.Header.Set("X-Enterprise-Id", cred.enterprise)
		req.Header.Set("X-Tenant-Id", cred.enterprise)
	}
}

func last8(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 8 {
		return value
	}
	return value[len(value)-8:]
}

func dialWorkBuddy(ctx context.Context, m Machine, account, model string, body []byte, stream bool) (*http.Response, error) {
	cred, err := workBuddyCred(ctx, m, account)
	if err != nil {
		return nil, err
	}
	payload := workBuddyChatBody(body, model)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	url := workBuddyOrigin(m, cred) + "/v2/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	workBuddyHeaders(req, cred)
	resp, err := m.client().Do(req)
	if err != nil {
		return nil, err
	}
	return finishWorkBuddy(resp, stream)
}

func workBuddyCred(ctx context.Context, m Machine, account string) (wbCred, error) {
	if account == "workbuddy" || account == "workbuddy-ai" {
		if strings.TrimSpace(m.WorkBuddyAuth) == "" {
			return wbCred{}, os.ErrNotExist
		}
		raw, err := m.read(filepath.Join(m.WorkBuddyAuth, desktopInfoName(account)))
		if err != nil {
			return wbCred{}, err
		}
		cred, state, err := credFromDesktop(raw, account)
		if state == wbEncrypted || state == wbAbsent {
			return wbCred{}, os.ErrNotExist
		}
		if err != nil || state != wbPlain {
			return wbCred{}, os.ErrInvalid
		}
		return cred, nil
	}
	workBuddyMu.Lock()
	defer workBuddyMu.Unlock()
	doc, err := loadWorkBuddyStore(m)
	if err != nil {
		return wbCred{}, err
	}
	index := -1
	for i := range doc.Accounts {
		if doc.Accounts[i].ID == account {
			index = i
			break
		}
	}
	if index < 0 {
		return wbCred{}, os.ErrNotExist
	}
	cred := doc.Accounts[index].cred()
	if cred.kind == "oauth" && cred.refresh != "" && refreshDue(m, cred.expires) {
		next, err := refreshWorkBuddy(ctx, m, cred)
		if err != nil {
			return wbCred{}, err
		}
		doc.Accounts[index].Access = next.access
		doc.Accounts[index].Refresh = next.refresh
		doc.Accounts[index].Expires = next.expires
		if err := saveWorkBuddyStore(m, doc); err != nil {
			return wbCred{}, err
		}
		cred = doc.Accounts[index].cred()
	}
	return cred, nil
}

func refreshDue(m Machine, expires int64) bool {
	if expires <= 0 {
		return true
	}
	return m.now().Add(workBuddySkew).UnixMilli() >= expires
}

func refreshWorkBuddy(ctx context.Context, m Machine, cred wbCred) (wbCred, error) {
	origin := workBuddyOrigin(m, cred)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/v2/plugin/auth/token/refresh", nil)
	if err != nil {
		return wbCred{}, err
	}
	req.Header.Set("X-Refresh-Token", cred.refresh)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
	req.Header.Set("User-Agent", workBuddyUA(cred.build))
	if cred.domain != "" {
		req.Header.Set("X-Domain", cred.domain)
	}
	resp, err := m.client().Do(req)
	if err != nil {
		return wbCred{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return wbCred{}, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return wbCred{}, ErrWorkBuddyRefresh
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresIn    int64  `json:"expiresIn"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Code != 0 || envelope.Data.AccessToken == "" {
		return wbCred{}, ErrWorkBuddyRefresh
	}
	cred.access = envelope.Data.AccessToken
	if envelope.Data.RefreshToken != "" {
		cred.refresh = envelope.Data.RefreshToken
	}
	cred.expires = m.now().Add(time.Duration(envelope.Data.ExpiresIn) * time.Second).UnixMilli()
	return cred, nil
}

func workBuddyChatBody(body []byte, model string) []byte {
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return body
	}
	if model != "" {
		if encoded, err := json.Marshal(model); err == nil {
			doc["model"] = encoded
		}
	}
	doc["stream"] = []byte("true")
	if value, ok := doc["max_completion_tokens"]; ok {
		doc["max_tokens"] = value
		delete(doc, "max_completion_tokens")
	}
	if raw, ok := doc["messages"]; ok {
		var messages []map[string]json.RawMessage
		if json.Unmarshal(raw, &messages) == nil {
			changed := false
			for i := range messages {
				var role string
				if json.Unmarshal(messages[i]["role"], &role) == nil && role == "developer" {
					messages[i]["role"] = []byte(`"system"`)
					changed = true
				}
			}
			if changed {
				if encoded, err := json.Marshal(messages); err == nil {
					doc["messages"] = encoded
				}
			}
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

func finishWorkBuddy(resp *http.Response, stream bool) (*http.Response, error) {
	if stream || !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return resp, nil
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	header := resp.Header.Clone()
	header.Set("Content-Type", "application/json")
	header.Del("Content-Encoding")
	return &http.Response{
		StatusCode: resp.StatusCode,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(sseToChat(raw))),
	}, nil
}

func sseToChat(raw []byte) []byte {
	var text strings.Builder
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		if chunk.Choices[0].Delta.Content != "" {
			text.WriteString(chunk.Choices[0].Delta.Content)
		} else {
			text.WriteString(chunk.Choices[0].Message.Content)
		}
	}
	out, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": text.String()}}},
	})
	if err != nil {
		return []byte(`{"choices":[{"message":{"role":"assistant","content":""}}]}`)
	}
	return out
}

// SaveWorkBuddyKey stores an API key in AIGate's own login file.
// The desktop file is not read or written.
func SaveWorkBuddyKey(m Machine, build, key string) error {
	build = workBuddyBuild(build)
	key = strings.TrimSpace(key)
	if build == "" || key == "" {
		return ErrWorkBuddyRejected
	}
	if strings.TrimSpace(m.WorkBuddyStore) == "" {
		return ErrWorkBuddyStore
	}
	workBuddyMu.Lock()
	defer workBuddyMu.Unlock()
	doc, err := loadWorkBuddyStore(m)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	item := wbSaved{
		ID: build + "#key-" + last8(key), Build: build, Kind: "api", Name: "API key", Key: key,
	}
	doc.Accounts = upsertSaved(doc.Accounts, item)
	return saveWorkBuddyStore(m, doc)
}

// ImportWorkBuddy stores a sign-in from a desktop file or a token JSON.
// A saved sign-in is refreshed once and written only to AIGate's own file.
func ImportWorkBuddy(ctx context.Context, m Machine, build string, raw []byte) error {
	build = workBuddyBuild(build)
	if build == "" {
		return ErrWorkBuddyRejected
	}
	if strings.TrimSpace(m.WorkBuddyStore) == "" {
		return ErrWorkBuddyStore
	}
	records, err := importRecords(raw, build)
	if err != nil {
		return err
	}
	workBuddyMu.Lock()
	defer workBuddyMu.Unlock()
	doc, err := loadWorkBuddyStore(m)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, cred := range records {
		if cred.refresh == "" && cred.access == "" {
			return ErrWorkBuddyRejected
		}
		if cred.refresh != "" {
			next, err := refreshWorkBuddy(ctx, m, cred)
			if err != nil {
				return err
			}
			cred.access = next.access
			cred.refresh = next.refresh
			cred.expires = next.expires
		}
		if cred.uid == "" || cred.name == "" {
			if looked, err := lookupWorkBuddyAccount(ctx, m, cred); err == nil {
				if cred.uid == "" {
					cred.uid = looked.uid
				}
				if cred.name == "" {
					cred.name = looked.name
				}
				if cred.enterprise == "" {
					cred.enterprise = looked.enterprise
				}
			}
		}
		id := cred.build + "#" + cred.uid
		if cred.uid == "" {
			id = cred.build + "#import-" + last8(cred.refresh)
		}
		if cred.enterprise != "" {
			id += "@" + cred.enterprise
		}
		doc.Accounts = upsertSaved(doc.Accounts, wbSaved{
			ID: id, Build: cred.build, Kind: "oauth", Name: cred.name, UID: cred.uid,
			EnterpriseID: cred.enterprise, Domain: cred.domain, Access: cred.access,
			Refresh: cred.refresh, Expires: cred.expires,
		})
	}
	return saveWorkBuddyStore(m, doc)
}

// ImportWorkBuddyDesktop reads one desktop sign-in file and stores a renewed copy.
// The desktop file is not written.
func ImportWorkBuddyDesktop(ctx context.Context, m Machine, build string) error {
	build = workBuddyBuild(build)
	if build == "" || strings.TrimSpace(m.WorkBuddyAuth) == "" {
		return ErrWorkBuddyRejected
	}
	raw, err := m.read(filepath.Join(m.WorkBuddyAuth, desktopInfoName(build)))
	if err != nil {
		return err
	}
	return ImportWorkBuddy(ctx, m, build, raw)
}

func workBuddyBuild(build string) string {
	switch strings.TrimSpace(build) {
	case "", "workbuddy":
		return "workbuddy"
	case "workbuddy-ai":
		return "workbuddy-ai"
	default:
		return ""
	}
}

func importRecords(raw []byte, build string) ([]wbCred, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '{' && raw[0] != '[') {
		return nil, ErrWorkBuddyRejected
	}
	if raw[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil, ErrWorkBuddyRejected
		}
		var out []wbCred
		for _, item := range items {
			cred, err := oneImport(item, build)
			if err != nil {
				return nil, err
			}
			out = append(out, cred)
		}
		if len(out) == 0 {
			return nil, ErrWorkBuddyRejected
		}
		return out, nil
	}
	if cred, err := oneImport(raw, build); err == nil {
		return []wbCred{cred}, nil
	} else if errors.Is(err, ErrWorkBuddyEncrypted) {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil, ErrWorkBuddyRejected
	}
	var out []wbCred
	for _, value := range doc {
		cred, err := oneImport(value, build)
		if err != nil {
			continue
		}
		out = append(out, cred)
	}
	if len(out) == 0 {
		return nil, ErrWorkBuddyRejected
	}
	return out, nil
}

func oneImport(raw []byte, build string) (wbCred, error) {
	if cred, state, err := credFromDesktop(raw, build); state == wbPlain && err == nil && cred.access != "" {
		refresh, rstate := desktopRefresh(raw)
		if rstate == wbEncrypted {
			return wbCred{}, ErrWorkBuddyEncrypted
		}
		cred.refresh = refresh
		return cred, nil
	} else if state == wbEncrypted {
		return wbCred{}, ErrWorkBuddyEncrypted
	}
	var doc struct {
		Type           string `json:"type"`
		Access         string `json:"access"`
		Refresh        string `json:"refresh"`
		UID            string `json:"uid"`
		Nickname       string `json:"nickname"`
		Domain         string `json:"domain"`
		EnterpriseID   string `json:"enterpriseId"`
		EnterpriseName string `json:"enterpriseName"`
		AccountType    string `json:"accountType"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return wbCred{}, ErrWorkBuddyRejected
	}
	if strings.TrimSpace(doc.Access) == "" && strings.TrimSpace(doc.Refresh) == "" {
		return wbCred{}, ErrWorkBuddyRejected
	}
	cred := wbCred{
		build: build, kind: "oauth", uid: doc.UID, name: doc.Nickname, domain: doc.Domain,
		access: strings.TrimSpace(doc.Access), refresh: strings.TrimSpace(doc.Refresh),
	}
	if doc.AccountType != "personal" && doc.EnterpriseID != "" {
		cred.enterprise = doc.EnterpriseID
		cred.name = enterpriseLabel(firstText(doc.Nickname, cred.name), doc.EnterpriseName, doc.EnterpriseID)
	}
	return cred, nil
}

func desktopRefresh(raw []byte) (string, tokenState) {
	var doc struct {
		Auth map[string]json.RawMessage `json:"auth"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Auth == nil {
		return "", wbAbsent
	}
	return jsonString(doc.Auth["refreshToken"])
}

func firstText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func lookupWorkBuddyAccount(ctx context.Context, m Machine, cred wbCred) (wbCred, error) {
	origin := workBuddyOrigin(m, cred)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/v2/plugin/accounts", nil)
	if err != nil {
		return wbCred{}, err
	}
	workBuddyHeaders(req, cred)
	resp, err := m.client().Do(req)
	if err != nil {
		return wbCred{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return wbCred{}, ErrWorkBuddyRejected
	}
	var envelope struct {
		Data struct {
			Accounts []wbAccountInfo `json:"accounts"`
		} `json:"data"`
		Accounts []wbAccountInfo `json:"accounts"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return wbCred{}, ErrWorkBuddyRejected
	}
	list := envelope.Data.Accounts
	if len(list) == 0 {
		list = envelope.Accounts
	}
	picked, ok := pickAccount(list, cred.uid)
	if !ok {
		return wbCred{}, ErrWorkBuddyRejected
	}
	out := cred
	out.uid = picked.UID
	out.name = picked.Nickname
	if picked.Type != "personal" && picked.EnterpriseID != "" {
		out.enterprise = picked.EnterpriseID
		out.name = enterpriseLabel(picked.Nickname, picked.EnterpriseName, picked.EnterpriseID)
	}
	return out, nil
}

func pickAccount(list []wbAccountInfo, uid string) (wbAccountInfo, bool) {
	var fallback wbAccountInfo
	found := false
	for _, item := range list {
		if uid != "" && item.UID != uid {
			continue
		}
		if item.LastLogin && item.Type == "personal" {
			return item, true
		}
		if item.LastLogin && !found {
			fallback = item
			found = true
		}
		if !found {
			fallback = item
			found = true
		}
	}
	return fallback, found
}

func upsertSaved(items []wbSaved, item wbSaved) []wbSaved {
	for i := range items {
		if items[i].ID == item.ID {
			items[i] = item
			return items
		}
	}
	return append(items, item)
}

func loadWorkBuddyStore(m Machine) (wbStoreFile, error) {
	if strings.TrimSpace(m.WorkBuddyStore) == "" {
		return wbStoreFile{}, ErrWorkBuddyStore
	}
	raw, err := m.read(m.WorkBuddyStore)
	if err != nil {
		return wbStoreFile{}, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return wbStoreFile{}, nil
	}
	var doc wbStoreFile
	if json.Unmarshal(raw, &doc) != nil {
		return wbStoreFile{}, os.ErrInvalid
	}
	return doc, nil
}

func saveWorkBuddyStore(m Machine, doc wbStoreFile) error {
	if doc.Accounts == nil {
		doc.Accounts = []wbSaved{}
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writePrivate(m.WorkBuddyStore, raw)
}
