package gateway

import (
	"context"
	"sync"
	"time"
)

const (
	defaultQueueSlots = 8
	defaultQueueWait  = 10 * time.Second
	gatePoll          = 20 * time.Millisecond
)

// seatGate is the in-process concurrency and per-minute gate of one seat.
// A seat with no configured cap is not gated and never queues.
type seatGate struct {
	mu      sync.Mutex
	active  int
	starts  []time.Time
	waiters int
}

func seatLimits(candidate seatCandidate) (maxConcurrent, maxRPM, slots int, wait time.Duration) {
	if candidate.Login {
		return 0, 0, 0, 0
	}
	provider := candidate.Provider
	maxConcurrent = provider.MaxConcurrent
	maxRPM = provider.MaxRPM
	if maxConcurrent == 0 && maxRPM == 0 {
		return 0, 0, 0, 0
	}
	slots = provider.QueueSlots
	if slots == 0 {
		slots = defaultQueueSlots
	}
	wait = time.Duration(provider.QueueWaitMS) * time.Millisecond
	if wait == 0 {
		wait = defaultQueueWait
	}
	return maxConcurrent, maxRPM, slots, wait
}

func (s *Server) gateFor(seatKey string) *seatGate {
	mem := s.routingState()
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	gate := mem.gates[seatKey]
	if gate == nil {
		gate = &seatGate{}
		mem.gates[seatKey] = gate
	}
	return gate
}

// tryAcquire takes a slot without waiting. wait is how long until another slot
// should appear for the reason that stopped it, or zero when that is unknown.
func (g *seatGate) tryAcquire(maxConcurrent, maxRPM int, now time.Time) (release func(), wait time.Duration, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if maxRPM > 0 {
		cutoff := now.Add(-time.Minute)
		keep := g.starts[:0]
		for _, at := range g.starts {
			if at.After(cutoff) {
				keep = append(keep, at)
			}
		}
		g.starts = keep
		if len(g.starts) >= maxRPM {
			wait = g.starts[0].Add(time.Minute).Sub(now)
			return nil, wait, false
		}
	}
	if maxConcurrent > 0 && g.active >= maxConcurrent {
		return nil, 0, false
	}
	g.active++
	if maxRPM > 0 {
		g.starts = append(g.starts, now)
	}
	return func() {
		g.mu.Lock()
		if g.active > 0 {
			g.active--
		}
		g.mu.Unlock()
	}, 0, true
}

func (g *seatGate) addWaiter(slots int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waiters >= slots {
		return false
	}
	g.waiters++
	return true
}

func (g *seatGate) dropWaiter() {
	g.mu.Lock()
	if g.waiters > 0 {
		g.waiters--
	}
	g.mu.Unlock()
}

// acquireSeat reserves a seat at or after from. If the first seats are full
// and a later seat is idle, it lets the request go to the later seat instead
// of waiting on the busy one. It only waits when every remaining seat is full.
// Blocked seats are resting or spent. While the request is still walking the
// ready segment, a blocked seat is not a free alternative: it is ordered
// after the ready seats and is tried only when those seats have failed.
// The returned index is the chosen seat; ok false means the queue or the wait
// limit was reached and the caller should answer 429.
func (s *Server) acquireSeat(ctx context.Context, ordered []seatCandidate, from int, blocked map[string]bool) (int, func(), int, bool) {
	skippable := func(index int) bool {
		return from < len(ordered) && !blocked[ordered[from].Key] && blocked[ordered[index].Key]
	}
	try := func() (int, func(), bool) {
		now := time.Now()
		for i := from; i < len(ordered); i++ {
			if skippable(i) {
				continue
			}
			maxConcurrent, maxRPM, _, _ := seatLimits(ordered[i])
			if maxConcurrent == 0 && maxRPM == 0 {
				return i, nil, true
			}
			release, _, ok := s.gateFor(ordered[i].Key).tryAcquire(maxConcurrent, maxRPM, now)
			if ok {
				return i, release, true
			}
		}
		return -1, nil, false
	}
	if index, release, ok := try(); ok {
		return index, release, 0, true
	}
	// Every remaining seat has a cap and none is free. Refuse at once when a
	// queue is already full; otherwise wait a bounded time and look again.
	slots := 0
	wait := time.Duration(0)
	var waiting []*seatGate
	for i := from; i < len(ordered); i++ {
		if skippable(i) {
			continue
		}
		maxConcurrent, maxRPM, capSlots, capWait := seatLimits(ordered[i])
		if maxConcurrent == 0 && maxRPM == 0 {
			continue
		}
		if capWait > wait {
			wait = capWait
		}
		if capSlots > slots {
			slots = capSlots
		}
		gate := s.gateFor(ordered[i].Key)
		if !gate.addWaiter(capSlots) {
			for _, held := range waiting {
				held.dropWaiter()
			}
			return from, nil, 0, false
		}
		waiting = append(waiting, gate)
	}
	if wait == 0 {
		wait = defaultQueueWait
	}
	defer func() {
		for _, gate := range waiting {
			gate.dropWaiter()
		}
	}()
	deadline := time.Now().Add(wait)
	timer := time.NewTimer(gatePoll)
	defer timer.Stop()
	for {
		if index, release, ok := try(); ok {
			return index, release, 0, true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return from, nil, int(wait / time.Millisecond), false
		}
		if remaining < gatePoll {
			timer.Reset(remaining)
		} else {
			timer.Reset(gatePoll)
		}
		select {
		case <-ctx.Done():
			return from, nil, 0, false
		case <-timer.C:
		}
	}
}
