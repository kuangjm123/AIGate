package gateway

import (
	"strings"

	"github.com/kuangjm123/AIGate/internal/state"
)

// gateModel reports a refusal code, or empty when the name may be attempted.
func gateModel(cfg state.Config, model string, allow []string) string {
	if _, ok := cfg.RouteByModel(model); ok {
		if !state.AllowsModel(allow, model, "") {
			return CodeModelDenied
		}
		return ""
	}
	if providerID, name, ok := cfg.MatchCatalog(model); ok {
		provider, pok := cfg.Provider(providerID)
		if !pok || !provider.Lists(name) {
			return ""
		}
		if info, found := provider.InfoFor(name); found {
			if info.Hidden {
				return CodeHidden
			}
			if info.Kind == "image" || info.Kind == "video" {
				return CodeNotChat
			}
		}
		if !state.AllowsModel(allow, model, name) {
			return CodeModelDenied
		}
		return ""
	}
	listed, blocked := 0, 0
	block := ""
	for _, provider := range cfg.Providers {
		if !provider.Lists(model) {
			continue
		}
		listed++
		info, found := provider.InfoFor(model)
		if !found {
			continue
		}
		if info.Hidden {
			blocked++
			block = CodeHidden
			continue
		}
		if info.Kind == "image" || info.Kind == "video" {
			blocked++
			if block == "" {
				block = CodeNotChat
			}
		}
	}
	if listed > 0 && blocked == listed {
		return block
	}
	if listed > 0 && !state.AllowsModel(allow, model, model) {
		return CodeModelDenied
	}
	return ""
}

func upstreamModel(cfg state.Config, asked, providerID string) string {
	name, ok := strings.CutPrefix(asked, providerID+"/")
	if !ok || name == "" {
		return asked
	}
	provider, pok := cfg.Provider(providerID)
	if pok && provider.Lists(name) {
		return name
	}
	return asked
}

func withoutBlind(cfg state.Config, asked string, ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if visionOpen(cfg, asked, id) {
			out = append(out, id)
		}
	}
	return out
}

func visionOpen(cfg state.Config, asked, providerID string) bool {
	provider, ok := cfg.Provider(providerID)
	if !ok {
		return true
	}
	name := upstreamModel(cfg, asked, providerID)
	info, found := provider.InfoFor(name)
	if !found || info.Vision == nil {
		return true
	}
	return *info.Vision
}
