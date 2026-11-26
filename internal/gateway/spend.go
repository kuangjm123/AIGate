package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/state"
)

// spendBlock refuses a gateway token that has already reached any of its
// caps. The older single spendLimit is checked as one more cap. A token with
// no cap is not checked. A dollar cap with no readable price list, or a row
// with billable tokens and no price, is price_unknown, which is not zero.
func (s *Server) spendBlock(ctx context.Context, admitted admission, cfg state.Config) (string, int) {
	if !admitted.spendSet && len(admitted.limits) == 0 && admitted.issuerID == "" {
		return "", 0
	}
	now := time.Now()
	type check struct {
		limit    float64
		hasLimit bool
		tokens   int
		hasToken bool
		start    time.Time
		provider string
		model    string
		keyID    string
	}
	var checks []check
	needPrices := false
	var earliest time.Time
	haveEarliest := false
	allTime := false
	addStart := func(start time.Time) {
		if start.IsZero() {
			allTime = true
			return
		}
		if allTime {
			return
		}
		if !haveEarliest || start.Before(earliest) {
			earliest, haveEarliest = start, true
		}
	}
	own := admitted.tokenID
	if admitted.spendSet {
		period := admitted.spendPeriod
		if period == "" {
			period = "30d"
		}
		start, ok := state.PeriodStart(period, now)
		if !ok {
			return CodePriceUnknown, http.StatusServiceUnavailable
		}
		checks = append(checks, check{limit: admitted.spendLimit, hasLimit: true, start: start, keyID: own})
		needPrices = true
		addStart(start)
	}
	for _, limit := range admitted.limits {
		start, ok := limit.WindowStart(now)
		if !ok {
			return CodePriceUnknown, http.StatusServiceUnavailable
		}
		entry := check{start: start, provider: limit.Provider, model: limit.Model, keyID: own}
		if limit.USD != nil && *limit.USD > 0 {
			entry.limit, entry.hasLimit = *limit.USD, true
			needPrices = true
		}
		if limit.Tokens != nil && *limit.Tokens > 0 {
			entry.tokens, entry.hasToken = *limit.Tokens, true
		}
		checks = append(checks, entry)
		addStart(start)
	}
	// A short-lived token spends its issuer's budget too. The issuer's caps
	// apply to this call, and every key it issued counts toward them.
	if admitted.issuerID != "" && admitted.issuerID != own {
		if issuer, ok := cfg.TokenByID(admitted.issuerID); ok {
			if issuer.SpendLimit != nil && *issuer.SpendLimit > 0 {
				period := issuer.SpendPeriod
				if period == "" {
					period = "30d"
				}
				start, ok := state.PeriodStart(period, now)
				if !ok {
					return CodePriceUnknown, http.StatusServiceUnavailable
				}
				checks = append(checks, check{
					limit: *issuer.SpendLimit, hasLimit: true, start: start, keyID: issuer.ID,
				})
				needPrices = true
				addStart(start)
			}
			for _, limit := range issuer.Limits {
				start, ok := limit.WindowStart(now)
				if !ok {
					return CodePriceUnknown, http.StatusServiceUnavailable
				}
				entry := check{
					start: start, provider: limit.Provider, model: limit.Model, keyID: issuer.ID,
				}
				if limit.USD != nil && *limit.USD > 0 {
					entry.limit, entry.hasLimit = *limit.USD, true
					needPrices = true
				}
				if limit.Tokens != nil && *limit.Tokens > 0 {
					entry.tokens, entry.hasToken = *limit.Tokens, true
				}
				checks = append(checks, entry)
				addStart(start)
			}
		}
	}
	hasCheck := false
	for _, item := range checks {
		if item.hasLimit || item.hasToken {
			hasCheck = true
			break
		}
	}
	if !hasCheck {
		return "", 0
	}
	if s == nil || s.Store == nil {
		return CodePriceUnknown, http.StatusServiceUnavailable
	}
	if needPrices && (s.Prices == nil || !s.Prices.Ready(ctx)) {
		return CodePriceUnknown, http.StatusServiceUnavailable
	}
	filter := state.UsageFilter{}
	if !allTime && haveEarliest {
		filter.Start = &earliest
	}
	recs, err := s.Store.ReadUsage(filter)
	if err != nil {
		return CodePriceUnknown, http.StatusServiceUnavailable
	}
	engine := price.Engine{Catalog: s.Prices, Config: cfg}
	unpriced := false
	for _, item := range checks {
		if !item.hasLimit && !item.hasToken {
			continue
		}
		var tokens int
		var cost float64
		for _, rec := range recs {
			if !rec.InLimit() || !rec.ChargedTo(item.keyID) {
				continue
			}
			if !item.start.IsZero() && rec.Started.Before(item.start) {
				continue
			}
			if item.provider != "" && item.provider != rec.ProviderID {
				continue
			}
			if item.model != "" && item.model != rec.Model {
				continue
			}
			counts := rec.Counts()
			tokens += counts.Total()
			if !item.hasLimit {
				continue
			}
			if got, ok := engine.Cost(ctx, rec); ok {
				cost += got
			} else if counts.In+counts.Out > 0 {
				unpriced = true
			}
		}
		if item.hasToken && tokens >= item.tokens {
			return CodeSpendLimit, http.StatusTooManyRequests
		}
		if item.hasLimit && cost >= item.limit {
			return CodeSpendLimit, http.StatusTooManyRequests
		}
	}
	if unpriced {
		return CodePriceUnknown, http.StatusServiceUnavailable
	}
	return "", 0
}
