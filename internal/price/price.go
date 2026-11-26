// Package price reads list prices and turns token counts into a dollar cost.
// The cost is computed when a record is read. It is not written back to the usage file.
package price

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
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
	Input         float64
	Output        float64
	CacheRead     float64
	CacheWrite    float64
	CacheReadSet  bool
	CacheWriteSet bool
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
	return (float64(c.In)*r.Input + float64(c.Out)*r.Output + float64(c.CacheRead)*cacheRead + float64(c.CacheWrite)*cacheWrite) / 1e6
}

// Tally sums priced calls. A call with no input and no output tokens is skipped.
// A call with tokens and no rate adds one to unpriced and nothing to the cost.
func Tally(recs []state.AttemptRecord, rate func(model string) (Rate, bool)) (cost float64, unpriced int) {
	for _, rec := range recs {
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
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

// ParseCatalog reads a models.dev api.json document.
// The same model id from a lab provider replaces one from another host.
func ParseCatalog(body []byte) (map[string]Rate, error) {
	var doc map[string]catalogProvider
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	type hit struct {
		rate Rate
		rank int
	}
	best := map[string]hit{}
	put := func(id string, rate Rate, rank int) {
		if id == "" {
			return
		}
		if old, ok := best[id]; ok && old.rank > rank {
			return
		}
		best[id] = hit{rate: rate, rank: rank}
	}
	for provider, item := range doc {
		rank := 1
		if labProvider(provider) {
			rank = 2
		}
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
			put(id, rate, rank)
			put(provider+"/"+id, rate, rank)
		}
	}
	out := make(map[string]Rate, len(best))
	for id, item := range best {
		out[id] = item.rate
	}
	return out, nil
}

func labProvider(id string) bool {
	switch id {
	case "openai", "anthropic", "google", "gemini":
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
