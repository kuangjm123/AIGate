package gateway

import (
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

func (s *Server) loginEntries(kind string) []catalogEntry {
	if kind != "" && kind != "chat" {
		return nil
	}
	snap := login.Discover(s.loginMachine())
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
			})
		}
	}
	return out
}

func (s *Server) serveLogin(w http.ResponseWriter, r *http.Request, snap *state.Snapshot, proto Protocol, parsed Parsed, raw []byte, call CallContext, told []RequestHook, ancillary bool, meta attemptMeta) int {
	account, model, _ := login.ParseID(parsed.Model)
	discovered := login.Discover(s.loginMachine())
	started := time.Now().UTC()
	if discovered.Status == "unknown" {
		s.note(snap, meta, "", "login/"+account, CodeUnknown, 0, started, nil)
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
		writeProtocolError(w, proto, http.StatusNotFound, CodeNoRoute, CodeNoRoute)
		return http.StatusNotFound
	}
	if !found.Readable || found.Plugin {
		code := CodeUnknown
		if found.Plugin {
			code = "plugin"
		}
		s.note(snap, meta, "", "login/"+account, code, 0, started, nil)
		writeProtocolError(w, proto, http.StatusBadGateway, code, code)
		return http.StatusBadGateway
	}
	dest := loginProtocol(account)
	body, err := outboundBody(parsed, raw, dest, model)
	if err != nil {
		s.note(snap, meta, "", "login/"+account, "unsupported", 0, started, nil)
		writeProtocolError(w, proto, http.StatusBadGateway, "unsupported", "unsupported")
		return http.StatusBadGateway
	}
	up, err := login.Dial(r.Context(), s.loginMachine(), account, model, body, parsed.Stream && account != "claude")
	if errors.Is(err, login.ErrPlugin) {
		s.note(snap, meta, "", "login/"+account, "plugin", 0, started, nil)
		writeProtocolError(w, proto, http.StatusBadGateway, "plugin", "plugin")
		return http.StatusBadGateway
	}
	if err != nil {
		s.note(snap, meta, "", "login/"+account, string(state.ClassUpstreamUnavailable), 0, started, nil)
		writeProtocolError(w, proto, http.StatusBadGateway, string(state.ClassUpstreamUnavailable), string(state.ClassUpstreamUnavailable))
		return http.StatusBadGateway
	}
	asked := parsed
	if account == "claude" {
		asked.Stream = false
	}
	status, usage, output, complete := s.writeReply(w, r, up, proto, dest, asked, call, told, ancillary)
	outcome := OutcomeSuccess
	if status < 200 || status >= 300 {
		outcome = string(state.ClassUpstreamUnavailable)
	}
	s.note(snap, meta, "", "login/"+account, outcome, status, started, usage)
	if s.Store != nil && complete && !ancillary && call.Session != "" && status >= 200 && status < 300 {
		_ = s.Store.AppendTurn(state.TurnRecord{
			Session: call.Session, At: time.Now().UTC(), Model: meta.Model, Caller: meta.Caller,
			ProviderID: "login/" + account, Protocol: meta.Protocol, Input: lastUserText(parsed.Messages), Output: output,
		})
	}
	return status
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
