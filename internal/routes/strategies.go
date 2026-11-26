package routes

import (
	"sort"
	"time"
)

// smartStrategy is the default: among the seats with quota left, the one
// whose largest window resets soonest goes first. Identical seats keep the
// listed order, so a vendor's prompt cache keeps landing on one account.
type smartStrategy struct{}

func (smartStrategy) Fields() Fields {
	return Fields{ByUse: true, ByPlan: true}
}

func (smartStrategy) Order(_ string, seats []Seat, _ time.Time) []int {
	return smartOrder(seats)
}

// smartOrder is also the paid segment of expiring. The sort is stable.
func smartOrder(seats []Seat) []int {
	order := identity(len(seats))
	tier := make([]int, len(seats))
	for i, s := range seats {
		tier[i] = tierOf(s)
	}
	sort.SliceStable(order, func(a, b int) bool {
		i, j := order[a], order[b]
		if tier[i] != tier[j] {
			return tier[i] < tier[j]
		}
		si, sj := seats[i], seats[j]
		switch tier[i] {
		case 0:
			if c := compareSoon(si, sj); c != 0 {
				return c < 0
			}
			if c := compareLapse(si, sj); c != 0 {
				return c < 0
			}
			return compareLeft(si, sj) < 0
		case 1:
			return si.Used < sj.Used
		default:
			if si.Credits != sj.Credits {
				return si.Credits
			}
			return si.Used < sj.Used
		}
	})
	lead := make([]int, 0, len(order))
	rest := make([]int, 0, len(order))
	for _, i := range order {
		if seats[i].Lead && tier[i] < 2 {
			lead = append(lead, i)
		} else {
			rest = append(rest, i)
		}
	}
	return append(lead, rest...)
}

// orderStrategy tries the list in order until one cannot answer.
type orderStrategy struct{}

func (orderStrategy) Fields() Fields { return Fields{} }

func (orderStrategy) Order(_ string, seats []Seat, _ time.Time) []int {
	return identity(len(seats))
}

// rotateStrategy moves one seat down per request. The counter is in memory
// and resets with the process.
type rotateStrategy struct {
	b *Builtin
}

func (rotateStrategy) Fields() Fields { return Fields{} }

func (s rotateStrategy) Order(pool string, seats []Seat, _ time.Time) []int {
	if len(seats) == 0 {
		return nil
	}
	s.b.mu.Lock()
	if s.b.rotate == nil {
		s.b.rotate = map[string]int{}
	}
	start := s.b.rotate[pool] % len(seats)
	s.b.rotate[pool] = start + 1
	s.b.mu.Unlock()
	order := make([]int, 0, len(seats))
	for k := 0; k < len(seats); k++ {
		order = append(order, (start+k)%len(seats))
	}
	return order
}

// usageStrategy prefers the seat the vendor says is least used.
// A seat that never reported a usage share is not "empty": a pay-as-you-go
// key reports 0 too, so unknown sorts after reported-with-room, behind the
// accounts still being learned.
type usageStrategy struct{}

func (usageStrategy) Fields() Fields {
	return Fields{ByUse: true}
}

func (usageStrategy) Order(_ string, seats []Seat, _ time.Time) []int {
	segment := func(s Seat) int {
		switch {
		case s.Lapsing >= 0.1 && !s.exhausted():
			return 0
		case s.Learns:
			return 1
		case s.HasUsed && tierOf(s) == 0:
			return 2
		case !s.HasUsed:
			return 3
		default:
			return 3 + tierOf(s)
		}
	}
	order := identity(len(seats))
	sort.SliceStable(order, func(a, b int) bool {
		si, sj := seats[order[a]], seats[order[b]]
		gi, gj := segment(si), segment(sj)
		if gi != gj {
			return gi < gj
		}
		if gi == 0 {
			if c := compareLapse(si, sj); c != 0 {
				return c < 0
			}
		}
		if si.Used != sj.Used {
			return si.Used < sj.Used
		}
		if c := compareLeft(si, sj); c != 0 {
			return c < 0
		}
		return si.Tokens < sj.Tokens
	})
	return order
}

// paceStrategy prefers the highest "weekly percent left per hour to reset".
// An account whose week is not known counts as a whole fresh week; a plain
// API key has no week and paces at 0 unless it reports a window itself.
// Banding sorts by pace first and cuts below 0.9 of the band top, so the cut
// line never depends on the order seats arrive in.
type paceStrategy struct{}

// FreshPace is a whole week spread over its hours.
const FreshPace = 100.0 / (7 * 24)

func (paceStrategy) Fields() Fields {
	return Fields{ByUse: true, ByPlan: true}
}

func (paceStrategy) Order(_ string, seats []Seat, now time.Time) []int {
	pace := make([]float64, len(seats))
	for i, s := range seats {
		pace[i] = paceOf(s, now)
	}
	order := identity(len(seats))
	tier := make([]int, len(seats))
	for i, s := range seats {
		tier[i] = tierOf(s)
	}
	// Band the tier-0 seats that are not still learning.
	band := make([]int, len(seats))
	ranked := make([]int, 0, len(seats))
	for _, i := range order {
		if tier[i] == 0 && !seats[i].Learns {
			ranked = append(ranked, i)
		}
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		return pace[ranked[a]] > pace[ranked[b]]
	})
	top := -1.0
	next := 0
	for _, i := range ranked {
		if top < 0 || pace[i] < 0.9*top {
			top = pace[i]
			next++
		}
		band[i] = next
	}
	sort.SliceStable(order, func(a, b int) bool {
		i, j := order[a], order[b]
		if tier[i] != tier[j] {
			return tier[i] < tier[j]
		}
		si, sj := seats[i], seats[j]
		switch tier[i] {
		case 0:
			if si.Learns != sj.Learns {
				return si.Learns
			}
			if band[i] != band[j] {
				return band[i] < band[j]
			}
			if c := compareLeft(si, sj); c != 0 {
				return c < 0
			}
			return si.Tokens < sj.Tokens
		case 1:
			return si.Used < sj.Used
		default:
			if si.Credits != sj.Credits {
				return si.Credits
			}
			return si.Used < sj.Used
		}
	})
	return order
}

func paceOf(s Seat, now time.Time) float64 {
	if s.HasWeek {
		hours := s.WeekReset.Sub(now).Hours()
		if hours < 1 {
			hours = 1
		}
		return s.WeekLeft / hours
	}
	if s.Learns || !s.HasUsed {
		return FreshPace
	}
	return 0
}

// freeStrategy puts the seats where this model costs nothing first,
// keeping the listed order inside each half.
type freeStrategy struct{}

func (freeStrategy) Fields() Fields {
	return Fields{ByFree: true}
}

func (freeStrategy) Order(_ string, seats []Seat, _ time.Time) []int {
	free := make([]int, 0, len(seats))
	paid := make([]int, 0, len(seats))
	for i, s := range seats {
		if s.Free || s.FreePlan {
			free = append(free, i)
		} else {
			paid = append(paid, i)
		}
	}
	return append(free, paid...)
}

// cheapStrategy orders by the price of this request's moment: 1M input plus
// 1M output. Unpriced seats go after every priced one. Prices within a cent
// per million tokens are equal and keep the listed order, so rounding never
// flips two vendors.
type cheapStrategy struct{}

const priceTolerance = 0.01

func (cheapStrategy) Fields() Fields {
	return Fields{ByPrice: true}
}

func (cheapStrategy) Order(_ string, seats []Seat, _ time.Time) []int {
	order := identity(len(seats))
	sort.SliceStable(order, func(a, b int) bool {
		si, sj := seats[order[a]], seats[order[b]]
		if si.Priced != sj.Priced {
			return si.Priced
		}
		if !si.Priced {
			return false
		}
		if d := si.Cost - sj.Cost; d < -priceTolerance {
			return true
		} else if d > priceTolerance {
			return false
		}
		return si.CacheCost < sj.CacheCost-priceTolerance
	})
	return order
}

// drainState is one pool's drain memory: the seat it sits on, and the seats
// the vendor refused for quota. A drained seat is not returned to even when
// its window has reset since; the user asked to empty one seat before the
// next, not to ask the refilled one back.
type drainState struct {
	current string
	drained map[string]bool
}

// drainStrategy sits on one seat until it is exhausted, then sits on the
// next. Rate limits and plain failures are rests, not exhaustion: the pool
// stays where it was.
type drainStrategy struct {
	b *Builtin
}

func (drainStrategy) Fields() Fields {
	return Fields{ByUse: true}
}

func (s drainStrategy) Order(pool string, seats []Seat, _ time.Time) []int {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	state := s.b.drains[pool]
	if state == nil {
		state = &drainState{drained: map[string]bool{}}
		if s.b.drains == nil {
			s.b.drains = map[string]*drainState{}
		}
		s.b.drains[pool] = state
	}
	at := -1
	for i, seat := range seats {
		if seat.Key == state.current {
			at = i
			break
		}
	}
	spent := func(i int) bool {
		return seats[i].exhausted() || state.drained[seats[i].Key]
	}
	if at < 0 || spent(at) {
		// Sit on the first seat after the current one that is not exhausted.
		// All exhausted stays where it was.
		next := -1
		for k := 1; k <= len(seats); k++ {
			candidate := ((at + k) % len(seats))
			if !spent(candidate) {
				next = candidate
				break
			}
		}
		if next >= 0 {
			at = next
			state.current = seats[at].Key
		} else if at < 0 && len(seats) > 0 {
			at = 0
			state.current = seats[0].Key
		}
	}
	if at < 0 {
		return identity(len(seats))
	}
	order := make([]int, 0, len(seats))
	for k := 0; k < len(seats); k++ {
		order = append(order, (at+k)%len(seats))
	}
	return order
}

// NoteDrained remembers a quota refusal on the pool. The seat is exhausted
// from now on, however its windows look later.
func (b *Builtin) NoteDrained(pool, key string) {
	if b == nil || pool == "" || key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.drains[pool]
	if state == nil {
		state = &drainState{drained: map[string]bool{}}
		if b.drains == nil {
			b.drains = map[string]*drainState{}
		}
		b.drains[pool] = state
	}
	state.drained[key] = true
}

// Drained reports whether a quota refusal is remembered on the pool.
func (b *Builtin) Drained(pool, key string) bool {
	if b == nil || pool == "" || key == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.drains[pool]
	return state != nil && state.drained[key]
}

// UnDrain forgets a quota refusal so the user can ask the seat to be tried
// again before the process restarts. The drain position itself is kept.
func (b *Builtin) UnDrain(pool, key string) {
	if b == nil || pool == "" || key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.drains[pool]
	if state == nil {
		return
	}
	delete(state.drained, key)
}

// UnDrainAll forgets a key's quota refusal on every pool. It is the console's
// explicit reset when the caller does not name a route or group.
func (b *Builtin) UnDrainAll(key string) {
	if b == nil || key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, state := range b.drains {
		if state != nil {
			delete(state.drained, key)
		}
	}
}

// expiringStrategy puts free quota that expires soonest first, then paid
// seats in Smart order, then the exhausted ones.
type expiringStrategy struct{}

func (expiringStrategy) Fields() Fields {
	return Fields{ByFree: true, ByUse: true, ByPlan: true}
}

func (expiringStrategy) Order(pool string, seats []Seat, now time.Time) []int {
	freeIdx := make([]int, 0, len(seats))
	restIdx := make([]int, 0, len(seats))
	for i, s := range seats {
		if (s.Free || s.FreePlan) && !s.exhausted() {
			freeIdx = append(freeIdx, i)
		} else {
			restIdx = append(restIdx, i)
		}
	}
	sort.SliceStable(freeIdx, func(a, b int) bool {
		si, sj := seats[freeIdx[a]], seats[freeIdx[b]]
		switch {
		case si.ExpiresSet && sj.ExpiresSet:
			return si.Expires.Truncate(time.Hour).Before(sj.Expires.Truncate(time.Hour))
		case si.ExpiresSet:
			return true
		case sj.ExpiresSet:
			return false
		}
		return false
	})
	restSeats := make([]Seat, len(restIdx))
	for k, i := range restIdx {
		restSeats[k] = seats[i]
	}
	sub := smartStrategy{}.Order(pool, restSeats, now)
	order := append([]int{}, freeIdx...)
	for _, k := range sub {
		order = append(order, restIdx[k])
	}
	return order
}
