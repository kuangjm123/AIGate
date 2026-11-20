package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kuangjm123/AIGate/internal/state"
)

func TestProbeReachesTheProviderWithItsKey(t *testing.T) {
	const secret = "provider-key"
	var gotAuth, gotPath, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer up.Close()
	got := (&Server{}).Probe(context.Background(), state.Provider{
		ID: "p", Protocol: "", BaseURL: up.URL, Models: []string{"demo"}, Credential: secret,
	}, "")
	if !got.OK || got.Status != http.StatusOK || got.Protocol != "chat" || got.Model != "demo" || got.Error != "" {
		t.Fatalf("probe %+v", got)
	}
	if gotAuth != "Bearer "+secret || !strings.Contains(gotPath, "/v1/chat/completions") || !strings.Contains(gotBody, `"max_tokens":1`) {
		t.Fatalf("auth %q path %q body %s", gotAuth, gotPath, gotBody)
	}
}

func TestProbeReportsTheProviderStatus(t *testing.T) {
	var gotKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer up.Close()
	got := (&Server{}).Probe(context.Background(), state.Provider{
		Protocol: "anthropic", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}, "")
	if got.OK || got.Status != http.StatusUnauthorized || got.Error != "auth_failure" || got.Protocol != "anthropic" || gotKey != "k" {
		t.Fatalf("probe %+v key %q", got, gotKey)
	}
}

func TestProbeDoesNotDialWithoutAConversationModel(t *testing.T) {
	called := false
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer up.Close()
	no := false
	got := (&Server{}).Probe(context.Background(), state.Provider{
		Protocol: "chat", BaseURL: up.URL, Models: []string{"pic"}, Credential: "k",
		Info: []state.ModelInfo{{ID: "pic", Kind: "image", Vision: &no}},
	}, "")
	if called || got.OK || got.Error != "model" {
		t.Fatalf("called %v probe %+v", called, got)
	}
}

func TestProbeDoesNotDialAnEmptyBaseURL(t *testing.T) {
	got := (&Server{}).Probe(context.Background(), state.Provider{
		Protocol: "chat", Models: []string{"demo"}, Credential: "k",
	}, "")
	if got.OK || got.Error != CodeUnknown || got.Status != 0 {
		t.Fatalf("probe %+v", got)
	}
}
