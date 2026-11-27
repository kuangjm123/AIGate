package console

import (
	"net/http"
	"path/filepath"
	"strings"

	"github.com/kuangjm123/AIGate/internal/agent"
	"github.com/kuangjm123/AIGate/internal/respond"
	"github.com/kuangjm123/AIGate/internal/state"
)

func agentAction(path string) (string, string, bool) {
	rest, ok := strings.CutPrefix(path, "/agents/")
	if !ok {
		return "", "", false
	}
	id, action, found := strings.Cut(rest, "/")
	if !found || id == "" || action == "" || strings.Contains(action, "/") {
		return "", "", false
	}
	if action != "connect" && action != "disconnect" {
		return "", "", false
	}
	return id, action, true
}

func (s *Server) machine() (agent.Machine, error) {
	if s.Agents != nil {
		return *s.Agents, nil
	}
	dir := ""
	if s.Store != nil {
		dir = filepath.Join(filepath.Dir(s.Store.Paths().ConfigFile), "agents")
	}
	return agent.ThisMachine(dir)
}

func (s *Server) gatewayOrigin() string {
	if s.Host == nil {
		return ""
	}
	st := s.Host.Status()
	if !st.Gateway.Listening || strings.TrimSpace(st.Gateway.Effective) == "" {
		return ""
	}
	return "http://" + st.Gateway.Effective
}

func (s *Server) listAgents(w http.ResponseWriter) int {
	m, err := s.machine()
	if err != nil {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "unknown"})
		return http.StatusOK
	}
	_ = agent.SyncCodex(m)
	snap := s.Store.Current()
	if snap == nil {
		respond.Error(w, http.StatusServiceUnavailable, "config_unreadable")
		return http.StatusServiceUnavailable
	}
	origin := s.gatewayOrigin()
	respond.JSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"gateway": origin,
		"agents":  agent.Inspect(m, gatewayTarget(snap.Config, origin, nil)),
	})
	return http.StatusOK
}

func (s *Server) connectAgent(w http.ResponseWriter, id string) int {
	return s.changeAgent(w, id, true)
}

func (s *Server) disconnectAgent(w http.ResponseWriter, id string) int {
	return s.changeAgent(w, id, false)
}

func (s *Server) changeAgent(w http.ResponseWriter, id string, connect bool) int {
	m, err := s.machine()
	if err != nil {
		return agentError(w, err)
	}
	snap := s.Store.Current()
	if snap == nil {
		respond.Error(w, http.StatusServiceUnavailable, "config_unreadable")
		return http.StatusServiceUnavailable
	}
	gw := gatewayTarget(snap.Config, s.gatewayOrigin(), s.recentModels("codex"))
	if connect {
		err = agent.Connect(m, id, gw, s.offerModels(snap.Config))
	} else {
		err = agent.Disconnect(m, id)
	}
	if err != nil {
		return agentError(w, err)
	}
	for _, st := range agent.Inspect(m, gw) {
		if st.ID == id {
			respond.JSON(w, http.StatusOK, map[string]any{"status": "ok", "agent": st})
			return http.StatusOK
		}
	}
	respond.Error(w, http.StatusNotFound, "not_found")
	return http.StatusNotFound
}

func gatewayTarget(cfg state.Config, origin string, used []string) agent.Target {
	target := agent.Target{Origin: origin, Used: used}
	for _, token := range cfg.GatewayTokens {
		secret := strings.TrimSpace(token.Secret)
		if token.Revoked || secret == "" {
			continue
		}
		target.Keys = append(target.Keys, secret)
		if target.Allowed == nil {
			target.Allowed = map[string][]string{}
		}
		target.Allowed[secret] = append([]string(nil), token.Models...)
	}
	return target
}

func (s *Server) recentModels(agentID string) []string {
	if s.Store == nil {
		return nil
	}
	recs, err := s.Store.ReadUsage(state.UsageFilter{})
	if err != nil {
		return nil
	}
	state.SortUsageNewestFirst(recs)
	seen := map[string]bool{}
	var out []string
	for _, rec := range recs {
		if rec.Model == "" {
			continue
		}
		name := rec.AgentName()
		if agentID == "codex" {
			if name != "codex" && name != callerForModelRanking && name != "magpie-codex" {
				continue
			}
		} else if agentID != "" && name != agentID {
			continue
		}
		if seen[rec.Model] {
			continue
		}
		seen[rec.Model] = true
		out = append(out, rec.Model)
		if len(out) == 100 {
			break
		}
	}
	return out
}

const callerForModelRanking = "aigate-codex"

func agentError(w http.ResponseWriter, err error) int {
	code := agent.CodeOf(err)
	switch code {
	case "not_found":
		respond.Error(w, http.StatusNotFound, "not_found")
		return http.StatusNotFound
	case "no_models", "no_credential", "not_listening", "unknown", "absent", "unsupported", "not_connected", "id_taken":
		respond.Error(w, http.StatusConflict, code)
		return http.StatusConflict
	default:
		respond.Error(w, http.StatusConflict, "unknown")
		return http.StatusConflict
	}
}
