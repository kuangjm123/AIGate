package gateway

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/routes"
	"github.com/kuangjm123/AIGate/internal/state"
)

// This file is the gateway side of routing strategies: it fills the seats a
// strategy asks for, applies it, and keeps the little memory strategies and
// seats need. A strategy itself never dials or waits (internal/routes).
//
// The memory is in-process only: recent send volume and reply speed decay,
// rests end on their own, and a restart forgets everything.

const (
	volumeHalfLife = time.Hour
	speedHalfLife  = 20 * time.Minute
	restRateLimit  = time.Minute
	restFailure    = 30 * time.Second
)

// decaySum is a counter that halves about every half-life: a busy morning
// does not give the whole day's "least used" away.
type decaySum struct {
	value float64
	at    time.Time
}

func (d *decaySum) add(n float64, now time.Time, halfLife time.Duration) {
	d.value = d.current(now, halfLife) + n
	d.at = now
}

func (d *decaySum) current(now time.Time, halfLife time.Duration) float64 {
	if d.at.IsZero() {
		return 0
	}
	return d.value * math.Pow(0.5, now.Sub(d.at).Seconds()/halfLife.Seconds())
}

// decayAvg is an average whose old samples fade with the same half-life.
type decayAvg struct {
	sum    float64
	weight float64
	at     time.Time
}

func (d *decayAvg) add(sample float64, now time.Time, halfLife time.Duration) {
	fade := 1.0
	if !d.at.IsZero() {
		fade = math.Pow(0.5, now.Sub(d.at).Seconds()/halfLife.Seconds())
	}
	d.sum = d.sum*fade + sample
	d.weight = d.weight*fade + 1
	d.at = now
}

func (d *decayAvg) avg() float64 {
	if d.weight == 0 {
		return 0
	}
	return d.sum / d.weight
}

// speedStat is one seat+model's decayed reply speed: first-token
// milliseconds and output tokens per second after the first token.
type speedStat struct {
	ttft decayAvg
	tps  decayAvg
}

// measuredAt is the newer of the two samples.
func (s *speedStat) measuredAt() time.Time {
	if s.ttft.at.After(s.tps.at) {
		return s.ttft.at
	}
	return s.tps.at
}

// routingMemory is the process-local state behind seat fields.
type routingMemory struct {
	volume  map[string]*decaySum  // seat key -> decayed sent tokens
	speed   map[string]*speedStat // seat key + \n + model -> reply speed
	rests   map[string]time.Time  // seat key -> resting until
	spent   map[string]bool       // seat key -> out of credit or quota
	learned map[string]bool       // seat key -> a learning account was tried once
	gates   map[string]*seatGate
}

func (s *Server) strategies() *routes.Builtin {
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	if s.routeStrategies == nil {
		s.routeStrategies = routes.NewBuiltin()
	}
	return s.routeStrategies
}

func (s *Server) routingState() *routingMemory {
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	if s.routingMem == nil {
		s.routingMem = &routingMemory{
			volume:  map[string]*decaySum{},
			speed:   map[string]*speedStat{},
			rests:   map[string]time.Time{},
			spent:   map[string]bool{},
			learned: map[string]bool{},
			gates:   map[string]*seatGate{},
		}
	}
	return s.routingMem
}

// poolOf names the pool for strategies with memory.
func poolOf(routeID, model string) string {
	if routeID != "" {
		return routeID
	}
	return "model/" + model
}

// orderSeats applies the route's strategy to the candidate seats. It returns
// the ordered seats, the seat the strategy would try first (before stickiness),
// the seat keys that cannot answer right now, and the pool and strategy used.
// An unknown strategy id leaves the listed order.
func (s *Server) orderSeats(ctx context.Context, route state.Route, asked string, candidates []seatCandidate) (ordered []seatCandidate, picked string, blocked map[string]bool, pool, strategyID string) {
	pool = poolOf(route.ID, asked)
	strategyID = route.Routing
	if len(candidates) < 2 {
		if len(candidates) == 1 {
			picked = candidates[0].Key
			if s.seatBlocked(pool, candidates[0].Key) {
				blocked = map[string]bool{candidates[0].Key: true}
			}
		}
		return candidates, picked, blocked, pool, strategyID
	}
	book := s.strategies()
	strategy, known := book.Find(strategyID)
	now := time.Now()
	var fields routes.Fields
	if known {
		fields = strategy.Fields()
	}
	var quotas map[string]quotaView
	if hasLoginSeat(candidates) {
		quotas = s.accountQuotas(ctx, fields)
	}
	balances := s.seatBalances(ctx, candidates, fields)
	seats := make([]routes.Seat, len(candidates))
	for i, candidate := range candidates {
		seat := s.seatState(ctx, candidate, route, fields, quotas, balances, now)
		seat.Drained = book.Drained(pool, candidate.Key)
		seats[i] = seat
	}
	order := identityOrder(len(candidates))
	if known {
		order = routes.Apply(strategy, pool, seats, now)
	}
	ready := make([]int, 0, len(order))
	resting := make([]int, 0, len(order))
	spent := make([]int, 0, len(order))
	for _, i := range order {
		switch {
		case seats[i].Spent || seats[i].Drained || (seats[i].HasUsed && seats[i].Used >= 100):
			spent = append(spent, i)
		case seats[i].Resting:
			resting = append(resting, i)
		default:
			ready = append(ready, i)
		}
	}
	blocked = map[string]bool{}
	for _, i := range resting {
		blocked[seats[i].Key] = true
	}
	for _, i := range spent {
		blocked[seats[i].Key] = true
	}
	ordered = make([]seatCandidate, 0, len(candidates))
	for _, i := range append(append(ready, resting...), spent...) {
		ordered = append(ordered, candidates[i])
	}
	if len(ready) > 0 {
		picked = seats[ready[0]].Key
	} else if len(ordered) > 0 {
		picked = ordered[0].Key
	}
	return ordered, picked, blocked, pool, strategyID
}

func identityOrder(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// seatBlocked reports the process-local reasons a single seat cannot answer
// right now. It is the cheap check used when a pool has no ordering to do.
func (s *Server) seatBlocked(pool, key string) bool {
	if key == "" {
		return false
	}
	now := time.Now()
	mem := s.routingState()
	s.routingMu.Lock()
	until, resting := mem.rests[key]
	spent := mem.spent[key]
	s.routingMu.Unlock()
	return spent || (resting && now.Before(until)) || s.strategies().Drained(pool, key)
}

// seatState fills one strategy seat. Only declared fields are read.
func (s *Server) seatState(ctx context.Context, candidate seatCandidate, route state.Route, fields routes.Fields, quotas map[string]quotaView, balances map[string]balanceView, now time.Time) routes.Seat {
	weight := candidate.Weight
	if weight == 0 {
		weight = route.Weights[candidate.ProviderID]
	}
	seat := routes.Seat{Key: candidate.Key, Weight: weight}
	mem := s.routingState()
	s.routingMu.Lock()
	if until, resting := mem.rests[candidate.Key]; resting && now.Before(until) {
		seat.Resting = true
	}
	seat.Spent = mem.spent[candidate.Key]
	if fields.ByUse {
		if volume := mem.volume[candidate.Key]; volume != nil {
			seat.Tokens = volume.current(now, volumeHalfLife)
		}
	}
	if fields.BySpeed {
		if stat := mem.speed[candidate.Key+"\n"+candidate.Model]; stat != nil {
			seat.TTFT = stat.ttft.avg()
			seat.TTFTSet = stat.ttft.weight > 0
			seat.TPS = stat.tps.avg()
			seat.TPSSet = stat.tps.weight > 0
			seat.MeasuredAt = stat.measuredAt()
		}
	}
	learned := mem.learned[candidate.Key]
	s.routingMu.Unlock()
	if candidate.Login {
		if quota, ok := quotas[candidate.Account]; ok {
			applyQuota(&seat, quota, candidate.Model, now, learned)
		} else if !learned && (fields.ByUse || fields.ByPlan || fields.ByFree) {
			seat.Learns = true
		}
	} else {
		if fields.ByUse {
			if reading, ok := balances[candidate.Key]; ok {
				applyBalance(&seat, reading)
			}
		}
		if fields.ByPrice || fields.ByFree {
			if rate, ok := s.seatRate(ctx, candidate.ProviderID, candidate.Model); ok {
				rate = scaleRateForSeat(rate, candidate)
				seat.Priced = true
				seat.Cost = rate.Input + rate.Output
				seat.CacheCost = rate.CacheRead
				seat.Free = rate.Input == 0 && rate.Output == 0
			}
		}
	}
	return seat
}

func scaleRateForSeat(rate price.Rate, candidate seatCandidate) price.Rate {
	factor := 1.0
	if candidate.Provider.Multiplier != nil {
		factor = *candidate.Provider.Multiplier
	}
	if candidate.KeyIndex >= 0 && candidate.KeyIndex < len(candidate.Provider.KeyMultipliers) {
		factor = candidate.Provider.KeyMultipliers[candidate.KeyIndex]
	}
	rate.Input *= factor
	rate.Output *= factor
	if rate.CacheReadSet {
		rate.CacheRead *= factor
	}
	return rate
}

// seatRate reads the list price for the model at one provider. A
// provider-specific entry wins over the bare model name.
func (s *Server) seatRate(ctx context.Context, providerID, model string) (price.Rate, bool) {
	if s.Prices == nil {
		return price.Rate{}, false
	}
	if rate, ok := s.Prices.Rate(ctx, providerID+"/"+model); ok {
		return rate, true
	}
	return s.Prices.Rate(ctx, model)
}

// markFailure records what an attempt's failure means for later requests:
// a rate limit or an unavailable upstream rests the seat, an empty balance
// spends it and drains it for the drain strategy.
func (s *Server) markFailure(pool, seatKey, class string) {
	if seatKey == "" {
		return
	}
	now := time.Now()
	mem := s.routingState()
	s.routingMu.Lock()
	switch state.ErrorClass(class) {
	case state.ClassRateLimit:
		mem.rests[seatKey] = now.Add(restRateLimit)
	case state.ClassUpstreamUnavailable:
		mem.rests[seatKey] = now.Add(restFailure)
	case state.ClassEmptyBalance:
		mem.spent[seatKey] = true
	}
	s.routingMu.Unlock()
	if class == string(state.ClassEmptyBalance) {
		s.strategies().NoteDrained(pool, seatKey)
	}
}

// markSuccess clears the seat's rest and feeds the send volume and the speed
// table. Only answered calls count. ttft is the time from the attempt start
// to the first streamed byte; a negative ttft means the reply was not a
// stream and only the whole-reply speed is updated. Spent is kept: an empty
// quota and a transient failure are not the same thing.
func (s *Server) markSuccess(seat seatCandidate, started time.Time, usage json.RawMessage, ttft time.Duration) {
	if seat.Key == "" {
		return
	}
	now := time.Now()
	mem := s.routingState()
	s.routingMu.Lock()
	delete(mem.rests, seat.Key)
	if seat.Login {
		mem.learned[seat.Key] = true
	}
	counts, ok := state.CountsFromUsage(usage)
	if !ok {
		s.routingMu.Unlock()
		return
	}
	if sum := float64(counts.In + counts.Out); sum > 0 {
		volume := mem.volume[seat.Key]
		if volume == nil {
			volume = &decaySum{}
			mem.volume[seat.Key] = volume
		}
		volume.add(sum, now, volumeHalfLife)
	}
	key := seat.Key + "\n" + seat.Model
	stat := mem.speed[key]
	if stat == nil {
		stat = &speedStat{}
		mem.speed[key] = stat
	}
	elapsed := now.Sub(started)
	if ttft >= 0 {
		stat.ttft.add(float64(ttft)/float64(time.Millisecond), now, speedHalfLife)
	}
	if counts.Out > 0 {
		// TPS is the output rate after the first token when that split is
		// known, else over the whole reply. A reply inside one clock tick
		// counts as a millisecond; a zero duration would record nothing.
		seconds := elapsed.Seconds()
		if ttft >= 0 && elapsed > ttft {
			seconds = (elapsed - ttft).Seconds()
		}
		if seconds <= 0 {
			seconds = 0.001
		}
		stat.tps.add(float64(counts.Out)/seconds, now, speedHalfLife)
	}
	s.routingMu.Unlock()
}

// ResetSeat clears a quota refusal so the user can ask for the seat again
// without restarting the process. It also forgets the drain strategy's
// refusal on the pool. A rest is left alone.
func (s *Server) ResetSeat(pool, seatKey string) {
	if seatKey == "" {
		return
	}
	if pool != "" {
		s.strategies().UnDrain(pool, seatKey)
	} else {
		s.strategies().UnDrainAll(seatKey)
	}
	mem := s.routingState()
	s.routingMu.Lock()
	delete(mem.spent, seatKey)
	s.routingMu.Unlock()
}

// moveRound corrects the weight table when stickiness sent the request to
// another seat than the one the strategy picked first.
func (s *Server) moveRound(pool, strategyID string, picked, used seatCandidate, sticky string) {
	if strategyID != routes.Weight || picked.Key == "" || used.Key == "" || used.Key != sticky {
		return
	}
	if picked.Key == used.Key {
		return
	}
	if picked.TopMember != "" && picked.TopMember == used.TopMember {
		return
	}
	s.strategies().MoveRound(pool, picked.Key, used.Key)
}
