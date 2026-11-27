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
// except the local magpie, magpie-<name>, aigate, and aigate-<name> keys,
// which are application names.
type admission struct {
	caller      string
	tokenID     string
	issuerID    string
	allow       []string
	spendSet    bool
	spendLimit  float64
	spendPeriod string
	limits      []state.TokenLimit
}

func admit(r *http.Request, cfg state.Config) (admission, bool) {
	secret := callerSecret(r)
	if id, ok := cfg.TokenID(secret); ok {
		token, _ := cfg.TokenByID(id)
		allow := append([]string(nil), token.Models...)
		got := admission{caller: id, tokenID: id, allow: allow}
		if token.SpendLimit != nil && *token.SpendLimit > 0 {
			got.spendSet = true
			got.spendLimit = *token.SpendLimit
			got.spendPeriod = token.SpendPeriod
		}
		got.issuerID = token.IssuerID
		got.limits = append([]state.TokenLimit(nil), token.Limits...)
		return got, true
	}
	if requestOrigin(r) != "local" || secret == "" {
		return admission{}, false
	}
	if name, ok := appCaller(secret); ok {
		return admission{caller: name}, true
	}
	return admission{caller: clientWord(r)}, true
}

// callerSecret is the gateway credential. A bearer token wins. Claude Code's
// ANTHROPIC_AUTH_TOKEN arrives as a bearer. ANTHROPIC_API_KEY arrives as
// x-api-key, and Gemini CLI's GEMINI_API_KEY arrives as x-goog-api-key.
func callerSecret(r *http.Request) string {
	if r == nil {
		return ""
	}
	if secret := security.Bearer(r); secret != "" {
		return secret
	}
	for _, header := range []string{"x-api-key", "x-goog-api-key"} {
		if secret := strings.TrimSpace(r.Header.Get(header)); secret != "" {
			return secret
		}
	}
	return ""
}

func appCaller(secret string) (string, bool) {
	for _, prefix := range []string{"magpie", "aigate"} {
		if secret == prefix {
			return prefix, true
		}
		name, ok := strings.CutPrefix(secret, prefix+"-")
		if !ok || !callerToken(name) {
			continue
		}
		return secret, true
	}
	return "", false
}

func callerToken(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		if i == 0 && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
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
