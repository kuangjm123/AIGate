// Package login reads coding-agent subscription sign-ins from where those
// agents keep them. The tokens are not copied into AIGate's config. A missing
// file means signed out. A file that does not parse is unknown, not signed out.
package login

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"
)

// Model is one model a sign-in can serve. ID is the upstream model id.
type Model struct {
	ID        string
	Name      string
	Context   int
	Reasoning bool
}

// Account is one local sign-in shown as a provider.
// Readable false means the file was there and did not parse.
// ModelsKnown false means the model list was not read, which is not an empty list.
type Account struct {
	ID          string
	Name        string
	Plugin      bool
	Readable    bool
	ModelsKnown bool
	Models      []Model
	// Origin is desktop or saved for a WorkBuddy sign-in, and empty for every other account.
	Origin string
}

// Snapshot is the result of one discovery.
// Status is ok, off, or unknown. unknown means the home directory could not be read.
type Snapshot struct {
	Status   string
	Accounts []Account
}

// Machine is the computer whose agent files are read.
// Off skips every credential file. PluginIDs are enabled plugin ids; a plugin
// whose id equals an account id occupies that account's slot and this process
// does not dial it.
type Machine struct {
	Home      string
	Off       bool
	PluginIDs []string
	Client    *http.Client
	Now       func() time.Time
	ReadFile  func(string) ([]byte, error)
	Claude    ClaudeFunc
	CodexAuth string
	CodexAPI  string
	OpenAI    string
	// WorkBuddyAuth is the CodeBuddy auth directory that holds
	// workbuddy-desktop.info and workbuddy-desktop-ai.info.
	// Empty means those files are not read. Tests set it; the process sets the real directory.
	WorkBuddyAuth string
	// WorkBuddyStore is AIGate's own WorkBuddy login file. Empty means no saved accounts.
	WorkBuddyStore string
	// WorkBuddyAPI and WorkBuddyAI replace the personal origins. An enterprise domain still uses its own host.
	WorkBuddyAPI string
	WorkBuddyAI  string
	// ChatGPT is the ChatGPT backend origin that serves /wham/usage. Empty means chatgpt.com.
	ChatGPT string
	// CloudCode and AntiCloud replace the Google quota hosts. Empty means the public hosts.
	CloudCode string
	AntiCloud string
	// ClaudeUsage runs claude -p /usage. A test supplies one. Nil runs the local binary.
	ClaudeUsage func(ctx context.Context, home string) ([]byte, error)
}

func (m Machine) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (m Machine) read(path string) ([]byte, error) {
	if m.ReadFile != nil {
		return m.ReadFile(path)
	}
	return os.ReadFile(path)
}

func (m Machine) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m Machine) plugin(id string) bool {
	for _, plugin := range m.PluginIDs {
		if plugin == id {
			return true
		}
	}
	return false
}

// Discover lists sign-ins. Off reads nothing.
func Discover(m Machine) Snapshot {
	if m.Off {
		return Snapshot{Status: "off"}
	}
	if strings.TrimSpace(m.Home) == "" {
		return Snapshot{Status: "unknown"}
	}
	var out []Account
	for _, read := range []func(Machine) (Account, bool){readClaude, readCodex, readCopilot, readGemini, readAntigravity} {
		account, ok := read(m)
		if ok {
			out = append(out, account)
		}
	}
	out = append(out, readWorkBuddyAccounts(m)...)
	if out == nil {
		out = []Account{}
	}
	return Snapshot{Status: "ok", Accounts: out}
}

// CatalogID is the id other agents are given for one subscription model.
func CatalogID(account, model string) string {
	return "login/" + account + "/" + model
}

// ParseID splits a catalog id into the account and the upstream model.
func ParseID(id string) (account, model string, ok bool) {
	parts := strings.SplitN(id, "/", 3)
	if len(parts) != 3 || parts[0] != "login" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// IsWorkBuddy reports a WorkBuddy catalog account, desktop or saved.
func IsWorkBuddy(id string) bool {
	return id == "workbuddy" || id == "workbuddy-ai" || strings.HasPrefix(id, "workbuddy#") || strings.HasPrefix(id, "workbuddy-ai#")
}
