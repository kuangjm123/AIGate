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
