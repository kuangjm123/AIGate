package console

import "strings"

// IsModelPath reports a model request path. The console rejects these and does not forward them.
func IsModelPath(path string) bool {
	switch path {
	case "/chat/completions", "/responses", "/messages", "/models":
		return true
	}
	if path == "/v1" || strings.HasPrefix(path, "/v1/") {
		return true
	}
	if path == "/v1beta" || strings.HasPrefix(path, "/v1beta/") {
		return true
	}
	if strings.HasPrefix(path, "/models/") {
		return true
	}
	return path == "/api/chat" || path == "/api/tags" || path == "/api/hello"
}

// IsConsolePath reports a control route. The gateway rejects these and does not apply them.
func IsConsolePath(path string) bool {
	switch path {
	case "/", "/config-status", "/register", "/login", "/logout", "/providers", "/routes", "/plugins", "/plugins/order", "/tokens", "/usage", "/listeners", "/conversations", "/conversations/turns", "/agents":
		return true
	}
	if rest, ok := strings.CutPrefix(path, "/agents/"); ok && rest != "" {
		return true
	}
	if rest, ok := strings.CutPrefix(path, "/providers/"); ok && rest != "" {
		return true
	}
	if rest, ok := strings.CutPrefix(path, "/routes/"); ok && rest != "" {
		return true
	}
	if rest, ok := strings.CutPrefix(path, "/plugins/"); ok && rest != "" {
		return true
	}
	if rest, ok := strings.CutPrefix(path, "/tokens/"); ok && rest != "" {
		return true
	}
	if rest, ok := strings.CutPrefix(path, "/assets/"); ok && rest != "" && !strings.Contains(rest, "..") {
		return true
	}
	return false
}
