package login

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUsageOffReadsNothing(t *testing.T) {
	reads := 0
	got := Usage(t.Context(), Machine{Home: t.TempDir(), Off: true, ReadFile: func(string) ([]byte, error) {
		reads++
		return nil, os.ErrNotExist
	}})
	if len(got) != 0 || reads != 0 {
		t.Fatalf("off %#v reads %d", got, reads)
	}
}

func TestCodexUsageReadsWhamWindows(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"chatgpt","tokens":{"access_token":"`+jwtAt(time.Now().Add(2*time.Hour))+`","refresh_token":"r-1","account_id":"acct-1"}}`)
	reset := time.Now().Add(3 * time.Hour).Unix()
	var gotAuth, gotAccount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-ID")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "plus",
			"rate_limit": map[string]any{
				"primary_window":   map[string]any{"used_percent": 25, "limit_window_seconds": 18000, "reset_at": reset},
				"secondary_window": map[string]any{"used_percent": 4.5, "limit_window_seconds": 604800, "reset_after_seconds": 86400},
			},
			"credits": map[string]any{"has_credits": true, "unlimited": false, "balance": "12.34"},
		})
	}))
	defer srv.Close()
	got := Usage(t.Context(), Machine{Home: home, Client: srv.Client(), ChatGPT: srv.URL})
	if len(got) != 1 {
		t.Fatalf("quotas %#v", got)
	}
	quota := got[0]
	if quota.Account != "codex" || quota.Status != "ok" || quota.Credits != "12.34" || len(quota.Windows) != 2 {
		t.Fatalf("quota %#v", quota)
	}
	primary := quota.Windows[0]
	if primary.Name != "5h" || primary.UsedPercent == nil || *primary.UsedPercent != 25 || primary.Reset == nil || primary.Reset.Unix() != reset {
		t.Fatalf("primary %#v", primary)
	}
	week := quota.Windows[1]
	if week.Name != "week" || week.UsedPercent == nil || *week.UsedPercent != 4.5 || week.Reset == nil {
		t.Fatalf("week %#v", week)
	}
	if gotAuth == "" || gotAccount != "acct-1" {
		t.Fatalf("headers %q %q", gotAuth, gotAccount)
	}
	if blob, _ := json.Marshal(got); strings.Contains(string(blob), "r-1") {
		t.Fatal("quota contains the token")
	}
}

func TestCodexUsageSignedOutAndAPIKeyAreNotListed(t *testing.T) {
	home := t.TempDir()
	if got := Usage(t.Context(), Machine{Home: home}); len(got) != 0 {
		t.Fatalf("empty home %#v", got)
	}
	write(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`)
	if got := Usage(t.Context(), Machine{Home: home}); len(got) != 0 {
		t.Fatalf("apikey %#v", got)
	}
}

func TestCodexUsageFailedReadIsUnknown(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"chatgpt","tokens":{"access_token":"`+jwtAt(time.Now().Add(2*time.Hour))+`","account_id":"acct-1"}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	got := Usage(t.Context(), Machine{Home: home, Client: srv.Client(), ChatGPT: srv.URL})
	if len(got) != 1 || got[0].Status != "unknown" || len(got[0].Windows) != 0 {
		t.Fatalf("quota %#v", got)
	}
}

func TestClaudeUsageParsesSessionAndWeek(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"secret-token"}}`)
	machine := Machine{Home: home, ClaudeUsage: func(_ context.Context, _ string) ([]byte, error) {
		return []byte("Current session\n████ 13% used\nResets 4pm\n\nCurrent week (all models)\n4% used\nResets Nov 30 at 12pm\n"), nil
	}}
	got := Usage(t.Context(), machine)
	if len(got) != 1 {
		t.Fatalf("quotas %#v", got)
	}
	quota := got[0]
	if quota.Account != "claude" || quota.Status != "ok" || len(quota.Windows) != 2 {
		t.Fatalf("quota %#v", quota)
	}
	session := quota.Windows[0]
	if session.Name != "session" || session.UsedPercent == nil || *session.UsedPercent != 13 || session.ResetText != "4pm" {
		t.Fatalf("session %#v", session)
	}
	week := quota.Windows[1]
	if week.Name != "week (all models)" || week.UsedPercent == nil || *week.UsedPercent != 4 || week.ResetText != "Nov 30 at 12pm" {
		t.Fatalf("week %#v", week)
	}
	if blob, _ := json.Marshal(got); strings.Contains(string(blob), "secret-token") {
		t.Fatal("quota contains the token")
	}
}

func TestParseClaudeUsageReadsTheOneLineForm(t *testing.T) {
	windows := parseClaudeUsage([]byte("Current session: 13% used\nCurrent week (all models): 4% used\n"))
	if len(windows) != 2 {
		t.Fatalf("windows %#v", windows)
	}
	if windows[0].Name != "session" || *windows[0].UsedPercent != 13 {
		t.Fatalf("session %#v", windows[0])
	}
	if windows[1].Name != "week (all models)" || *windows[1].UsedPercent != 4 {
		t.Fatalf("week %#v", windows[1])
	}
	if windows := parseClaudeUsage([]byte("no usage here\n")); len(windows) != 0 {
		t.Fatalf("noise %#v", windows)
	}
}

func TestClaudeUsageSignedOutIsNotListed(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":null}`)
	asked := false
	got := Usage(t.Context(), Machine{Home: home, ClaudeUsage: func(context.Context, string) ([]byte, error) {
		asked = true
		return nil, nil
	}})
	if len(got) != 0 || asked {
		t.Fatalf("signed out %#v asked %v", got, asked)
	}
}

func TestGeminiUsageReadsRemainingFraction(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".gemini", "oauth_creds.json"), `{"access_token":"ya29-secret","project_id":"proj-1"}`)
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:fetchAvailableModels" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		gotBody = buf.String()
		_, _ = w.Write([]byte(`{"models":{"gemini-2.5-pro":{"quotaInfo":{"remainingFraction":0.87,"resetTime":"2026-11-26T10:00:00Z"}},"gemini-2.5-flash":{"quotaInfo":{"remainingFraction":1}},"gemini-no-quota":{}}}`))
	}))
	defer srv.Close()
	got := Usage(t.Context(), Machine{Home: home, Client: srv.Client(), CloudCode: srv.URL})
	if len(got) != 1 {
		t.Fatalf("quotas %#v", got)
	}
	quota := got[0]
	if quota.Account != "gemini" || quota.Status != "ok" || len(quota.Windows) != 2 {
		t.Fatalf("quota %#v", quota)
	}
	pro := quota.Windows[1]
	if pro.Name != "gemini-2.5-pro" || pro.RemainingFraction == nil || *pro.RemainingFraction != 0.87 || pro.Reset == nil || pro.Reset.Format(time.RFC3339) != "2026-11-26T10:00:00Z" {
		t.Fatalf("pro %#v", pro)
	}
	if gotAuth != "Bearer ya29-secret" || !strings.Contains(gotBody, "proj-1") {
		t.Fatalf("auth %q body %q", gotAuth, gotBody)
	}
	if blob, _ := json.Marshal(got); strings.Contains(string(blob), "ya29-secret") {
		t.Fatal("quota contains the token")
	}
}

func TestAntigravityUsagePrefersThePooledSummary(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".gemini", "antigravity", "oauth_creds.json"), `{"access_token":"ya29-anti"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:fetchAvailableModels":
			_, _ = w.Write([]byte(`{"models":{"gemini-3-pro":{"quotaInfo":{"remainingFraction":0.5,"resetTime":"2026-11-26T06:00:00Z"}}}}`))
		case "/v1internal:retrieveUserQuotaSummary":
			_, _ = w.Write([]byte(`{"buckets":[{"modelId":"gemini-3-pro","remainingFraction":0.75,"resetTime":"2026-11-26T06:00:00Z"},{"modelId":"gemini-3-flash","remainingFraction":0.9,"resetTime":"2026-11-30T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	got := Usage(t.Context(), Machine{Home: home, Client: srv.Client(), AntiCloud: srv.URL})
	if len(got) != 1 {
		t.Fatalf("quotas %#v", got)
	}
	quota := got[0]
	if quota.Account != "antigravity" || quota.Status != "ok" || len(quota.Windows) != 2 {
		t.Fatalf("quota %#v", quota)
	}
	if quota.Windows[0].Name != "gemini-3-pro" || *quota.Windows[0].RemainingFraction != 0.75 {
		t.Fatalf("pooled window %#v", quota.Windows[0])
	}
	if quota.Windows[1].Reset == nil || quota.Windows[1].Reset.Format(time.RFC3339) != "2026-11-30T00:00:00Z" {
		t.Fatalf("weekly window %#v", quota.Windows[1])
	}
}

func TestUsagePluginSlotIsNotAsked(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"chatgpt","tokens":{"access_token":"`+jwtAt(time.Now().Add(2*time.Hour))+`","account_id":"acct-1"}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("plugin-occupied account was dialed")
	}))
	defer srv.Close()
	got := Usage(t.Context(), Machine{Home: home, Client: srv.Client(), ChatGPT: srv.URL, PluginIDs: []string{"codex"}})
	if len(got) != 0 {
		t.Fatalf("quotas %#v", got)
	}
}
