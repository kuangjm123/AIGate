// Package routes orders one pool of candidate seats for a request.
// A strategy is pure ordering: the input is []Seat, the output is a
// permutation of indices. Resting, session stickiness, and failover after the
// first byte are the gateway's, not the strategy's. A strategy never dials,
// never waits; the gateway fills every field it asked for before Order runs.
package routes

import (
	"sync"
	"time"
)

// Strategy ids. Smart is the core default with the empty id.
const (
	Smart    = ""
	Order    = "order"
	Rotate   = "rotate"
	Usage    = "usage"
	Pace     = "pace"
	Weight   = "weight"
	Free     = "free"
	Fast     = "fast"
	Cheap    = "cheap"
	Drain    = "drain"
	Expiring = "expiring"
)

// Exact reports whether the strategy's order is the order to try. For other
// strategies the gateway may still pull a better-fitting key of one provider
// forward; the strategy's relative order inside one tier is kept.
func Exact(id string) bool {
	return id == Order || id == Drain
}

// Fields are the seat fields a strategy reads. The gateway fills only these;
// reading the model catalog or the speed table is not cheap.
type Fields struct {
	ByUse   bool // Used, Tokens, Left, Lapsing
	ByFree  bool // Free, FreePlan, Expires
	ByPlan  bool // Soon, Unreached, Lapse, Credits, Lead
	BySpeed bool // TTFT, TPS, MeasuredAt
	ByPrice bool // Cost, CacheCost, Priced
}

// Seat is one candidate the strategy may order. Every field is filled by the
// gateway before Order runs; an unset field means unknown, not zero.
type Seat struct {
	Key    string
	Weight int

	Resting bool // rate limited or failed; the gateway moves these to the end
	Spent   bool // out of credit or quota
	Drained bool // refused for quota on the last ask
	First   bool // the user said spend credits first: always tier 0
	Learns  bool // will report its remainder in the reply; must be tried once
	Lead    bool // pulled to the front unless exhausted

	HasUsed  bool
	Used     float64 // 0-100, only windows that constrain this model
	UsedLong bool    // the window at 90 or more is a long one like a week

	Soon      []time.Time // window resets, largest window first
	Unreached int         // largest windows that cannot be spent before reset
	Lapse     time.Time   // when credits expire
	LapseSet  bool
	Lapsing   float64 // credits expiring soon
	Credits   bool    // can still spend credits
	Left      float64 // prepaid wallet balance
	LeftIn    string  // unit of Left; balances in different units never compare

	Tokens float64 // decayed recent send volume, about 1h half-life

	WeekLeft  float64 // percent of the weekly window still left
	WeekReset time.Time
	HasWeek   bool

	TTFT       float64 // first-token milliseconds, decayed average
	TTFTSet    bool
	TPS        float64 // output tokens per second after the first token
	TPSSet     bool
	MeasuredAt time.Time // stale after about an hour: unmeasured again

	Cost      float64 // dollars of 1M input plus 1M output at request time
	CacheCost float64 // dollars of 1M cache read
	Priced    bool

	Free       bool // the model costs nothing at this seat
	FreePlan   bool // the account plan sounds free or trial
	Expires    time.Time
	ExpiresSet bool
}

// Strategy answers "in which order do we try these seats".
// Order returns a permutation of seat indices. pool identifies the pool for
// strategies with memory (rotate, weight, drain, fast probes).
type Strategy interface {
	Order(pool string, seats []Seat, now time.Time) []int
	Fields() Fields
}

// Apply runs one strategy. An invalid permutation or a panic falls back to
// the listed order; the request still goes out.
func Apply(s Strategy, pool string, seats []Seat, now time.Time) []int {
	order := safeOrder(s, pool, seats, now)
	if !validPermutation(order, len(seats)) {
		return identity(len(seats))
	}
	return order
}

func safeOrder(s Strategy, pool string, seats []Seat, now time.Time) (order []int) {
	defer func() { _ = recover() }()
	if s == nil {
		return identity(len(seats))
	}
	return s.Order(pool, seats, now)
}

func identity(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func validPermutation(order []int, n int) bool {
	if len(order) != n {
		return false
	}
	seen := make([]bool, n)
	for _, i := range order {
		if i < 0 || i >= n || seen[i] {
			return false
		}
		seen[i] = true
	}
	return true
}

// Builtin holds the in-memory state of the strategies that have any:
// rotate counters, weight tables, drain positions, fast probe turns.
// A restart forgets all of it.
type Builtin struct {
	mu        sync.Mutex
	rotate    map[string]int
	probe     map[string]int
	rounds    map[string]map[string]float64
	lastTotal map[string]float64
	drains    map[string]*drainState
}

// NewBuiltin returns the registry with every strategy this build serves.
func NewBuiltin() *Builtin {
	return &Builtin{}
}

// Known reports whether id names a strategy this build serves.
func Known(id string) bool {
	switch id {
	case Smart, Order, Rotate, Usage, Pace, Weight, Free, Fast, Cheap, Drain, Expiring:
		return true
	}
	return false
}

// Find returns the strategy for id. Smart is the empty id.
func (b *Builtin) Find(id string) (Strategy, bool) {
	switch id {
	case Smart:
		return smartStrategy{}, true
	case Order:
		return orderStrategy{}, true
	case Rotate:
		return rotateStrategy{b: b}, true
	case Usage:
		return usageStrategy{}, true
	case Pace:
		return paceStrategy{}, true
	case Weight:
		return weightStrategy{b: b}, true
	case Free:
		return freeStrategy{}, true
	case Fast:
		return fastStrategy{b: b}, true
	case Cheap:
		return cheapStrategy{}, true
	case Drain:
		return drainStrategy{b: b}, true
	case Expiring:
		return expiringStrategy{}, true
	}
	return nil, false
}

// Known reports whether id names a strategy this build serves.
func (b *Builtin) Known(id string) bool {
	return Known(id)
}

// tierOf buckets a seat by its vendor-reported usage. First is always tier 0.
// An unknown reading is usable, tier 0.
func tierOf(s Seat) int {
	if s.First || !s.HasUsed {
		return 0
	}
	switch {
	case s.Used >= 100:
		return 2
	case s.Used >= 90 && !s.UsedLong:
		return 1
	default:
		return 0
	}
}

// window skips the seat's Unreached largest windows.
func window(s Seat) []time.Time {
	if s.Unreached >= len(s.Soon) {
		return nil
	}
	return s.Soon[s.Unreached:]
}

// compareSoon puts the seat whose largest window resets earlier first,
// truncated to the hour; a tie in the hour moves to the next smaller window.
// A seat with no window reading goes after one that has it.
func compareSoon(a, b Seat) int {
	wa, wb := window(a), window(b)
	switch {
	case len(wa) == 0 && len(wb) == 0:
		return 0
	case len(wa) == 0:
		return 1
	case len(wb) == 0:
		return -1
	}
	for k := 0; k < len(wa) && k < len(wb); k++ {
		ha, hb := wa[k].Truncate(time.Hour), wb[k].Truncate(time.Hour)
		if ha.Before(hb) {
			return -1
		}
		if ha.After(hb) {
			return 1
		}
	}
	return 0
}

// compareLapse puts the seat whose credits expire earlier first.
// A seat with no lapse time goes after one that has it.
func compareLapse(a, b Seat) int {
	switch {
	case a.LapseSet && b.LapseSet:
		if a.Lapse.Before(b.Lapse) {
			return -1
		}
		if a.Lapse.After(b.Lapse) {
			return 1
		}
	case a.LapseSet:
		return -1
	case b.LapseSet:
		return 1
	}
	return 0
}

// compareLeft puts the larger prepaid balance first, only between seats whose
// balance is in the same unit. Different units never compare.
func compareLeft(a, b Seat) int {
	if a.LeftIn == "" || a.LeftIn != b.LeftIn {
		return 0
	}
	switch {
	case a.Left > b.Left:
		return -1
	case a.Left < b.Left:
		return 1
	}
	return 0
}

// exhausted reports the drain line: spent, at 100, or drained by a refusal.
func (s Seat) exhausted() bool {
	return s.Spent || s.Drained || (s.HasUsed && s.Used >= 100)
}

func clampWeight(w int) int {
	if w < 1 {
		return 1
	}
	return w
}
