package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/state"
)

func TestReportedUsageIsStoredOnceAndCounted(t *testing.T) {
	store, secret := dialStore(t, nil, nil)
	srv := &Server{Store: store}
	body := `{"requestId":"rep-1","agent":"codex","provider":"direct","model":"demo","in":12,"out":3,"cache_read":2,"status":200}`
	rr := callGateway(t, srv, secret, http.MethodPost, "/v1/magpie/usage/requests", body)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"duplicate":false`) {
		t.Fatalf("report %d %s", rr.Code, rr.Body.String())
	}
	rr = callGateway(t, srv, secret, http.MethodPost, "/v1/magpie/usage/requests", body)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("records %+v %v", recs, err)
	}
	rec := recs[0]
	if rec.Source != state.SourceReported || rec.RouteID != "rep-1" || rec.Agent != "codex" || rec.ProviderID != "direct" {
		t.Fatalf("record %+v", rec)
	}
	if rec.Counts().In != 12 || rec.Counts().Out != 3 || rec.Counts().CacheRead != 2 {
		t.Fatalf("counts %+v", rec.Counts())
	}
	if rec.InLimit() {
		t.Fatal("reported line consumes a gateway token limit")
	}
	summary := state.SummarizeUsage(recs)
	if summary.Calls != 1 || summary.In != 12 || summary.Out != 3 || summary.CacheRead != 2 {
		t.Fatalf("summary %+v", summary)
	}
}

func TestReportedUsageDeduplicatesGatewayRows(t *testing.T) {
	store, secret := dialStore(t, nil, nil)
	if err := store.AppendUsage(state.AttemptRecord{
		RouteID: "rep-gateway", ProviderID: "p1", Outcome: OutcomeSuccess, In: 7, Out: 1,
		Started: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/magpie/usage/requests",
		`{"requestId":"rep-gateway","provider":"direct","model":"demo","in":100,"out":20}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"duplicate":true`) {
		t.Fatalf("duplicate %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("records %+v %v", recs, err)
	}
	if recs[0].Source != "" || recs[0].In != 7 || recs[0].Out != 1 {
		t.Fatalf("gateway row changed %+v", recs[0])
	}
}

func TestReportedUsageNeedsARequestID(t *testing.T) {
	store, secret := dialStore(t, nil, nil)
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/magpie/usage/requests", `{"model":"demo","in":1,"out":1}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing id %d %s", rr.Code, rr.Body.String())
	}
}

func TestReportedCostOverridesThePriceList(t *testing.T) {
	store, secret := dialStore(t, nil, nil)
	body := `{"requestId":"rep-cost","provider":"direct","model":"demo","in":1000,"out":10,"cost":0.123,"status":200}`
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/magpie/usage/requests", body)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"duplicate":false`) {
		t.Fatalf("report %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 || recs[0].Cost == nil || *recs[0].Cost != 0.123 {
		t.Fatalf("stored cost %+v %v", recs, err)
	}
	sum := state.Summarize(recs, state.SummarizeOptions{Price: func(state.AttemptRecord) (float64, bool) {
		return 999, true
	}})
	if sum.Cost != 0.123 || sum.Unpriced != 0 {
		t.Fatalf("summary cost %+v", sum)
	}
	engine := price.Engine{Catalog: price.Fixed{"demo": {Input: 100, Output: 100}}}
	got, ok := engine.Cost(context.Background(), recs[0])
	if !ok || got != 0.123 {
		t.Fatalf("engine cost %v ok %v", got, ok)
	}
}

func TestReportedFailureShowsAFailedOutcome(t *testing.T) {
	store, secret := dialStore(t, nil, nil)
	body := `{"requestId":"rep-fail","provider":"direct","model":"demo","in":10,"out":2,"status":500}`
	rr := callGateway(t, &Server{Store: store}, secret, http.MethodPost, "/v1/magpie/usage/requests", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("report %d %s", rr.Code, rr.Body.String())
	}
	recs, err := store.ReadUsage(state.UsageFilter{})
	if err != nil || len(recs) != 1 {
		t.Fatalf("records %+v %v", recs, err)
	}
	if recs[0].Outcome == OutcomeSuccess || !recs[0].Failed() {
		t.Fatalf("reported failure %+v", recs[0])
	}
	if sum := state.SummarizeUsage(recs); sum.Calls != 1 || sum.Failed != 1 {
		t.Fatalf("summary %+v", sum)
	}
}

func TestTrafficCountsLiveBytesAndRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer up.Close()
	store, secret := dialStore(t, []state.Provider{{
		ID: "p1", Protocol: "chat", BaseURL: up.URL, Models: []string{"demo"}, Credential: "k",
	}}, []state.Route{{ID: "r1", ModelMatch: "demo", ProviderIDs: []string{"p1"}}})
	gw := &Server{Store: store}
	live := httptest.NewServer(gw)
	defer live.Close()
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, live.URL+"/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		res.Body.Close()
		done <- res.StatusCode
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream did not start")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := gw.Traffic()
		if snap.ActiveRequests == 1 && snap.BytesIn > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("traffic while in flight %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
	}
	snap := gw.Traffic()
	if snap.ActiveRequests != 0 || snap.BytesOut == 0 || snap.At.IsZero() {
		t.Fatalf("traffic after reply %+v", snap)
	}
}
