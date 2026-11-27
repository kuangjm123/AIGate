package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/balance"
	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/routes"
	"github.com/kuangjm123/AIGate/internal/state"
)

func percent(v float64) *float64 { return &v }

func TestApplyQuotaUsesOnlyWindowsThatConstrainTheModel(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	weekReset := now.Add(3 * 24 * time.Hour)
	shortReset := now.Add(2 * time.Hour)
	quota := login.Quota{Account: "codex", Status: "ok", Windows: []login.Window{
		{Name: "week", UsedPercent: percent(4.5), Reset: &weekReset},
		{Name: "5h", UsedPercent: percent(25), Reset: &shortReset},
	}}
	var seat routes.Seat
	applyQuota(&seat, quota, "gpt-5", now, false)
	if !seat.HasUsed || seat.Used != 25 || seat.UsedLong {
		t.Fatalf("used %+v", seat)
	}
	if len(seat.Soon) != 2 || !seat.Soon[0].Equal(weekReset) || !seat.Soon[1].Equal(shortReset) {
		t.Fatalf("soon %v", seat.Soon)
	}
	if !seat.HasWeek || seat.WeekLeft != 95.5 || !seat.WeekReset.Equal(weekReset) {
		t.Fatalf("week %+v", seat)
	}

	modelA := percent(100)
	modelB := percent(10)
	quota = login.Quota{Account: "gemini", Status: "ok", Windows: []login.Window{
		{Name: "gemini-3-pro", UsedPercent: modelA},
		{Name: "gemini-2.5-pro", UsedPercent: modelB},
	}}
	seat = routes.Seat{}
	applyQuota(&seat, quota, "gemini-2.5-pro", now, false)
	if !seat.HasUsed || seat.Used != 10 {
		t.Fatalf("model filter %+v", seat)
	}
}

func TestFreeStrategyReadsTheAccountPlan(t *testing.T) {
	now := time.Now()
	reset := now.Add(48 * time.Hour)
	srv := &Server{Login: func() login.Machine { return login.Machine{Off: true} }}
	srv.quotaMu.Lock()
	srv.quotaAt = now
	srv.quotaByID = map[string]quotaView{
		"paid": {Account: "paid", Name: "Pro", Status: "ok", Windows: []login.Window{{Name: "week", UsedPercent: percent(10), Reset: &reset}}},
		"free": {Account: "free", Name: "Free trial", Status: "ok", Windows: []login.Window{{Name: "week", UsedPercent: percent(90), Reset: &reset}}},
	}
	srv.quotaMu.Unlock()
	candidates := []seatCandidate{loginSeatCandidate("paid", "m"), loginSeatCandidate("free", "m")}
	ordered, _, _, _, _ := srv.orderSeats(context.Background(), state.Route{ID: "r", Routing: routes.Free}, "m", candidates)
	if ordered[0].Account != "free" {
		t.Fatalf("free plan not first: %+v", ordered)
	}
}

func TestUsageOrderUsesSubscriptionQuota(t *testing.T) {
	now := time.Now()
	reset := now.Add(48 * time.Hour)
	srv := &Server{Login: func() login.Machine { return login.Machine{Off: true} }}
	srv.quotaMu.Lock()
	srv.quotaAt = now
	srv.quotaByID = map[string]quotaView{
		"a": {Account: "a", Status: "ok", Windows: []login.Window{{Name: "week", UsedPercent: percent(80), Reset: &reset}}},
		"b": {Account: "b", Status: "ok", Windows: []login.Window{{Name: "week", UsedPercent: percent(10), Reset: &reset}}},
	}
	srv.quotaMu.Unlock()
	candidates := []seatCandidate{loginSeatCandidate("a", "m"), loginSeatCandidate("b", "m")}
	ordered, _, _, _, _ := srv.orderSeats(context.Background(), state.Route{ID: "r", Routing: routes.Usage}, "m", candidates)
	if ordered[0].Account != "b" {
		t.Fatalf("order %+v", ordered)
	}
}

func TestUnknownAccountIsTriedOnceThenFollowsKnownQuota(t *testing.T) {
	now := time.Now()
	reset := now.Add(48 * time.Hour)
	srv := &Server{Login: func() login.Machine { return login.Machine{Off: true} }}
	srv.quotaMu.Lock()
	srv.quotaAt = now
	srv.quotaByID = map[string]quotaView{
		"known": {Account: "known", Status: "ok", Windows: []login.Window{{Name: "week", UsedPercent: percent(50), Reset: &reset}}},
		"new":   {Account: "new", Status: "unknown"},
	}
	srv.quotaMu.Unlock()
	candidates := []seatCandidate{loginSeatCandidate("known", "m"), loginSeatCandidate("new", "m")}
	ordered, _, _, _, _ := srv.orderSeats(context.Background(), state.Route{ID: "r", Routing: routes.Usage}, "m", candidates)
	if ordered[0].Account != "new" {
		t.Fatalf("learning seat not first: %+v", ordered)
	}
	srv.markSuccess(candidates[1], now, nil, -1)
	ordered, _, _, _, _ = srv.orderSeats(context.Background(), state.Route{ID: "r", Routing: routes.Usage}, "m", candidates)
	if ordered[0].Account != "known" {
		t.Fatalf("learned seat stayed first: %+v", ordered)
	}
}

func TestBalanceBecomesLeftOnlyInOneUnit(t *testing.T) {
	var seat routes.Seat
	applyBalance(&seat, balanceView{ProviderID: "p1", Status: "ok", Remaining: "$12.34"})
	if seat.Left != 12.34 || seat.LeftIn != "balance:p1:$" {
		t.Fatalf("seat %+v", seat)
	}
	seat = routes.Seat{}
	applyBalance(&seat, balanceView{ProviderID: "p1", Status: "unknown", Remaining: "$12.34"})
	if seat.Left != 0 || seat.LeftIn != "" {
		t.Fatalf("unknown read became a value: %+v", seat)
	}
	seat = routes.Seat{}
	available := false
	applyBalance(&seat, balanceView{ProviderID: "p1", Status: "ok", Remaining: "¥ 5", Available: &available})
	if !seat.Spent || seat.Left != 5 || seat.LeftIn != "balance:p1:¥" {
		t.Fatalf("unavailable %+v", seat)
	}
}

func TestBalanceFillsSeatsBeforeSmartOrder(t *testing.T) {
	balance.ResetCache()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		amount := 5.0
		if key == "k2" {
			amount = 10.0
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"remaining": amount})
	}))
	defer up.Close()
	provider := state.Provider{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"},
		Credentials: []string{"k1", "k2"}, BalanceURL: up.URL, BalanceField: "remaining",
	}
	srv := &Server{}
	candidates := providerCandidates(state.Config{Providers: []state.Provider{provider}}, "demo", []string{"p1"})
	ordered, _, _, _, _ := srv.orderSeats(context.Background(), state.Route{ID: "r"}, "demo", candidates)
	if len(ordered) != 2 || ordered[0].KeyIndex != 1 {
		t.Fatalf("order %+v", ordered)
	}
}
