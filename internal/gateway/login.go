package gateway

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/state"
)

func (s *Server) loginMachine() login.Machine {
	if s == nil || s.Login == nil {
		return login.Machine{Off: true}
	}
	return s.Login()
}

func (s *Server) loginEntries(ctx context.Context, kind string) []catalogEntry {
	if kind != "" && kind != "chat" {
		return nil
	}
	snap := s.loginSnapshot(ctx, loginCatalogWait)
	if snap.Status != "ok" {
		return nil
	}
	var out []catalogEntry
	for _, account := range snap.Accounts {
		if account.Plugin || !account.ModelsKnown {
			continue
		}
		for _, model := range account.Models {
			out = append(out, catalogEntry{
				ID: login.CatalogID(account.ID, model.ID), Name: model.Name, Owner: "login/" + account.ID, Bare: model.ID,
				Context: model.Context, Reasoning: model.Reasoning,
				ReasoningEfforts:      append([]string(nil), model.ReasoningEfforts...),
				ReasoningEffortsKnown: model.ReasoningEffortsKnown,
			})
		}
	}
	return out
}

func (s *Server) serveLogin(w http.ResponseWriter, r *http.Request, snap *state.Snapshot, proto Protocol, parsed Parsed, raw []byte, call CallContext, told []RequestHook, ancillary bool, meta attemptMeta, requestStart time.Time, requestID string) int {
	account, model, _ := login.ParseID(parsed.Model)
	meta.Model = model
	discovered := s.loginSnapshot(r.Context(), loginRequestWait)
	started := requestStart
	if started.IsZero() {
		started = time.Now().UTC()
	}
	attemptStart := time.Now().UTC()
	if requestID == "" {
		requestID = "login"
	}
	fail := func(provider, outcome string, status int, rejected bool) {
		errText := ""
		if status == 0 && !rejected {
			errText = outcome
		}
		s.note(snap, meta, usageNote{
			RouteID: requestID, ProviderID: provider, ProviderAccount: account,
			Outcome: outcome, Status: status, Started: started, Rejected: rejected, Err: errText,
		})
	}
	if discovered.Status == "unknown" {
		fail("login/"+account, CodeUnknown, 0, true)
		writeProtocolError(w, proto, http.StatusBadGateway, CodeUnknown, CodeUnknown)
		return http.StatusBadGateway
	}
	found := login.Account{}
	for _, item := range discovered.Accounts {
		if item.ID == account {
			found = item
			break
		}
	}
	if found.ID == "" {
		fail("login/"+account, CodeNoRoute, http.StatusNotFound, true)
		writeProtocolError(w, proto, http.StatusNotFound, CodeNoRoute, CodeNoRoute)
		return http.StatusNotFound
	}
	if !found.Readable || found.Plugin {
		code := CodeUnknown
		if found.Plugin {
			code = "plugin"
		}
		fail("login/"+account, code, 0, true)
		writeProtocolError(w, proto, http.StatusBadGateway, code, code)
		return http.StatusBadGateway
	}
	dest := loginProtocol(account)
	body, err := outboundBody(parsed, raw, dest, model)
	if err != nil {
		fail("login/"+account, "unsupported", 0, true)
		writeProtocolError(w, proto, http.StatusBadGateway, "unsupported", "unsupported")
		return http.StatusBadGateway
	}
	up, err := login.Dial(r.Context(), s.loginMachine(), account, model, body, parsed.Stream && account != "claude")
	if errors.Is(err, login.ErrPlugin) {
		fail("login/"+account, "plugin", 0, true)
		writeProtocolError(w, proto, http.StatusBadGateway, "plugin", "plugin")
		return http.StatusBadGateway
	}
	if err != nil {
		if r.Context().Err() != nil {
			s.note(snap, meta, usageNote{
				RouteID: requestID, ProviderID: "login/" + account, ProviderAccount: account,
				Outcome: "canceled", Status: 499, Started: started,
				Err: state.CanceledText, ErrType: "agent_canceled",
			})
			return 499
		}
		fail("login/"+account, string(state.ClassUpstreamUnavailable), 0, false)
		writeProtocolError(w, proto, http.StatusBadGateway, string(state.ClassUpstreamUnavailable), string(state.ClassUpstreamUnavailable))
		return http.StatusBadGateway
	}
	asked := parsed
	if account == "claude" {
		asked.Stream = false
	}
	reply := s.writeReply(w, r, up, proto, dest, asked, call, told, ancillary, false)
	status, usage, output, complete := reply.status, reply.usage, reply.output, reply.complete
	outcome := OutcomeSuccess
	if status < 200 || status >= 300 {
		outcome = string(state.ClassUpstreamUnavailable)
	}
	if reply.canceled {
		outcome = "canceled"
		status = 499
	}
	s.note(snap, meta, usageNote{
		RouteID: requestID, ProviderID: "login/" + account, ProviderAccount: account,
		Outcome: outcome, Status: status, Started: started, Usage: usage,
		Err: canceledErr(reply.canceled), ErrType: canceledType(reply.canceled),
		TTFTMS: millis(reply.firstByte.Sub(attemptStart)), FirstTextMS: millis(reply.firstText.Sub(attemptStart)),
		BytesIn: int64(len(body)), BytesOut: reply.bytesOut,
	})
	if s.Store != nil && complete && !ancillary && call.Session != "" && status >= 200 && status < 300 {
		_ = s.Store.AppendTurn(state.TurnRecord{
			Session: call.Session, At: time.Now().UTC(), Model: meta.Model, Caller: meta.Caller,
			ProviderID: "login/" + account, Protocol: meta.Protocol, Input: lastUserText(parsed.Messages), Output: output,
		})
	}
	return status
}

func canceledErr(canceled bool) string {
	if canceled {
		return state.CanceledText
	}
	return ""
}

func canceledType(canceled bool) string {
	if canceled {
		return "agent_canceled"
	}
	return ""
}

func loginProtocol(account string) Protocol {
	switch account {
	case "claude":
		return ProtoAnthropic
	case "codex":
		return ProtoResponses
	case "gemini", "antigravity":
		return ProtoGemini
	default:
		return ProtoChat
	}
}
