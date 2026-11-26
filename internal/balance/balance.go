// Package balance asks a provider for the credit still on one of its keys.
// It dials only a balance URL that vendor documents for that host.
package balance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

// Reading is one provider key's balance as that vendor reported it.
// Remaining is the vendor's own text, with the currency. It is empty when
// status is not ok. The provider key is not part of a reading.
type Reading struct {
	ProviderID string `json:"providerId"`
	Name       string `json:"name,omitempty"`
	Index      int    `json:"index"`
	Remaining  string `json:"remaining,omitempty"`
	Available  *bool  `json:"available,omitempty"`
	Status     string `json:"status"`
}

// ForProviders asks each saved key. Identical keys on one base URL are asked once.
// A host with no balance API is unsupported and is not dialed.
func ForProviders(ctx context.Context, client *http.Client, providers []state.Provider, consoleListen string) []Reading {
	if client == nil {
		client = defaultClient()
	}
	type keySlot struct {
		provider state.Provider
		index    int
		key      string
	}
	var slots []keySlot
	for _, prov := range providers {
		for i, key := range prov.Keys() {
			slots = append(slots, keySlot{provider: prov, index: i + 1, key: key})
		}
	}
	out := make([]Reading, len(slots))
	asked := map[string]int{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, slot := range slots {
		id := slot.provider.BaseURL + "\n" + slot.key
		if _, ok := asked[id]; ok {
			continue
		}
		asked[id] = i
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, slot keySlot) {
			defer wg.Done()
			defer func() { <-sem }()
			got := Ask(ctx, client, slot.provider.BaseURL, slot.key, consoleListen)
			got.ProviderID = slot.provider.ID
			got.Name = slot.provider.Name
			got.Index = slot.index
			out[i] = got
		}(i, slot)
	}
	wg.Wait()
	for i, slot := range slots {
		id := slot.provider.BaseURL + "\n" + slot.key
		first := asked[id]
		if first == i {
			continue
		}
		got := out[first]
		got.ProviderID = slot.provider.ID
		got.Name = slot.provider.Name
		got.Index = slot.index
		out[i] = got
	}
	if out == nil {
		out = []Reading{}
	}
	return out
}

func defaultClient() *http.Client {
	return &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Ask reads the balance for one key. An unknown host is unsupported and is not dialed.
func Ask(ctx context.Context, client *http.Client, baseURL, key, consoleListen string) Reading {
	endpoint, ok := Endpoint(baseURL)
	if !ok {
		return Reading{Status: "unsupported"}
	}
	if security.CheckBaseURL(baseURL, consoleListen, "") != security.BaseOK {
		return Reading{Status: "unknown"}
	}
	if client == nil {
		client = defaultClient()
	}
	body, status, err := get(ctx, client, endpoint, key)
	if err != nil || status != http.StatusOK {
		return Reading{Status: "unknown"}
	}
	remaining, available, ok := ParseDeepSeek(body)
	if !ok {
		return Reading{Status: "unknown"}
	}
	return Reading{Remaining: remaining, Available: available, Status: "ok"}
}

// Endpoint is the balance URL for a saved provider base URL.
// DeepSeek's balance is on the origin, not under /v1.
func Endpoint(baseURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	if !strings.EqualFold(u.Hostname(), "api.deepseek.com") {
		return "", false
	}
	u.Path = "/user/balance"
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), true
}

func get(ctx context.Context, client *http.Client, endpoint, key string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, res.StatusCode, err
	}
	return body, res.StatusCode, nil
}

// ParseDeepSeek reads GET /user/balance. The amounts stay the vendor's strings.
func ParseDeepSeek(body []byte) (remaining string, available *bool, ok bool) {
	var doc struct {
		IsAvailable *bool `json:"is_available"`
		Infos       []struct {
			Currency string `json:"currency"`
			Total    string `json:"total_balance"`
		} `json:"balance_infos"`
	}
	if json.Unmarshal(body, &doc) != nil || len(doc.Infos) == 0 {
		return "", nil, false
	}
	parts := make([]string, 0, len(doc.Infos))
	for _, info := range doc.Infos {
		total := strings.TrimSpace(info.Total)
		if total == "" {
			continue
		}
		currency := strings.TrimSpace(info.Currency)
		if currency == "" {
			parts = append(parts, total)
			continue
		}
		parts = append(parts, currency+" "+total)
	}
	if len(parts) == 0 {
		return "", nil, false
	}
	return strings.Join(parts, ", "), doc.IsAvailable, true
}
