package routes

import "time"

// weightStrategy is smooth weighted round robin: weights 3 and 1 spread as
// A, A, B, A, …, not A, A, A, B. Each pool keeps a current-weight table.
// Every round the seats that are not resting add their weight to their
// current value, the largest wins and pays the total weight of the round.
// A resting seat adds nothing: it lends its share out and rejoins on its own
// weight when it comes back.
type weightStrategy struct {
	b *Builtin
}

func (weightStrategy) Fields() Fields { return Fields{} }

func (s weightStrategy) Order(pool string, seats []Seat, _ time.Time) []int {
	if len(seats) == 0 {
		return nil
	}
	pick := -1
	total := 0.0
	s.b.mu.Lock()
	table := s.b.rounds[pool]
	if table == nil {
		table = map[string]float64{}
		if s.b.rounds == nil {
			s.b.rounds = map[string]map[string]float64{}
		}
		s.b.rounds[pool] = table
	}
	for i, seat := range seats {
		if seat.Resting {
			continue
		}
		w := float64(clampWeight(seat.Weight))
		table[seat.Key] += w
		total += w
		if pick < 0 || table[seat.Key] > table[seats[pick].Key] {
			pick = i
		}
	}
	if pick >= 0 {
		table[seats[pick].Key] -= total
	}
	if s.b.lastTotal == nil {
		s.b.lastTotal = map[string]float64{}
	}
	s.b.lastTotal[pool] = total
	s.b.mu.Unlock()
	order := make([]int, 0, len(seats))
	if pick >= 0 {
		order = append(order, pick)
	}
	for i := range seats {
		if i != pick {
			order = append(order, i)
		}
	}
	return order
}

// MoveRound corrects a round when stickiness sent the request to another seat
// than the one the table picked: the total weight the pick paid is given back
// and charged to the seat actually used. Without it new conversations would
// keep compensating a seat for rounds it never served, and the ratio drifts.
func (b *Builtin) MoveRound(pool, picked, actual string) {
	if b == nil || pool == "" || picked == "" || actual == "" || picked == actual {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	table := b.rounds[pool]
	if table == nil {
		return
	}
	// The payment is the total weight of the round the pick won, remembered
	// when the round ran.
	total := b.lastTotal[pool]
	if total <= 0 {
		return
	}
	table[picked] += total
	table[actual] -= total
}
