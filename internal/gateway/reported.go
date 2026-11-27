package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kuangjm123/AIGate/internal/respond"
	"github.com/kuangjm123/AIGate/internal/state"
)

// reportedUsage is one client's record of a call it made directly to a
// vendor. The gateway only stores it; it never dialed that call.
type reportedUsage struct {
	RequestID       string          `json:"requestId"`
	ID              string          `json:"id"`
	At              time.Time       `json:"at"`
	Agent           string          `json:"agent"`
	ProviderID      string          `json:"provider"`
	ProviderAccount string          `json:"providerAccount"`
	Model           string          `json:"model"`
	Session         string          `json:"session"`
	Operation       string          `json:"operation"`
	Status          int             `json:"status"`
	MS              int             `json:"ms"`
	In              int             `json:"in"`
	Out             int             `json:"out"`
	CacheRead       int             `json:"cache_read"`
	CacheWrite      int             `json:"cache_write"`
	CacheWrite1H    int             `json:"cache_write_1h"`
	Reasoning       int             `json:"reasoning"`
	Cost            *float64        `json:"cost"`
	Err             string          `json:"err"`
	ErrType         string          `json:"err_type"`
	Usage           json.RawMessage `json:"usage"`
}

// reportUsage appends POST /v1/magpie/usage/requests. A repeated request id
// is acknowledged but not written twice. Reported lines are visible on the
// usage page and never consume a gateway token's limit.
func (s *Server) reportUsage(w http.ResponseWriter, r *http.Request, snap *state.Snapshot, admitted admission) int {
	if s.Store == nil {
		respond.Error(w, http.StatusServiceUnavailable, CodeUnknown)
		return http.StatusServiceUnavailable
	}
	body, tooBig := readBody(r.Body)
	if tooBig {
		respond.Error(w, http.StatusRequestEntityTooLarge, "too_large")
		return http.StatusRequestEntityTooLarge
	}
	var reported reportedUsage
	if err := json.Unmarshal(body, &reported); err != nil {
		respond.Error(w, http.StatusBadRequest, "bad_request")
		return http.StatusBadRequest
	}
	id := strings.TrimSpace(reported.RequestID)
	if id == "" {
		id = strings.TrimSpace(reported.ID)
	}
	if id == "" {
		respond.Error(w, http.StatusBadRequest, "bad_request")
		return http.StatusBadRequest
	}
	s.reportMu.Lock()
	defer s.reportMu.Unlock()
	existing, err := s.Store.ReadUsage(state.UsageFilter{RouteID: id})
	if err != nil {
		respond.Error(w, http.StatusServiceUnavailable, CodeUnknown)
		return http.StatusServiceUnavailable
	}
	for _, rec := range existing {
		if rec.RouteID == id {
			respond.JSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": true})
			return http.StatusOK
		}
	}
	started := reported.At
	if started.IsZero() {
		started = time.Now().UTC()
	}
	finished := started
	if reported.MS > 0 {
		finished = started.Add(time.Duration(reported.MS) * time.Millisecond)
	}
	agent := strings.TrimSpace(reported.Agent)
	if agent == "" {
		agent = admitted.caller
	}
	rec := state.AttemptRecord{
		RouteID:         id,
		ProviderID:      strings.TrimSpace(reported.ProviderID),
		ProviderAccount: strings.TrimSpace(reported.ProviderAccount),
		Outcome:         OutcomeSuccess,
		Status:          reported.Status,
		Started:         started,
		Finished:        finished,
		Usage:           cloneRaw(reported.Usage),
		Model:           strings.TrimSpace(reported.Model),
		Session:         strings.TrimSpace(reported.Session),
		Agent:           agent,
		Operation:       strings.TrimSpace(reported.Operation),
		Source:          state.SourceReported,
		Err:             reported.Err,
		ErrType:         reported.ErrType,
		Cost:            reported.Cost,
		In:              reported.In,
		Out:             reported.Out,
		CacheRead:       reported.CacheRead,
		CacheWrite:      reported.CacheWrite,
		CacheWrite1H:    reported.CacheWrite1H,
		Reasoning:       reported.Reasoning,
		MS:              reported.MS,
	}
	if rec.Status == 0 {
		rec.Status = http.StatusOK
	}
	switch {
	case rec.Canceled():
		rec.Outcome = "canceled"
	case rec.Status >= 400:
		if class := classifyStatus(rec.Status); class != "" {
			rec.Outcome = class
		} else {
			rec.Outcome = "http_" + strconv.Itoa(rec.Status)
		}
	case rec.Err != "" || rec.ErrType != "":
		rec.Outcome = "reported_error"
	}
	if rec.In+rec.Out+rec.CacheRead+rec.CacheWrite+rec.Reasoning == 0 && len(rec.Usage) == 0 && rec.Cost == nil {
		respond.Error(w, http.StatusBadRequest, "bad_request")
		return http.StatusBadRequest
	}
	if err := s.Store.AppendUsage(rec); err != nil {
		respond.Error(w, http.StatusInternalServerError, "usage_unwritable")
		return http.StatusInternalServerError
	}
	respond.JSON(w, http.StatusOK, map[string]any{"ok": true, "duplicate": false})
	return http.StatusOK
}
