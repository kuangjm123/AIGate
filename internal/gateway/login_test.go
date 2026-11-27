package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/state"
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

func TestUnknownSubscriptionWritesALocalRejection(t *testing.T) {
	store, token := dialStore(t, nil, nil)
	srv := &Server{Store: store, Login: func() login.Machine {
		return login.Machine{Home: t.TempDir()}
	}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"login/missing/claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), CodeNoRoute) {
		t.Fatalf("reply %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("usage %+v %v", recs, err)
	}
	rec := recs[0]
	if rec.RouteID == "" || rec.ProviderID != "login/missing" || rec.ProviderAccount != "missing" ||
		rec.Model != "claude-sonnet-4-6" || rec.Outcome != CodeNoRoute ||
		rec.Status != http.StatusNotFound || !rec.Rejected || !rec.Failed() {
		t.Fatalf("usage %+v", rec)
	}
}

func TestExplicitRouteAndCatalogSkipSubscriptionDiscovery(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	cases := []struct {
		name   string
		model  string
		routes []state.Route
	}{
		{name: "route", model: "demo", routes: []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}}},
		{name: "catalog", model: "p1/demo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, secret := dialStore(t, []state.Provider{{
				ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
			}}, tc.routes)
			reads := 0
			srv := &Server{Store: store, Login: func() login.Machine {
				return login.Machine{Home: t.TempDir(), ReadFile: func(string) ([]byte, error) {
					reads++
					return nil, os.ErrNotExist
				}}
			}}
			rr := callGateway(t, srv, secret, http.MethodPost, "/v1/chat/completions",
				`{"model":"`+tc.model+`","messages":[{"role":"user","content":"hi"}]}`)
			if rr.Code != http.StatusOK || reads != 0 {
				t.Fatalf("model %q %d reads %d %s", tc.model, rr.Code, reads, rr.Body.String())
			}
		})
	}
}

func TestCatalogDoesNotWaitForSlowSubscriptionDiscovery(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	releaseDiscovery := func() {
		if !released {
			released = true
			close(release)
		}
	}
	defer releaseDiscovery()
	var mu sync.Mutex
	blocked := false
	srv := &Server{Store: store, Login: func() login.Machine {
		return login.Machine{Home: t.TempDir(), ReadFile: func(string) ([]byte, error) {
			mu.Lock()
			if !blocked {
				blocked = true
				close(started)
				mu.Unlock()
				<-release
				return nil, os.ErrNotExist
			}
			mu.Unlock()
			return nil, os.ErrNotExist
		}}
	}}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		done <- rr
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog did not start subscription discovery")
	}
	var rr *httptest.ResponseRecorder
	select {
	case rr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog waited for subscription discovery")
	}
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"id":"p1/demo"`) {
		t.Fatalf("catalog %d %s", rr.Code, rr.Body.String())
	}
	releaseDiscovery()
	_ = srv.loginSnapshot(context.Background(), 2*time.Second)
}
