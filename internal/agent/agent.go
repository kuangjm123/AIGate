// Package agent detects local coding agents and points their own config at the gateway.
// A connect records the previous values. A disconnect writes those values back.
// Tests pass a temporary home. Nothing here reads the machine that is running the test
// unless the caller fills Machine with that machine.
package agent

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/kuangjm123/AIGate/internal/state"
)

// Model is one conversation model the gateway can serve.
// ID is the catalog id a JSON body should send. Bare has no slash, for a path that
// cannot carry one.
type Model struct {
	ID        string
	Name      string
	Bare      string
	Context   int
	Reasoning bool
	Vision    *bool
}

// Target is the gateway the agent should call. Origin has no path.
// Token overrides the local app caller for a Codex that reaches the gateway
// from another machine. Keys are issued gateway secrets that a provider table
// may already carry. Allowed maps an issued secret to its model list; an empty
// or missing list allows every model. Used lists model ids Codex used, most
// recent first.
type Target struct {
	Origin  string
	Token   string
	Keys    []string
	Allowed map[string][]string
	Used    []string
}

func (t Target) clean() Target {
	t.Origin = strings.TrimRight(strings.TrimSpace(t.Origin), "/")
	t.Token = strings.TrimSpace(t.Token)
	return t
}

func (t Target) v1() string {
	return t.clean().Origin + "/v1"
}

func (t Target) codexURL() string {
	return t.clean().Origin + "/backend-api/codex"
}

// bearer is the token a new provider table carries, and whether the target
// has one the gateway can take. A loopback caller may present this
// application's name. One from another machine needs an issued gateway
// token, so the first live key stands in for the sharing key.
func (t Target) bearer(id string) (string, bool) {
	t = t.clean()
	if t.Token != "" {
		return t.Token, true
	}
	if t.local() {
		return caller(id), true
	}
	for _, key := range t.Keys {
		if key = strings.TrimSpace(key); key != "" {
			return key, true
		}
	}
	return "", false
}

func (t Target) keepsCredential(secret string) bool {
	t = t.clean()
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return false
	}
	if secret == t.Token {
		return true
	}
	for _, key := range t.Keys {
		if secret == strings.TrimSpace(key) {
			return true
		}
	}
	if t.Token != "" {
		return false
	}
	return t.local()
}

func (t Target) allowed(secret string) ([]string, bool) {
	secret = strings.TrimSpace(secret)
	if len(t.Allowed) == 0 {
		return nil, false
	}
	models, ok := t.Allowed[secret]
	return models, ok
}

func (t Target) modelsFor(secret string, models []Model) []Model {
	allow, ok := t.allowed(secret)
	if !ok || len(allow) == 0 {
		return models
	}
	out := make([]Model, 0, len(models))
	for _, model := range models {
		if state.AllowsModel(allow, model.ID, model.Bare) {
			out = append(out, model)
		}
	}
	return out
}

// local reports whether Codex on this computer reaches the gateway on a
// loopback address. A loopback caller may present any non-empty credential;
// one from another machine needs a token the gateway issued.
func (t Target) local() bool {
	u, err := urlParse(t.Origin)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Machine is the computer whose agent config is read. StashDir holds the notes
// of what a connect replaced. LookPath reports a command on PATH. AppDirs are
// extra install roots, used only to notice Cursor.
type Machine struct {
	Home          string
	StashDir      string
	XDGConfigHome string
	AppDirs       []string
	LookPath      func(string) bool
}

func (m Machine) has(name string) bool {
	return m.LookPath != nil && m.LookPath(name)
}

// ThisMachine is the computer this process is running on.
// stashDir is where connect notes are written. An empty home is an error.
func ThisMachine(stashDir string) (Machine, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return Machine{}, fail("unknown")
	}
	m := Machine{
		Home:          home,
		StashDir:      stashDir,
		XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
		LookPath: func(name string) bool {
			_, err := exec.LookPath(name)
			return err == nil
		},
	}
	switch runtime.GOOS {
	case "windows":
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			m.AppDirs = []string{filepath.Join(local, "Programs"), local}
		}
	case "darwin":
		m.AppDirs = []string{"/Applications"}
	default:
		if data := os.Getenv("XDG_DATA_HOME"); data != "" {
			m.AppDirs = append(m.AppDirs, data)
		}
		m.AppDirs = append(m.AppDirs, filepath.Join(home, ".local", "share"))
	}
	return m, nil
}

// Status is one agent as the console shows it.
// State is absent, off, on, drift, hand, unknown, or manual.
// Support is wire or manual. A manual agent is detected and not edited.
type Status struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Where     string `json:"where,omitempty"`
	State     string `json:"state"`
	Support   string `json:"support"`
	Model     string `json:"model,omitempty"`
	Note      string `json:"note,omitempty"`
}

// CodeError is a refused connect or disconnect. Code is a fixed token.
type CodeError struct{ Code string }

func (e *CodeError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func fail(code string) error { return &CodeError{Code: code} }

// CodeOf returns the fixed token, or empty when err is nil or not a CodeError.
func CodeOf(err error) string {
	var c *CodeError
	if errors.As(err, &c) {
		return c.Code
	}
	return ""
}

// Offers lists the conversation models a connect may write.
// A route name comes first. A hidden, image, or video model is left out.
// A provider model is providerId/model, which is the id the catalog serves.
func Offers(cfg state.Config) []Model {
	routed := map[string]bool{}
	seen := map[string]bool{}
	var out []Model
	add := func(id, name, bare string, context int, reasoning bool, vision *bool) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		if name == "" {
			name = id
		}
		if bare == "" {
			bare = id
		}
		out = append(out, Model{ID: id, Name: name, Bare: bare, Context: context, Reasoning: reasoning, Vision: vision})
	}
	for _, route := range cfg.Routes {
		if route.ModelMatch == "" {
			continue
		}
		routed[route.ModelMatch] = true
		add(route.ModelMatch, route.ModelMatch, route.ModelMatch, 0, false, nil)
	}
	for _, group := range cfg.Groups {
		if group.ID == "" {
			continue
		}
		add("group/"+group.ID, group.ID, group.ID, 0, false, nil)
	}
	for _, p := range cfg.Providers {
		owner := p.ID
		if owner == "" {
			owner = "aigate"
		}
		for _, model := range p.Models {
			if !p.ChatModel(model) || routed[model] {
				continue
			}
			name := model
			var context int
			var reasoning bool
			var vision *bool
			if info, ok := p.InfoFor(model); ok {
				if info.DisplayName != "" {
					name = info.DisplayName
				}
				context = info.Context
				reasoning = info.Reasoning
				if info.Vision != nil {
					v := *info.Vision
					vision = &v
				}
			}
			add(owner+"/"+model, name, model, context, reasoning, vision)
		}
	}
	return out
}

type wiring interface {
	id() string
	status(Machine, Target) Status
	connect(Machine, Target, []Model) error
	disconnect(Machine) error
}

var (
	wireMu sync.Mutex
	wires  = []wiring{claudeApp{}, codexApp{}, workbuddyApp{}, opencodeApp{}, geminiApp{}, aiderApp{}, cursorApp{}}
)

func findWire(id string) (wiring, bool) {
	for _, w := range wires {
		if w.id() == id {
			return w, true
		}
	}
	return nil, false
}

// Inspect lists every agent this process knows, installed or not.
func Inspect(m Machine, gw Target) []Status {
	wireMu.Lock()
	defer wireMu.Unlock()
	gw = gw.clean()
	out := make([]Status, 0, len(wires))
	for _, w := range wires {
		out = append(out, w.status(m, gw))
	}
	return out
}

// Connect points id at the gateway and records the values it replaced.
// A second connect keeps the first record, so a later disconnect still restores it.
func Connect(m Machine, id string, gw Target, models []Model) error {
	wireMu.Lock()
	defer wireMu.Unlock()
	w, ok := findWire(id)
	if !ok {
		return fail("not_found")
	}
	err := w.connect(m, gw.clean(), models)
	if err == nil || CodeOf(err) != "" {
		return err
	}
	return fail("unknown")
}

// Disconnect writes back the values recorded at connect.
// A missing record does not guess at the previous config.
func Disconnect(m Machine, id string) error {
	wireMu.Lock()
	defer wireMu.Unlock()
	w, ok := findWire(id)
	if !ok {
		return fail("not_found")
	}
	err := w.disconnect(m)
	if err == nil || CodeOf(err) != "" {
		return err
	}
	return fail("unknown")
}

func caller(id string) string { return "aigate-" + id }

func pickID(models []Model, current string) string {
	current = strings.TrimSpace(current)
	for _, model := range models {
		if model.ID == current {
			return model.ID
		}
	}
	if len(models) == 0 {
		return ""
	}
	return models[0].ID
}

func pickBare(models []Model, current string) (string, bool) {
	current = strings.TrimSpace(current)
	if bareOK(current) {
		for _, model := range models {
			if model.Bare == current && bareOK(model.Bare) {
				return model.Bare, true
			}
		}
	}
	for _, model := range models {
		if bareOK(model.Bare) {
			return model.Bare, true
		}
	}
	return "", false
}

func bareOK(s string) bool {
	if s == "" || strings.ContainsAny(s, "/:\\ \t") {
		return false
	}
	return true
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func sameOrigin(raw, origin string) bool {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(origin) == "" {
		return false
	}
	u, err := parseOrigin(raw)
	o, err2 := parseOrigin(origin)
	if err != nil || err2 != nil {
		return false
	}
	return strings.EqualFold(u, o)
}

func parseOrigin(raw string) (string, error) {
	u, err := urlParse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errHost
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}
