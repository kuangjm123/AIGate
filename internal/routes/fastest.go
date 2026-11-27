package routes

import (
	"sort"
	"time"
)

// staleAfter is how long a speed reading counts. Older is unmeasured again.
const staleAfter = time.Hour

// fastStrategy prefers the seat whose recent streamed replies were quicker.
// The estimate is the cost of one short reply: TTFT + 200 tokens at TPS.
// A seat that never reported TPS borrows the pool median, so "did not say"
// is neither an advantage nor a punishment. Measured seats band by estimate
// (a new band above 1.1 times the band's fastest); inside a band the listed
// order stands, so channels a few milliseconds apart do not swap per request.
type fastStrategy struct {
	b *Builtin
}

func (fastStrategy) Fields() Fields {
	return Fields{BySpeed: true}
}

func (s fastStrategy) Order(pool string, seats []Seat, now time.Time) []int {
	measured := make([]int, 0, len(seats))
	unmeasured := make([]int, 0, len(seats))
	resting := make([]int, 0, len(seats))
	spent := make([]int, 0, len(seats))
	est := make([]float64, len(seats))
	tpsValues := make([]float64, 0, len(seats))
	fresh := func(i int) bool {
		return now.Sub(seats[i].MeasuredAt) < staleAfter && (seats[i].TPSSet || seats[i].TTFTSet)
	}
	for i, seat := range seats {
		switch {
		case seat.Spent:
			spent = append(spent, i)
		case seat.Resting:
			resting = append(resting, i)
		case fresh(i):
			measured = append(measured, i)
			if seat.TPSSet && seat.TPS > 0 {
				tpsValues = append(tpsValues, seat.TPS)
			}
		default:
			unmeasured = append(unmeasured, i)
		}
	}
	sort.Float64s(tpsValues)
	median := 0.0
	if len(tpsValues) > 0 {
		median = tpsValues[len(tpsValues)/2]
	}
	for _, i := range measured {
		tps := seats[i].TPS
		if !seats[i].TPSSet || tps <= 0 {
			tps = median
		}
		ttft := seats[i].TTFT
		if !seats[i].TTFTSet {
			ttft = 0
		}
		if tps > 0 {
			est[i] = ttft + 200*1000/tps
		} else {
			est[i] = ttft
		}
	}
	// Bands are drawn on a copy sorted by estimate; measured keeps the listed
	// order, so one band keeps it too.
	ranked := append([]int(nil), measured...)
	sort.SliceStable(ranked, func(a, b int) bool {
		return est[ranked[a]] < est[ranked[b]]
	})
	band := make([]int, len(seats))
	top := -1.0
	next := 0
	for _, i := range ranked {
		if top < 0 || est[i] > 1.1*top {
			top = est[i]
			next++
		}
		band[i] = next
	}
	sort.SliceStable(measured, func(a, b int) bool {
		return band[measured[a]] < band[measured[b]]
	})
	order := make([]int, 0, len(seats))
	order = append(order, measured...)
	order = append(order, unmeasured...)
	order = append(order, resting...)
	order = append(order, spent...)
	// Every fifth request that has both measured and unmeasured seats probes
	// one unmeasured seat at the front; probes rotate among them, so a seat
	// that never reports speed does not keep the probe slot.
	if len(measured) > 0 && len(unmeasured) > 0 {
		s.b.mu.Lock()
		if s.b.probe == nil {
			s.b.probe = map[string]int{}
		}
		s.b.probe[pool]++
		count := s.b.probe[pool]
		s.b.mu.Unlock()
		if count%5 == 0 {
			pick := (count/5 - 1) % len(unmeasured)
			probed := unmeasured[pick]
			out := []int{probed}
			for _, i := range order {
				if i != probed {
					out = append(out, i)
				}
			}
			order = out
		}
	}
	return order
}
