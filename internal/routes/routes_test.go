package routes

import (
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

type panicStrategy struct{}

func (panicStrategy) Fields() Fields { return Fields{} }
func (panicStrategy) Order(string, []Seat, time.Time) []int {
	panic("boom")
}

type badStrategy struct{}

func (badStrategy) Fields() Fields { return Fields{} }
func (badStrategy) Order(_ string, seats []Seat, _ time.Time) []int {
	return []int{0, 0}
}

func TestApplyFallsBackToTheListedOrder(t *testing.T) {
	seats := []Seat{{Key: "a"}, {Key: "b"}, {Key: "c"}}
	for name, s := range map[string]Strategy{"panic": panicStrategy{}, "bad": badStrategy{}, "nil": nil} {
		got := Apply(s, "pool", seats, testNow)
		for i, want := range []int{0, 1, 2} {
			if got[i] != want {
				t.Fatalf("%s fell back to %v", name, got)
			}
		}
	}
}

func TestSmartPrefersTheSoonerWeekAndSkipsUnreached(t *testing.T) {
	weekEarly := testNow.Add(2 * 24 * time.Hour)
	weekLate := testNow.Add(5 * 24 * time.Hour)
	fiveHour := testNow.Add(3 * time.Hour)
	seats := []Seat{
		{Key: "late-week", Soon: []time.Time{weekLate}},
		{Key: "early-week", Soon: []time.Time{weekEarly, fiveHour}},
		{Key: "no-window"},
	}
	got := Apply(smartStrategy{}, "pool", seats, testNow)
	if seats[got[0]].Key != "early-week" || seats[got[2]].Key != "no-window" {
		t.Fatalf("order %v", keys(got, seats))
	}
	// A same-hour tie on the week compares the smaller window inside it.
	seats = []Seat{
		{Key: "slow-inside", Soon: []time.Time{weekEarly, testNow.Add(4 * time.Hour)}},
		{Key: "fast-inside", Soon: []time.Time{weekEarly, fiveHour}},
	}
	got = Apply(smartStrategy{}, "pool", seats, testNow)
	if seats[got[0]].Key != "fast-inside" {
		t.Fatalf("inner window %v", keys(got, seats))
	}
	// Unreached largest windows do not compare; the next one does. Without
	// the skip the hoarding month would win; skipped, its later week loses.
	seats = []Seat{
		{Key: "hoarding", Soon: []time.Time{weekEarly, testNow.Add(6 * 24 * time.Hour)}, Unreached: 1},
		{Key: "spendable", Soon: []time.Time{testNow.Add(4 * 24 * time.Hour)}},
	}
	got = Apply(smartStrategy{}, "pool", seats, testNow)
	if seats[got[0]].Key != "spendable" {
		t.Fatalf("unreached %v", keys(got, seats))
	}
	// Reset hours compare truncated: minutes apart do not reorder.
	seats = []Seat{
		{Key: "first", Soon: []time.Time{fiveHour}},
		{Key: "second", Soon: []time.Time{fiveHour.Add(20 * time.Minute)}},
	}
	got = Apply(smartStrategy{}, "pool", seats, testNow)
	if seats[got[0]].Key != "first" {
		t.Fatalf("truncated %v", keys(got, seats))
	}
}

func TestUsageDoesNotTreatUnknownAsEmpty(t *testing.T) {
	seats := []Seat{
		{Key: "pay-as-you-go"},                     // Used 0, never reported
		{Key: "reported", HasUsed: true, Used: 60}, // reported, room left
		{Key: "learning", Learns: true},            // must be tried once
		{Key: "capped", HasUsed: true, Used: 95},   // tier 1
		{Key: "lapsing", HasUsed: true, Used: 80, Lapsing: 0.5, LapseSet: true, Lapse: testNow.Add(2 * time.Hour)},
	}
	got := Apply(usageStrategy{}, "pool", seats, testNow)
	want := []string{"lapsing", "learning", "reported", "pay-as-you-go", "capped"}
	for i, key := range want {
		if seats[got[i]].Key != key {
			t.Fatalf("order %v want %v", keys(got, seats), want)
		}
	}
}

func TestPaceBandsByValueThenKeepsTheListedOrder(t *testing.T) {
	week := testNow.Add(3 * 24 * time.Hour)
	seats := []Seat{
		{Key: "a", HasWeek: true, WeekLeft: 72, WeekReset: week}, // pace 1.0
		{Key: "b", HasWeek: true, WeekLeft: 70, WeekReset: week}, // 0.97, same band as a
		{Key: "c", HasWeek: true, WeekLeft: 50, WeekReset: week}, // 0.69, new band
		{Key: "d", HasWeek: true, WeekLeft: 47, WeekReset: week}, // 0.65, same band as c
	}
	got := Apply(paceStrategy{}, "pool", seats, testNow)
	want := []string{"a", "b", "c", "d"}
	for i, key := range want {
		if seats[got[i]].Key != key {
			t.Fatalf("order %v want %v", keys(got, seats), want)
		}
	}
	// Inside one band the listed order stands even if values differ slightly.
	seats = []Seat{
		{Key: "listed-first", HasWeek: true, WeekLeft: 70, WeekReset: week},
		{Key: "listed-second", HasWeek: true, WeekLeft: 72, WeekReset: week},
	}
	got = Apply(paceStrategy{}, "pool", seats, testNow)
	if seats[got[0]].Key != "listed-first" {
		t.Fatalf("band order %v", keys(got, seats))
	}
}

func TestDrainDoesNotReturnAfterAReset(t *testing.T) {
	b := NewBuiltin()
	strategy, _ := b.Find(Drain)
	seats := []Seat{{Key: "a"}, {Key: "b"}, {Key: "c"}}
	got := Apply(strategy, "pool", seats, testNow)
	if seats[got[0]].Key != "a" {
		t.Fatalf("first %v", keys(got, seats))
	}
	b.NoteDrained("pool", "a")
	// The window reset: nothing on the seat says exhausted now.
	got = Apply(strategy, "pool", seats, testNow.Add(6*time.Hour))
	if seats[got[0]].Key != "b" {
		t.Fatalf("after drain %v", keys(got, seats))
	}
	// A rest is not exhaustion: the pool stays.
	seats[1].Resting = true
	got = Apply(strategy, "pool", seats, testNow.Add(7*time.Hour))
	if seats[got[0]].Key != "b" {
		t.Fatalf("rest moved the pool %v", keys(got, seats))
	}
}

func TestWeightSpreadsThreeToOne(t *testing.T) {
	b := NewBuiltin()
	strategy, _ := b.Find(Weight)
	seats := []Seat{{Key: "a", Weight: 3}, {Key: "b", Weight: 1}}
	seq := ""
	count := map[string]int{}
	for i := 0; i < 8; i++ {
		got := Apply(strategy, "pool", seats, testNow)
		key := seats[got[0]].Key
		seq += key
		count[key]++
	}
	if count["a"] != 6 || count["b"] != 2 {
		t.Fatalf("counts %v in %q", count, seq)
	}
	// The smooth pattern spreads them: not "aaab...".
	if seq != "aabaaaba" {
		t.Fatalf("pattern %q", seq)
	}
}

func TestMoveRoundChargesTheSeatActuallyUsed(t *testing.T) {
	b := NewBuiltin()
	strategy, _ := b.Find(Weight)
	seats := []Seat{{Key: "a", Weight: 3}, {Key: "b", Weight: 1}}
	first := Apply(strategy, "pool", seats, testNow)
	picked := seats[first[0]].Key // a
	actual := "b"
	b.MoveRound("pool", picked, actual)
	got := Apply(strategy, "pool", seats, testNow)
	if seats[got[0]].Key != "a" {
		t.Fatalf("after move %v", keys(got, seats))
	}
}

func TestFastProbesOneUnmeasuredEveryFiveMixedRequests(t *testing.T) {
	b := NewBuiltin()
	strategy, _ := b.Find(Fast)
	measured := Seat{Key: "fast", TTFT: 100, TTFTSet: true, TPS: 50, TPSSet: true, MeasuredAt: testNow}
	seats := []Seat{measured, {Key: "unknown"}}
	probes := 0
	for i := 1; i <= 5; i++ {
		got := Apply(strategy, "pool", seats, testNow)
		if seats[got[0]].Key == "unknown" {
			probes++
			if i != 5 {
				t.Fatalf("probed on request %d", i)
			}
		}
	}
	if probes != 1 {
		t.Fatalf("probes %d", probes)
	}
}

func TestFastBandsByEstimate(t *testing.T) {
	b := NewBuiltin()
	strategy, _ := b.Find(Fast)
	seats := []Seat{
		{Key: "listed-first", TTFT: 102, TTFTSet: true, TPS: 50, TPSSet: true, MeasuredAt: testNow},
		{Key: "listed-second", TTFT: 100, TTFTSet: true, TPS: 50, TPSSet: true, MeasuredAt: testNow},
		{Key: "slow", TTFT: 5000, TTFTSet: true, TPS: 50, TPSSet: true, MeasuredAt: testNow},
	}
	got := Apply(strategy, "pool", seats, testNow)
	if seats[got[0]].Key != "listed-first" || seats[got[1]].Key != "listed-second" || seats[got[2]].Key != "slow" {
		t.Fatalf("bands %v", keys(got, seats))
	}
}

func TestCheapKeepsEqualPricesInTheListedOrder(t *testing.T) {
	seats := []Seat{
		{Key: "expensive", Priced: true, Cost: 5.0},
		{Key: "unpriced"},
		{Key: "cheap", Priced: true, Cost: 1.0},
		{Key: "cheap-peer", Priced: true, Cost: 1.005}, // within a cent of 1.0
	}
	got := Apply(cheapStrategy{}, "pool", seats, testNow)
	want := []string{"cheap", "cheap-peer", "expensive", "unpriced"}
	for i, key := range want {
		if seats[got[i]].Key != key {
			t.Fatalf("order %v want %v", keys(got, seats), want)
		}
	}
}

func TestExpiringPutsFreeByExpiryFirst(t *testing.T) {
	seats := []Seat{
		{Key: "paid"},
		{Key: "free-later", Free: true, ExpiresSet: true, Expires: testNow.Add(48 * time.Hour)},
		{Key: "free-soon", FreePlan: true, ExpiresSet: true, Expires: testNow.Add(2 * time.Hour)},
	}
	got := Apply(expiringStrategy{}, "pool", seats, testNow)
	want := []string{"free-soon", "free-later", "paid"}
	for i, key := range want {
		if seats[got[i]].Key != key {
			t.Fatalf("order %v want %v", keys(got, seats), want)
		}
	}
}

func TestRotateMovesOneDownPerRequest(t *testing.T) {
	b := NewBuiltin()
	strategy, _ := b.Find(Rotate)
	seats := []Seat{{Key: "a"}, {Key: "b"}, {Key: "c"}}
	for i, want := range []string{"a", "b", "c", "a"} {
		got := Apply(strategy, "pool", seats, testNow)
		if seats[got[0]].Key != want {
			t.Fatalf("request %d got %v", i, keys(got, seats))
		}
	}
}

func keys(order []int, seats []Seat) []string {
	out := make([]string, len(order))
	for i, k := range order {
		out[i] = seats[k].Key
	}
	return out
}
