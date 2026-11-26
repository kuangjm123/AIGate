package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResolvePaths(t *testing.T) {
	dir := t.TempDir()
	got, err := ResolvePaths("", dir)
	if err != nil {
		t.Fatal(err)
	}
	wantFile := filepath.Join(dir, "aigate", "config.json")
	wantUsage := filepath.Join(dir, "aigate", "usage")
	if got.ConfigFile != wantFile || got.UsageDir != wantUsage {
		t.Fatalf("paths = %#v", got)
	}

	custom := filepath.Join(dir, "custom", "gate.json")
	got, err = ResolvePaths(custom, filepath.Join(dir, "unused"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigFile != custom {
		t.Fatalf("config = %s", got.ConfigFile)
	}
	if got.UsageDir != filepath.Join(dir, "custom", "usage") {
		t.Fatalf("usage = %s", got.UsageDir)
	}
	if _, err := os.Stat(filepath.Join(dir, "unused")); !os.IsNotExist(err) {
		t.Fatal("default directory was consulted")
	}
}

func TestCustomPathDoesNotWriteDefault(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "custom", "gate.json")
	paths, err := ResolvePaths(custom, filepath.Join(dir, "unused-home"))
	if err != nil {
		t.Fatal(err)
	}
	s := Open(paths)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(cfg *Config) error {
		id, err := FreshID(nil)
		if err != nil {
			return err
		}
		cfg.Providers = append(cfg.Providers, Provider{ID: id, Protocol: "p", Models: []string{}, Credential: "k"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defaultFile := filepath.Join(dir, "unused-home", "aigate", "config.json")
	if _, err := os.Stat(defaultFile); !os.IsNotExist(err) {
		t.Fatal("default config path was written")
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatal(err)
	}
}

func TestMissingFileIsEmptyConfig(t *testing.T) {
	s := openTemp(t)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	snap := s.Current()
	if snap == nil || len(snap.Config.Providers) != 0 {
		t.Fatalf("snapshot = %#v", snap)
	}
	if _, err := os.Stat(s.Paths().ConfigFile); !os.IsNotExist(err) {
		t.Fatal("missing file was created on load")
	}
}

func TestEmptyProviderListIsNotUnreadable(t *testing.T) {
	s := openTemp(t)
	writeRaw(t, s.Paths().ConfigFile, []byte(`{"providers":[]}`))
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	snap := s.Current()
	if snap == nil || len(snap.Config.Providers) != 0 {
		t.Fatalf("snapshot = %#v", snap)
	}
	if _, err := os.Stat(s.Paths().ConfigFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Paths().ConfigFile + ".bad"); !os.IsNotExist(err) {
		t.Fatal("empty list was set aside")
	}
}

func TestBadFileIsUnknownAndSetAside(t *testing.T) {
	s := openTemp(t)
	writeRaw(t, s.Paths().ConfigFile, []byte(`{"providers":[{"id":"p","protocol":"p","baseUrl":"","models":[]}]}`))
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	writeRaw(t, s.Paths().ConfigFile, []byte(`{`))
	err := s.Load()
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v", err)
	}
	snap := s.Current()
	if snap == nil || len(snap.Config.Providers) != 1 || snap.Config.Providers[0].ID != "p" {
		t.Fatalf("last good lost: %#v", snap)
	}
	if _, err := os.Stat(s.Paths().ConfigFile); !os.IsNotExist(err) {
		t.Fatal("bad file still at the config path")
	}
	aside, err := os.ReadFile(s.Paths().ConfigFile + ".bad")
	if err != nil {
		t.Fatal(err)
	}
	if string(aside) != `{` {
		t.Fatalf("aside = %s", aside)
	}
}

func TestFirstBadLoadIsNotEmpty(t *testing.T) {
	s := openTemp(t)
	writeRaw(t, s.Paths().ConfigFile, []byte(`{`))
	if err := s.Load(); !errors.Is(err, ErrUnreadable) {
		t.Fatal(err)
	}
	if s.Current() != nil {
		t.Fatal("unreadable file became an empty snapshot")
	}
}

func TestHashFailureDoesNotPublish(t *testing.T) {
	s := openTemp(t)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(cfg *Config) error {
		cfg.Providers = []Provider{{ID: "keep", Protocol: "p", Models: []string{}, Credential: "k"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	s.hash = func([]byte) (string, error) { return "", errors.New("hash") }
	err = s.Update(func(cfg *Config) error {
		cfg.Providers = append(cfg.Providers, Provider{ID: "new", Protocol: "p", Models: []string{}, Credential: "k"})
		return nil
	})
	if err == nil {
		t.Fatal("expected hash failure")
	}
	after, err := os.ReadFile(s.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("file changed after hash failure")
	}
	if len(s.Current().Config.Providers) != 1 || s.Current().Config.Providers[0].ID != "keep" {
		t.Fatal("snapshot changed after hash failure")
	}
}

func TestSnapshotStaysPut(t *testing.T) {
	s := openTemp(t)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(cfg *Config) error {
		cfg.Providers = []Provider{{ID: "p", Protocol: "old", Models: []string{}, Credential: "k"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	held := s.Current()
	if err := s.Update(func(cfg *Config) error {
		cfg.Providers[0].Protocol = "new"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if held.Config.Providers[0].Protocol != "old" {
		t.Fatalf("held snapshot changed to %s", held.Config.Providers[0].Protocol)
	}
	held.Config.Providers[0].Protocol = "mutated"
	if s.Current().Config.Providers[0].Protocol != "new" {
		t.Fatal("caller mutation reached the store")
	}
}

func TestDefaultsAreNotWritten(t *testing.T) {
	s := openTemp(t)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(cfg *Config) error {
		cfg.Providers = []Provider{{ID: "p", Protocol: "p", BaseURL: "https://example.com/v1", Models: []string{}, Credential: "k"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" || strings.Contains(string(b), "127.0.0.1:3425") || strings.Contains(string(b), "127.0.0.1:8080") || strings.Contains(string(b), "127.0.0.1:8081") {
		t.Fatalf("defaults were written: %s", b)
	}
}

func TestEligibleOmittedAndEmpty(t *testing.T) {
	var omitted Route
	if err := json.Unmarshal([]byte(`{"id":"r","modelMatch":"m","providerIds":[]}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.EligibleSet {
		t.Fatal("omitted eligible was set")
	}
	got := omitted.EligibleOrDefault()
	if len(got) != 1 || got[0] != ClassUpstreamUnavailable {
		t.Fatalf("default = %#v", got)
	}

	var empty Route
	if err := json.Unmarshal([]byte(`{"id":"r","modelMatch":"m","providerIds":[],"eligible":[]}`), &empty); err != nil {
		t.Fatal(err)
	}
	if !empty.EligibleSet || len(empty.Eligible) != 0 {
		t.Fatalf("empty = %+v", empty)
	}
	if len(empty.EligibleOrDefault()) != 0 {
		t.Fatal("explicit empty eligible gained a default")
	}
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"eligible":[]`) {
		t.Fatalf("marshal = %s", b)
	}
	b, err = json.Marshal(omitted)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "eligible") {
		t.Fatalf("omitted eligible was written: %s", b)
	}
}

func TestNegativeSpendLimitRejected(t *testing.T) {
	cfg := emptyConfig()
	neg := -1.0
	cfg.GatewayTokens = []GatewayToken{{ID: "t", Secret: "secret-secret", SpendLimit: &neg}}
	err := Validate(cfg)
	ve, ok := AsValidation(err)
	if !ok || ve.Reason != "spend_limit" {
		t.Fatalf("err = %v", err)
	}
}

func TestSameListenRejected(t *testing.T) {
	cfg := emptyConfig()
	cfg.Listeners.Gateway = "127.0.0.1:8081"
	err := Validate(cfg)
	ve, ok := AsValidation(err)
	if !ok || ve.Reason != "same_listen" {
		t.Fatalf("err = %v", err)
	}
}

func TestUsageAppendDoesNotRewriteConfig(t *testing.T) {
	s := openTemp(t)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(cfg *Config) error { return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendUsage(AttemptRecord{
		RouteID: "r", ProviderID: "p", Outcome: "success",
		Started: time.Now().UTC(), Finished: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.Paths().ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("usage append rewrote config")
	}
	recs, err := s.ReadUsage(UsageFilter{RouteID: "r"})
	if err != nil || len(recs) != 1 {
		t.Fatalf("recs=%v err=%v", recs, err)
	}
}

func TestUsageUnknownIsNotEmpty(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "usage-as-file")
	writeRaw(t, file, []byte("not a directory"))
	s := Open(Paths{ConfigFile: filepath.Join(dir, "config.json"), UsageDir: file})
	_, err := s.ReadUsage(UsageFilter{})
	if !errors.Is(err, ErrUsageUnknown) {
		t.Fatalf("err = %v", err)
	}
}

func TestUsageMissingIsEmpty(t *testing.T) {
	s := openTemp(t)
	recs, err := s.ReadUsage(UsageFilter{})
	if err != nil || len(recs) != 0 {
		t.Fatalf("recs=%v err=%v", recs, err)
	}
}

func TestUsageFilter(t *testing.T) {
	s := openTemp(t)
	start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	recs := []AttemptRecord{
		{RouteID: "r1", ProviderID: "p", Outcome: "success", Started: start, Finished: start},
		{RouteID: "r2", ProviderID: "q", Outcome: "success", Started: start.Add(time.Hour), Finished: start},
		{RouteID: "r1", ProviderID: "p", Outcome: "success", Started: start.Add(2 * time.Hour), Finished: start},
	}
	for _, rec := range recs {
		if err := s.AppendUsage(rec); err != nil {
			t.Fatal(err)
		}
	}
	from := start.Add(30 * time.Minute)
	to := start.Add(90 * time.Minute)
	got, err := s.ReadUsage(UsageFilter{RouteID: "r2", Start: &from, End: &to})
	if err != nil || len(got) != 1 || got[0].RouteID != "r2" {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestLegacyConfigIsUnreadable(t *testing.T) {
	s := openTemp(t)
	writeRaw(t, s.Paths().ConfigFile, []byte(`{"accounts":[{"id":"a","credential":"kept-aside"}],"routes":[{"id":"r","modelMatch":"m","accountIds":["a"]}]}`))
	if err := s.Load(); !errors.Is(err, ErrUnreadable) {
		t.Fatal(err)
	}
	if s.Current() != nil {
		t.Fatal("legacy config published a snapshot")
	}
	if _, err := os.Stat(s.Paths().ConfigFile); !os.IsNotExist(err) {
		t.Fatal("legacy file still at the config path")
	}
	aside, err := os.ReadFile(s.Paths().ConfigFile + ".bad")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(aside), `"accounts"`) {
		t.Fatalf("aside = %s", aside)
	}
}

func TestOldUsageAccountFieldIsIgnored(t *testing.T) {
	s := openTemp(t)
	if err := os.MkdirAll(s.Paths().UsageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"routeId":"r","accountId":"a","providerId":"p","outcome":"success","started":"2026-01-02T00:00:00Z","finished":"2026-01-02T00:00:00Z"}` + "\n"
	writeRaw(t, s.usageFile(), []byte(line))
	recs, err := s.ReadUsage(UsageFilter{ProviderID: "p"})
	if err != nil || len(recs) != 1 || recs[0].RouteID != "r" || recs[0].ProviderID != "p" {
		t.Fatalf("recs=%v err=%v", recs, err)
	}
	b, err := json.Marshal(recs[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "accountId") {
		t.Fatalf("account id kept: %s", b)
	}
}

func TestBadUsageLineIsUnknown(t *testing.T) {
	s := openTemp(t)
	if err := os.MkdirAll(s.Paths().UsageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRaw(t, s.usageFile(), []byte("{not json}\n"))
	if _, err := s.ReadUsage(UsageFilter{}); !errors.Is(err, ErrUsageUnknown) {
		t.Fatal(err)
	}
}

func TestUsagePartialLineIsLeft(t *testing.T) {
	s := openTemp(t)
	if err := os.MkdirAll(s.Paths().UsageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	good := `{"routeId":"r","providerId":"p","outcome":"success","started":"2026-01-02T00:00:00Z","finished":"2026-01-02T00:00:01Z"}`
	writeRaw(t, s.usageFile(), []byte(good+"\n"+`{"routeId":"cut"`))
	recs, err := s.ReadUsage(UsageFilter{})
	if err != nil || len(recs) != 1 || recs[0].RouteID != "r" {
		t.Fatalf("recs=%v err=%v", recs, err)
	}
	writeRaw(t, s.usageFile(), []byte(`{"routeId":"cut"`))
	recs, err = s.ReadUsage(UsageFilter{})
	if err != nil || len(recs) != 0 {
		t.Fatalf("partial recs=%v err=%v", recs, err)
	}
	writeRaw(t, s.usageFile(), []byte(good+"\n{not json}\n"))
	if _, err := s.ReadUsage(UsageFilter{}); !errors.Is(err, ErrUsageUnknown) {
		t.Fatal(err)
	}
}

func TestUsageCountsRoundTrip(t *testing.T) {
	s := openTemp(t)
	start := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	finish := start.Add(1500 * time.Millisecond)
	cases := []struct {
		usage string
		want  UsageCounts
	}{
		{`{"prompt_tokens":10,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":1}}`, UsageCounts{In: 6, Out: 3, CacheRead: 4, Reasoning: 1}},
		{`{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":4,"cache_creation_input_tokens":2}`, UsageCounts{In: 10, Out: 3, CacheRead: 4, CacheWrite: 2}},
		{`{"promptTokenCount":8,"candidatesTokenCount":2,"cachedContentTokenCount":5,"thoughtsTokenCount":1}`, UsageCounts{In: 3, Out: 2, CacheRead: 5, Reasoning: 1}},
	}
	for i, tc := range cases {
		err := s.AppendUsage(AttemptRecord{
			RouteID: "r", ProviderID: "p", Outcome: "http_201",
			Started: start, Finished: finish, Usage: json.RawMessage(tc.usage),
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.ReadUsage(UsageFilter{})
		if err != nil || len(got) != i+1 {
			t.Fatalf("read %v %v", got, err)
		}
		last := got[len(got)-1]
		if last.Counts() != tc.want || last.Status != 201 || last.MS != 1500 {
			t.Fatalf("got %+v counts %+v", last, last.Counts())
		}
		b, err := json.Marshal(last)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"cacheWrite":`) || !strings.Contains(string(b), `"in":`) {
			t.Fatalf("counts omitted: %s", b)
		}
	}
	err := s.AppendUsage(AttemptRecord{
		RouteID: "total", ProviderID: "p", Outcome: "success",
		Started: start, Finished: start, Usage: json.RawMessage(`{"total_tokens":3}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadUsage(UsageFilter{RouteID: "total"})
	if err != nil || len(got) != 1 {
		t.Fatalf("total %v %v", got, err)
	}
	b, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"in"`) || got[0].Counts() != (UsageCounts{}) {
		t.Fatalf("total-only counted: %s", b)
	}
}

func TestUsageSummarySkipsYesterday(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, loc)
	today, ok := PeriodStart("today", now)
	week, weekOK := PeriodStart("7d", now)
	month, monthOK := PeriodStart("30d", now)
	all, allOK := PeriodStart("all", now)
	if !ok || !weekOK || !monthOK || !allOK || !all.IsZero() {
		t.Fatal("period")
	}
	if !today.Equal(time.Date(2026, 6, 15, 0, 0, 0, 0, loc)) || !week.Equal(time.Date(2026, 6, 9, 0, 0, 0, 0, loc)) || !month.Equal(time.Date(2026, 5, 17, 0, 0, 0, 0, loc)) {
		t.Fatalf("today %s week %s month %s", today, week, month)
	}
	if _, bad := PeriodStart("year", now); bad {
		t.Fatal("year accepted")
	}
	s := openTemp(t)
	oldUsage := json.RawMessage(`{"prompt_tokens":100,"completion_tokens":1}`)
	if err := s.AppendUsage(AttemptRecord{RouteID: "old", ProviderID: "p", Outcome: "success", Started: today.Add(-time.Minute), Finished: today, Usage: oldUsage}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendUsage(AttemptRecord{RouteID: "new", ProviderID: "p", Outcome: "success", Started: today, Finished: today.Add(time.Second), Usage: json.RawMessage(`{"input_tokens":8,"output_tokens":2,"input_tokens_details":{"cached_tokens":3}}`)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadUsage(UsageFilter{Start: &today})
	if err != nil || len(got) != 1 || got[0].RouteID != "new" {
		t.Fatalf("today %v %v", got, err)
	}
	sum := SummarizeUsage(got)
	if sum.Calls != 1 || sum.In != 5 || sum.Out != 2 || sum.CacheRead != 3 {
		t.Fatalf("summary %+v", sum)
	}
	allRecs, err := s.ReadUsage(UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	allSum := SummarizeUsage(allRecs)
	if allSum.Calls != 2 || allSum.In != 105 {
		t.Fatalf("all %+v", allSum)
	}
	file, err := os.ReadFile(s.usageFile())
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"routeId":"raw","providerId":"p","outcome":"success","started":"2026-06-15T01:00:00Z","finished":"2026-06-15T01:00:00Z","usage":{"prompt_tokens":9,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":2}}}` + "\n"
	writeRaw(t, s.usageFile(), append(file, []byte(raw)...))
	withRaw, err := s.ReadUsage(UsageFilter{RouteID: "raw"})
	if err != nil || len(withRaw) != 1 || withRaw[0].Counts() != (UsageCounts{In: 7, Out: 1, CacheRead: 2}) {
		t.Fatalf("raw %+v %v", withRaw, err)
	}
}

func TestAttemptUsageOmitsMissingCounts(t *testing.T) {
	b, err := json.Marshal(AttemptRecord{RouteID: "r", Outcome: "success", Started: time.Unix(0, 0).UTC(), Finished: time.Unix(0, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "prompt_tokens") || strings.Contains(string(b), `"usage"`) {
		t.Fatalf("missing usage stored as a value: %s", b)
	}
}

func TestUpdateAndCurrentRace(t *testing.T) {
	s := openTemp(t)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			_ = s.Update(func(cfg *Config) error {
				cfg.Providers = []Provider{{ID: "p", Protocol: "p", Models: []string{}, Credential: "k"}}
				return nil
			})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 40; i++ {
			if snap := s.Current(); snap != nil && len(snap.Config.Providers) > 0 {
				_ = snap.Config.Providers[0].Protocol
			}
		}
	}()
	wg.Wait()
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	paths, err := ResolvePaths(filepath.Join(dir, "gate.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	return Open(paths)
}

func writeRaw(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
