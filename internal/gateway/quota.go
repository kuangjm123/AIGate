package gateway

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kuangjm123/AIGate/internal/balance"
	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/routes"
)

const (
	quotaTTL     = 30 * time.Second
	quotaTimeout = 12 * time.Second
)

type quotaView = login.Quota
type balanceView = balance.Reading

// accountQuotas reads subscription allowances at most once per quotaTTL. A
// failed read is kept as unknown, not as an empty allowance. Callers run
// before Order, so a strategy still never waits.
func (s *Server) accountQuotas(ctx context.Context, fields routes.Fields) map[string]quotaView {
	if s == nil || s.Login == nil || (!fields.ByUse && !fields.ByPlan && !fields.ByFree) {
		return nil
	}
	now := time.Now()
	s.quotaMu.Lock()
	if !s.quotaAt.IsZero() && now.Sub(s.quotaAt) < quotaTTL {
		out := s.quotaByID
		s.quotaMu.Unlock()
		return out
	}
	if wait := s.quotaWait; wait != nil {
		s.quotaMu.Unlock()
		select {
		case <-wait:
			return s.accountQuotas(ctx, fields)
		case <-ctx.Done():
			return nil
		}
	}
	wait := make(chan struct{})
	s.quotaWait = wait
	s.quotaMu.Unlock()
	readCtx, cancel := context.WithTimeout(ctx, quotaTimeout)
	quotas := login.Usage(readCtx, s.loginMachine())
	cancel()
	out := make(map[string]quotaView, len(quotas))
	for _, quota := range quotas {
		out[quota.Account] = quota
	}
	s.quotaMu.Lock()
	s.quotaByID = out
	s.quotaAt = time.Now()
	s.quotaWait = nil
	close(wait)
	s.quotaMu.Unlock()
	return out
}

// seatBalances reads the prepaid balance of the provider key seats when the
// strategy asked for usage fields. The balance package caches successful reads
// for a minute, so repeated requests do not dial the vendor each time.
func (s *Server) seatBalances(ctx context.Context, candidates []seatCandidate, fields routes.Fields) map[string]balanceView {
	if !fields.ByUse {
		return nil
	}
	queries := make([]balance.Query, 0, len(candidates))
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Login {
			continue
		}
		key := ""
		keysOfProvider := candidate.Provider.Keys()
		if candidate.KeyIndex >= 0 && candidate.KeyIndex < len(keysOfProvider) {
			key = keysOfProvider[candidate.KeyIndex]
		}
		queries = append(queries, balance.Query{
			ProviderID: candidate.ProviderID,
			Name:       candidate.Provider.Name,
			Index:      candidate.KeyIndex + 1,
			BaseURL:    candidate.Provider.BaseURL,
			Key:        key,
			URL:        candidate.Provider.BalanceURL,
			Field:      candidate.Provider.BalanceField,
			Manual:     candidate.Provider.BalanceManual,
			Currency:   candidate.Provider.BalanceCurrency,
		})
		keys = append(keys, candidate.Key)
	}
	if len(queries) == 0 {
		return nil
	}
	readings := balance.ForQueries(ctx, nil, queries, s.consoleAddr())
	out := make(map[string]balanceView, len(readings))
	for i, reading := range readings {
		if i >= len(keys) {
			break
		}
		out[keys[i]] = reading
	}
	return out
}

// applyBalance turns a vendor balance card into the Left fields. The amount is
// only compared inside the same provider and unit, so a dollar balance never
// outranks a credit balance.
func applyBalance(seat *routes.Seat, reading balanceView) {
	if seat == nil || reading.ProviderID == "" {
		return
	}
	switch reading.Status {
	case "ok", "manual":
	default:
		return
	}
	amount, unit, ok := parseAmount(reading.Remaining)
	if !ok {
		return
	}
	seat.Left = amount
	seat.LeftIn = "balance:" + reading.ProviderID + ":" + unit
	if reading.Available != nil && !*reading.Available {
		seat.Spent = true
	}
}

func parseAmount(text string) (float64, string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, "", false
	}
	// A vendor that reports several currencies at once is not one balance to
	// compare; leave it unknown instead of picking an arbitrary one.
	if strings.Contains(text, ", ") {
		return 0, "", false
	}
	text = strings.ReplaceAll(text, ",", "")
	start, end := -1, -1
	for i, r := range text {
		if (r >= '0' && r <= '9') || r == '-' || r == '+' || r == '.' {
			if start < 0 {
				start = i
			}
			end = i + 1
		}
	}
	if start < 0 {
		return 0, "", false
	}
	number := strings.TrimSpace(text[start:end])
	value, err := strconv.ParseFloat(number, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, "", false
	}
	unit := strings.TrimSpace(text[:start] + " " + text[end:])
	if unit == "" {
		unit = "unit"
	}
	return value, unit, true
}

// applyQuota fills the Smart, usage, pace, and expiring fields from one
// subscription account's windows. Windows for other models are not applied to
// this model. A read that failed leaves every field unknown.
func applyQuota(seat *routes.Seat, quota quotaView, model string, now time.Time, learned bool) {
	if seat == nil {
		return
	}
	if quota.Status != "ok" {
		if !learned {
			seat.Learns = true
		}
		return
	}
	if soundsFree(quota.Name) {
		seat.FreePlan = true
	}
	type window struct {
		name    string
		length  time.Duration
		used    float64
		usedSet bool
		reset   time.Time
		resetOK bool
	}
	windows := make([]window, 0, len(quota.Windows))
	for _, item := range quota.Windows {
		length, generic := windowLength(item.Name)
		if !generic && !windowMatchesModel(item.Name, model) {
			continue
		}
		used, usedSet := windowUsed(item)
		if !usedSet && item.Reset == nil {
			continue
		}
		w := window{name: item.Name, length: length, used: used, usedSet: usedSet}
		if item.Reset != nil {
			w.reset = *item.Reset
			w.resetOK = true
		}
		windows = append(windows, w)
	}
	if len(windows) == 0 {
		if !learned {
			seat.Learns = true
		}
		return
	}
	sort.SliceStable(windows, func(i, j int) bool {
		if windows[i].length != windows[j].length {
			return windows[i].length > windows[j].length
		}
		if windows[i].resetOK != windows[j].resetOK {
			return windows[i].resetOK
		}
		return windows[i].reset.Before(windows[j].reset)
	})
	seat.Soon = seat.Soon[:0]
	for _, w := range windows {
		if w.resetOK {
			seat.Soon = append(seat.Soon, w.reset)
		}
	}
	best := -1
	for i, w := range windows {
		if !w.usedSet {
			continue
		}
		if best < 0 || w.used > windows[best].used {
			best = i
			continue
		}
		if w.used == windows[best].used && w.length > windows[best].length {
			best = i
		}
	}
	if best >= 0 {
		seat.HasUsed = true
		seat.Used = windows[best].used
		seat.UsedLong = windows[best].length >= 7*24*time.Hour
	}
	for _, w := range windows {
		if !isWeekWindow(w.name) || !w.usedSet {
			continue
		}
		seat.HasWeek = true
		seat.WeekLeft = 100 - w.used
		if w.resetOK {
			seat.WeekReset = w.reset
		}
		break
	}
	if credits, ok := parseCredits(quota.Credits); ok {
		seat.Credits = credits > 0
		if credits > 0 {
			seat.Left = credits
			seat.LeftIn = "credits:" + quota.Account
		}
	} else if strings.EqualFold(strings.TrimSpace(quota.Credits), "unlimited") {
		seat.Credits = true
	}
	if seat.FreePlan && len(seat.Soon) > 0 {
		earliest := seat.Soon[0]
		for _, reset := range seat.Soon[1:] {
			if reset.Before(earliest) {
				earliest = reset
			}
		}
		seat.Expires = earliest
		seat.ExpiresSet = true
	}
	_ = now
}

func windowUsed(item login.Window) (float64, bool) {
	if item.UsedPercent != nil {
		return clampPercent(*item.UsedPercent), true
	}
	if item.RemainingFraction != nil {
		return clampPercent((1 - *item.RemainingFraction) * 100), true
	}
	return 0, false
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func windowLength(name string) (time.Duration, bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.Contains(n, "week"):
		return 7 * 24 * time.Hour, true
	case strings.Contains(n, "month"):
		return 30 * 24 * time.Hour, true
	case strings.Contains(n, "day"):
		return 24 * time.Hour, true
	case strings.Contains(n, "5h"), strings.Contains(n, "5-hour"), strings.Contains(n, "session"):
		return 5 * time.Hour, true
	case strings.Contains(n, "hour"):
		return time.Hour, true
	case n == "primary" || n == "secondary":
		return 0, true
	}
	return 0, false
}

func isWeekWindow(name string) bool {
	return strings.Contains(strings.ToLower(name), "week")
}

func windowMatchesModel(name, model string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	model = strings.ToLower(strings.TrimSpace(model))
	if name == "" || model == "" {
		return false
	}
	if name == model {
		return true
	}
	if len(name) >= 4 && strings.Contains(model, name) {
		return true
	}
	return len(model) >= 4 && strings.Contains(name, model)
}

func soundsFree(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, word := range []string{"free", "trial", "免费", "试用"} {
		if strings.Contains(n, word) {
			return true
		}
	}
	return false
}

func parseCredits(text string) (float64, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false
	}
	value, unit, ok := parseAmount(text)
	if !ok {
		return 0, false
	}
	_ = unit
	return value, true
}
