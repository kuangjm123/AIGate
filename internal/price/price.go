// Package price reads list prices and turns token counts into a dollar cost.
// The cost is computed when a record is read. It is not written back to the usage file.
package price

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kuangjm123/AIGate/internal/state"
)

// CatalogURL is the public model list. Prices are US dollars per million tokens.
// OpenAI, Anthropic, and Gemini model endpoints do not return a price.
const CatalogURL = "https://models.dev/api.json"

const catalogTTL = time.Hour

// Rate is one model's list price, in US dollars per million tokens.
// A zero rate is a price of nothing. A missing model is not a zero rate.
type Rate struct {
	Input           float64
	Output          float64
	CacheRead       float64
	CacheWrite      float64
	CacheWrite1H    float64
	CacheReadSet    bool
	CacheWriteSet   bool
	CacheWrite1HSet bool
}

// Cost is the dollar cost of one split. Cache tokens use their own rate when the
// catalog has one, and add nothing when it does not.
func (r Rate) Cost(c state.UsageCounts) float64 {
	cacheRead, cacheWrite := 0.0, 0.0
	if r.CacheReadSet {
		cacheRead = r.CacheRead
	}
	if r.CacheWriteSet {
		cacheWrite = r.CacheWrite
	}
	oneHour := cacheWrite
	if r.CacheWrite1HSet {
		oneHour = r.CacheWrite1H
	}
	cacheWrite1H := c.CacheWrite1H
	if cacheWrite1H < 0 {
		cacheWrite1H = 0
	}
	if cacheWrite1H > c.CacheWrite {
		cacheWrite1H = c.CacheWrite
	}
	cacheWrite5M := c.CacheWrite - cacheWrite1H
	return (float64(c.In)*r.Input +
		float64(c.Out)*r.Output +
		float64(c.CacheRead)*cacheRead +
		float64(cacheWrite5M)*cacheWrite +
		float64(cacheWrite1H)*oneHour) / 1e6
}

// Tally sums priced calls. An external cost is used as given. A call with no
// input and no output tokens is skipped. A call with tokens and no rate adds
// one to unpriced and nothing to the cost.
func Tally(recs []state.AttemptRecord, rate func(model string) (Rate, bool)) (cost float64, unpriced int) {
	for _, rec := range recs {
		if rec.Cost != nil {
			cost += *rec.Cost
			continue
		}
		counts := rec.Counts()
		if counts.In+counts.Out == 0 {
			continue
		}
		got, ok := rate(rec.Model)
		if !ok {
			unpriced++
			continue
		}
		cost += got.Cost(counts)
	}
	return round6(cost), unpriced
}

func round6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}

// Pricer looks up a model list price. Ready is false when no catalog has been read.
type Pricer interface {
	Rate(ctx context.Context, model string) (Rate, bool)
	Ready(ctx context.Context) bool
}

// Book is the models.dev catalog. A failed refresh keeps the last good copy.
type Book struct {
	url    string
	client *http.Client
	mu     sync.Mutex
	rates  map[string]Rate
	at     time.Time
}

// NewBook reads CatalogURL. It does not send a provider key or a gateway token.
func NewBook() *Book {
	return &Book{
		url: CatalogURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Rate returns the list price for model.
func (b *Book) Rate(ctx context.Context, model string) (Rate, bool) {
	rates, ok := b.snapshot(ctx)
	if !ok {
		return Rate{}, false
	}
	got, found := rates[model]
	return got, found
}

// Ready reports whether a catalog is loaded.
func (b *Book) Ready(ctx context.Context) bool {
	rates, ok := b.snapshot(ctx)
	return ok && len(rates) > 0
}

func (b *Book) snapshot(ctx context.Context) (map[string]Rate, bool) {
	if b == nil {
		return nil, false
	}
	b.mu.Lock()
	if b.rates != nil && time.Since(b.at) < catalogTTL {
		rates := b.rates
		b.mu.Unlock()
		return rates, true
	}
	b.mu.Unlock()
	rates, err := b.fetch(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil || len(rates) == 0 {
		if b.rates != nil {
			return b.rates, true
		}
		return nil, false
	}
	b.rates = rates
	b.at = time.Now()
	return b.rates, true
}

func (b *Book) fetch(ctx context.Context) (map[string]Rate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url, nil)
	if err != nil {
		return nil, err
	}
	res, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil, io.ErrUnexpectedEOF
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	return ParseCatalog(body)
}

type catalogProvider struct {
	Models map[string]catalogModel `json:"models"`
}

type catalogModel struct {
	Cost *catalogCost `json:"cost"`
}

type catalogCost struct {
	Input        float64  `json:"input"`
	Output       float64  `json:"output"`
	CacheRead    *float64 `json:"cache_read"`
	CacheWrite   *float64 `json:"cache_write"`
	CacheWrite1H *float64 `json:"cache_write_1h"`
}

// ParseCatalog reads a models.dev api.json document.
// A provider-qualified id keeps every host's price. A bare model id comes only
// from the model maker, so a reseller price is never guessed as the maker's.
func ParseCatalog(body []byte) (map[string]Rate, error) {
	var doc map[string]catalogProvider
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	type hit struct {
		rate     Rate
		rank     int
		provider string
	}
	best := map[string]hit{}
	put := func(id string, rate Rate, rank int, provider string) {
		if id == "" {
			return
		}
		if old, ok := best[id]; ok {
			if old.rank > rank || (old.rank == rank && old.provider < provider) {
				return
			}
		}
		best[id] = hit{rate: rate, rank: rank, provider: provider}
	}
	for provider, item := range doc {
		maker := labProvider(provider)
		for id, model := range item.Models {
			if model.Cost == nil {
				continue
			}
			rate := Rate{Input: model.Cost.Input, Output: model.Cost.Output}
			if model.Cost.CacheRead != nil {
				rate.CacheRead = *model.Cost.CacheRead
				rate.CacheReadSet = true
			}
			if model.Cost.CacheWrite != nil {
				rate.CacheWrite = *model.Cost.CacheWrite
				rate.CacheWriteSet = true
			}
			if model.Cost.CacheWrite1H != nil {
				rate.CacheWrite1H = *model.Cost.CacheWrite1H
				rate.CacheWrite1HSet = true
			}
			put(provider+"/"+id, rate, 1, provider)
			if maker {
				put(id, rate, 2, provider)
			}
		}
	}
	out := make(map[string]Rate, len(best))
	for id, item := range best {
		out[id] = item.rate
	}
	return out, nil
}

func labProvider(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "openai", "anthropic", "google", "gemini",
		"deepseek", "mistral", "mistralai", "xai", "x-ai",
		"meta", "meta-llama", "cohere", "perplexity",
		"alibaba", "alibaba-cloud", "qwen",
		"moonshot", "moonshotai", "zhipu", "zhipuai", "z-ai", "minimax",
		"bytedance", "volcengine", "baidu", "tencent",
		"ai21", "amazon", "microsoft", "nvidia", "upstage", "reka",
		"01-ai", "baichuan", "stepfun":
		return true
	default:
		return false
	}
}

// Fixed is a pricer for tests. Ready is true.
type Fixed map[string]Rate

// Rate returns the rate stored for model.
func (f Fixed) Rate(_ context.Context, model string) (Rate, bool) {
	got, ok := f[model]
	return got, ok
}

// Ready reports that the fixed list can be read.
func (f Fixed) Ready(context.Context) bool {
	return true
}

// Engine turns a usage record into a dollar cost. It applies user prices,
// provider and upstream-key multipliers, and peak windows from the config in
// that order. It never writes any of them back to the ledger.
type Engine struct {
	Catalog  Pricer
	Config   state.Config
	Location *time.Location
}

// Cost is one record's dollar cost. The second value is false when the record
// has billable tokens but no price.
func (e Engine) Cost(ctx context.Context, rec state.AttemptRecord) (float64, bool) {
	if rec.Cost != nil {
		return *rec.Cost, true
	}
	counts := rec.Counts()
	if counts.In+counts.Out == 0 {
		return 0, false
	}
	rate, ok := e.rate(ctx, rec)
	if !ok {
		return 0, false
	}
	return rate.Cost(counts), true
}

func (e Engine) rate(ctx context.Context, rec state.AttemptRecord) (Rate, bool) {
	rate, found := Rate{}, false
	if e.Catalog != nil {
		for _, key := range e.catalogKeys(rec) {
			if got, ok := e.Catalog.Rate(ctx, key); ok {
				rate, found = got, true
				break
			}
		}
		if !found {
			if got, ok := e.Catalog.Rate(ctx, rec.Model); ok {
				rate, found = got, true
			}
		}
	}
	if found {
		scaleRate(&rate, e.multiplier(rec))
	}
	rule, overridden := e.userRule(rec.ProviderID, rec.Model)
	if overridden {
		if rule.Input != nil {
			rate.Input = *rule.Input
		}
		if rule.Output != nil {
			rate.Output = *rule.Output
		}
		if rule.CacheRead != nil {
			rate.CacheRead, rate.CacheReadSet = *rule.CacheRead, true
		}
		if rule.CacheWrite != nil {
			rate.CacheWrite, rate.CacheWriteSet = *rule.CacheWrite, true
		}
		if rule.CacheWrite1H != nil {
			rate.CacheWrite1H, rate.CacheWrite1HSet = *rule.CacheWrite1H, true
		}
		found = true
	}
	if !found {
		return Rate{}, false
	}
	scaleRate(&rate, e.peakFactor(rec))
	return rate, true
}

// scaleRate multiplies every price present in the rate. User-written fields
// are laid over the catalog after the multiplier, so they keep their own rate.
func scaleRate(rate *Rate, factor float64) {
	rate.Input *= factor
	rate.Output *= factor
	if rate.CacheReadSet {
		rate.CacheRead *= factor
	}
	if rate.CacheWriteSet {
		rate.CacheWrite *= factor
	}
	if rate.CacheWrite1HSet {
		rate.CacheWrite1H *= factor
	}
}

// catalogKeys are the catalog provider names to try for a record. Usage lines
// store the configured provider id; models.dev names the vendor. The display
// name and host are safe, local fallbacks when a user-id is not a vendor id.
func (e Engine) catalogKeys(rec state.AttemptRecord) []string {
	if rec.Model == "" {
		return nil
	}
	keys := make([]string, 0, 3)
	seen := map[string]struct{}{}
	add := func(provider string) {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			return
		}
		key := provider + "/" + rec.Model
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	add(rec.ProviderID)
	for _, provider := range e.Config.Providers {
		if provider.ID != rec.ProviderID {
			continue
		}
		add(provider.Name)
		if u, err := url.Parse(strings.TrimSpace(provider.BaseURL)); err == nil {
			add(u.Hostname())
		}
		break
	}
	return keys
}

func (e Engine) userRule(provider, model string) (state.PriceRule, bool) {
	for _, rule := range e.Config.Prices {
		if rule.Provider == provider && rule.Model == model {
			return rule, true
		}
	}
	for _, rule := range e.Config.Prices {
		if rule.Provider == provider && rule.Model == "*" {
			return rule, true
		}
	}
	for _, rule := range e.Config.Prices {
		if (rule.Provider == "" || rule.Provider == "*") && rule.Model == model {
			return rule, true
		}
	}
	for _, rule := range e.Config.Prices {
		if (rule.Provider == "" || rule.Provider == "*") && rule.Model == "*" {
			return rule, true
		}
	}
	return state.PriceRule{}, false
}

func (e Engine) multiplier(rec state.AttemptRecord) float64 {
	for _, provider := range e.Config.Providers {
		if provider.ID != rec.ProviderID {
			continue
		}
		if rec.ProviderKeyID != "" && len(provider.KeyMultipliers) > 0 {
			for i, key := range provider.Keys() {
				if i >= len(provider.KeyMultipliers) {
					break
				}
				if state.KeyFingerprint(key) == rec.ProviderKeyID {
					return provider.KeyMultipliers[i]
				}
			}
		}
		if provider.Multiplier != nil {
			return *provider.Multiplier
		}
		return 1
	}
	return 1
}

func (e Engine) peakFactor(rec state.AttemptRecord) float64 {
	if len(e.Config.Peaks) == 0 || rec.Started.IsZero() {
		return 1
	}
	loc := e.Location
	if loc == nil {
		loc = time.Local
	}
	local := rec.Started.In(loc)
	minute := local.Hour()*60 + local.Minute()
	for _, peak := range e.Config.Peaks {
		if peak.Provider != "" && peak.Provider != "*" && peak.Provider != rec.ProviderID {
			continue
		}
		if peak.Model != "" && peak.Model != "*" && peak.Model != rec.Model {
			continue
		}
		start, ok := clock(peak.Start)
		if !ok {
			continue
		}
		end, ok := clock(peak.End)
		if !ok || start == end {
			continue
		}
		if inClock(minute, start, end) {
			return peak.Factor
		}
	}
	return 1
}

func clock(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	hour, err := strconv.Atoi(s[:2])
	if err != nil || hour < 0 || hour > 23 {
		return 0, false
	}
	minute, err := strconv.Atoi(s[3:])
	if err != nil || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

func inClock(minute, start, end int) bool {
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}
