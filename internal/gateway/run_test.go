package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/state"
)

func TestFallbackRules(t *testing.T) {
	const secret = "provider-secret"
	snap := twoProviderSnap(secret)
	var calls []string
	dial := func(class string, completed bool, body string) Dialer {
		return dialFunc(func(req DialRequest) (DialResponse, error) {
			calls = append(calls, req.ProviderID)
			if req.ProviderID == "p1" && req.Credential != secret {
				t.Errorf("credential = %s", req.Credential)
			}
			if req.RouteID != "r" {
				t.Errorf("route=%s", req.RouteID)
			}
			if completed && req.ProviderID == "p2" {
				return DialResponse{Completed: true, Status: http.StatusOK, Body: []byte(body)}, nil
			}
			if completed && req.ProviderID == "p1" && class == "" {
				return DialResponse{Completed: true, Status: http.StatusOK, Body: []byte(body)}, nil
			}
			return DialResponse{Class: class}, nil
		})
	}

	t.Run("upstream continues", func(t *testing.T) {
		calls = nil
		var recs []state.AttemptRecord
		res, err := Run(context.Background(), snap, "m", "127.0.0.1:8081", dial(string(state.ClassUpstreamUnavailable), true, "second"), nil, collect(&recs))
		if err != nil {
			t.Fatal(err)
		}
		if string(res.Body) != "second" || len(calls) != 2 || len(recs) != 1 {
			t.Fatalf("body=%s calls=%v recs=%d", res.Body, calls, len(recs))
		}
		if recs[0].ProviderID != "p2" || recs[0].RouteID == "" || recs[0].Model != "m" || recs[0].Tries != 2 {
			t.Fatalf("record %+v", recs[0])
		}
	})

	t.Run("provider auth stops", func(t *testing.T) {
		calls = nil
		res, err := Run(context.Background(), snap, "m", "127.0.0.1:8081", dial(string(state.ClassAuthFailure), false, ""), nil, discardUsage)
		if err != nil {
			t.Fatal(err)
		}
		if res.Code != string(state.ClassAuthFailure) || len(calls) != 1 {
			t.Fatalf("code=%s calls=%v", res.Code, calls)
		}
	})

	t.Run("rate limit stops unless listed", func(t *testing.T) {
		calls = nil
		res, err := Run(context.Background(), snap, "m", "127.0.0.1:8081", dial(string(state.ClassRateLimit), false, ""), nil, discardUsage)
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || res.Code != string(state.ClassRateLimit) {
			t.Fatalf("code=%s calls=%v", res.Code, calls)
		}
	})

	t.Run("listed rate limit continues", func(t *testing.T) {
		calls = nil
		listed := *snap
		listed.Config = snap.Config.Clone()
		listed.Config.Routes[0].EligibleSet = true
		listed.Config.Routes[0].Eligible = []state.ErrorClass{state.ClassRateLimit}
		res, err := Run(context.Background(), &listed, "m", "127.0.0.1:8081", dial(string(state.ClassRateLimit), true, "ok"), nil, discardUsage)
		if err != nil {
			t.Fatal(err)
		}
		if string(res.Body) != "ok" || len(calls) != 2 {
			t.Fatalf("body=%s calls=%v", res.Body, calls)
		}
	})

	t.Run("explicit empty eligible does not continue", func(t *testing.T) {
		calls = nil
		listed := *snap
		listed.Config = snap.Config.Clone()
		listed.Config.Routes[0].EligibleSet = true
		listed.Config.Routes[0].Eligible = nil
		res, err := Run(context.Background(), &listed, "m", "127.0.0.1:8081", dial(string(state.ClassUpstreamUnavailable), false, ""), nil, discardUsage)
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || res.Code != string(state.ClassUpstreamUnavailable) {
			t.Fatalf("code=%s calls=%v", res.Code, calls)
		}
	})

	t.Run("success stops", func(t *testing.T) {
		calls = nil
		res, err := Run(context.Background(), snap, "m", "127.0.0.1:8081", dial("", true, "first"), nil, discardUsage)
		if err != nil {
			t.Fatal(err)
		}
		if string(res.Body) != "first" || len(calls) != 1 {
			t.Fatalf("body=%s calls=%v", res.Body, calls)
		}
	})
}

func TestRunRecordsTheModelSentToTheDialer(t *testing.T) {
	var sent string
	var recs []state.AttemptRecord
	hook := &spyHook{model: "upstream-m"}
	res, err := Run(context.Background(), twoProviderSnap("s"), "m", "127.0.0.1:8081", dialFunc(func(req DialRequest) (DialResponse, error) {
		sent = req.Fields.Model
		return DialResponse{Completed: true, Status: http.StatusOK, Body: []byte("ok")}, nil
	}), []Hook{hook}, collect(&recs))
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Body) != "ok" || sent != "upstream-m" {
		t.Fatalf("body=%s sent=%q", res.Body, sent)
	}
	if len(recs) != 1 || recs[0].Model != "upstream-m" {
		t.Fatalf("records %+v", recs)
	}
}

func TestRunCompletedNonSuccessStatusIsAFailure(t *testing.T) {
	var recs []state.AttemptRecord
	res, err := Run(context.Background(), twoProviderSnap("s"), "m", "127.0.0.1:8081", dialFunc(func(DialRequest) (DialResponse, error) {
		return DialResponse{Completed: true, Status: http.StatusInternalServerError, Body: []byte("provider error")}, nil
	}), nil, collect(&recs))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != http.StatusInternalServerError || string(res.Body) != "provider error" || res.Code == "" {
		t.Fatalf("result %+v", res)
	}
	if len(recs) != 1 || recs[0].Status != http.StatusInternalServerError || !recs[0].Failed() || recs[0].Outcome == OutcomeSuccess {
		t.Fatalf("records %+v", recs)
	}
	summary := state.SummarizeUsage(recs)
	if summary.Calls != 1 || summary.Failed != 1 {
		t.Fatalf("summary %+v", summary)
	}
}

func TestRunFailureKeepsTheDialerStatus(t *testing.T) {
	snap := twoProviderSnap("s")
	snap.Config.Routes[0].ProviderIDs = []string{"p1"}
	var recs []state.AttemptRecord
	res, err := Run(context.Background(), snap, "m", "127.0.0.1:8081", dialFunc(func(DialRequest) (DialResponse, error) {
		return DialResponse{Class: string(state.ClassUpstreamUnavailable), Status: http.StatusGatewayTimeout}, nil
	}), nil, collect(&recs))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != http.StatusGatewayTimeout || len(recs) != 1 || recs[0].Status != http.StatusGatewayTimeout || !recs[0].Failed() {
		t.Fatalf("result %+v records %+v", res, recs)
	}
}

func TestRunBilledFailureThenSuccessWritesHandedOn(t *testing.T) {
	snap := twoProviderSnap("s")
	var recs []state.AttemptRecord
	res, err := Run(context.Background(), snap, "m", "127.0.0.1:8081", dialFunc(func(req DialRequest) (DialResponse, error) {
		if req.ProviderID == "p1" {
			return DialResponse{
				Class: string(state.ClassUpstreamUnavailable),
				Usage: json.RawMessage(`{"prompt_tokens":10,"completion_tokens":3}`),
			}, nil
		}
		return DialResponse{
			Completed: true, Status: http.StatusOK, Body: []byte("second"),
			Usage: json.RawMessage(`{"prompt_tokens":5,"completion_tokens":2}`),
		}, nil
	}), nil, collect(&recs))
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Body) != "second" || len(recs) != 2 {
		t.Fatalf("body=%s records=%+v", res.Body, recs)
	}
	if recs[0].RouteID == "" || recs[0].RouteID != recs[1].RouteID {
		t.Fatalf("route ids %q %q", recs[0].RouteID, recs[1].RouteID)
	}
	if !recs[0].HandedOn || recs[0].Model != "m" || recs[0].ProviderID != "p1" {
		t.Fatalf("handed on %+v", recs[0])
	}
	if recs[1].HandedOn || recs[1].Model != "m" || recs[1].ProviderID != "p2" || recs[1].Tries != 2 {
		t.Fatalf("final %+v", recs[1])
	}
	summary := state.SummarizeUsage(recs)
	if summary.Calls != 1 || summary.Failed != 0 || summary.In != 15 || summary.Out != 5 {
		t.Fatalf("summary %+v", summary)
	}
}

func TestRunLastBilledFailureIsTheFinalRow(t *testing.T) {
	snap := twoProviderSnap("s")
	snap.Config.Routes[0].ProviderIDs = []string{"p1"}
	var recs []state.AttemptRecord
	res, err := Run(context.Background(), snap, "m", "127.0.0.1:8081", dialFunc(func(DialRequest) (DialResponse, error) {
		return DialResponse{
			Class: string(state.ClassUpstreamUnavailable),
			Usage: json.RawMessage(`{"prompt_tokens":10,"completion_tokens":3}`),
		}, nil
	}), nil, collect(&recs))
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != string(state.ClassUpstreamUnavailable) || res.Status != http.StatusBadGateway {
		t.Fatalf("result %+v", res)
	}
	if len(recs) != 1 || recs[0].HandedOn || recs[0].Model != "m" || !recs[0].Failed() {
		t.Fatalf("records %+v", recs)
	}
	if got := recs[0].Counts(); got.In != 10 || got.Out != 3 {
		t.Fatalf("counts %+v", got)
	}
}

func TestRunReturnsTheLastAllowedHookRejection(t *testing.T) {
	var recs []state.AttemptRecord
	hook := &spyHook{reject: string(state.ClassUpstreamUnavailable)}
	res, err := Run(context.Background(), twoProviderSnap("s"), "m", "127.0.0.1:8081", dialFunc(func(DialRequest) (DialResponse, error) {
		t.Fatal("dialed after hook rejection")
		return DialResponse{}, nil
	}), []Hook{hook}, collect(&recs))
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != string(state.ClassUpstreamUnavailable) || res.Status != http.StatusBadGateway {
		t.Fatalf("result %+v", res)
	}
	if len(recs) != 1 || recs[0].ProviderID != "p2" || recs[0].Model != "m" || recs[0].Tries != 2 || !recs[0].Rejected {
		t.Fatalf("records %+v", recs)
	}
}

func TestUnknownModelDoesNotDial(t *testing.T) {
	calls := 0
	res, err := Run(context.Background(), twoProviderSnap("s"), "missing", "127.0.0.1:8081", dialFunc(func(DialRequest) (DialResponse, error) {
		calls++
		return DialResponse{}, nil
	}), nil, discardUsage)
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != CodeNoRoute || calls != 0 {
		t.Fatalf("code=%s calls=%d", res.Code, calls)
	}
}

func TestUnsetBaseURLIsNotDialed(t *testing.T) {
	snap := twoProviderSnap("s")
	snap.Config.Providers[0].BaseURL = ""
	calls := 0
	res, err := Run(context.Background(), snap, "m", "127.0.0.1:8081", dialFunc(func(DialRequest) (DialResponse, error) {
		calls++
		return DialResponse{Completed: true, Body: []byte("no")}, nil
	}), nil, discardUsage)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || res.Code != CodeUnknown {
		t.Fatalf("code=%s calls=%d", res.Code, calls)
	}
}

func TestLinkLocalIsNotDialed(t *testing.T) {
	snap := twoProviderSnap("s")
	snap.Config.Providers[0].BaseURL = "http://169.254.1.1/v1"
	calls := 0
	res, err := Run(context.Background(), snap, "m", "127.0.0.1:8081", dialFunc(func(DialRequest) (DialResponse, error) {
		calls++
		return DialResponse{Completed: true}, nil
	}), nil, discardUsage)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || res.Code != CodeBaseURL {
		t.Fatalf("code=%s calls=%d", res.Code, calls)
	}
}

func TestAppendFailureKeepsBody(t *testing.T) {
	res, err := Run(context.Background(), twoProviderSnap("s"), "m", "127.0.0.1:8081", dialFunc(func(DialRequest) (DialResponse, error) {
		return DialResponse{Completed: true, Status: 200, Body: []byte("hello")}, nil
	}), nil, func(state.AttemptRecord) error { return errors.New("disk") })
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Body) != "hello" || !res.AppendFailed {
		t.Fatalf("result = %+v", res)
	}
}

func TestMissingUsageIsNotZero(t *testing.T) {
	var rec state.AttemptRecord
	_, err := Run(context.Background(), twoProviderSnap("s"), "m", "127.0.0.1:8081", dialFunc(func(DialRequest) (DialResponse, error) {
		return DialResponse{Completed: true, Body: []byte("hello")}, nil
	}), nil, func(got state.AttemptRecord) error {
		rec = got
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "prompt_tokens") || strings.Contains(string(b), `"usage"`) {
		t.Fatalf("record = %s", b)
	}
}

func TestHeldSnapshotIgnoresLaterWrite(t *testing.T) {
	dir := t.TempDir()
	paths, err := state.ResolvePaths(dir+"/gate.json", "")
	if err != nil {
		t.Fatal(err)
	}
	store := state.Open(paths)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Providers = []state.Provider{{ID: "p", Protocol: "openai", BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"m"}, Credential: "old-secret"}}
		cfg.Routes = []state.Route{{ID: "r", ModelMatch: "m", ProviderIDs: []string{"p"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snap := store.Current()
	started := make(chan struct{})
	release := make(chan struct{})
	var got string
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = Run(context.Background(), snap, "m", "127.0.0.1:8081", dialFunc(func(req DialRequest) (DialResponse, error) {
			got = req.Credential
			close(started)
			<-release
			return DialResponse{Completed: true, Body: []byte("ok")}, nil
		}), nil, discardUsage)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dial did not start")
	}
	if err := store.Update(func(cfg *state.Config) error {
		cfg.Providers[0].Credential = "new-secret"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if got != "old-secret" {
		t.Fatalf("dial saw %s", got)
	}
	if store.Current().Config.Providers[0].Credential != "new-secret" {
		t.Fatal("store did not publish the new credential")
	}
}

func TestHookDoesNotSeeSecret(t *testing.T) {
	hook := &spyHook{reject: string(state.ClassRateLimit)}
	res, err := Run(context.Background(), twoProviderSnap("super-secret-value"), "m", "127.0.0.1:8081", dialFunc(func(DialRequest) (DialResponse, error) {
		t.Fatal("dialed after hook rejection")
		return DialResponse{}, nil
	}), []Hook{hook}, discardUsage)
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != string(state.ClassRateLimit) || hook.after != 1 {
		t.Fatalf("code=%s after=%d", res.Code, hook.after)
	}
	b, err := json.Marshal(hook.view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "super-secret-value") {
		t.Fatalf("hook saw secret: %s", b)
	}
}

func TestNoDialectBeforeLoop(t *testing.T) {
	store := loadTemp(t)
	secret := ""
	if err := store.Update(func(cfg *state.Config) error {
		sec, err := state.NewSecret()
		if err != nil {
			return err
		}
		secret = sec
		cfg.GatewayTokens = []state.GatewayToken{{ID: "tok", Secret: sec}}
		cfg.Plugins = []state.Plugin{{ID: "plug", Enabled: true, Order: 0}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	srv := &Server{Store: store, Log: log.New(&buf, "", 0)}
	req := httptest.NewRequest(http.MethodPost, "/v1/other", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer "+secret)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != CodeNoDialect {
		t.Fatalf("body %s", rr.Body.String())
	}
	for _, class := range []string{"auth_failure", "rate_limit", "empty_balance", "upstream_unavailable"} {
		if strings.Contains(body["error"], class) {
			t.Fatalf("no dialect classified as %s", class)
		}
	}
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("log leaked token: %s", buf.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 0 {
		t.Fatalf("usage recorded without a dialect: %v %v", recs, err)
	}
}

func TestGatewayAuthAndWrongPort(t *testing.T) {
	store := loadTemp(t)
	srv := &Server{Store: store}
	req := httptest.NewRequest(http.MethodPost, "/providers", strings.NewReader(`{"protocol":"p","baseUrl":"https://example.com","models":[]}`))
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), CodeRejected) {
		t.Fatalf("console route on gateway: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(store.Paths().ConfigFile); !os.IsNotExist(err) {
		t.Fatal("gateway applied a console write")
	}

	req = httptest.NewRequest(http.MethodGet, "/assets/index.js", nil)
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), CodeRejected) {
		t.Fatalf("console asset on gateway: %d %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), CodeRejected) {
		t.Fatalf("control page on gateway: %d %s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "auth_failure") {
		t.Fatalf("missing token: %d %s", rr.Code, rr.Body.String())
	}
}

func loadTemp(t *testing.T) *state.Store {
	t.Helper()
	dir := t.TempDir()
	paths, err := state.ResolvePaths(dir+"/gate.json", "")
	if err != nil {
		t.Fatal(err)
	}
	store := state.Open(paths)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	return store
}

type dialFunc func(DialRequest) (DialResponse, error)

func (f dialFunc) Dial(_ context.Context, req DialRequest) (DialResponse, error) {
	return f(req)
}

func discardUsage(state.AttemptRecord) error { return nil }

func collect(dst *[]state.AttemptRecord) func(state.AttemptRecord) error {
	return func(rec state.AttemptRecord) error {
		*dst = append(*dst, rec)
		return nil
	}
}

func twoProviderSnap(secret string) *state.Snapshot {
	return &state.Snapshot{Config: state.Config{
		Providers: []state.Provider{
			{ID: "p1", Protocol: "openai", BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"m"}, Credential: secret},
			{ID: "p2", Protocol: "openai", BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"m"}, Credential: "other"},
		},
		Routes: []state.Route{{
			ID: "r", ModelMatch: "m", ProviderIDs: []string{"p1", "p2"},
		}},
	}}
}

type spyHook struct {
	view   View
	reject string
	model  string
	after  int
}

func (h *spyHook) BeforeDial(_ context.Context, view View, in Fields) (Fields, string) {
	h.view = view
	if h.model != "" {
		in.Model = h.model
	}
	return in, h.reject
}

func (h *spyHook) AfterAttempt(context.Context, View, state.AttemptRecord, []byte) {
	h.after++
}
