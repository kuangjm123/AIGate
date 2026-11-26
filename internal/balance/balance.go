// Package balance asks a provider for the credit still on one of its keys.
// It dials only a balance URL that the vendor documents for that host, a URL
// the user supplied, or nothing at all.
package balance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

// Reading is one provider key's balance as that vendor reported it.
// Remaining is the vendor's own text, with the currency. It is empty when no
// reading has succeeded. Stale is true when a refresh failed and Remaining is
// the last good reading; Error then says why the refresh failed.
// The provider key is not part of a reading.
type Reading struct {
	ProviderID string `json:"providerId"`
	Name       string `json:"name,omitempty"`
	Index      int    `json:"index"`
	Remaining  string `json:"remaining,omitempty"`
	Available  *bool  `json:"available,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Stale      bool   `json:"stale,omitempty"`
}

// Query is one balance card to read. URL is a balance endpoint the user
// supplied; when it is empty the hostname table is used. Field is the dotted
// JSON path of the amount. Manual is the amount the user typed, shown when no
// endpoint answers and no earlier reading is kept. Currency prefixes it.
type Query struct {
	ProviderID string
	Name       string
	Index      int
	BaseURL    string
	Key        string
	URL        string
	Field      string
	Manual     string
	Currency   string
}

// Source is one vendor's balance endpoint. Host is the hostname (or host:port)
// the provider base URL uses. Path is appended to that saved origin; URL wins
// when it is set. Amount, CurrencyField, Available, and Subtract are dotted
// JSON paths. Parse overrides the field reader for a vendor whose response is
// not a single amount, such as DeepSeek's list of currencies.
type Source struct {
	Host          string
	URL           string
	Path          string
	Amount        string
	Currency      string
	CurrencyField string
	Available     string
	Subtract      string
	Parse         func(body []byte) (remaining string, available *bool, ok bool)
}

var registry = struct {
	sync.RWMutex
	byHost map[string]Source
}{byHost: builtinSources()}

func builtinSources() map[string]Source {
	return map[string]Source{
		"api.deepseek.com": {
			Host: "api.deepseek.com", Path: "/user/balance", Parse: ParseDeepSeek,
		},
		"openrouter.ai": {
			Host: "openrouter.ai", Path: "/api/v1/credits",
			Amount: "data.total_credits", Subtract: "data.total_usage", Currency: "$",
		},
	}
}

// Register adds or replaces one hostname's balance source. It is how a host
// outside the built-in table is read, and how a test points a source at an
// httptest server. Nothing is dialed here.
func Register(source Source) error {
	if strings.TrimSpace(source.URL) != "" {
		u, err := url.Parse(strings.TrimSpace(source.URL))
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("balance: source URL is not http or https")
		}
		if strings.TrimSpace(source.Host) == "" {
			source.Host = u.Host
		}
	} else {
		if strings.TrimSpace(source.Path) == "" || strings.TrimSpace(source.Host) == "" {
			return errors.New("balance: source needs a host and a URL or path")
		}
		if !strings.HasPrefix(source.Path, "/") {
			source.Path = "/" + source.Path
		}
	}
	host := strings.ToLower(strings.TrimSpace(source.Host))
	if host == "" {
		return errors.New("balance: source host is empty")
	}
	source.Host = host
	registry.Lock()
	defer registry.Unlock()
	if registry.byHost == nil {
		registry.byHost = map[string]Source{}
	}
	registry.byHost[host] = source
	return nil
}

// Reading cache. A successful read is kept for a minute; a failed refresh
// keeps the last good amount and marks the card stale. Identical keys on one
// base URL are asked once per minute.
var (
	clock     = time.Now
	readCache = struct {
		sync.Mutex
		reads map[string]cacheEntry
		good  map[string]Reading
	}{reads: map[string]cacheEntry{}, good: map[string]Reading{}}
)

type cacheEntry struct {
	reading Reading
	at      time.Time
}

const cacheTTL = time.Minute

// ResetCache forgets every cached reading. Tests use it between servers.
func ResetCache() {
	readCache.Lock()
	readCache.reads = map[string]cacheEntry{}
	readCache.good = map[string]Reading{}
	readCache.Unlock()
}

// ForProviders asks each saved key. Identical keys on one base URL are asked
// once. A provider's BalanceURL and BalanceField override the hostname table;
// BalanceManual is shown when no reading has succeeded. A host with no balance
// API and no manual amount is unsupported and is not dialed.
func ForProviders(ctx context.Context, client *http.Client, providers []state.Provider, consoleListen string) []Reading {
	var queries []Query
	for _, prov := range providers {
		for i, key := range prov.Keys() {
			queries = append(queries, Query{
				ProviderID: prov.ID, Name: prov.Name, Index: i + 1,
				BaseURL: prov.BaseURL, Key: key,
				URL: prov.BalanceURL, Field: prov.BalanceField,
				Manual: prov.BalanceManual, Currency: prov.BalanceCurrency,
			})
		}
	}
	return ForQueries(ctx, client, queries, consoleListen)
}

// ForQueries asks each balance card, in parallel but at most four at a time.
// A query with the same base URL, key, URL, field, and manual amount is asked
// once; each reply keeps its own provider id, name, and key index.
func ForQueries(ctx context.Context, client *http.Client, queries []Query, consoleListen string) []Reading {
	if client == nil {
		client = defaultClient()
	}
	out := make([]Reading, len(queries))
	first := map[string]int{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, q := range queries {
		id := queryID(q)
		if _, ok := first[id]; ok {
			continue
		}
		first[id] = i
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, q Query) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = askCached(ctx, client, q, consoleListen)
		}(i, q)
	}
	wg.Wait()
	for i, q := range queries {
		from := first[queryID(q)]
		if from == i {
			continue
		}
		out[i] = decorate(out[from], q)
	}
	return out
}

func queryID(q Query) string {
	return strings.Join([]string{
		q.BaseURL, state.KeyFingerprint(q.Key), q.URL, q.Field, q.Manual, q.Currency,
	}, "\n")
}

func defaultClient() *http.Client {
	return &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Ask reads the balance for one key using the hostname table. An unknown host
// is unsupported and is not dialed.
func Ask(ctx context.Context, client *http.Client, baseURL, key, consoleListen string) Reading {
	return AskQuery(ctx, client, Query{BaseURL: baseURL, Key: key}, consoleListen)
}

// AskQuery reads one balance card. A user-supplied URL is used before the
// hostname table. The caller keeps an earlier reading and decides when a
// manual amount is the fallback.
func AskQuery(ctx context.Context, client *http.Client, q Query, consoleListen string) Reading {
	if client == nil {
		client = defaultClient()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	source, endpoint, ok := endpointOf(q)
	if !ok {
		if strings.TrimSpace(q.Manual) != "" {
			return Manual(q.ProviderID, q.Name, q.Index, q.Manual, q.Currency)
		}
		return decorate(Reading{Status: "unsupported"}, q)
	}
	check := q.BaseURL
	if source.URL != "" {
		check = endpoint
	}
	if security.CheckBaseURL(check, consoleListen, "") != security.BaseOK {
		return decorate(Reading{Status: "unknown", Error: "balance URL is not allowed"}, q)
	}
	body, status, err := get(ctx, client, endpoint, q.Key)
	if err != nil {
		return decorate(Reading{Status: "unknown", Error: "balance endpoint could not be read"}, q)
	}
	if status != http.StatusOK {
		return decorate(Reading{Status: "unknown", Error: fmt.Sprintf("balance endpoint returned HTTP %d", status)}, q)
	}
	remaining, available, ok := parseSource(body, source)
	if !ok {
		return decorate(Reading{Status: "unknown", Error: "balance response did not hold an amount"}, q)
	}
	return decorate(Reading{Remaining: remaining, Available: available, Status: "ok"}, q)
}

// Manual is a user-typed amount. It is not a reading from the vendor.
func Manual(providerID, name string, index int, amount, currency string) Reading {
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return Reading{ProviderID: providerID, Name: name, Index: index, Status: "unsupported"}
	}
	return Reading{
		ProviderID: providerID, Name: name, Index: index,
		Remaining: joinAmount(strings.TrimSpace(currency), amount),
		Status:    "manual",
	}
}

func decorate(r Reading, q Query) Reading {
	r.ProviderID = q.ProviderID
	r.Name = q.Name
	r.Index = q.Index
	return r
}

func askCached(ctx context.Context, client *http.Client, q Query, consoleListen string) Reading {
	id := queryID(q)
	now := clock()
	readCache.Lock()
	entry, cached := readCache.reads[id]
	readCache.Unlock()
	if cached && now.Sub(entry.at) < cacheTTL {
		return decorate(entry.reading, q)
	}
	got := AskQuery(ctx, client, q, consoleListen)
	if got.Status != "ok" && got.Status != "manual" && got.Status != "unsupported" {
		readCache.Lock()
		previous, had := readCache.good[id]
		readCache.Unlock()
		if had {
			got.Remaining = previous.Remaining
			got.Available = previous.Available
			got.Stale = true
			if got.Error == "" {
				got.Error = "balance refresh failed"
			}
		} else if strings.TrimSpace(q.Manual) != "" {
			failure := got.Error
			got = Manual(q.ProviderID, q.Name, q.Index, q.Manual, q.Currency)
			got.Error = failure
		}
	}
	core := got
	core.ProviderID, core.Name, core.Index = "", "", 0
	readCache.Lock()
	readCache.reads[id] = cacheEntry{reading: core, at: now}
	if got.Status == "ok" {
		readCache.good[id] = core
	}
	readCache.Unlock()
	return decorate(core, q)
}

func endpointOf(q Query) (Source, string, bool) {
	if strings.TrimSpace(q.URL) != "" {
		source := Source{URL: strings.TrimSpace(q.URL), Amount: q.Field, Currency: q.Currency}
		return source, source.URL, true
	}
	return EndpointSource(q.BaseURL)
}

// Endpoint is the balance URL for a saved provider base URL.
// DeepSeek's balance is on the origin, not under /v1.
func Endpoint(baseURL string) (string, bool) {
	_, endpoint, ok := EndpointSource(baseURL)
	return endpoint, ok
}

// EndpointSource is Endpoint with the source that serves it.
func EndpointSource(baseURL string) (Source, string, bool) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return Source{}, "", false
	}
	source, ok := sourceOf(u)
	if !ok {
		return Source{}, "", false
	}
	if source.URL != "" {
		return source, source.URL, true
	}
	out := *u
	out.Path = source.Path
	out.RawPath = ""
	out.RawQuery = ""
	out.Fragment = ""
	return source, out.String(), true
}

func sourceOf(u *url.URL) (Source, bool) {
	if u == nil {
		return Source{}, false
	}
	registry.RLock()
	defer registry.RUnlock()
	if source, ok := registry.byHost[strings.ToLower(u.Host)]; ok {
		return source, true
	}
	source, ok := registry.byHost[strings.ToLower(u.Hostname())]
	return source, ok
}

func get(ctx context.Context, client *http.Client, endpoint, key string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(key) != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
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

var commonAmounts = []string{
	"remaining", "balance",
	"data.remaining", "data.balance", "data.available_balance",
	"data.total_balance", "data.totalBalance",
}

func parseSource(body []byte, source Source) (string, *bool, bool) {
	if source.Parse != nil {
		return source.Parse(body)
	}
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		return "", nil, false
	}
	path := source.Amount
	if path == "" {
		for _, candidate := range commonAmounts {
			if _, ok := lookup(doc, candidate); ok {
				path = candidate
				break
			}
		}
	}
	if path == "" {
		return "", nil, false
	}
	raw, ok := lookup(doc, path)
	if !ok {
		return "", nil, false
	}
	amount, numeric := amountText(raw)
	if amount == "" {
		return "", nil, false
	}
	if source.Subtract != "" {
		used, ok := numberAt(doc, source.Subtract)
		if !ok {
			return "", nil, false
		}
		left, ok := numberValue(raw)
		if !ok {
			return "", nil, false
		}
		amount = formatMoney(left - used)
	} else if numeric {
		amount = formatMoneyNumber(raw)
	}
	currency := strings.TrimSpace(source.Currency)
	if source.CurrencyField != "" {
		if got, ok := lookup(doc, source.CurrencyField); ok {
			if text, ok := got.(string); ok {
				currency = strings.TrimSpace(text)
			}
		}
	} else if currency == "" {
		for _, candidate := range []string{"currency", "data.currency"} {
			if got, ok := lookup(doc, candidate); ok {
				if text, ok := got.(string); ok {
					currency = strings.TrimSpace(text)
					break
				}
			}
		}
	}
	var available *bool
	if source.Available != "" {
		if got, ok := lookup(doc, source.Available); ok {
			if value, ok := got.(bool); ok {
				available = &value
			}
		}
	}
	return joinAmount(currency, amount), available, true
}

func lookup(doc any, path string) (any, bool) {
	current := doc
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			return nil, false
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := object[part]
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func amountText(raw any) (string, bool) {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value), false
	case float64:
		return formatMoney(value), true
	case json.Number:
		return value.String(), true
	default:
		return "", false
	}
}

func numberValue(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case string:
		got, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return got, err == nil
	case json.Number:
		got, err := value.Float64()
		return got, err == nil
	default:
		return 0, false
	}
}

func numberAt(doc any, path string) (float64, bool) {
	raw, ok := lookup(doc, path)
	if !ok {
		return 0, false
	}
	return numberValue(raw)
}

func formatMoneyNumber(raw any) string {
	value, ok := numberValue(raw)
	if !ok {
		return ""
	}
	return formatMoney(value)
}

func formatMoney(value float64) string {
	return strconv.FormatFloat(value, 'f', 2, 64)
}

func joinAmount(currency, amount string) string {
	currency = strings.TrimSpace(currency)
	amount = strings.TrimSpace(amount)
	if currency == "" {
		return amount
	}
	switch currency {
	case "$", "¥", "￥", "€", "£":
		return currency + amount
	default:
		return currency + " " + amount
	}
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
