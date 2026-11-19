package state

import "strings"

func cloneInfo(in []ModelInfo) []ModelInfo {
	if in == nil {
		return nil
	}
	out := make([]ModelInfo, len(in))
	for i, info := range in {
		if info.Vision != nil {
			v := *info.Vision
			info.Vision = &v
		}
		out[i] = info
	}
	return out
}

// Lists reports whether name is one of the provider's model ids.
func (p Provider) Lists(name string) bool {
	for _, model := range p.Models {
		if model == name {
			return true
		}
	}
	return false
}

// InfoFor returns the catalog record for id, if one was stored.
func (p Provider) InfoFor(id string) (ModelInfo, bool) {
	for _, info := range p.Info {
		if info.ID == id {
			return info, true
		}
	}
	return ModelInfo{}, false
}

// ChatModel reports whether name may be called on a conversation path.
// A hidden model and an image or video model are not conversation models.
func (p Provider) ChatModel(name string) bool {
	if !p.Lists(name) {
		return false
	}
	info, ok := p.InfoFor(name)
	if !ok {
		return true
	}
	if info.Hidden || info.Kind == "image" || info.Kind == "video" {
		return false
	}
	return true
}

// MatchCatalog reports a provider/model id that names one listed model.
func (c Config) MatchCatalog(asked string) (providerID, name string, ok bool) {
	for _, p := range c.Providers {
		prefix := p.ID + "/"
		if !strings.HasPrefix(asked, prefix) {
			continue
		}
		name = strings.TrimPrefix(asked, prefix)
		if p.Lists(name) {
			return p.ID, name, true
		}
	}
	return "", "", false
}

// TokenByID returns the gateway token with that id.
func (c Config) TokenByID(id string) (GatewayToken, bool) {
	for _, token := range c.GatewayTokens {
		if token.ID == id {
			return token, true
		}
	}
	return GatewayToken{}, false
}

// AllowsModel reports whether a token model list permits asked or its bare name.
// An empty list permits every model.
func AllowsModel(allow []string, asked, bare string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, item := range allow {
		if item == asked || (bare != "" && bare != asked && item == bare) {
			return true
		}
	}
	return false
}
