package gateway

import (
	"net/http"
	"strings"
	"unicode"

	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

// admission is who may make this gateway call.
// tokenID is set only for an issued gateway token. allow is that token's model list.
// caller is the name stored on the usage line. It is never the bearer secret,
// except the local magpie and magpie-<name> keys, which are application names.
type admission struct {
	caller  string
	tokenID string
	allow   []string
}

func admit(r *http.Request, cfg state.Config) (admission, bool) {
	secret := security.Bearer(r)
	if id, ok := cfg.TokenID(secret); ok {
		token, _ := cfg.TokenByID(id)
		allow := append([]string(nil), token.Models...)
		return admission{caller: id, tokenID: id, allow: allow}, true
	}
	if requestOrigin(r) != "local" || secret == "" {
		return admission{}, false
	}
	if name, ok := magpieCaller(secret); ok {
		return admission{caller: name}, true
	}
	return admission{caller: clientWord(r)}, true
}

func magpieCaller(secret string) (string, bool) {
	if secret == "magpie" {
		return "magpie", true
	}
	name, ok := strings.CutPrefix(secret, "magpie-")
	if !ok || name == "" || len(name) > 64 {
		return "", false
	}
	for i, r := range name {
		if i == 0 && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return "", false
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			continue
		}
		return "", false
	}
	return secret, true
}

func clientWord(r *http.Request) string {
	agent := strings.TrimSpace(r.Header.Get("x-aigate-agent"))
	if agent == "" {
		agent = strings.TrimSpace(r.Header.Get("User-Agent"))
	}
	if agent == "" {
		return "local"
	}
	word := strings.Fields(agent)[0]
	if word == "" {
		return "local"
	}
	return word
}
