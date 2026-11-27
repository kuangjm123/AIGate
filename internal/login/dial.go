package login

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
)

// Dial asks the subscription upstream for one catalog model.
// A plugin-owned account is not dialed. Claude is asked by starting Claude Code.
func Dial(ctx context.Context, m Machine, account, model string, body []byte, stream bool) (*http.Response, error) {
	if m.plugin(account) {
		return nil, ErrPlugin
	}
	switch account {
	case "claude":
		var reply []byte
		var err error
		if m.Claude != nil {
			reply, err = m.Claude(ctx, m.Home, model, body)
		} else {
			reply, err = runClaude(ctx, m.Home, m.ClaudeConfigDir, model, body)
		}
		if err != nil {
			return nil, err
		}
		return jsonResponse(reply), nil
	case "codex":
		return dialCodex(ctx, m, model, body, stream)
	case "copilot":
		return dialCopilot(ctx, m, body, stream)
	case "gemini":
		return dialGoogle(ctx, m, geminiCreds(m.Home), model, body)
	case "antigravity":
		return dialGoogle(ctx, m, antigravityCreds(m.Home), model, body)
	default:
		if IsWorkBuddy(account) {
			return dialWorkBuddy(ctx, m, account, model, body, stream)
		}
		return nil, os.ErrNotExist
	}
}

func jsonResponse(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

// ErrPlugin means an enabled plugin occupies this sign-in.
var ErrPlugin = errPluginSentinel{}

type errPluginSentinel struct{}

func (errPluginSentinel) Error() string { return "plugin" }
