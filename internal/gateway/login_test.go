package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuangjm123/AIGate/internal/login"
)

func TestClaudeSubscriptionReturnsTheToolToTheCaller(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "sk-ant-oat-secret"
	if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"`+secret+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, token := dialStore(t, nil, nil)
	srv := &Server{Store: store, Login: func() login.Machine {
		return login.Machine{Home: home, Claude: func(context.Context, string, string, []byte) ([]byte, error) {
			return []byte(`{"id":"msg_local","type":"message","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"bash","input":{"cmd":"ls"}}],"stop_reason":"tool_use"}`), nil
		}}
	}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"login/claude/claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "bash") || strings.Contains(rr.Body.String(), secret) {
		t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
	}
}
