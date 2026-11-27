package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int) *int           { return &v }

func appendAll(t *testing.T, s *Store, recs []AttemptRecord) {
	t.Helper()
	for _, rec := range recs {
		if err := s.AppendUsage(rec); err != nil {
			t.Fatal(err)
		}
	}
}

func groupOf(groups []UsageGroup, key string) (UsageGroup, bool) {
	for _, group := range groups {
		if group.Key == key {
			return group, true
		}
	}
	return UsageGroup{}, false
}

func seriesCalls(points []UsagePoint) int {
	total := 0
	for _, point := range points {
		total += point.Calls
	}
	return total
}

func TestUsageAppendFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "usage-as-file")
	writeRaw(t, file, []byte("not a directory"))
	s := Open(Paths{ConfigFile: filepath.Join(dir, "config.json"), UsageDir: file})
	if err := s.AppendUsage(AttemptRecord{RouteID: "r", Started: time.Now()}); err == nil {
		t.Fatal("append into a file succeeded")
	}
}

func TestUsageAppendFillsZeroTimes(t *testing.T) {
	s := openTemp(t)
	if err := s.AppendUsage(AttemptRecord{RouteID: "zero"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendUsage(AttemptRecord{RouteID: "ms", MS: 123}); err != nil {
		t.Fatal(err)
	}
	recs, err := s.ReadUsage(UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("records %+v", recs)
	}
	for _, rec := range recs {
		if rec.Started.IsZero() || rec.Finished.IsZero() {
			t.Fatalf("zero time %+v", rec)
		}
	}
	if recs[0].MS != 0 {
		t.Fatalf("zero record ms %+v", recs[0])
	}
	if recs[1].MS != 123 {
		t.Fatalf("explicit ms %+v", recs[1])
	}
}

func TestAttemptRecordRoundTripKeepsEveryField(t *testing.T) {
	s := openTemp(t)
	start := time.Date(2026, 6, 15, 1, 2, 3, 0, time.UTC)
	cost := 0.125
	want := AttemptRecord{
		RouteID: "req-1", Route: "route-1", ProviderID: "p1", Outcome: "success",
		Status: 200, Started: start, Finished: start.Add(2345 * time.Millisecond), MS: 2345,
		Usage:           json.RawMessage(`{"input_tokens":10,"output_tokens":3}`),
		Model:           "m1",
		Session:         "s1",
		TokenID:         "old-name",
		CallerKeyID:     "caller-key",
		CallerIssuerID:  "issuer-key",
		ProviderKeyID:   "upstream-key",
		ProviderAccount: "account@example.com",
		Caller:          "cli",
		Agent:           "agent",
		Protocol:        "chat",
		Operation:       "generate_content",
		Source:          SourceReported,
		Err:             "boom",
		ErrType:         "upstream",
		Tries:           2,
		HandedOn:        true,
		Rejected:        true,
		Cost:            &cost,
		TTFTMS:          120,
		FirstTextMS:     130,
		QueueMS:         4,
		RetryMS:         5,
		BytesIn:         1000,
		BytesOut:        2000,
		In:              10,
		Out:             3,
		CacheRead:       2,
		CacheWrite:      4,
		CacheWrite1H:    1,
		Reasoning:       1,
	}
	if err := s.AppendUsage(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadUsage(UsageFilter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("read %v %v", got, err)
	}
	want.counted = true
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("round trip mismatch\n got %+v\nwant %+v", got[0], want)
	}
	line, err := os.ReadFile(s.usageFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"ttft_ms":120`, `"first_text_ms":130`, `"queue_ms":4`, `"retry_ms":5`,
		`"bytes_in":1000`, `"bytes_out":2000`, `"cacheWrite1h":1`,
		`"handedOn":true`, `"routeId":"req-1"`, `"callerIssuerId":"issuer-key"`,
	} {
		assertNeedle(t, string(line), key)
	}
}

func assertNeedle(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%q missing from %s", needle, haystack)
	}
}

func TestUsageSummaryCountsOneCallAcrossHandedOn(t *testing.T) {
	recs := []AttemptRecord{
		{
			RouteID: "req", ProviderID: "p1", Outcome: "upstream_unavailable", Status: 503,
			HandedOn: true, Model: "m", ProviderKeyID: "k1", In: 10, Out: 4, CacheRead: 2,
			Started: time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC),
		},
		{
			RouteID: "req", ProviderID: "p2", Outcome: "success", Status: 200,
			Tries: 2, Model: "m", ProviderKeyID: "k2", In: 20, Out: 6,
			Started: time.Date(2026, 6, 15, 10, 0, 1, 0, time.UTC),
		},
	}
	sum := SummarizeUsage(recs)
	if sum.Calls != 1 || sum.Failed != 0 || sum.Canceled != 0 || sum.Retried != 1 {
		t.Fatalf("counts %+v", sum)
	}
	if sum.In != 30 || sum.Out != 10 || sum.CacheRead != 2 {
		t.Fatalf("tokens %+v", sum)
	}
	if _, ok := groupOf(sum.ProviderKeys, "k1"); !ok {
		t.Fatal("handed-on provider key missing from groups")
	}
	if recs[0].Answered() || !recs[1].Answered() {
		t.Fatalf("answered handed %v final %v", recs[0].Answered(), recs[1].Answered())
	}
}

func TestUsageCanceledFailedAndRejected(t *testing.T) {
	recs := []AttemptRecord{
		{Status: 499, Err: CanceledText, In: 1, Out: 1},
		{Status: 499, Err: "gateway timeout", In: 1},
		{Status: 500, In: 1},
		{Status: 200, In: 1},
		{Status: 502, Rejected: true},
	}
	sum := SummarizeUsage(recs)
	if sum.Calls != 5 || sum.Canceled != 1 || sum.Failed != 3 {
		t.Fatalf("summary %+v", sum)
	}
	if recs[0].Failed() || !recs[0].Canceled() || recs[0].Answered() {
		t.Fatalf("agent cancel %+v", recs[0])
	}
	if !recs[1].Failed() || recs[1].Canceled() || recs[1].Answered() {
		t.Fatalf("gateway 499 %+v", recs[1])
	}
	if !recs[2].Failed() || !recs[4].Failed() {
		t.Fatalf("failures %+v %+v", recs[2], recs[4])
	}
	if !recs[3].Answered() || recs[3].Failed() {
		t.Fatalf("success %+v", recs[3])
	}
}

func TestUsageSourcesAndLimitEligibility(t *testing.T) {
	base := AttemptRecord{ProviderID: "p", Model: "m", Status: 200, In: 10, Out: 2}
	probe := base
	probe.Source = SourceProbe
	mcp := base
	mcp.Source = SourceMCP
	search := base
	search.Source = SourceSearch
	cost := 0.5
	reported := base
	reported.Source = SourceReported
	reported.Cost = &cost

	sum := SummarizeUsage([]AttemptRecord{base, probe, mcp, search, reported})
	if sum.Calls != 2 || sum.In != 20 || sum.Out != 4 || sum.Cost != 0.5 {
		t.Fatalf("summary %+v", sum)
	}
	if !base.InLimit() {
		t.Fatal("gateway row must consume a limit")
	}
	for _, rec := range []AttemptRecord{probe, mcp, search, reported} {
		if rec.InLimit() {
			t.Fatalf("%q row consumed a limit", rec.Source)
		}
	}
	handed := base
	handed.HandedOn = true
	if !handed.InLimit() {
		t.Fatal("handed-on row must consume a limit")
	}
	canceled := base
	canceled.Status = 499
	canceled.Err = CanceledText
	if canceled.InLimit() {
		t.Fatal("canceled row consumed a limit")
	}
	rejected := base
	rejected.Rejected = true
	if rejected.InLimit() {
		t.Fatal("rejected row consumed a limit")
	}
	local := AttemptRecord{Status: 502}
	if local.InLimit() {
		t.Fatal("local rejection consumed a limit")
	}
}

func TestReportedRowsDoNotJoinSessionGroups(t *testing.T) {
	now := time.Now().UTC()
	recs := []AttemptRecord{
		{RouteID: "gateway", Session: "s1", Outcome: "success", Started: now, In: 10, Out: 1},
		{RouteID: "reported", Session: "s1", Source: SourceReported, Outcome: "success", Started: now, In: 20, Out: 2},
	}
	sum := SummarizeUsage(recs)
	if sum.Calls != 2 || sum.In != 30 || sum.Out != 3 {
		t.Fatalf("totals %+v", sum)
	}
	session, ok := groupOf(sum.Sessions, "s1")
	if !ok {
		t.Fatalf("session group missing %+v", sum.Sessions)
	}
	if session.Calls != 1 || session.In != 10 || session.Out != 1 {
		t.Fatalf("session group counted a reported row %+v", session)
	}
}

func TestUsageGroupsAndTodaySeries(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 15, 12, 30, 0, 0, loc)
	healthy := func(agent, model, key, account, caller, session string, in, out int) AttemptRecord {
		return AttemptRecord{
			ProviderID: "p", Agent: agent, Model: model, ProviderKeyID: key,
			ProviderAccount: account, CallerKeyID: caller, Session: session,
			Status: 200, Started: now.Add(-time.Minute), In: in, Out: out,
		}
	}
	a := healthy("agent-a", "model-a", "key-1", "acct-1", "caller-1", "session-1", 1, 1)
	b := healthy("agent-a", "model-a", "key-2", "acct-2", "caller-1", "", 2, 2)
	c := AttemptRecord{
		ProviderID: "p", Agent: "agent-b", Model: "model-b", ProviderKeyID: "key-1",
		ProviderAccount: "acct-1", CallerKeyID: "caller-2", Session: "session-2",
		Status: 500, Started: now.Add(-2 * time.Minute), In: 3,
	}
	sum := Summarize([]AttemptRecord{a, b, c}, SummarizeOptions{Period: "today", Now: now, Location: loc})
	if sum.Calls != 3 || sum.Failed != 1 || sum.In != 6 || sum.Out != 3 {
		t.Fatalf("totals %+v", sum)
	}

	agents := map[string]int{}
	for _, group := range sum.Agents {
		agents[group.Key] = group.Calls
	}
	if agents["agent-a"] != 2 || agents["agent-b"] != 1 {
		t.Fatalf("agents %+v", sum.Agents)
	}
	if group, ok := groupOf(sum.Models, "model-a"); !ok || group.Calls != 2 || group.In != 3 || group.Out != 3 {
		t.Fatalf("model group %+v", sum.Models)
	}
	if group, ok := groupOf(sum.ProviderKeys, "key-1"); !ok || group.Calls != 2 || group.Failed != 1 || group.In != 4 {
		t.Fatalf("key group %+v", sum.ProviderKeys)
	}
	if group, ok := groupOf(sum.Accounts, "acct-2"); !ok || group.Calls != 1 {
		t.Fatalf("account group %+v", sum.Accounts)
	}
	if group, ok := groupOf(sum.CallerKeys, "caller-1"); !ok || group.Calls != 2 {
		t.Fatalf("caller group %+v", sum.CallerKeys)
	}
	if len(sum.Sessions) != 2 {
		t.Fatalf("sessions %+v", sum.Sessions)
	}
	if _, ok := groupOf(sum.Sessions, ""); ok {
		t.Fatal("empty session was grouped")
	}
	if len(sum.Series) != now.Hour()+1 {
		t.Fatalf("today points %d", len(sum.Series))
	}
	if want := time.Date(2026, 6, 15, 0, 0, 0, 0, loc); !sum.Series[0].Start.Equal(want) {
		t.Fatalf("first point %s", sum.Series[0].Start)
	}
	last := sum.Series[len(sum.Series)-1]
	if last.Calls != 3 || last.Failed != 1 || last.In != 6 || last.Out != 3 {
		t.Fatalf("current hour %+v", last)
	}
}

func TestUsageSeriesWindows(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, loc)
	at := func(t time.Time) AttemptRecord {
		return AttemptRecord{ProviderID: "p", Outcome: "success", Status: 200, Started: t, In: 1}
	}
	recs := []AttemptRecord{
		at(time.Date(2026, 6, 15, 10, 0, 0, 0, loc)),
		at(time.Date(2026, 6, 14, 23, 0, 0, 0, loc)),
		at(time.Date(2026, 6, 8, 9, 0, 0, 0, loc)),
		at(time.Date(2026, 5, 6, 9, 0, 0, 0, loc)),
	}
	cases := []struct {
		period string
		points int
		calls  int
	}{
		{"today", 13, 1},
		{"7d", 7, 2},
		{"30d", 30, 3},
		{"all", 7, 4},
	}
	for _, tc := range cases {
		t.Run(tc.period, func(t *testing.T) {
			sum := Summarize(recs, SummarizeOptions{Period: tc.period, Now: now, Location: loc})
			if len(sum.Series) != tc.points || seriesCalls(sum.Series) != tc.calls {
				t.Fatalf("points %d calls %d", len(sum.Series), seriesCalls(sum.Series))
			}
			if sum.Calls != tc.calls || sum.In != tc.calls {
				t.Fatalf("totals %+v", sum)
			}
			if tc.period == "today" {
				if want := time.Date(2026, 6, 15, 0, 0, 0, 0, loc); !sum.Series[0].Start.Equal(want) {
					t.Fatalf("today starts %s", sum.Series[0].Start)
				}
				if sum.Series[len(sum.Series)-1].Start.Hour() != now.Hour() {
					t.Fatalf("today ends %s", sum.Series[len(sum.Series)-1].Start)
				}
			}
			if tc.period == "all" {
				if want := time.Date(2026, 5, 4, 0, 0, 0, 0, loc); !sum.Series[0].Start.Equal(want) {
					t.Fatalf("all starts %s", sum.Series[0].Start)
				}
				if want := time.Date(2026, 6, 15, 0, 0, 0, 0, loc); !sum.Series[len(sum.Series)-1].Start.Equal(want) {
					t.Fatalf("all ends %s", sum.Series[len(sum.Series)-1].Start)
				}
			}
		})
	}
}

func TestSummarizeTodayStartsAtLocalMidnight(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 15, 0, 30, 0, 0, loc)
	old := AttemptRecord{
		ProviderID: "p", Status: 200, Started: time.Date(2026, 6, 14, 23, 59, 59, 0, loc),
		In: 100,
	}
	today := AttemptRecord{
		ProviderID: "p", Status: 200, Started: time.Date(2026, 6, 15, 0, 0, 0, 0, loc),
		In: 7,
	}
	sum := Summarize([]AttemptRecord{old, today}, SummarizeOptions{Period: "today", Now: now, Location: loc})
	if sum.Calls != 1 || sum.In != 7 {
		t.Fatalf("totals %+v", sum)
	}
	if len(sum.Series) != 1 || sum.Series[0].Start.Hour() != 0 || sum.Series[0].Calls != 1 {
		t.Fatalf("series %+v", sum.Series)
	}
	all := SummarizeUsage([]AttemptRecord{old, today})
	if all.Calls != 2 || all.In != 107 {
		t.Fatalf("unbounded totals %+v", all)
	}
}

func TestUsageOneHourCacheIsClamped(t *testing.T) {
	cases := []struct {
		usage string
		want  UsageCounts
	}{
		{
			`{"input_tokens":10,"output_tokens":2,"cache_creation_input_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":7,"ephemeral_1h_input_tokens":3}}`,
			UsageCounts{In: 10, Out: 2, CacheWrite: 10, CacheWrite1H: 3},
		},
		{
			`{"input_tokens":1,"cache_creation_input_tokens":2,"cache_creation":{"ephemeral_1h_input_tokens":5}}`,
			UsageCounts{In: 1, CacheWrite: 2, CacheWrite1H: 2},
		},
		{
			`{"input_tokens":4,"output_tokens":2}`,
			UsageCounts{In: 4, Out: 2},
		},
	}
	for _, tc := range cases {
		got, ok := CountsFromUsage(json.RawMessage(tc.usage))
		if !ok || got != tc.want {
			t.Fatalf("%s: got %+v ok %v want %+v", tc.usage, got, ok, tc.want)
		}
	}
}

func TestUsageIndexChunksRestartAndHalfLine(t *testing.T) {
	s := openTemp(t)
	start := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	recs := make([]AttemptRecord, 700)
	for i := range recs {
		recs[i] = AttemptRecord{
			RouteID: "req", ProviderID: "p", Outcome: "success", Status: 200,
			Started:  start.Add(time.Duration(i) * time.Second),
			Finished: start.Add(time.Duration(i)*time.Second + 2*time.Second),
			In:       i % 5, Out: i % 3,
		}
	}
	appendAll(t, s, recs)
	got, err := s.ReadUsage(UsageFilter{})
	if err != nil || len(got) != 700 {
		t.Fatalf("read %d %v", len(got), err)
	}
	if len(s.usageIndex.blocks) != 2 || len(s.usageIndex.blocks[0]) != 512 || len(s.usageIndex.blocks[1]) != 188 {
		t.Fatalf("blocks %d %d", len(s.usageIndex.blocks), len(s.usageIndex.blocks[0]))
	}

	file, err := os.ReadFile(s.usageFile())
	if err != nil {
		t.Fatal(err)
	}
	var direct []AttemptRecord
	for _, line := range strings.Split(strings.TrimSuffix(string(file), "\n"), "\n") {
		var rec AttemptRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		direct = append(direct, rec)
	}
	if !reflect.DeepEqual(got, direct) {
		t.Fatal("index differed from a line-by-line read")
	}

	restarted := Open(s.Paths())
	again, err := restarted.ReadUsage(UsageFilter{})
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("restart differs: %v", err)
	}
	if !reflect.DeepEqual(SummarizeUsage(got), SummarizeUsage(again)) {
		t.Fatal("restart summary differs")
	}

	for i := 0; i < 12; i++ {
		rec := AttemptRecord{
			RouteID: "more", ProviderID: "p", Outcome: "success", Status: 200,
			Started: start.Add(time.Duration(700+i) * time.Second), In: 1,
		}
		if err := restarted.AppendUsage(rec); err != nil {
			t.Fatal(err)
		}
	}
	more, err := restarted.ReadUsage(UsageFilter{})
	if err != nil || len(more) != 712 {
		t.Fatalf("after restart append %d %v", len(more), err)
	}

	appendRaw(t, restarted.usageFile(), `{"routeId":"partial"`)
	if got, err := restarted.ReadUsage(UsageFilter{}); err != nil || len(got) != 712 {
		t.Fatalf("partial line read %d %v", len(got), err)
	}
	appendRaw(t, restarted.usageFile(), "}\n")
	if got, err := restarted.ReadUsage(UsageFilter{}); err != nil || len(got) != 713 {
		t.Fatalf("completed partial %d %v", len(got), err)
	}
	if got, _ := restarted.ReadUsage(UsageFilter{RouteID: "partial"}); len(got) != 1 {
		t.Fatalf("partial row %+v", got)
	}

	before, err := os.Stat(restarted.usageFile())
	if err != nil {
		t.Fatal(err)
	}
	appendRaw(t, restarted.usageFile(), "{not json}\n")
	if _, err := restarted.ReadUsage(UsageFilter{}); !errors.Is(err, ErrUsageUnknown) {
		t.Fatalf("bad line after chunks: %v", err)
	}
	if err := os.Truncate(restarted.usageFile(), before.Size()); err != nil {
		t.Fatal(err)
	}
	if got, err := restarted.ReadUsage(UsageFilter{}); err != nil || len(got) != 713 {
		t.Fatalf("recovered %d %v", len(got), err)
	}
}

func appendRaw(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUsageAppendRepairsCrashedTail(t *testing.T) {
	s := openTemp(t)
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	if err := s.AppendUsage(AttemptRecord{
		RouteID: "good", ProviderID: "p", Outcome: "success", Status: 200, Started: start,
	}); err != nil {
		t.Fatal(err)
	}
	appendRaw(t, s.usageFile(), `{"routeId":"cut"`)
	if got, err := s.ReadUsage(UsageFilter{}); err != nil || len(got) != 1 || got[0].RouteID != "good" {
		t.Fatalf("partial read %+v %v", got, err)
	}
	if err := s.AppendUsage(AttemptRecord{
		RouteID: "after", ProviderID: "p", Outcome: "success", Status: 200, Started: start.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadUsage(UsageFilter{})
	if err != nil || len(got) != 2 || got[0].RouteID != "good" || got[1].RouteID != "after" {
		t.Fatalf("after repair %+v %v", got, err)
	}
	raw, err := os.ReadFile(s.usageFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cut") {
		t.Fatalf("partial tail kept: %s", raw)
	}

	fresh := openTemp(t)
	writeRaw(t, fresh.usageFile(), []byte(`{"routeId":"only-partial"`))
	if err := fresh.AppendUsage(AttemptRecord{
		RouteID: "first", ProviderID: "p", Outcome: "success", Status: 200, Started: start,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := fresh.ReadUsage(UsageFilter{}); err != nil || len(got) != 1 || got[0].RouteID != "first" {
		t.Fatalf("partial-only repair %+v %v", got, err)
	}
}

func TestSortUsageNewestFirstIsStable(t *testing.T) {
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	recs := []AttemptRecord{
		{RouteID: "old", Started: start},
		{RouteID: "tie-a", Started: start.Add(time.Hour)},
		{RouteID: "tie-b", Started: start.Add(time.Hour)},
		{RouteID: "new", Started: start.Add(2 * time.Hour)},
	}
	SortUsageNewestFirst(recs)
	for i, id := range []string{"new", "tie-a", "tie-b", "old"} {
		if recs[i].RouteID != id {
			t.Fatalf("order %q %q %q %q", recs[0].RouteID, recs[1].RouteID, recs[2].RouteID, recs[3].RouteID)
		}
	}
}

func TestUsageFilterLegacyCallerAgentAndSource(t *testing.T) {
	s := openTemp(t)
	start := time.Date(2026, 6, 15, 8, 0, 0, 0, time.UTC)
	recs := []AttemptRecord{
		{RouteID: "r1", ProviderID: "p", Outcome: "success", Status: 200, Started: start,
			TokenID: "legacy-key", Caller: "legacy-agent", Model: "m1"},
		{RouteID: "r2", ProviderID: "q", Outcome: "success", Status: 200, Started: start.Add(time.Hour),
			Route: "configured", CallerKeyID: "new-key", Agent: "agent", Model: "m2", Source: SourceProbe},
	}
	appendAll(t, s, recs)
	if got, err := s.ReadUsage(UsageFilter{CallerKeyID: "legacy-key"}); err != nil || len(got) != 1 || got[0].RouteID != "r1" {
		t.Fatalf("legacy caller key %+v %v", got, err)
	}
	if got, err := s.ReadUsage(UsageFilter{Agent: "legacy-agent"}); err != nil || len(got) != 1 || got[0].RouteID != "r1" {
		t.Fatalf("legacy agent %+v %v", got, err)
	}
	if got, err := s.ReadUsage(UsageFilter{Agent: "agent", Model: "m2", Source: SourceProbe}); err != nil || len(got) != 1 || got[0].RouteID != "r2" {
		t.Fatalf("agent/model/source %+v %v", got, err)
	}
	if got, err := s.ReadUsage(UsageFilter{RouteID: "configured"}); err != nil || len(got) != 1 || got[0].RouteID != "r2" {
		t.Fatalf("configured route %+v %v", got, err)
	}
}

func TestUsageCostOverrideAndUnpriced(t *testing.T) {
	cost := 0.25
	recs := []AttemptRecord{
		{Model: "priced", Status: 200, In: 10, Out: 1, Cost: &cost},
		{Model: "unpriced", Status: 200, In: 10, Out: 1},
		{Model: "cache-only", Status: 200, CacheRead: 5},
		{Model: "empty", Status: 200},
	}
	calls := 0
	sum := Summarize(recs, SummarizeOptions{Price: func(rec AttemptRecord) (float64, bool) {
		calls++
		if rec.Model == "unpriced" {
			return 0, false
		}
		return 2, true
	}})
	if calls != 1 {
		t.Fatalf("price called %d times", calls)
	}
	if sum.Cost != 0.25 || sum.Unpriced != 1 {
		t.Fatalf("cost %.4f unpriced %d", sum.Cost, sum.Unpriced)
	}
	if group, ok := groupOf(sum.Models, "unpriced"); !ok || group.Unpriced != 1 {
		t.Fatalf("unpriced group %+v", sum.Models)
	}
	if group, ok := groupOf(sum.Models, "cache-only"); !ok || group.Unpriced != 0 || group.Cost != 0 {
		t.Fatalf("cache-only group %+v", sum.Models)
	}
}

func TestUsageHideDoesNotAffectCopiesOfRows(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	before := time.Date(2026, 6, 15, 12, 0, 0, 0, loc)
	hidden := AttemptRecord{ProviderID: "p", Model: "m", Started: before, Status: 200, In: 1}
	kept := hidden
	kept.Started = before.Add(time.Second)
	other := hidden
	other.ProviderID = "q"
	hide := UsageHide{ProviderID: "p", Model: "m", Before: before}
	if !hide.Hides(hidden) || hide.Hides(kept) || hide.Hides(other) {
		t.Fatalf("hides %v %v %v", hide.Hides(hidden), hide.Hides(kept), hide.Hides(other))
	}
	cfg := Config{UsageHidden: []UsageHide{hide}}
	if !cfg.HiddenOnUsagePage(hidden) || cfg.HiddenOnUsagePage(kept) {
		t.Fatal("config hide")
	}
	if !hidden.InLimit() || !kept.InLimit() {
		t.Fatal("clear changed limit eligibility")
	}
	sum := Summarize([]AttemptRecord{hidden, kept}, SummarizeOptions{Hide: cfg.HiddenOnUsagePage})
	if sum.Calls != 1 || sum.In != 1 {
		t.Fatalf("hidden summary %+v", sum)
	}
}

func TestTokenLimitWindowAndScope(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 15, 12, 30, 0, 0, loc) // Monday
	cases := []struct {
		limit TokenLimit
		start time.Time
		ok    bool
	}{
		{TokenLimit{Window: "today"}, time.Date(2026, 6, 15, 0, 0, 0, 0, loc), true},
		{TokenLimit{Window: "week"}, time.Date(2026, 6, 15, 0, 0, 0, 0, loc), true},
		{TokenLimit{Window: "month"}, time.Date(2026, 6, 1, 0, 0, 0, 0, loc), true},
		{TokenLimit{Window: "all"}, time.Time{}, true},
		{TokenLimit{Window: "hours", Hours: 6}, now.Add(-6 * time.Hour), true},
		{TokenLimit{Window: "days", Days: 3}, now.AddDate(0, 0, -3), true},
		{TokenLimit{Window: "days", Days: 2, Since: timePtr(time.Date(2026, 6, 1, 0, 0, 0, 0, loc))}, time.Date(2026, 6, 1, 0, 0, 0, 0, loc), true},
		{TokenLimit{Window: "hours"}, time.Time{}, false},
		{TokenLimit{Window: "days"}, time.Time{}, false},
		{TokenLimit{Window: "year"}, time.Time{}, false},
	}
	for _, tc := range cases {
		got, ok := tc.limit.WindowStart(now)
		if ok != tc.ok || (ok && !got.Equal(tc.start)) {
			t.Fatalf("%+v: got %s ok %v want %s %v", tc.limit, got, ok, tc.start, tc.ok)
		}
	}
	limit := TokenLimit{Provider: "p", Model: "m"}
	if !limit.Matches(AttemptRecord{ProviderID: "p", Model: "m"}) {
		t.Fatal("exact scope did not match")
	}
	for _, rec := range []AttemptRecord{
		{ProviderID: "q", Model: "m"},
		{ProviderID: "p", Model: "n"},
	} {
		if limit.Matches(rec) {
			t.Fatalf("scope matched %+v", rec)
		}
	}
	if (TokenLimit{}).Matches(AttemptRecord{ProviderID: "p", Model: "m"}) != true {
		t.Fatal("empty scope did not match")
	}
}

func timePtr(t time.Time) *time.Time { return &t }

func TestUsageChargedToCallerAndIssuer(t *testing.T) {
	direct := AttemptRecord{CallerKeyID: "caller"}
	short := AttemptRecord{CallerKeyID: "short", CallerIssuerID: "issuer"}
	legacy := AttemptRecord{TokenID: "old"}
	if !direct.ChargedTo("caller") || direct.ChargedTo("issuer") {
		t.Fatal("direct charge")
	}
	if !short.ChargedTo("short") || !short.ChargedTo("issuer") || short.ChargedTo("other") {
		t.Fatal("short-lived charge")
	}
	if !legacy.ChargedTo("old") || legacy.ChargedTo("") {
		t.Fatal("legacy charge")
	}
}

func TestValidateTokenLimits(t *testing.T) {
	one := 1.0
	ten := 10
	valid := []TokenLimit{
		{USD: &one, Window: "today"},
		{Tokens: &ten, Window: "week"},
		{USD: &one, Tokens: &ten, Window: "hours", Hours: 2},
	}
	for _, limit := range valid {
		if err := validateLimit(limit); err != nil {
			t.Fatalf("%+v: %v", limit, err)
		}
	}
	zero := 0.0
	zeroTokens := 0
	bad := []TokenLimit{
		{Window: "today"},
		{USD: &zero, Tokens: &zeroTokens, Window: "today"},
		{USD: &one, Window: ""},
		{USD: &one, Window: "hours"},
		{USD: &one, Window: "days"},
		{USD: &one, Window: "week", Since: timePtr(time.Now())},
		{USD: floatPtr(-1), Window: "today"},
		{Tokens: intPtr(-1), Window: "today"},
	}
	for _, limit := range bad {
		if err := validateLimit(limit); err == nil {
			t.Fatalf("%+v accepted", limit)
		}
	}
	limits := make([]TokenLimit, 5)
	for i := range limits {
		limits[i] = TokenLimit{ID: "limit-" + string(rune('a'+i)), USD: &one, Window: "today"}
	}
	cfg := Config{GatewayTokens: []GatewayToken{{ID: "t", Secret: "secret", Limits: limits}}}
	if err := validateTokens(cfg); err != nil {
		t.Fatalf("five limits: %v", err)
	}
	cfg.GatewayTokens[0].Limits = append(cfg.GatewayTokens[0].Limits, TokenLimit{ID: "limit-f", USD: &one, Window: "today"})
	if err := validateTokens(cfg); err == nil {
		t.Fatal("six limits accepted")
	}
	cfg.GatewayTokens[0].Limits = []TokenLimit{
		{ID: "same", USD: &one, Window: "today"},
		{ID: "same", USD: &one, Window: "week"},
	}
	if err := validateTokens(cfg); err == nil {
		t.Fatal("duplicate limit id accepted")
	}
}
