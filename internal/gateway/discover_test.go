package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kuangjm123/AIGate/internal/state"
)

func TestDetectNamesChatWhenTheModelsListAnswers(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" && r.Header.Get("anthropic-version") == "" && r.Header.Get("x-api-key") == "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer up.Close()
	got := (&Server{}).Detect(context.Background(), up.URL, "k", "")
	if got.Protocol != "chat" || got.Error != "" {
		t.Fatalf("detect %+v", got)
	}
	models := (&Server{}).FetchModels(context.Background(), up.URL, "chat", "k", "")
	if models.Error != "" || len(models.Models) != 1 || models.Models[0] != "gpt" {
		t.Fatalf("models %+v", models)
	}
}

func TestDetectKeepsAnOpenAIListAsChat(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"deepseek-chat","object":"model","owned_by":"deepseek"}]}`))
	}))
	defer up.Close()
	got := (&Server{}).Detect(context.Background(), up.URL, "sk-deepseek", "")
	if got.Protocol != "chat" || got.Error != "" {
		t.Fatalf("detect %+v", got)
	}
}

func TestPostProviderUsesTheNextKeyAfterARejection(t *testing.T) {
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer up.Close()
	resp, err := (&Server{}).postProvider(context.Background(), state.Provider{
		BaseURL: up.URL, Credentials: []string{"bad", "good"},
	}, ProtoChat, "demo", false, []byte(`{"model":"demo"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("status %v %v", resp, err)
	}
	resp.Body.Close()
	if strings.Join(seen, ",") != "Bearer bad,Bearer good" {
		t.Fatalf("keys %v", seen)
	}
}
