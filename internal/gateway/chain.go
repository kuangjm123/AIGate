package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kuangjm123/aiconv"

	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

const (
	hookTimeout       = time.Second
	middlewareTimeout = 250 * time.Millisecond
	bodyLimit         = 32 << 20
	captureLimit      = 8 << 20
)

var (
	errBadBody = errors.New("bad body")
	errTools   = errors.New("tools")
	errHook    = errors.New("hook")
)

// CallContext is what a hook or middleware may see. It has no secrets.
type CallContext struct {
	Protocol Protocol `json:"protocol"`
	Model    string   `json:"model"`
	Stream   bool     `json:"stream"`
	Path     string   `json:"path"`
	Origin   string   `json:"origin"`
	Agent    string   `json:"agent"`
	Session  string   `json:"session"`
	Kind     string   `json:"kind"`
}

// Reject is a middleware decision to stop this request.
type Reject struct {
	Status  int
	Message string
}

// RequestHook adds context before routing and observes the reply afterward.
// It cannot reject the request. A failure, a timeout, or a change to the model,
// the stream flag, or the tool declaration restores the previous body.
// A hook that failed or was rolled back is not told about the reply.
type RequestHook interface {
	OnRequest(ctx context.Context, body json.RawMessage, call CallContext) (json.RawMessage, error)
	OnResponse(ctx context.Context, reply json.RawMessage, call CallContext, success bool)
}

// Middleware rewrites or rejects a request, rewrites one provider attempt,
// and may rewrite a finished JSON reply. A failure leaves the bytes unchanged.
// OnTry starts from the body left by OnRequest, not from the previous provider.
type Middleware interface {
	OnRequest(ctx context.Context, body json.RawMessage, call CallContext) (json.RawMessage, *Reject)
	OnTry(ctx context.Context, body json.RawMessage, call CallContext, providerID string, protocol Protocol) json.RawMessage
	OnReply(ctx context.Context, status int, body json.RawMessage, call CallContext) (int, json.RawMessage)
}

// EventEditor rewrites one streamed JSON event. drop omits the event.
// A nil out keeps the original bytes.
type EventEditor interface {
	EditEvent(data []byte) (out []byte, drop bool)
}

// SetConsoleListen records the console address a provider URL must not target.
func (s *Server) SetConsoleListen(addr string) {
	if s == nil {
		return
	}
	s.consoleListen.Store(addr)
}

func (s *Server) consoleAddr() string {
	if s == nil {
		return ""
	}
	v := s.consoleListen.Load()
	addr, _ := v.(string)
	return addr
}

func (s *Server) serveModel(w http.ResponseWriter, r *http.Request, snap *state.Snapshot, proto Protocol, admitted admission) int {
	requestStart := time.Now().UTC()
	requestID, _ := state.NewID()
	body, tooBig := readBody(r.Body)
	if tooBig {
		writeProtocolError(w, proto, http.StatusRequestEntityTooLarge, "too_large", "too_large")
		return http.StatusRequestEntityTooLarge
	}
	parsed, err := Parse(proto, r, body)
	if err != nil {
		writeProtocolError(w, proto, http.StatusBadRequest, "bad_request", "bad_request")
		return http.StatusBadRequest
	}
	call := callContext(r, parsed)
	ancillary := call.Kind == "title" || call.Kind == "summary" || call.Kind == "compact"
	meta := attemptMeta{
		Model: parsed.Model, Session: call.Session, TokenID: admitted.tokenID,
		CallerKeyID: admitted.tokenID, CallerIssuerID: admitted.issuerID,
		Caller: admitted.caller, Agent: call.Agent, Protocol: string(proto),
	}
	if meta.Agent == "" {
		meta.Agent = admitted.caller
	}
	cancelBeforeRoute := func() int {
		s.note(snap, meta, usageNote{
			RouteID: requestID, Outcome: "canceled", Status: 499, Started: requestStart,
			Err: state.CanceledText, ErrType: "agent_canceled",
		})
		return 499
	}
	accepted := append([]byte(nil), parsed.Raw...)
	var told []RequestHook
	if !ancillary {
		if r.Context().Err() != nil {
			return cancelBeforeRoute()
		}
		var hookErr error
		accepted, told, hookErr = s.runHooks(r.Context(), proto, r, parsed, accepted, call)
		if hookErr != nil {
			if r.Context().Err() != nil {
				return cancelBeforeRoute()
			}
			return http.StatusBadGateway
		}
		if r.Context().Err() != nil {
			return cancelBeforeRoute()
		}
		var status int
		var rejected, canceled bool
		accepted, status, rejected, canceled = s.runMiddleware(r.Context(), proto, accepted, call, w)
		if canceled {
			return cancelBeforeRoute()
		}
		if rejected {
			return status
		}
		if r.Context().Err() != nil {
			return cancelBeforeRoute()
		}
		again, perr := Parse(proto, r, accepted)
		if perr != nil {
			writeProtocolError(w, proto, http.StatusBadRequest, "bad_request", "bad_request")
			return http.StatusBadRequest
		}
		parsed = again
		call.Model = parsed.Model
		call.Stream = parsed.Stream
	}
	meta.Model = parsed.Model
	if r.Context().Err() != nil {
		return cancelBeforeRoute()
	}
	if code := gateModel(snap.Config, parsed.Model, admitted.allow); code != "" {
		status := http.StatusNotFound
		message := code
		if code == CodeModelDenied {
			status = http.StatusForbidden
		}
		if code == CodeNotChat {
			status = http.StatusBadRequest
			message = "模型不走对话接口"
		}
		if code == CodeHidden {
			message = "模型已隐藏"
		}
		writeProtocolError(w, proto, status, code, message)
		return status
	}
	if code, status := s.spendBlock(r.Context(), admitted, snap.Config); code != "" {
		writeProtocolError(w, proto, status, code, code)
		return status
	}
	loginAccount, loginModel, explicitLogin := login.ParseID(parsed.Model)
	loginSnap := login.Snapshot{Status: "off"}
	if explicitLogin || s.Login != nil {
		loginSnap = login.Discover(s.loginMachine())
	}
	var route routePick
	var stateRoute state.Route
	var seats []seatCandidate
	var plannedGroup *groupPlan
	if explicitLogin {
		meta.Model = loginModel
		stateRoute, _ = snap.Config.RouteByModel(parsed.Model)
		route = routePick{id: stateRoute.ID, found: stateRoute.ID != "", allows: func(class string) bool {
			return stateRoute.Allows(state.ErrorClass(class))
		}}
		rejectLogin := func(outcome, code string, status int) int {
			s.note(snap, meta, usageNote{
				RouteID: requestID, ProviderID: "login/" + loginAccount, ProviderAccount: loginAccount,
				Outcome: outcome, Status: status, Started: requestStart, Rejected: true, Err: code,
			})
			writeProtocolError(w, proto, status, code, code)
			return status
		}
		if loginSnap.Status == "unknown" {
			return rejectLogin(CodeUnknown, CodeUnknown, http.StatusBadGateway)
		}
		account, found := findLoginAccount(loginSnap, loginAccount)
		if !found {
			return rejectLogin(CodeNoRoute, CodeNoRoute, http.StatusNotFound)
		}
		if !account.Readable || account.Plugin {
			code := CodeUnknown
			if account.Plugin {
				code = "plugin"
			}
			return rejectLogin(code, code, http.StatusBadGateway)
		}
		if !state.AllowsModel(admitted.allow, parsed.Model, loginModel) {
			return rejectLogin(CodeModelDenied, CodeModelDenied, http.StatusForbidden)
		}
		seats = []seatCandidate{loginSeatCandidate(loginAccount, loginModel)}
	} else if group, found := snap.Config.GroupFor(parsed.Model); found {
		plannedGroup = s.planGroup(r.Context(), snap.Config, parsed.Model, group, "", nil)
		route = plannedGroup.route
		seats = plannedGroup.seats
	} else {
		var providerIDs []string
		route, providerIDs = candidates(snap.Config, parsed.Model)
		stateRoute, _ = snap.Config.RouteByModel(parsed.Model)
		noAuto := !route.found && snap.Config.NoAutoGroups && len(providerIDs) > 1
		switch {
		case !route.found && len(providerIDs) > 1 && !snap.Config.NoAutoGroups:
			auto := state.Group{ID: "", Members: autoGroupMembers(providerIDs, parsed.Model)}
			var extra []seatCandidate
			if _, _, catalogID := snap.Config.MatchCatalog(parsed.Model); !catalogID {
				extra = loginSeatCandidates(loginSnap, parsed.Model, "")
			}
			plannedGroup = s.planGroupWithExtra(r.Context(), snap.Config, parsed.Model, auto, "", nil, extra)
			route = plannedGroup.route
			seats = plannedGroup.seats
		case noAuto:
			// The user turned automatic groups off. A bare id shared by
			// several providers is not silently sent to the first one.
		default:
			seats = providerCandidates(snap.Config, parsed.Model, providerIDs)
			if !route.found {
				if _, _, catalogID := snap.Config.MatchCatalog(parsed.Model); !catalogID {
					seats = append(seats, loginSeatCandidates(loginSnap, parsed.Model, "")...)
				}
			}
		}
	}
	if hasMedia(accepted) {
		filtered := filterVisionCandidates(snap.Config, parsed.Model, seats)
		if plannedGroup != nil {
			seats = retainSeatOrder(seats, filtered)
			if !seatPresent(seats, plannedGroup.picked) {
				plannedGroup.picked = firstSeatKey(seats, plannedGroup.blocked)
			}
		} else {
			seats = filtered
		}
		if len(seats) == 0 {
			writeProtocolError(w, proto, http.StatusBadRequest, CodeMedia, "模型不接受图片或视频")
			return http.StatusBadRequest
		}
	}
	routeID := requestID
	if routeID == "" {
		routeID = stateRoute.ID
	}
	picked, pool, strategyID := "", "", ""
	var blocked map[string]bool
	if plannedGroup != nil {
		picked, blocked, pool, strategyID = plannedGroup.picked, plannedGroup.blocked, plannedGroup.pool, plannedGroup.strategyID
	} else {
		seats, picked, blocked, pool, strategyID = s.orderSeats(r.Context(), stateRoute, parsed.Model, seats)
	}
	stickyID := ""
	if !ancillary {
		stickyID = s.stickyOf(call.Session)
		seats = stickFirstSeat(seats, stickyID, blocked)
	}
	if route.id == "" && len(seats) == 0 && !route.found {
		writeProtocolError(w, proto, http.StatusNotFound, CodeNoRoute, CodeNoRoute)
		return http.StatusNotFound
	}
	if len(seats) == 0 {
		writeProtocolError(w, proto, http.StatusBadGateway, CodeNoProvider, CodeNoProvider)
		return http.StatusBadGateway
	}
	type attemptResult struct {
		providerID      string
		providerAccount string
		model           string
		outcome         string
		status          int
		started         time.Time
		usage           json.RawMessage
		providerKeyID   string
		errText         string
		errType         string
		rejected        bool
		firstByte       time.Time
		firstText       time.Time
		bytesIn         int64
		bytesOut        int64
		canceled        bool
	}
	modelOf := func(item attemptResult) string {
		if item.model != "" {
			return item.model
		}
		return upstreamModel(snap.Config, parsed.Model, item.providerID)
	}
	var last attemptResult
	attempts := 0
	firstStart := time.Time{}
	var lastEnd time.Time
	tries := func() int {
		if attempts > 1 {
			return attempts
		}
		return 0
	}
	queueMS := func() int {
		if firstStart.IsZero() {
			return 0
		}
		return millis(firstStart.Sub(requestStart))
	}
	retryMS := func(item attemptResult) int {
		if lastEnd.IsZero() || item.started.IsZero() {
			return 0
		}
		return millis(item.started.Sub(lastEnd))
	}
	ttftMS := func(item attemptResult, at time.Time) int {
		if item.started.IsZero() || at.IsZero() {
			return 0
		}
		return millis(at.Sub(item.started))
	}
	writeFinal := func(item attemptResult) {
		s.note(snap, meta, usageNote{
			Model:   modelOf(item),
			RouteID: routeID, Route: route.id, ProviderID: item.providerID,
			ProviderAccount: item.providerAccount,
			Outcome:         item.outcome, Status: item.status, Started: requestStart,
			Usage: item.usage, Tries: tries(), ProviderKeyID: item.providerKeyID,
			Err: item.errText, ErrType: item.errType, Rejected: item.rejected,
			TTFTMS: ttftMS(item, item.firstByte), FirstTextMS: ttftMS(item, item.firstText),
			QueueMS: queueMS(), RetryMS: retryMS(item),
			BytesIn: item.bytesIn, BytesOut: item.bytesOut,
		})
	}
	writeCanceled := func(item attemptResult) int {
		s.note(snap, meta, usageNote{
			Model:   modelOf(item),
			RouteID: routeID, Route: route.id, ProviderID: item.providerID,
			ProviderAccount: item.providerAccount,
			Outcome:         "canceled", Status: 499, Started: requestStart,
			Usage: item.usage, Tries: tries(), ProviderKeyID: item.providerKeyID,
			Err: state.CanceledText, ErrType: "agent_canceled",
			TTFTMS: ttftMS(item, item.firstByte), FirstTextMS: ttftMS(item, item.firstText),
			QueueMS: queueMS(), RetryMS: retryMS(item),
			BytesIn: item.bytesIn, BytesOut: item.bytesOut,
		})
		return 499
	}
	finishFailure := func(item attemptResult, class string) int {
		if class == "" {
			class = CodeUnknown
		}
		if item.outcome == "" {
			item.outcome = class
		}
		if item.status == 0 {
			item.status = statusFor(class)
		}
		if item.errText == "" {
			item.errText = class
		}
		writeFinal(item)
		writeProtocolError(w, proto, item.status, class, class)
		return item.status
	}
	for i := 0; i < len(seats); i++ {
		if r.Context().Err() != nil {
			return writeCanceled(attemptResult{providerID: last.providerID, providerAccount: last.providerAccount, model: last.model, started: requestStart})
		}
		chosen, release, waited, ok := s.acquireSeat(r.Context(), seats, i, blocked)
		if !ok {
			if r.Context().Err() != nil {
				return writeCanceled(attemptResult{providerID: last.providerID, providerAccount: last.providerAccount, model: last.model, started: requestStart})
			}
			errText := "queue_full"
			if waited > 0 {
				errText = "queue_wait"
			}
			seat := seats[i]
			return finishFailure(attemptResult{
				providerID: seat.ProviderID, providerAccount: seat.Account, model: seat.Model,
				outcome: string(state.ClassRateLimit), status: http.StatusTooManyRequests,
				started: time.Now().UTC(), errText: errText, rejected: true,
			}, string(state.ClassRateLimit))
		}
		i = chosen
		seat := seats[chosen]
		status, stop := func() (int, bool) {
			if release != nil {
				defer release()
			}
			attempts++
			started := time.Now().UTC()
			if firstStart.IsZero() {
				firstStart = started
			}
			providerID := seat.ProviderID
			dest := seat.Protocol
			providerKeyID := ""
			prov := state.Provider{}
			if seat.Login {
				if seat.Account == "" {
					return finishFailure(attemptResult{providerID: providerID, providerAccount: seat.Account, outcome: CodeUnknown, started: started, rejected: true}, CodeUnknown), true
				}
			} else {
				var pok bool
				prov, pok = snap.Config.Provider(providerID)
				if !pok {
					return finishFailure(attemptResult{providerID: providerID, providerAccount: seat.Account, outcome: CodeUnknown, started: started, rejected: true}, CodeUnknown), true
				}
				var known bool
				dest, known = providerProtocol(prov.Protocol, proto)
				keys := prov.Keys()
				if !known || len(keys) == 0 || seat.KeyIndex < 0 || seat.KeyIndex >= len(keys) {
					return finishFailure(attemptResult{providerID: providerID, providerAccount: seat.Account, outcome: CodeUnknown, started: started, rejected: true}, CodeUnknown), true
				}
				switch security.CheckBaseURL(prov.BaseURL, s.consoleAddr(), "") {
				case security.BaseOK:
				case security.BaseUnknown:
					return finishFailure(attemptResult{providerID: providerID, providerAccount: seat.Account, outcome: CodeUnknown, started: started, rejected: true}, CodeUnknown), true
				default:
					return finishFailure(attemptResult{providerID: providerID, providerAccount: seat.Account, outcome: CodeBaseURL, started: started, rejected: true}, CodeBaseURL), true
				}
				providerKeyID = state.KeyFingerprint(keys[seat.KeyIndex])
			}
			upModel := seat.Model
			outbound, err := outboundBody(parsed, accepted, dest, upModel)
			if err != nil {
				last = attemptResult{providerID: providerID, providerAccount: seat.Account, model: upModel, outcome: "unsupported", status: http.StatusBadGateway, started: started, errText: "unsupported", rejected: true}
				return 0, false
			}
			if !ancillary && !seat.Login {
				outbound = s.rewriteTry(r.Context(), outbound, call, prov.ID, dest)
			}
			sent := outboundModel(dest, outbound, upModel)
			up, err := s.postSeat(r.Context(), seat, dest, upModel, parsed.Stream, outbound)
			if err != nil && r.Context().Err() != nil {
				return writeCanceled(attemptResult{providerID: providerID, providerAccount: seat.Account, model: sent, providerKeyID: providerKeyID, started: started}), true
			}
			if err != nil {
				if errors.Is(err, login.ErrPlugin) {
					last = attemptResult{providerID: providerID, providerAccount: seat.Account, model: sent, outcome: "plugin", status: http.StatusBadGateway, started: started, errText: "plugin", rejected: true}
					return finishFailure(last, "plugin"), true
				}
				class := string(state.ClassUpstreamUnavailable)
				last = attemptResult{providerID: providerID, providerAccount: seat.Account, model: sent, outcome: class, started: started, providerKeyID: providerKeyID, errText: class, bytesIn: int64(len(outbound))}
				s.markFailure(pool, seat.Key, class)
				if !s.fallThrough(route, seats, chosen, class) {
					return finishFailure(last, class), true
				}
				lastEnd = time.Now().UTC()
				return 0, false
			}
			outcome := classifyStatus(up.StatusCode)
			if outcome != "" || up.StatusCode < 200 || up.StatusCode >= 300 {
				limited, _ := readLimited(up.Body, captureLimit)
				up.Body.Close()
				usage := usageObject(dest, limited)
				class := outcome
				if class == "" {
					last = attemptResult{providerID: providerID, providerAccount: seat.Account, model: sent, outcome: "http_" + itoa(up.StatusCode), status: up.StatusCode, started: started, usage: usage, providerKeyID: providerKeyID, bytesIn: int64(len(outbound)), bytesOut: int64(len(limited))}
					writeFinal(last)
					writeUpstreamOrError(w, proto, up.StatusCode, limited)
					return up.StatusCode, true
				}
				last = attemptResult{providerID: providerID, providerAccount: seat.Account, model: sent, outcome: class, status: up.StatusCode, started: started, usage: usage, providerKeyID: providerKeyID, bytesIn: int64(len(outbound)), bytesOut: int64(len(limited))}
				s.markFailure(pool, seat.Key, class)
				if !s.fallThrough(route, seats, chosen, class) {
					return finishFailure(last, class), true
				}
				retry := time.Now().UTC()
				if chosen < len(seats)-1 && usageTokens(usage) > 0 {
					s.note(snap, meta, usageNote{
						Model:   sent,
						RouteID: routeID, Route: route.id, ProviderID: providerID, ProviderAccount: seat.Account,
						Outcome: class, Status: up.StatusCode, Started: started,
						Usage: usage, ProviderKeyID: providerKeyID, HandedOn: true,
						BytesIn: last.bytesIn, BytesOut: last.bytesOut,
					})
				}
				lastEnd = retry
				return 0, false
			}
			s.remember(call.Session, seat.Key)
			asked := parsed
			if seat.Login && seat.Account == "claude" {
				asked.Stream = false
			}
			reply := s.writeReply(w, r, up, proto, dest, asked, call, told, ancillary)
			status, usage, output, complete, firstByte := reply.status, reply.usage, reply.output, reply.complete, reply.firstByte
			outcome = OutcomeSuccess
			if status < 200 || status >= 300 {
				outcome = string(state.ClassUpstreamUnavailable)
			}
			last = attemptResult{
				providerID: providerID, providerAccount: seat.Account, model: sent, outcome: outcome, status: status, started: started,
				usage: usage, providerKeyID: providerKeyID,
				firstByte: firstByte, firstText: reply.firstText,
				bytesIn: int64(len(outbound)), bytesOut: reply.bytesOut,
			}
			if reply.canceled {
				last.outcome = "canceled"
				last.status = 499
				return writeCanceled(last), true
			}
			writeFinal(last)
			if outcome == OutcomeSuccess {
				// A negative ttft means no first byte was measured (whole reply).
				ttft := time.Duration(-1)
				if !firstByte.IsZero() {
					if ttft = firstByte.Sub(started); ttft < 0 {
						ttft = 0
					}
				}
				s.markSuccess(seat, started, usage, ttft)
				s.moveRound(pool, strategyID, seatByKey(seats, picked), seat, stickyID)
			}
			if s.Store != nil && complete && !ancillary && call.Session != "" && status >= 200 && status < 300 {
				_ = s.Store.AppendTurn(state.TurnRecord{
					Session:    call.Session,
					At:         time.Now().UTC(),
					Model:      upModel,
					Caller:     meta.Caller,
					ProviderID: providerID,
					Protocol:   meta.Protocol,
					Input:      lastUserText(parsed.Messages),
					Output:     output,
				})
			}
			return status, true
		}()
		if stop {
			return status
		}
	}
	if last.providerID == "" {
		return finishFailure(attemptResult{outcome: "unsupported", rejected: true}, "unsupported")
	}
	return finishFailure(last, last.outcome)
}

type routePick struct {
	id     string
	allows func(string) bool
	found  bool
}

func candidates(cfg state.Config, model string) (routePick, []string) {
	if route, ok := cfg.RouteByModel(model); ok {
		ids := append([]string(nil), route.ProviderIDs...)
		return routePick{id: route.ID, found: true, allows: func(class string) bool {
			return route.Allows(state.ErrorClass(class))
		}}, ids
	}
	allows := func(class string) bool {
		return class == string(state.ClassUpstreamUnavailable)
	}
	if providerID, name, ok := cfg.MatchCatalog(model); ok {
		if provider, pok := cfg.Provider(providerID); pok && provider.ChatModel(name) {
			return routePick{allows: allows}, []string{providerID}
		}
		return routePick{allows: allows}, nil
	}
	var ids []string
	for _, provider := range cfg.Providers {
		if provider.ChatModel(model) {
			ids = append(ids, provider.ID)
		}
	}
	return routePick{allows: allows}, ids
}

type attemptMeta struct {
	Model          string
	Session        string
	TokenID        string
	CallerKeyID    string
	CallerIssuerID string
	Caller         string
	Agent          string
	Protocol       string
}

type usageNote struct {
	Model           string
	RouteID         string
	Route           string
	ProviderID      string
	ProviderAccount string
	ProviderKeyID   string
	Outcome         string
	Status          int
	Started         time.Time
	Finished        time.Time
	Usage           json.RawMessage
	Tries           int
	HandedOn        bool
	Rejected        bool
	Source          string
	Err             string
	ErrType         string
	Operation       string
	Cost            *float64
	TTFTMS          int
	FirstTextMS     int
	QueueMS         int
	RetryMS         int
	BytesIn         int64
	BytesOut        int64
}

func (s *Server) note(snap *state.Snapshot, meta attemptMeta, n usageNote) {
	if s == nil || s.Store == nil {
		return
	}
	if n.Started.IsZero() {
		n.Started = time.Now().UTC()
	}
	if n.Finished.IsZero() {
		n.Finished = time.Now().UTC()
	}
	callerKeyID := meta.CallerKeyID
	if callerKeyID == "" {
		callerKeyID = meta.TokenID
	}
	agent := meta.Agent
	if agent == "" {
		agent = meta.Caller
	}
	model := n.Model
	if model == "" {
		model = meta.Model
	}
	_ = s.Store.AppendUsage(state.AttemptRecord{
		RouteID:         n.RouteID,
		Route:           n.Route,
		ProviderID:      n.ProviderID,
		ProviderAccount: n.ProviderAccount,
		ProviderKeyID:   n.ProviderKeyID,
		Outcome:         n.Outcome,
		Status:          n.Status,
		Started:         n.Started,
		Finished:        n.Finished,
		Usage:           cloneRaw(n.Usage),
		Model:           model,
		Session:         meta.Session,
		TokenID:         callerKeyID,
		CallerKeyID:     callerKeyID,
		CallerIssuerID:  meta.CallerIssuerID,
		Caller:          meta.Caller,
		Agent:           agent,
		Protocol:        meta.Protocol,
		Operation:       n.Operation,
		Source:          n.Source,
		Err:             n.Err,
		ErrType:         n.ErrType,
		Tries:           n.Tries,
		HandedOn:        n.HandedOn,
		Rejected:        n.Rejected,
		Cost:            n.Cost,
		TTFTMS:          n.TTFTMS,
		FirstTextMS:     n.FirstTextMS,
		QueueMS:         n.QueueMS,
		RetryMS:         n.RetryMS,
		BytesIn:         n.BytesIn,
		BytesOut:        n.BytesOut,
	})
}

// millis converts a non-negative duration to milliseconds. Negative spans and
// sub-millisecond spans are zero.
func millis(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	ms := d / time.Millisecond
	if ms <= 0 {
		return 0
	}
	if ms > 1<<31-1 {
		return 1<<31 - 1
	}
	return int(ms)
}

func usageTokens(raw json.RawMessage) int {
	counts, ok := state.CountsFromUsage(raw)
	if !ok {
		return 0
	}
	return counts.Total()
}

func classifyStatus(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return string(state.ClassAuthFailure)
	case http.StatusTooManyRequests:
		return string(state.ClassRateLimit)
	case http.StatusPaymentRequired:
		return string(state.ClassEmptyBalance)
	}
	if status == http.StatusRequestTimeout || status >= 500 {
		return string(state.ClassUpstreamUnavailable)
	}
	return ""
}

// outboundModel is the model the provider receives. Chat, Responses,
// Anthropic, and Ollama carry it in the body, including a rewrite made by
// OnTry. Gemini carries it in the URL, so that protocol keeps the endpoint
// model.
func outboundModel(proto Protocol, body []byte, fallback string) string {
	if proto == ProtoGemini {
		return fallback
	}
	var doc struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &doc) == nil && doc.Model != "" {
		return doc.Model
	}
	return fallback
}

func (s *Server) postProvider(ctx context.Context, prov state.Provider, proto Protocol, model string, stream bool, body []byte) (*http.Response, error) {
	resp, _, err := s.postProviderKey(ctx, prov, proto, model, stream, body)
	return resp, err
}

// postSeat dials one strategy seat: one provider key, or one subscription
// account. A provider key is never swapped inside this call; the next seat is
// the next key the strategy ordered.
func (s *Server) postSeat(ctx context.Context, seat seatCandidate, proto Protocol, model string, stream bool, body []byte) (*http.Response, error) {
	if seat.Login {
		if seat.Account == "claude" {
			stream = false
		}
		return login.Dial(ctx, s.loginMachine(), seat.Account, model, body, stream)
	}
	keys := seat.Provider.Keys()
	if seat.KeyIndex < 0 || seat.KeyIndex >= len(keys) {
		return nil, errBadBody
	}
	return s.postKey(ctx, seat.Provider, proto, model, stream, body, keys[seat.KeyIndex])
}

// fallThrough reports whether a failed seat may be followed by another one.
// A sibling seat of the same provider or WorkBuddy family is always tried;
// crossing to another vendor follows a common group's eligible list, or the
// route's eligible list when no group covers both.
func (s *Server) fallThrough(route routePick, seats []seatCandidate, at int, class string) bool {
	if at+1 >= len(seats) {
		return false
	}
	next := seats[at+1]
	if sameVendor(seats[at], next) {
		return true
	}
	if scopesAllow(seats[at], next, class) {
		return true
	}
	return route.allows != nil && route.allows(class)
}

// postProviderKey sends the request and names the upstream key fingerprint
// that produced the returned response. The key itself is not returned.
func (s *Server) postProviderKey(ctx context.Context, prov state.Provider, proto Protocol, model string, stream bool, body []byte) (*http.Response, string, error) {
	keys := prov.Keys()
	if len(keys) == 0 {
		keys = []string{""}
	}
	var last *http.Response
	lastKey := ""
	for i, key := range keys {
		resp, err := s.postKey(ctx, prov, proto, model, stream, body, key)
		if err != nil {
			if last != nil {
				last.Body.Close()
			}
			return nil, state.KeyFingerprint(key), err
		}
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
			if last != nil {
				last.Body.Close()
			}
			return resp, state.KeyFingerprint(key), nil
		}
		if last != nil {
			last.Body.Close()
		}
		last = resp
		lastKey = state.KeyFingerprint(key)
		if i == len(keys)-1 {
			return resp, lastKey, nil
		}
	}
	return last, lastKey, nil
}

func (s *Server) postKey(ctx context.Context, prov state.Provider, proto Protocol, model string, stream bool, body []byte, key string) (*http.Response, error) {
	target, err := endpoint(prov.BaseURL, proto, model, stream)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	applyProviderAuth(req, proto, key)
	return s.httpClient(prov.BaseURL).Do(req)
}

func (s *Server) httpClient(base string) *http.Client {
	transport := http.DefaultTransport
	if s != nil && s.Transport != nil {
		transport = s.Transport
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || !security.AllowRedirect(base, req.URL.String()) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// firstWriteWriter records when the first body byte went to the client and
// how many body bytes the gateway sent. Header bytes are not counted.
type firstWriteWriter struct {
	http.ResponseWriter
	first *time.Time
	bytes int64
}

func (w *firstWriteWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		w.bytes += int64(n)
		if w.first.IsZero() {
			*w.first = time.Now()
		}
	}
	return n, err
}

// Flush keeps the wrapped writer's streaming capability.
func (w *firstWriteWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// replyStats is one provider reply as it went to the client. firstByte and
// firstText are zero for a whole JSON reply.
type replyStats struct {
	status    int
	usage     json.RawMessage
	output    string
	complete  bool
	firstByte time.Time
	firstText time.Time
	bytesOut  int64
	canceled  bool
}

func (s *Server) writeReply(w http.ResponseWriter, r *http.Request, up *http.Response, inbound, dest Protocol, parsed Parsed, call CallContext, told []RequestHook, ancillary bool) replyStats {
	defer up.Body.Close()
	stream := parsed.Stream || strings.Contains(strings.ToLower(up.Header.Get("Content-Type")), "text/event-stream")
	if stream {
		return s.writeStream(w, r.Context(), up, inbound, dest, parsed.Model, call, told, ancillary)
	}
	body, err := readLimited(up.Body, bodyLimit)
	if err != nil {
		writeProtocolError(w, inbound, http.StatusBadGateway, string(state.ClassUpstreamUnavailable), string(state.ClassUpstreamUnavailable))
		s.observe(told, call, nil, false)
		return replyStats{status: http.StatusBadGateway, canceled: r.Context().Err() != nil}
	}
	status := up.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	out := body
	if dest != inbound {
		converted, cerr := aiconv.ConvertReply(aiconv.Protocol(dest), aiconv.Protocol(inbound), body, &aiconv.Options{Model: parsed.Model})
		if cerr != nil {
			writeProtocolError(w, inbound, http.StatusBadGateway, string(state.ClassUpstreamUnavailable), cerr.Error())
			s.observe(told, call, nil, false)
			return replyStats{status: http.StatusBadGateway, usage: usageObject(dest, body)}
		}
		out = converted
	}
	if !ancillary {
		status, out = s.rewriteReply(r.Context(), status, out, call)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(out)
	complete := status >= 200 && status < 300 && len(out) <= captureLimit
	s.observe(told, call, out, complete)
	output := ""
	if complete {
		output = replyText(inbound, out)
	}
	return replyStats{
		status: status, usage: usageObject(dest, body), output: output,
		complete: complete, bytesOut: int64(len(out)),
		canceled: !complete && r.Context().Err() != nil,
	}
}

func (s *Server) writeStream(w http.ResponseWriter, ctx context.Context, up *http.Response, inbound, dest Protocol, model string, call CallContext, told []RequestHook, ancillary bool) replyStats {
	var first time.Time
	ww := &firstWriteWriter{ResponseWriter: w, first: &first}
	w = ww
	if dest != inbound && inbound == ProtoOllama {
		w.Header().Set("Content-Type", "application/x-ndjson")
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	var captured bytes.Buffer
	success := true
	usage := usageScan{proto: dest}
	editors := s.editors()
	if dest == inbound && len(editors) == 0 {
		truncated, err := copyFlush(io.MultiWriter(&usage, w), up.Body, &captured, flush)
		usage.finish()
		if err != nil && !errors.Is(err, io.EOF) {
			success = false
		}
		complete := success && !truncated && err == nil
		output := ""
		if complete {
			output = joinedStreamText(dest, captured.Bytes())
		}
		s.observe(told, call, captureBytes(captured.Bytes(), success), complete)
		stats := replyStats{
			status: http.StatusOK, usage: usage.object(), output: output, complete: complete,
			firstByte: first, bytesOut: ww.bytes,
		}
		if ancillary {
			stats.output = ""
			stats.complete = false
			return stats
		}
		if output != "" {
			stats.firstText = first
		}
		stats.canceled = !complete && ctx.Err() != nil
		return stats
	}
	if dest != inbound {
		status, usageOut, output, complete := s.convertStream(w, up, inbound, dest, model, call, told, ancillary, flush)
		stats := replyStats{
			status: status, usage: usageOut, output: output, complete: complete,
			firstByte: first, bytesOut: ww.bytes,
		}
		if output != "" {
			stats.firstText = first
		}
		stats.canceled = !complete && ctx.Err() != nil
		return stats
	}
	var text strings.Builder
	var firstText time.Time
	err := readSSE(up.Body, func(data []byte) error {
		usage.see(data)
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return writeSSE(w, clientEvent(inbound, model, text.String(), true), flush)
		}
		out := data
		drop := false
		for _, editor := range editors {
			next, omit := editor.EditEvent(out)
			if omit {
				drop = true
				break
			}
			if next != nil {
				out = next
			}
		}
		if drop {
			return nil
		}
		delta := deltaText(inbound, out)
		if delta != "" && firstText.IsZero() {
			firstText = time.Now()
		}
		text.WriteString(delta)
		return writeSSE(w, out, flush)
	})
	if err != nil && !errors.Is(err, io.EOF) {
		success = false
	}
	complete := success && text.Len() <= captureLimit
	output := ""
	if complete && !ancillary {
		output = text.String()
	}
	stats := replyStats{
		status: http.StatusOK, usage: usage.object(), output: output, complete: complete,
		firstByte: first, firstText: firstText, bytesOut: ww.bytes,
		canceled: !complete && ctx.Err() != nil,
	}
	if ancillary {
		stats.output = ""
		stats.complete = false
		return stats
	}
	s.observe(told, call, nil, false)
	return stats
}

func (s *Server) convertStream(w http.ResponseWriter, up *http.Response, inbound, dest Protocol, model string, call CallContext, told []RequestHook, ancillary bool, flush func()) (int, json.RawMessage, string, bool) {
	pr, pw := io.Pipe()
	usage := usageScan{proto: dest}
	var captured capWriter
	captured.limit = captureLimit
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := io.Copy(io.MultiWriter(&usage, &captured, pw), up.Body)
		usage.finish()
		_ = pw.CloseWithError(err)
	}()
	streamErr := aiconv.ConvertStream(aiconv.Protocol(dest), aiconv.Protocol(inbound), pr, flushWriter{w: w, flush: flush}, &aiconv.Options{Model: model})
	_ = pr.Close()
	<-done
	if ancillary {
		return http.StatusOK, usage.object(), "", false
	}
	if streamErr != nil || captured.trunc {
		s.observe(told, call, nil, false)
		return http.StatusOK, usage.object(), "", false
	}
	output := spokenText(dest, captured.buf.Bytes())
	observed, err := aiconv.ConvertReply(aiconv.Protocol(dest), aiconv.Protocol(inbound), captured.buf.Bytes(), &aiconv.Options{Model: model})
	if err != nil || len(observed) > captureLimit {
		observed = nil
	}
	s.observe(told, call, observed, observed != nil)
	return http.StatusOK, usage.object(), output, true
}

func (s *Server) editors() []EventEditor {
	var out []EventEditor
	for _, item := range s.Middleware {
		if editor, ok := item.(EventEditor); ok && editor != nil {
			out = append(out, editor)
		}
	}
	return out
}

func (s *Server) runHooks(ctx context.Context, proto Protocol, r *http.Request, parsed Parsed, body []byte, call CallContext) ([]byte, []RequestHook, error) {
	var told []RequestHook
	for _, hook := range s.Hooks {
		if hook == nil {
			continue
		}
		next, err := callWithTimeout(ctx, hookTimeout, func(ctx context.Context) (json.RawMessage, error) {
			return hook.OnRequest(ctx, json.RawMessage(body), call)
		})
		if ctx.Err() != nil {
			return body, told, ctx.Err()
		}
		if err != nil {
			continue
		}
		if len(bytes.TrimSpace(next)) == 0 {
			told = append(told, hook)
			continue
		}
		again, perr := Parse(proto, r, next)
		if perr != nil || !sameShape(parsed, again) {
			continue
		}
		body = append([]byte(nil), next...)
		parsed = again
		call.Model = parsed.Model
		call.Stream = parsed.Stream
		told = append(told, hook)
	}
	return body, told, nil
}

func (s *Server) runMiddleware(ctx context.Context, proto Protocol, body []byte, call CallContext, w http.ResponseWriter) ([]byte, int, bool, bool) {
	for _, item := range s.Middleware {
		if item == nil {
			continue
		}
		type decision struct {
			body   json.RawMessage
			reject *Reject
		}
		ch := make(chan decision, 1)
		go func(item Middleware) {
			defer func() {
				if recover() != nil {
					ch <- decision{}
				}
			}()
			mctx, cancel := context.WithTimeout(ctx, middlewareTimeout)
			defer cancel()
			next, reject := item.OnRequest(mctx, json.RawMessage(body), call)
			ch <- decision{body: next, reject: reject}
		}(item)
		select {
		case <-ctx.Done():
			return body, 0, false, true
		case got := <-ch:
			if got.reject != nil {
				status := got.reject.Status
				if status == 0 {
					status = http.StatusBadRequest
				}
				writeProtocolError(w, proto, status, "rejected", got.reject.Message)
				return body, status, true, false
			}
			if json.Valid(got.body) && len(got.body) > 0 && got.body[0] == '{' {
				body = append([]byte(nil), got.body...)
			}
		case <-time.After(middlewareTimeout + 50*time.Millisecond):
		}
	}
	return body, 0, false, false
}

func (s *Server) rewriteTry(ctx context.Context, body []byte, call CallContext, providerID string, proto Protocol) []byte {
	current := append([]byte(nil), body...)
	for _, item := range s.Middleware {
		if item == nil {
			continue
		}
		func() {
			defer func() { _ = recover() }()
			mctx, cancel := context.WithTimeout(ctx, middlewareTimeout)
			defer cancel()
			next := item.OnTry(mctx, json.RawMessage(current), call, providerID, proto)
			if json.Valid(next) && len(next) > 0 && next[0] == '{' {
				current = append([]byte(nil), next...)
			}
		}()
	}
	return current
}

func (s *Server) rewriteReply(ctx context.Context, status int, body []byte, call CallContext) (int, []byte) {
	current := append([]byte(nil), body...)
	for _, item := range s.Middleware {
		if item == nil {
			continue
		}
		func() {
			defer func() { _ = recover() }()
			mctx, cancel := context.WithTimeout(ctx, middlewareTimeout)
			defer cancel()
			nextStatus, next := item.OnReply(mctx, status, json.RawMessage(current), call)
			if json.Valid(next) && len(next) > 0 && (next[0] == '{' || next[0] == '[') {
				current = append([]byte(nil), next...)
				if nextStatus != 0 {
					status = nextStatus
				}
			}
		}()
	}
	return status, current
}

func (s *Server) observe(hooks []RequestHook, call CallContext, reply []byte, success bool) {
	if len(hooks) == 0 {
		return
	}
	saved := append([]byte(nil), reply...)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
		defer cancel()
		for _, hook := range hooks {
			if hook == nil {
				continue
			}
			func() {
				defer func() { _ = recover() }()
				hook.OnResponse(ctx, json.RawMessage(saved), call, success)
			}()
		}
	}()
}

func (s *Server) stickyOf(session string) string {
	if session == "" {
		return ""
	}
	s.stickyMu.Lock()
	id := s.sticky[session]
	s.stickyMu.Unlock()
	return id
}

// stickFirst moves the session's provider to the front when it can still
// answer. A blocked seat (resting or spent) is not stuck to.
func stickFirst(ids []string, stickyID string, blocked map[string]bool) []string {
	if stickyID == "" || len(ids) < 2 || blocked[stickyID] {
		return ids
	}
	out := make([]string, 0, len(ids))
	found := false
	for _, candidate := range ids {
		if candidate == stickyID {
			found = true
			continue
		}
		out = append(out, candidate)
	}
	if !found {
		return ids
	}
	return append([]string{stickyID}, out...)
}

// stickFirstSeat moves the session's seat to the front when it is still a
// candidate and can answer. The seat key is per key or subscription account,
// so two keys of one provider are two different sticky targets.
func stickFirstSeat(seats []seatCandidate, stickyKey string, blocked map[string]bool) []seatCandidate {
	if stickyKey == "" || len(seats) < 2 || blocked[stickyKey] {
		return seats
	}
	out := make([]seatCandidate, 0, len(seats))
	found := false
	for _, candidate := range seats {
		if candidate.Key == stickyKey {
			found = true
			continue
		}
		out = append(out, candidate)
	}
	if !found {
		return seats
	}
	for _, candidate := range seats {
		if candidate.Key == stickyKey {
			return append([]seatCandidate{candidate}, out...)
		}
	}
	return seats
}

func (s *Server) remember(session, providerID string) {
	if session == "" || providerID == "" {
		return
	}
	s.stickyMu.Lock()
	defer s.stickyMu.Unlock()
	if s.sticky == nil {
		s.sticky = map[string]string{}
	}
	s.sticky[session] = providerID
}

func callContext(r *http.Request, parsed Parsed) CallContext {
	return CallContext{
		Protocol: parsed.Protocol,
		Model:    parsed.Model,
		Stream:   parsed.Stream,
		Path:     r.URL.Path,
		Origin:   requestOrigin(r),
		Agent:    strings.TrimSpace(r.Header.Get("x-aigate-agent")),
		Session:  sessionHeader(r),
		Kind:     kindHeader(r),
	}
}

func sessionHeader(r *http.Request) string {
	for _, name := range []string{"x-session-id", "x-claude-code-session-id", "x-conversation-id", "conversation-id"} {
		if v := strings.TrimSpace(r.Header.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

func kindHeader(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("x-aigate-kind")); v != "" {
		return v
	}
	if strings.TrimSpace(r.Header.Get("x-claude-code-agent-id")) != "" {
		return "subagent"
	}
	if strings.Contains(strings.ToLower(r.Header.Get("x-openai-subagent")), "collab_spawn") {
		return "subagent"
	}
	return ""
}

func requestOrigin(r *http.Request) string {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" {
		return "remote"
	}
	host := r.RemoteAddr
	if split, _, err := net.SplitHostPort(host); err == nil {
		host = split
	}
	if security.IsLoopbackHost(host) {
		return "local"
	}
	return "remote"
}

func callWithTimeout(parent context.Context, timeout time.Duration, fn func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	type result struct {
		body json.RawMessage
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if recover() != nil {
				ch <- result{err: errHook}
			}
		}()
		body, err := fn(ctx)
		ch <- result{body: body, err: err}
	}()
	select {
	case got := <-ch:
		if errors.Is(got.err, context.DeadlineExceeded) && parent.Err() == nil {
			return nil, errHook
		}
		return got.body, got.err
	case <-ctx.Done():
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		return nil, errHook
	}
}

func readBody(src io.Reader) ([]byte, bool) {
	if src == nil {
		return nil, false
	}
	b, err := readLimited(src, bodyLimit+1)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, false
	}
	if len(b) > bodyLimit {
		return nil, true
	}
	return b, false
}

func readLimited(src io.Reader, limit int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(src, int64(limit)+1))
	if len(b) > limit {
		return b[:limit], io.ErrUnexpectedEOF
	}
	if err != nil {
		return b, err
	}
	return b, nil
}

// usageObject is the provider's token-count object on a whole JSON reply.
// A missing object is nil. It is not stored as zero.
func usageObject(proto Protocol, body []byte) json.RawMessage {
	return eventUsage(proto, body)
}

// eventUsage reads one provider event. Chat and responses use usage.
// A responses event nests it under response. Gemini names it usageMetadata.
// Anthropic puts input counts on message.usage and output counts on usage;
// both are returned so the caller can lay the later object over the earlier one.
func eventUsage(proto Protocol, data []byte) json.RawMessage {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	switch proto {
	case ProtoGemini:
		if u := objectField(doc["usageMetadata"]); u != nil {
			return u
		}
		return objectField(doc["usage"])
	case ProtoResponses:
		if u := nestedUsage(doc["response"]); u != nil {
			return u
		}
		return objectField(doc["usage"])
	case ProtoAnthropic:
		u := nestedUsage(doc["message"])
		if top := objectField(doc["usage"]); top != nil {
			u = mergeUsage(u, top)
		}
		return u
	default:
		return objectField(doc["usage"])
	}
}

func nestedUsage(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	return objectField(doc["usage"])
}

func objectField(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || raw[0] != '{' {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func mergeUsage(base, next json.RawMessage) json.RawMessage {
	if len(next) == 0 {
		return base
	}
	if len(base) == 0 {
		return append(json.RawMessage(nil), next...)
	}
	var a, b map[string]json.RawMessage
	if json.Unmarshal(base, &a) != nil || json.Unmarshal(next, &b) != nil {
		return append(json.RawMessage(nil), next...)
	}
	for k, v := range b {
		a[k] = append(json.RawMessage(nil), v...)
	}
	out, err := json.Marshal(a)
	if err != nil {
		return append(json.RawMessage(nil), next...)
	}
	return out
}

// usageScan keeps the provider usage seen on a stream.
// Chat, responses, and Gemini keep the last object. Anthropic lays each
// later object over the earlier one, because input and output arrive apart.
type usageScan struct {
	proto Protocol
	raw   json.RawMessage
	data  bytes.Buffer
	rest  []byte
}

func (s *usageScan) Write(p []byte) (int, error) {
	s.feed(p, false)
	return len(p), nil
}

func (s *usageScan) finish() {
	s.feed(nil, true)
}

func (s *usageScan) feed(p []byte, eof bool) {
	if len(p) > 0 {
		s.rest = append(s.rest, p...)
	}
	for {
		i := bytes.IndexByte(s.rest, '\n')
		if i < 0 {
			break
		}
		line := s.rest[:i]
		s.rest = s.rest[i+1:]
		s.line(line)
	}
	if !eof {
		return
	}
	if len(s.rest) > 0 {
		s.line(s.rest)
		s.rest = nil
	}
	s.flushEvent()
}

func (s *usageScan) line(line []byte) {
	line = bytes.TrimRight(line, "\r")
	if len(line) == 0 {
		s.flushEvent()
		return
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	piece := bytes.TrimPrefix(line, []byte("data:"))
	piece = bytes.TrimPrefix(piece, []byte(" "))
	if s.data.Len() > 0 {
		s.data.WriteByte('\n')
	}
	_, _ = s.data.Write(piece)
}

func (s *usageScan) flushEvent() {
	if s.data.Len() == 0 {
		return
	}
	s.see(s.data.Bytes())
	s.data.Reset()
}

func (s *usageScan) see(event []byte) {
	if !bytes.Contains(event, []byte("usage")) {
		return
	}
	next := eventUsage(s.proto, event)
	if next == nil {
		return
	}
	if s.proto == ProtoAnthropic {
		s.raw = mergeUsage(s.raw, next)
		return
	}
	s.raw = next
}

func (s *usageScan) object() json.RawMessage {
	if len(s.raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), s.raw...)
}

func writeUpstreamOrError(w http.ResponseWriter, proto Protocol, status int, body []byte) {
	if json.Valid(body) && len(bytes.TrimSpace(body)) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	writeProtocolError(w, proto, status, "upstream", "upstream")
}

func copyFlush(dst io.Writer, src io.Reader, captured *bytes.Buffer, flush func()) (bool, error) {
	buf := make([]byte, 32*1024)
	truncated := false
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return truncated, werr
			}
			if captured != nil && captured.Len() < captureLimit {
				remain := captureLimit - captured.Len()
				if n > remain {
					truncated = true
					n = remain
				}
				_, _ = captured.Write(buf[:n])
			} else if captured != nil {
				truncated = true
			}
			flush()
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return truncated, nil
			}
			return truncated, err
		}
	}
}

func joinedStreamText(proto Protocol, raw []byte) string {
	var b strings.Builder
	_ = readSSE(bytes.NewReader(raw), func(data []byte) error {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return nil
		}
		b.WriteString(deltaText(proto, data))
		return nil
	})
	return b.String()
}

func lastUserText(messages []turn) string {
	text := ""
	for _, msg := range messages {
		if msg.Role == "user" && msg.Text != "" {
			text = msg.Text
		}
	}
	return text
}

type flushWriter struct {
	w     io.Writer
	flush func()
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if n > 0 && f.flush != nil {
		f.flush()
	}
	return n, err
}

type capWriter struct {
	buf   bytes.Buffer
	limit int
	trunc bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	if c.buf.Len() >= c.limit {
		c.trunc = true
		return len(p), nil
	}
	remain := c.limit - c.buf.Len()
	if len(p) > remain {
		c.trunc = true
		_, _ = c.buf.Write(p[:remain])
		return len(p), nil
	}
	_, _ = c.buf.Write(p)
	return len(p), nil
}

func spokenText(proto Protocol, raw []byte) string {
	if text := joinedStreamText(proto, raw); text != "" {
		return text
	}
	reply, err := aiconv.DecodeReply(aiconv.Protocol(proto), raw)
	if err != nil {
		return replyText(proto, raw)
	}
	var b strings.Builder
	for _, part := range reply.Parts {
		if part.Kind == aiconv.Text {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func readSSE(src io.Reader, fn func([]byte) error) error {
	var data bytes.Buffer
	rest := make([]byte, 0)
	buf := make([]byte, 4096)
	flushEvent := func() error {
		if data.Len() == 0 {
			return nil
		}
		err := fn(data.Bytes())
		data.Reset()
		return err
	}
	for {
		n, err := src.Read(buf)
		rest = append(rest, buf[:n]...)
		for {
			i := bytes.IndexByte(rest, '\n')
			if i < 0 {
				break
			}
			line := rest[:i]
			rest = rest[i+1:]
			line = bytes.TrimRight(line, "\r")
			if len(line) == 0 {
				if ferr := flushEvent(); ferr != nil {
					return ferr
				}
				continue
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				piece := bytes.TrimPrefix(line, []byte("data:"))
				piece = bytes.TrimPrefix(piece, []byte(" "))
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.Write(piece)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return flushEvent()
			}
			return err
		}
	}
}

func writeSSE(w io.Writer, data []byte, flush func()) error {
	if len(data) == 0 {
		return nil
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return err
	}
	flush()
	return nil
}

func captureBytes(b []byte, success bool) []byte {
	if !success {
		return nil
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
