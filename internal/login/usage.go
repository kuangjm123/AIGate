package login

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Window is one quota window of a subscription allowance, as the vendor just
// answered it. UsedPercent is the share of the window already spent;
// RemainingFraction is the share still left. Reset is when the window renews;
// ResetText keeps the vendor's own words when no clock time was read.
type Window struct {
	Name              string     `json:"name"`
	UsedPercent       *float64   `json:"usedPercent,omitempty"`
	RemainingFraction *float64   `json:"remainingFraction,omitempty"`
	Reset             *time.Time `json:"reset,omitempty"`
	ResetText         string     `json:"resetText,omitempty"`
}

// Quota is one subscription account's allowance. Status is ok or unknown.
// A signed-out or plugin-occupied account is not listed at all. A failed read
// is unknown, never zero.
type Quota struct {
	Account string   `json:"account"`
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Windows []Window `json:"windows,omitempty"`
	Credits string   `json:"credits,omitempty"`
}

// Usage asks every readable subscription sign-in for its remaining allowance.
// Codex is asked at the ChatGPT backend's /wham/usage with the token Codex
// saved. Claude Code answers `claude -p /usage` itself; the sign-in is not
// sent to Anthropic. Gemini CLI and Antigravity are asked at their Code Assist
// host's quota methods.
func Usage(ctx context.Context, m Machine) []Quota {
	if m.Off || strings.TrimSpace(m.Home) == "" {
		return []Quota{}
	}
	readers := []struct {
		id   string
		read func(context.Context, Machine) (Quota, bool)
	}{
		{"codex", codexUsage},
		{"claude", claudeUsage},
		{"gemini", geminiUsage},
		{"antigravity", antigravityUsage},
	}
	out := make([]Quota, len(readers))
	keep := make([]bool, len(readers))
	var wg sync.WaitGroup
	for i, reader := range readers {
		if m.plugin(reader.id) {
			continue
		}
		wg.Add(1)
		go func(i int, read func(context.Context, Machine) (Quota, bool)) {
			defer wg.Done()
			quota, ok := read(ctx, m)
			out[i] = quota
			keep[i] = ok
		}(i, reader.read)
	}
	wg.Wait()
	list := make([]Quota, 0, len(readers))
	for i := range out {
		if keep[i] {
			list = append(list, out[i])
		}
	}
	return list
}

func (m Machine) chatGPTRoot() string {
	if m.ChatGPT != "" {
		return strings.TrimRight(m.ChatGPT, "/")
	}
	return "https://chatgpt.com"
}

func (m Machine) cloudCodeRoot() string {
	if m.CloudCode != "" {
		return strings.TrimRight(m.CloudCode, "/")
	}
	return "https://cloudcode-pa.googleapis.com"
}

func (m Machine) antiCloudRoot() string {
	if m.AntiCloud != "" {
		return strings.TrimRight(m.AntiCloud, "/")
	}
	return "https://cloudcode-pa.googleapis.com"
}

// codexUsage reads Codex's auth.json and asks the ChatGPT backend how much of
// each plan window is spent and how many separately bought credits remain.
// An apikey sign-in has no plan windows and is not listed.
func codexUsage(ctx context.Context, m Machine) (Quota, bool) {
	path := codexAuthPath(m.Home)
	raw, err := m.read(path)
	if err != nil {
		return Quota{}, false
	}
	if _, ok := codexAPIKey(raw); ok {
		return Quota{}, false
	}
	access, accountID, _, ok := codexTokens(raw)
	if !ok || access == "" {
		if !json.Valid(raw) {
			return Quota{Account: "codex", Name: "Codex", Status: "unknown"}, true
		}
		return Quota{}, false
	}
	access, accountID, err = ensureCodex(m, path, access, accountID)
	if err != nil {
		return Quota{Account: "codex", Name: "Codex", Status: "unknown"}, true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.chatGPTRoot()+"/backend-api/wham/usage", nil)
	if err != nil {
		return Quota{Account: "codex", Name: "Codex", Status: "unknown"}, true
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	body, status, err := doRead(m.client(), req)
	if err != nil || status < 200 || status >= 300 {
		return Quota{Account: "codex", Name: "Codex", Status: "unknown"}, true
	}
	quota, ok := parseWhamUsage(body, m.now())
	if !ok {
		return Quota{Account: "codex", Name: "Codex", Status: "unknown"}, true
	}
	return quota, true
}

type whamWindow struct {
	UsedPercent     float64 `json:"used_percent"`
	LimitWindowSecs int64   `json:"limit_window_seconds"`
	ResetAfterSecs  int64   `json:"reset_after_seconds"`
	ResetAt         int64   `json:"reset_at"`
}

// parseWhamUsage reads GET /backend-api/wham/usage. A window with neither a
// used percent nor a reset is not a window.
func parseWhamUsage(body []byte, now time.Time) (Quota, bool) {
	var doc struct {
		RateLimit struct {
			Primary   *whamWindow `json:"primary_window"`
			Secondary *whamWindow `json:"secondary_window"`
		} `json:"rate_limit"`
		Credits struct {
			HasCredits bool   `json:"has_credits"`
			Unlimited  bool   `json:"unlimited"`
			Balance    string `json:"balance"`
		} `json:"credits"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return Quota{}, false
	}
	quota := Quota{Account: "codex", Name: "Codex", Status: "ok"}
	for _, item := range []struct {
		fallback string
		window   *whamWindow
	}{
		{"primary", doc.RateLimit.Primary},
		{"secondary", doc.RateLimit.Secondary},
	} {
		if item.window == nil {
			continue
		}
		used := item.window.UsedPercent
		window := Window{Name: windowName(item.window.LimitWindowSecs, item.fallback), UsedPercent: &used}
		switch {
		case item.window.ResetAt > 0:
			at := time.Unix(item.window.ResetAt, 0)
			window.Reset = &at
		case item.window.ResetAfterSecs > 0:
			at := now.Add(time.Duration(item.window.ResetAfterSecs) * time.Second)
			window.Reset = &at
		}
		quota.Windows = append(quota.Windows, window)
	}
	if doc.Credits.HasCredits {
		switch {
		case doc.Credits.Unlimited:
			quota.Credits = "unlimited"
		case strings.TrimSpace(doc.Credits.Balance) != "":
			quota.Credits = strings.TrimSpace(doc.Credits.Balance)
		}
	}
	if len(quota.Windows) == 0 && quota.Credits == "" {
		return Quota{}, false
	}
	return quota, true
}

// windowName names a plan window from its length: five hours is "5h", a week
// is "week". An unusual length keeps the vendor's position word.
func windowName(seconds int64, fallback string) string {
	switch seconds {
	case 5 * 3600:
		return "5h"
	case 7 * 24 * 3600:
		return "week"
	case 24 * 3600:
		return "day"
	}
	return fallback
}

// claudeUsage runs Claude Code's own /usage answer. The subscription token
// stays in Claude Code's file; nothing is sent to Anthropic.
func claudeUsage(ctx context.Context, m Machine) (Quota, bool) {
	raw, err := m.read(claudeCreds(m))
	if err != nil {
		return Quota{}, false
	}
	raw = withoutUTF8BOM(raw)
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return Quota{Account: "claude", Name: "Claude Code", Status: "unknown"}, true
	}
	oauth := bytes.TrimSpace(doc["claudeAiOauth"])
	if len(oauth) == 0 || string(oauth) == "null" {
		return Quota{}, false
	}
	var token struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(oauth, &token) != nil || strings.TrimSpace(token.AccessToken) == "" {
		return Quota{Account: "claude", Name: "Claude Code", Status: "unknown"}, true
	}
	var out []byte
	if m.ClaudeUsage != nil {
		out, err = m.ClaudeUsage(ctx, m.Home)
	} else {
		out, err = runClaudeUsage(ctx, m.Home, m.ClaudeConfigDir)
	}
	if err != nil {
		return Quota{Account: "claude", Name: "Claude Code", Status: "unknown"}, true
	}
	windows := parseClaudeUsage(out)
	if len(windows) == 0 {
		return Quota{Account: "claude", Name: "Claude Code", Status: "unknown"}, true
	}
	return Quota{Account: "claude", Name: "Claude Code", Status: "ok", Windows: windows}, true
}

func runClaudeUsage(ctx context.Context, home, configDir string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", "/usage")
	cmd.Dir = home
	cmd.Env = claudeEnv(home, configDir)
	cmd.Stderr = io.Discard
	return cmd.Output()
}

var (
	claudeUsedRe  = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*%\s*used`)
	claudeParenRe = regexp.MustCompile(`\(([^)]*)\)`)
)

// parseClaudeUsage reads the lines Claude Code prints for /usage, both the
// one-line "Current session: 13% used" form and the block form with the
// percent and the reset on their own lines. The reset words are kept as the
// vendor wrote them.
func parseClaudeUsage(out []byte) []Window {
	lines := strings.Split(string(out), "\n")
	var windows []Window
	for i, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		var name string
		switch {
		case strings.Contains(lower, "current session"):
			name = "session"
		case strings.Contains(lower, "current week"):
			name = "week"
		default:
			continue
		}
		if match := claudeParenRe.FindStringSubmatch(line); match != nil {
			name += " (" + strings.TrimSpace(match[1]) + ")"
		}
		window := Window{Name: name}
		end := i + 4
		if end > len(lines) {
			end = len(lines)
		}
		for _, next := range lines[i:end] {
			if window.UsedPercent == nil {
				if match := claudeUsedRe.FindStringSubmatch(next); match != nil {
					if used, err := strconv.ParseFloat(match[1], 64); err == nil {
						window.UsedPercent = &used
					}
				}
			}
			trimmed := strings.TrimSpace(next)
			if window.ResetText == "" && len(trimmed) > len("resets") && strings.EqualFold(trimmed[:len("resets")], "resets") {
				window.ResetText = strings.TrimSpace(trimmed[len("resets"):])
			}
		}
		if window.UsedPercent == nil && window.ResetText == "" {
			continue
		}
		windows = append(windows, window)
	}
	return windows
}

func geminiUsage(ctx context.Context, m Machine) (Quota, bool) {
	return googleUsage(ctx, m, "gemini", "Gemini CLI", geminiCreds(m.Home), m.cloudCodeRoot(), false)
}

func antigravityUsage(ctx context.Context, m Machine) (Quota, bool) {
	return googleUsage(ctx, m, "antigravity", "Antigravity", antigravityCreds(m.Home), m.antiCloudRoot(), true)
}

// googleUsage asks the Code Assist host for the quota the signed-in Google
// account still has. fetchAvailableModels answers each model's remaining
// fraction and reset. Antigravity also asks retrieveUserQuotaSummary, which
// pools the models that share one allowance into its 5-hour and weekly groups,
// and those pooled windows are the card when they answer.
func googleUsage(ctx context.Context, m Machine, id, name, path, root string, pooled bool) (Quota, bool) {
	raw, err := m.read(path)
	if err != nil {
		return Quota{}, false
	}
	var creds struct {
		AccessToken string `json:"access_token"`
		ProjectID   string `json:"project_id"`
	}
	if json.Unmarshal(raw, &creds) != nil {
		return Quota{Account: id, Name: name, Status: "unknown"}, true
	}
	if strings.TrimSpace(creds.AccessToken) == "" {
		return Quota{}, false
	}
	payload := []byte(`{}`)
	if creds.ProjectID != "" {
		payload, _ = json.Marshal(map[string]string{"project": creds.ProjectID})
	}
	body, err := postQuota(ctx, m, root+"/v1internal:fetchAvailableModels", creds.AccessToken, payload)
	if err != nil {
		return Quota{Account: id, Name: name, Status: "unknown"}, true
	}
	windows := parseAvailableModels(body)
	if pooled {
		if summary, err := postQuota(ctx, m, root+"/v1internal:retrieveUserQuotaSummary", creds.AccessToken, payload); err == nil {
			if pooledWindows := parseQuotaSummary(summary); len(pooledWindows) > 0 {
				windows = pooledWindows
			}
		}
	}
	if len(windows) == 0 {
		return Quota{Account: id, Name: name, Status: "unknown"}, true
	}
	return Quota{Account: id, Name: name, Status: "ok", Windows: windows}, true
}

func postQuota(ctx context.Context, m Machine, endpoint, token string, payload []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	body, status, err := doRead(m.client(), req)
	if err != nil || status < 200 || status >= 300 {
		return nil, os.ErrInvalid
	}
	return body, nil
}

func doRead(client *http.Client, req *http.Request) ([]byte, int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// parseAvailableModels reads fetchAvailableModels: each model's
// quotaInfo.remainingFraction is the share of its quota still left, and
// resetTime is when the window renews. Models without a quota answer are not
// windows.
func parseAvailableModels(body []byte) []Window {
	var doc struct {
		Models map[string]struct {
			QuotaInfo *struct {
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
			} `json:"quotaInfo"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &doc) != nil || len(doc.Models) == 0 {
		return nil
	}
	names := make([]string, 0, len(doc.Models))
	for name := range doc.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	var windows []Window
	for _, name := range names {
		info := doc.Models[name].QuotaInfo
		if info == nil || info.RemainingFraction == nil {
			continue
		}
		window := Window{Name: name, RemainingFraction: info.RemainingFraction}
		setReset(&window, info.ResetTime)
		windows = append(windows, window)
	}
	return windows
}

// parseQuotaSummary reads retrieveUserQuotaSummary. Each bucket is one shared
// allowance pool, named by a model in it.
func parseQuotaSummary(body []byte) []Window {
	type bucket struct {
		ModelID           string   `json:"modelId"`
		RemainingFraction *float64 `json:"remainingFraction"`
		ResetTime         string   `json:"resetTime"`
	}
	var doc struct {
		Buckets      []bucket `json:"buckets"`
		QuotaSummary struct {
			Buckets []bucket `json:"buckets"`
		} `json:"quotaSummary"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil
	}
	buckets := doc.Buckets
	if len(buckets) == 0 {
		buckets = doc.QuotaSummary.Buckets
	}
	var windows []Window
	for _, b := range buckets {
		if b.ModelID == "" || b.RemainingFraction == nil {
			continue
		}
		window := Window{Name: b.ModelID, RemainingFraction: b.RemainingFraction}
		setReset(&window, b.ResetTime)
		windows = append(windows, window)
	}
	return windows
}

func setReset(window *Window, resetTime string) {
	resetTime = strings.TrimSpace(resetTime)
	if resetTime == "" {
		return
	}
	if at, err := time.Parse(time.RFC3339, resetTime); err == nil {
		window.Reset = &at
		return
	}
	window.ResetText = resetTime
}
