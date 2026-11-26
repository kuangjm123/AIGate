package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/state"
)

// spendBlock refuses a gateway token that has already reached its dollar cap.
// A token with no cap is not checked. A cap with no readable price list is
// price_unknown, which is not treated as zero spend.
func (s *Server) spendBlock(ctx context.Context, admitted admission) (string, int) {
	if !admitted.spendSet {
		return "", 0
	}
	if s == nil || s.Store == nil || s.Prices == nil || !s.Prices.Ready(ctx) {
		return CodePriceUnknown, http.StatusServiceUnavailable
	}
	period := admitted.spendPeriod
	if period == "" {
		period = "30d"
	}
	start, ok := state.PeriodStart(period, time.Now())
	if !ok {
		return CodePriceUnknown, http.StatusServiceUnavailable
	}
	filter := state.UsageFilter{TokenID: admitted.tokenID}
	if !start.IsZero() {
		filter.Start = &start
	}
	recs, err := s.Store.ReadUsage(filter)
	if err != nil {
		return CodePriceUnknown, http.StatusServiceUnavailable
	}
	cost, _ := price.Tally(recs, func(model string) (price.Rate, bool) {
		return s.Prices.Rate(ctx, model)
	})
	if cost >= admitted.spendLimit {
		return CodeSpendLimit, http.StatusTooManyRequests
	}
	return "", 0
}
