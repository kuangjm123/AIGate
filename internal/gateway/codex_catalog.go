package gateway

import (
	"net/http"

	"github.com/kuangjm123/AIGate/internal/codexcat"
	"github.com/kuangjm123/AIGate/internal/state"
)

// writeCodexCatalog answers Codex's model_catalog_url with its own
// models.json shape. A gateway token's model list filters the entries before
// groups gain Codex's group/ prefix.
func (s *Server) writeCodexCatalog(w http.ResponseWriter, cfg state.Config, admitted admission, kind string) {
	if kind == "" {
		kind = "chat"
	}
	var entries []catalogEntry
	if kind == "chat" {
		entries = append(catalog(cfg, kind), s.loginEntries(kind)...)
	}
	models := make([]codexcat.Model, 0, len(entries))
	for _, entry := range entries {
		_, group := cfg.GroupByID(entry.ID)
		allowed := state.AllowsModel(admitted.allow, entry.ID, entry.Bare)
		if group && !allowed {
			allowed = state.AllowsModel(admitted.allow, "group/"+entry.ID, entry.Bare)
		}
		if !allowed {
			continue
		}
		id := entry.ID
		if group && entry.Owner == id && entry.Bare == id {
			id = "group/" + id
		}
		models = append(models, codexcat.Model{
			ID: id, Name: entry.Name, Context: entry.Context,
			Reasoning: entry.Reasoning, ReasoningEfforts: entry.ReasoningEfforts,
			ReasoningEffortsKnown: entry.ReasoningEffortsKnown, Vision: entry.Vision,
		})
	}
	body := codexcat.Catalog(models, "", s.codexUsed(admitted))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// codexUsed is the order Codex's models were most recently called in by this
// caller. A missing or unreadable ledger is no order, not an empty denial.
func (s *Server) codexUsed(admitted admission) []string {
	if s == nil || s.Store == nil {
		return nil
	}
	recs, err := s.Store.ReadUsage(state.UsageFilter{})
	if err != nil {
		return nil
	}
	state.SortUsageNewestFirst(recs)
	seen := map[string]bool{}
	out := make([]string, 0, 100)
	for _, rec := range recs {
		if rec.Model == "" {
			continue
		}
		if admitted.tokenID != "" {
			if rec.CallerKey() != admitted.tokenID {
				continue
			}
		} else if rec.Agent != "codex" && rec.Caller != "aigate-codex" {
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
	if len(out) == 0 {
		return nil
	}
	return out
}
