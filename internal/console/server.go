package console

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kuangjm123/AIGate/internal/agent"
	"github.com/kuangjm123/AIGate/internal/balance"
	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/respond"
	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

// ErrListen means a listener could not be opened. The body of the error is not shown to clients.
var ErrListen = errors.New("listen")

// PortStatus is one listener as the console reports it.
// Effective is empty when that listener is not open.
type PortStatus struct {
	Configured string `json:"configured"`
	Effective  string `json:"effective"`
	Listening  bool   `json:"listening"`
}

// ListenStatus is the address in effect for each port, plus the user-set value.
// Share is the user's switch for a non-loopback gateway address.
type ListenStatus struct {
	Gateway PortStatus `json:"gateway"`
	Console PortStatus `json:"console"`
	Share   bool       `json:"share"`
}

// Host applies config changes in-process. It does not call the other port over HTTP.
type Host interface {
	Status() ListenStatus
	Update(fn func(*state.Config) error) error
}

// ProbeResult is the outcome of one saved-provider check.
// Error is a fixed code. It does not contain the provider key.
type ProbeResult struct {
	OK       bool   `json:"ok"`
	Status   int    `json:"status,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Model    string `json:"model,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Server is the console JSON API and the control page. It does not forward model traffic.
// Probe, when set, checks one saved provider. A nil Probe does not dial.
type Server struct {
	Store       *state.Store
	Host        Host
	Log         *log.Logger
	Probe       func(ctx context.Context, prov state.Provider, consoleListen string) ProbeResult
	Detect      func(ctx context.Context, base, key, consoleListen string) (protocol, errCode string)
	FetchModels func(ctx context.Context, base, protocol, key, consoleListen string) (models []string, errCode string)
	CheckModels func(ctx context.Context, prov state.Provider, consoleListen string) []ModelReport
	// Agents, when set, is the machine the Agent page reads. Nil uses this computer.
	Agents *agent.Machine
	// Logins, when set, reads subscription sign-ins. Nil reads none.
	Logins func() login.Machine
	// Prices reads list prices for the usage summary. Nil leaves every priced call unpriced.
	Prices price.Pricer
}

// ModelReport is one model checked with a short request.
type ModelReport struct {
	Model  string `json:"model"`
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ServeHTTP dispatches one control request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	defer func() {
		if s.Log != nil {
			s.Log.Print(security.AccessLine(r.Method, r.URL.Path, status))
		}
	}()
	status = s.serve(w, r)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) int {
	if r.Method == http.MethodGet && (r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/assets/")) {
		if status := s.page(w, r); status != 0 {
			return status
		}
	}
	if r.Method == http.MethodGet && r.URL.Path == "/favicon.ico" {
		w.WriteHeader(http.StatusNoContent)
		return http.StatusNoContent
	}
	if IsModelPath(r.URL.Path) {
		respond.Error(w, http.StatusForbidden, "rejected")
		return http.StatusForbidden
	}
	if r.URL.Path == "/config-status" {
		return s.configStatus(w, r)
	}
	if r.URL.Path == "/register" && r.Method == http.MethodPost {
		return s.register(w, r)
	}
	if r.URL.Path == "/login" && r.Method == http.MethodPost {
		return s.login(w, r)
	}
	snap := s.Store.Current()
	if snap == nil {
		respond.Error(w, http.StatusServiceUnavailable, "config_unreadable")
		return http.StatusServiceUnavailable
	}
	if !snap.Config.SessionOK(security.Bearer(r)) {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return http.StatusUnauthorized
	}
	return s.dispatch(w, r)
}

func (s *Server) configStatus(w http.ResponseWriter, r *http.Request) int {
	if r.Method != http.MethodGet {
		return methodNotAllowed(w)
	}
	snap := s.Store.Current()
	if snap == nil {
		respond.JSON(w, http.StatusOK, map[string]string{"config": "unreadable"})
		return http.StatusOK
	}
	if !snap.Config.SessionOK(security.Bearer(r)) {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return http.StatusUnauthorized
	}
	respond.JSON(w, http.StatusOK, map[string]string{"config": "ok"})
	return http.StatusOK
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) int {
	path, method := r.URL.Path, r.Method
	switch path {
	case "/providers":
		switch method {
		case http.MethodGet:
			return s.listProviders(w)
		case http.MethodPost:
			return s.createProvider(w, r)
		default:
			return methodNotAllowed(w)
		}
	case "/logout":
		if method != http.MethodPost {
			return methodNotAllowed(w)
		}
		return s.logout(w, r)
	case "/routes":
		switch method {
		case http.MethodGet:
			return s.listRoutes(w)
		case http.MethodPost:
			return s.createRoute(w, r)
		default:
			return methodNotAllowed(w)
		}
	case "/plugins/order":
		if method != http.MethodPut {
			return methodNotAllowed(w)
		}
		return s.reorderPlugins(w, r)
	case "/plugins":
		switch method {
		case http.MethodGet:
			return s.listPlugins(w)
		case http.MethodPost:
			return s.createPlugin(w, r)
		default:
			return methodNotAllowed(w)
		}
	case "/tokens":
		switch method {
		case http.MethodGet:
			return s.listTokens(w)
		case http.MethodPost:
			return s.createToken(w, r)
		default:
			return methodNotAllowed(w)
		}
	case "/usage":
		if method != http.MethodGet {
			return methodNotAllowed(w)
		}
		return s.listUsage(w, r)
	case "/conversations":
		if method != http.MethodGet {
			return methodNotAllowed(w)
		}
		return s.listConversations(w)
	case "/conversations/turns":
		if method != http.MethodGet {
			return methodNotAllowed(w)
		}
		return s.readConversation(w, r)
	case "/listeners":
		switch method {
		case http.MethodGet:
			return s.getListeners(w)
		case http.MethodPut:
			return s.putListeners(w, r)
		default:
			return methodNotAllowed(w)
		}
	case "/agents":
		if method != http.MethodGet {
			return methodNotAllowed(w)
		}
		return s.listAgents(w)
	case "/accounts":
		if method != http.MethodGet {
			return methodNotAllowed(w)
		}
		return s.listAccounts(w)
	case "/accounts/workbuddy":
		if method != http.MethodPost {
			return methodNotAllowed(w)
		}
		return s.postWorkBuddy(w, r)
	case "/accounts/discovery":
		if method != http.MethodPut {
			return methodNotAllowed(w)
		}
		return s.putAccountDiscovery(w, r)
	}
	if id, action, ok := agentAction(path); ok {
		if method != http.MethodPost {
			return methodNotAllowed(w)
		}
		switch action {
		case "connect":
			return s.connectAgent(w, id)
		case "disconnect":
			return s.disconnectAgent(w, id)
		}
	}
	if id, ok := revokeID(path); ok {
		if method != http.MethodPost {
			return methodNotAllowed(w)
		}
		return s.revokeToken(w, id)
	}
	switch path {
	case "/providers/detect":
		if method != http.MethodPost {
			return methodNotAllowed(w)
		}
		return s.detectProvider(w, r)
	case "/providers/models":
		if method != http.MethodPost {
			return methodNotAllowed(w)
		}
		return s.fetchProviderModels(w, r)
	case "/providers/check":
		if method != http.MethodPost {
			return methodNotAllowed(w)
		}
		return s.checkProviderModels(w, r)
	}
	if id, index, ok := providerKeyID(path); ok {
		if method != http.MethodDelete {
			return methodNotAllowed(w)
		}
		return s.deleteProviderKey(w, id, index)
	}
	if id, ok := providerTestID(path); ok {
		if method != http.MethodPost {
			return methodNotAllowed(w)
		}
		return s.testProvider(w, r, id)
	}
	if id, ok := oneID(path, "/providers/"); ok {
		switch method {
		case http.MethodPut:
			return s.putProvider(w, r, id)
		case http.MethodDelete:
			return s.deleteProvider(w, id)
		default:
			return methodNotAllowed(w)
		}
	}
	if id, ok := oneID(path, "/routes/"); ok {
		switch method {
		case http.MethodPut:
			return s.putRoute(w, r, id)
		case http.MethodDelete:
			return s.deleteRoute(w, id)
		default:
			return methodNotAllowed(w)
		}
	}
	if id, ok := oneID(path, "/plugins/"); ok {
		if method != http.MethodPut {
			return methodNotAllowed(w)
		}
		return s.putPlugin(w, r, id)
	}
	return notFound(w)
}

type providerView struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Protocol      string            `json:"protocol"`
	BaseURL       string            `json:"baseUrl"`
	Models        []string          `json:"models"`
	Info          []state.ModelInfo `json:"info"`
	CredentialSet bool              `json:"credentialSet"`
	KeyCount      int               `json:"keyCount"`
}

func viewProvider(p state.Provider) providerView {
	models := p.Models
	if models == nil {
		models = []string{}
	}
	info := p.Info
	if info == nil {
		info = []state.ModelInfo{}
	}
	keys := p.Keys()
	return providerView{
		ID:            p.ID,
		Name:          p.Name,
		Protocol:      p.Protocol,
		BaseURL:       p.BaseURL,
		Models:        models,
		Info:          info,
		CredentialSet: len(keys) > 0,
		KeyCount:      len(keys),
	}
}

func (s *Server) listProviders(w http.ResponseWriter) int {
	snap := s.Store.Current()
	out := make([]providerView, len(snap.Config.Providers))
	for i, p := range snap.Config.Providers {
		out[i] = viewProvider(p)
	}
	respond.JSON(w, http.StatusOK, out)
	return http.StatusOK
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request) int {
	var in struct {
		Name        string            `json:"name"`
		Protocol    string            `json:"protocol"`
		BaseURL     string            `json:"baseUrl"`
		Models      []string          `json:"models"`
		Info        []state.ModelInfo `json:"info"`
		Credential  string            `json:"credential"`
		Credentials []string          `json:"credentials"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return invalidReason(w, "name")
	}
	keys := state.Provider{Credential: in.Credential, Credentials: in.Credentials}.Keys()
	if len(keys) == 0 {
		return invalidReason(w, "credential")
	}
	if in.Models == nil {
		in.Models = []string{}
	}
	var created providerView
	err := s.Host.Update(func(cfg *state.Config) error {
		id, err := state.FreshID(state.ProviderIDs(*cfg))
		if err != nil {
			return err
		}
		p := state.Provider{
			ID: id, Name: name, Protocol: in.Protocol, BaseURL: in.BaseURL,
			Models: in.Models, Info: in.Info, Credential: keys[0], Credentials: keys,
		}
		cfg.Providers = append(cfg.Providers, p)
		created = viewProvider(p)
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusCreated, created)
	return http.StatusCreated
}

func (s *Server) putProvider(w http.ResponseWriter, r *http.Request, id string) int {
	var in struct {
		Name           string             `json:"name"`
		Protocol       string             `json:"protocol"`
		BaseURL        string             `json:"baseUrl"`
		Models         []string           `json:"models"`
		Info           *[]state.ModelInfo `json:"info"`
		Credential     *string            `json:"credential"`
		Credentials    *[]string          `json:"credentials"`
		AddCredentials []string           `json:"addCredentials"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	if strings.TrimSpace(in.Name) == "" {
		return invalidReason(w, "name")
	}
	if in.Models == nil {
		in.Models = []string{}
	}
	var saved providerView
	err := s.Host.Update(func(cfg *state.Config) error {
		for i := range cfg.Providers {
			if cfg.Providers[i].ID != id {
				continue
			}
			cfg.Providers[i].Name = strings.TrimSpace(in.Name)
			cfg.Providers[i].Protocol = in.Protocol
			cfg.Providers[i].BaseURL = in.BaseURL
			cfg.Providers[i].Models = in.Models
			if in.Info != nil {
				cfg.Providers[i].Info = *in.Info
			}
			if in.Credentials != nil || in.Credential != nil {
				one := ""
				if in.Credential != nil {
					one = *in.Credential
				}
				var list []string
				if in.Credentials != nil {
					list = *in.Credentials
				}
				keys := state.Provider{Credential: one, Credentials: list}.Keys()
				if len(keys) == 0 {
					return &state.ValidationError{Reason: "credential"}
				}
				cfg.Providers[i].Credentials = keys
				cfg.Providers[i].Credential = keys[0]
			} else if extra := (state.Provider{Credentials: in.AddCredentials}).Keys(); len(extra) > 0 {
				keys := append(cfg.Providers[i].Keys(), extra...)
				keys = state.Provider{Credentials: keys}.Keys()
				cfg.Providers[i].Credentials = keys
				cfg.Providers[i].Credential = keys[0]
			}
			saved = viewProvider(cfg.Providers[i])
			return nil
		}
		return state.ErrNotFound
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusOK, saved)
	return http.StatusOK
}

func (s *Server) testProvider(w http.ResponseWriter, r *http.Request, id string) int {
	snap := s.Store.Current()
	if snap == nil {
		respond.Error(w, http.StatusServiceUnavailable, "config_unreadable")
		return http.StatusServiceUnavailable
	}
	prov, ok := snap.Config.Provider(id)
	if !ok {
		return notFound(w)
	}
	if s.Probe == nil {
		respond.Error(w, http.StatusServiceUnavailable, "unsupported")
		return http.StatusServiceUnavailable
	}
	consoleListen := ""
	if s.Host != nil {
		consoleListen = s.Host.Status().Console.Effective
	}
	result := s.Probe(r.Context(), prov, consoleListen)
	respond.JSON(w, http.StatusOK, result)
	return http.StatusOK
}

type toolIn struct {
	ID          string   `json:"id"`
	BaseURL     string   `json:"baseUrl"`
	Protocol    string   `json:"protocol"`
	Credential  string   `json:"credential"`
	Credentials []string `json:"credentials"`
	Models      []string `json:"models"`
}

func (s *Server) prepareTool(in toolIn) (state.Provider, string) {
	consoleListen := ""
	if s.Host != nil {
		consoleListen = s.Host.Status().Console.Effective
	}
	var prov state.Provider
	if in.ID != "" && s.Store != nil && s.Store.Current() != nil {
		if saved, ok := s.Store.Current().Config.Provider(in.ID); ok {
			prov = saved
		}
	}
	if strings.TrimSpace(in.BaseURL) != "" {
		prov.BaseURL = strings.TrimSpace(in.BaseURL)
	}
	if in.Protocol != "" || in.ID == "" {
		prov.Protocol = in.Protocol
	}
	if keys := (state.Provider{Credential: in.Credential, Credentials: in.Credentials}).Keys(); len(keys) > 0 {
		prov.Credentials = keys
		prov.Credential = keys[0]
	}
	if in.Models != nil {
		prov.Models = in.Models
		prov.Info = nil
	}
	return prov, consoleListen
}

func (s *Server) detectProvider(w http.ResponseWriter, r *http.Request) int {
	if s.Detect == nil {
		respond.Error(w, http.StatusServiceUnavailable, "unsupported")
		return http.StatusServiceUnavailable
	}
	var in toolIn
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	prov, consoleListen := s.prepareTool(in)
	key := ""
	if keys := prov.Keys(); len(keys) > 0 {
		key = keys[0]
	}
	protocol, errCode := s.Detect(r.Context(), prov.BaseURL, key, consoleListen)
	respond.JSON(w, http.StatusOK, map[string]string{"protocol": protocol, "error": errCode})
	return http.StatusOK
}

func (s *Server) fetchProviderModels(w http.ResponseWriter, r *http.Request) int {
	if s.FetchModels == nil {
		respond.Error(w, http.StatusServiceUnavailable, "unsupported")
		return http.StatusServiceUnavailable
	}
	var in toolIn
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	prov, consoleListen := s.prepareTool(in)
	key := ""
	if keys := prov.Keys(); len(keys) > 0 {
		key = keys[0]
	}
	models, errCode := s.FetchModels(r.Context(), prov.BaseURL, prov.Protocol, key, consoleListen)
	if models == nil {
		models = []string{}
	}
	respond.JSON(w, http.StatusOK, map[string]any{"models": models, "error": errCode})
	return http.StatusOK
}

func (s *Server) checkProviderModels(w http.ResponseWriter, r *http.Request) int {
	if s.CheckModels == nil {
		respond.Error(w, http.StatusServiceUnavailable, "unsupported")
		return http.StatusServiceUnavailable
	}
	var in toolIn
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	prov, consoleListen := s.prepareTool(in)
	rows := s.CheckModels(r.Context(), prov, consoleListen)
	if rows == nil {
		rows = []ModelReport{}
	}
	respond.JSON(w, http.StatusOK, map[string]any{"results": rows})
	return http.StatusOK
}

func (s *Server) deleteProviderKey(w http.ResponseWriter, id string, index int) int {
	var saved providerView
	err := s.Host.Update(func(cfg *state.Config) error {
		for i := range cfg.Providers {
			if cfg.Providers[i].ID != id {
				continue
			}
			keys := cfg.Providers[i].Keys()
			if index < 0 || index >= len(keys) {
				return state.ErrNotFound
			}
			keys = append(keys[:index], keys[index+1:]...)
			if len(keys) == 0 {
				return &state.ValidationError{Reason: "credential"}
			}
			cfg.Providers[i].Credentials = keys
			cfg.Providers[i].Credential = keys[0]
			saved = viewProvider(cfg.Providers[i])
			return nil
		}
		return state.ErrNotFound
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusOK, saved)
	return http.StatusOK
}

func (s *Server) deleteProvider(w http.ResponseWriter, id string) int {
	err := s.Host.Update(func(cfg *state.Config) error {
		for _, route := range cfg.Routes {
			for _, providerID := range route.ProviderIDs {
				if providerID == id {
					return state.ErrInUse
				}
			}
		}
		for i := range cfg.Providers {
			if cfg.Providers[i].ID == id {
				cfg.Providers = append(cfg.Providers[:i], cfg.Providers[i+1:]...)
				return nil
			}
		}
		return state.ErrNotFound
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.NoContent(w)
	return http.StatusNoContent
}

func (s *Server) listRoutes(w http.ResponseWriter) int {
	snap := s.Store.Current()
	respond.JSON(w, http.StatusOK, snap.Config.Routes)
	return http.StatusOK
}

func (s *Server) createRoute(w http.ResponseWriter, r *http.Request) int {
	body, err := readBody(w, r)
	if err != nil {
		return badJSON(w)
	}
	route, present, err := decodeRoute(body)
	if err != nil {
		return badJSON(w)
	}
	var saved state.Route
	err = s.Host.Update(func(cfg *state.Config) error {
		id, err := state.FreshID(state.RouteIDs(*cfg))
		if err != nil {
			return err
		}
		route.ID = id
		if route.ProviderIDs == nil {
			route.ProviderIDs = []string{}
		}
		if !present {
			route.EligibleSet = false
			route.Eligible = nil
		}
		cfg.Routes = append(cfg.Routes, route)
		saved = route
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusCreated, saved)
	return http.StatusCreated
}

func (s *Server) putRoute(w http.ResponseWriter, r *http.Request, id string) int {
	body, err := readBody(w, r)
	if err != nil {
		return badJSON(w)
	}
	in, present, err := decodeRoute(body)
	if err != nil {
		return badJSON(w)
	}
	var saved state.Route
	err = s.Host.Update(func(cfg *state.Config) error {
		for i := range cfg.Routes {
			if cfg.Routes[i].ID != id {
				continue
			}
			cfg.Routes[i].ModelMatch = in.ModelMatch
			if in.ProviderIDs == nil {
				cfg.Routes[i].ProviderIDs = []string{}
			} else {
				cfg.Routes[i].ProviderIDs = in.ProviderIDs
			}
			if present {
				cfg.Routes[i].EligibleSet = in.EligibleSet
				cfg.Routes[i].Eligible = in.Eligible
			}
			saved = cfg.Routes[i]
			return nil
		}
		return state.ErrNotFound
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusOK, saved)
	return http.StatusOK
}

func (s *Server) deleteRoute(w http.ResponseWriter, id string) int {
	err := s.Host.Update(func(cfg *state.Config) error {
		for i := range cfg.Routes {
			if cfg.Routes[i].ID == id {
				cfg.Routes = append(cfg.Routes[:i], cfg.Routes[i+1:]...)
				return nil
			}
		}
		return state.ErrNotFound
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.NoContent(w)
	return http.StatusNoContent
}

func (s *Server) listPlugins(w http.ResponseWriter) int {
	snap := s.Store.Current()
	out := append([]state.Plugin(nil), snap.Config.Plugins...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order == out[j].Order {
			return out[i].ID < out[j].ID
		}
		return out[i].Order < out[j].Order
	})
	respond.JSON(w, http.StatusOK, out)
	return http.StatusOK
}

func (s *Server) createPlugin(w http.ResponseWriter, r *http.Request) int {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	var saved state.Plugin
	err := s.Host.Update(func(cfg *state.Config) error {
		id, err := state.FreshID(state.PluginIDs(*cfg))
		if err != nil {
			return err
		}
		order := 0
		for _, p := range cfg.Plugins {
			if p.Order >= order {
				order = p.Order + 1
			}
		}
		saved = state.Plugin{ID: id, Enabled: in.Enabled, Order: order}
		cfg.Plugins = append(cfg.Plugins, saved)
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusCreated, saved)
	return http.StatusCreated
}

func (s *Server) putPlugin(w http.ResponseWriter, r *http.Request, id string) int {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	var saved state.Plugin
	err := s.Host.Update(func(cfg *state.Config) error {
		for i := range cfg.Plugins {
			if cfg.Plugins[i].ID != id {
				continue
			}
			cfg.Plugins[i].Enabled = in.Enabled
			saved = cfg.Plugins[i]
			return nil
		}
		return state.ErrNotFound
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusOK, saved)
	return http.StatusOK
}

func (s *Server) reorderPlugins(w http.ResponseWriter, r *http.Request) int {
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	err := s.Host.Update(func(cfg *state.Config) error {
		if len(in.IDs) != len(cfg.Plugins) {
			return &state.ValidationError{Reason: "plugin_order"}
		}
		index := map[string]int{}
		for i, p := range cfg.Plugins {
			index[p.ID] = i
		}
		seen := map[string]struct{}{}
		for order, id := range in.IDs {
			i, ok := index[id]
			if !ok {
				return state.ErrNotFound
			}
			if _, dup := seen[id]; dup {
				return &state.ValidationError{Reason: "plugin_order"}
			}
			seen[id] = struct{}{}
			cfg.Plugins[i].Order = order
		}
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	return s.listPlugins(w)
}

type tokenView struct {
	ID          string   `json:"id"`
	Mask        string   `json:"mask"`
	Revoked     bool     `json:"revoked"`
	Models      []string `json:"models,omitempty"`
	SpendLimit  *float64 `json:"spendLimit,omitempty"`
	SpendPeriod string   `json:"spendPeriod,omitempty"`
}

type tokenCreated struct {
	ID          string   `json:"id"`
	Secret      string   `json:"secret"`
	Mask        string   `json:"mask"`
	Revoked     bool     `json:"revoked"`
	SpendLimit  *float64 `json:"spendLimit,omitempty"`
	SpendPeriod string   `json:"spendPeriod,omitempty"`
}

func (s *Server) listTokens(w http.ResponseWriter) int {
	snap := s.Store.Current()
	out := make([]tokenView, len(snap.Config.GatewayTokens))
	for i, t := range snap.Config.GatewayTokens {
		out[i] = tokenView{ID: t.ID, Mask: security.Mask(t.Secret), Revoked: t.Revoked, Models: t.Models, SpendLimit: t.SpendLimit, SpendPeriod: t.SpendPeriod}
	}
	respond.JSON(w, http.StatusOK, out)
	return http.StatusOK
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) int {
	var in struct {
		Models      []string `json:"models"`
		SpendLimit  *float64 `json:"spendLimit"`
		SpendPeriod string   `json:"spendPeriod"`
	}
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			return badJSON(w)
		}
		if len(strings.TrimSpace(string(raw))) > 0 && json.Unmarshal(raw, &in) != nil {
			return badJSON(w)
		}
	}
	var created tokenCreated
	err := s.Host.Update(func(cfg *state.Config) error {
		id, err := state.FreshID(state.TokenIDs(*cfg))
		if err != nil {
			return err
		}
		secret, err := state.NewSecret()
		if err != nil {
			return err
		}
		token := state.GatewayToken{ID: id, Secret: secret, Models: in.Models, SpendLimit: in.SpendLimit, SpendPeriod: in.SpendPeriod}
		if token.SpendLimit != nil && token.SpendPeriod == "" {
			token.SpendPeriod = "30d"
		}
		cfg.GatewayTokens = append(cfg.GatewayTokens, token)
		created = tokenCreated{ID: id, Secret: secret, Mask: security.Mask(secret), SpendLimit: token.SpendLimit, SpendPeriod: token.SpendPeriod}
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusCreated, created)
	return http.StatusCreated
}

func (s *Server) revokeToken(w http.ResponseWriter, id string) int {
	var saved tokenView
	err := s.Host.Update(func(cfg *state.Config) error {
		for i := range cfg.GatewayTokens {
			if cfg.GatewayTokens[i].ID != id {
				continue
			}
			cfg.GatewayTokens[i].Revoked = true
			t := cfg.GatewayTokens[i]
			saved = tokenView{ID: t.ID, Mask: security.Mask(t.Secret), Revoked: true}
			return nil
		}
		return state.ErrNotFound
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusOK, saved)
	return http.StatusOK
}

func (s *Server) listUsage(w http.ResponseWriter, r *http.Request) int {
	q := r.URL.Query()
	filter := state.UsageFilter{
		RouteID:    q.Get("routeId"),
		ProviderID: q.Get("providerId"),
	}
	var err error
	filter.Start, err = parseTime(q.Get("start"))
	if err != nil {
		return badJSON(w)
	}
	filter.End, err = parseTime(q.Get("end"))
	if err != nil {
		return badJSON(w)
	}
	period := q.Get("period")
	if period != "" {
		start, ok := state.PeriodStart(period, time.Now())
		if !ok {
			return badJSON(w)
		}
		if !start.IsZero() && (filter.Start == nil || filter.Start.Before(start)) {
			filter.Start = &start
		}
	}
	recs, err := s.Store.ReadUsage(filter)
	if errors.Is(err, state.ErrUsageUnknown) {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "unknown"})
		return http.StatusOK
	}
	if err != nil {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "unknown"})
		return http.StatusOK
	}
	sum := state.SummarizeUsage(recs)
	sum.Cost, sum.Unpriced = price.Tally(recs, func(model string) (price.Rate, bool) {
		if s.Prices == nil {
			return price.Rate{}, false
		}
		return s.Prices.Rate(r.Context(), model)
	})
	body := map[string]any{
		"status":   "ok",
		"records":  recs,
		"summary":  sum,
		"balances": s.providerBalances(r.Context()),
		"quotas":   s.subscriptionQuotas(r.Context()),
	}
	if period != "" {
		body["period"] = period
	}
	respond.JSON(w, http.StatusOK, body)
	return http.StatusOK
}

func (s *Server) providerBalances(ctx context.Context) []balance.Reading {
	if s == nil || s.Store == nil || s.Store.Current() == nil {
		return []balance.Reading{}
	}
	return balance.ForProviders(ctx, nil, s.Store.Current().Config.Providers, "")
}

// subscriptionQuotas asks each readable subscription sign-in for its remaining
// allowance. Nil Logins reads none. A failed read is unknown, never zero.
func (s *Server) subscriptionQuotas(ctx context.Context) []login.Quota {
	if s == nil || s.Logins == nil {
		return []login.Quota{}
	}
	return login.Usage(ctx, s.Logins())
}

func (s *Server) listConversations(w http.ResponseWriter) int {
	list, err := s.Store.ListConversations()
	if errors.Is(err, state.ErrConversationUnknown) || err != nil {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "unknown"})
		return http.StatusOK
	}
	respond.JSON(w, http.StatusOK, map[string]any{"status": "ok", "conversations": list})
	return http.StatusOK
}

func (s *Server) readConversation(w http.ResponseWriter, r *http.Request) int {
	session := r.URL.Query().Get("session")
	if session == "" {
		return badJSON(w)
	}
	turns, err := s.Store.ReadConversation(session)
	if errors.Is(err, state.ErrConversationUnknown) || err != nil {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "unknown"})
		return http.StatusOK
	}
	respond.JSON(w, http.StatusOK, map[string]any{"status": "ok", "turns": turns})
	return http.StatusOK
}

func (s *Server) getListeners(w http.ResponseWriter) int {
	respond.JSON(w, http.StatusOK, s.Host.Status())
	return http.StatusOK
}

func (s *Server) putListeners(w http.ResponseWriter, r *http.Request) int {
	var in struct {
		Gateway *string `json:"gateway"`
		Console *string `json:"console"`
		Share   *bool   `json:"share"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	err := s.Host.Update(func(cfg *state.Config) error {
		if in.Share != nil {
			cfg.Share = *in.Share
		}
		if in.Gateway != nil {
			if *in.Gateway == "" {
				return &state.ValidationError{Reason: "empty_listen"}
			}
			cfg.Listeners.Gateway = *in.Gateway
		}
		if in.Console != nil {
			if *in.Console == "" {
				return &state.ValidationError{Reason: "empty_listen"}
			}
			cfg.Listeners.Console = *in.Console
		}
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusOK, s.Host.Status())
	return http.StatusOK
}

func decodeRoute(body []byte) (state.Route, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return state.Route{}, false, err
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	var in struct {
		ModelMatch  string              `json:"modelMatch"`
		ProviderIDs []string            `json:"providerIds"`
		Eligible    *[]state.ErrorClass `json:"eligible"`
	}
	if err := dec.Decode(&in); err != nil {
		return state.Route{}, false, err
	}
	route := state.Route{ModelMatch: in.ModelMatch, ProviderIDs: in.ProviderIDs}
	_, present := raw["eligible"]
	if present && in.Eligible != nil {
		route.EligibleSet = true
		route.Eligible = *in.Eligible
	}
	return route, present && in.Eligible != nil, nil
}

func parseTime(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func oneID(path, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	return rest, true
}

func providerKeyID(path string) (string, int, bool) {
	rest, ok := strings.CutPrefix(path, "/providers/")
	if !ok {
		return "", 0, false
	}
	id, rest, ok := strings.Cut(rest, "/keys/")
	if !ok || id == "" || strings.Contains(id, "/") || rest == "" || strings.Contains(rest, "/") {
		return "", 0, false
	}
	index := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			return "", 0, false
		}
		index = index*10 + int(r-'0')
	}
	return id, index, true
}

func providerTestID(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/providers/")
	if !ok || !strings.HasSuffix(rest, "/test") {
		return "", false
	}
	id := strings.TrimSuffix(rest, "/test")
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

func revokeID(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/tokens/")
	if !ok || !strings.HasSuffix(rest, "/revoke") {
		return "", false
	}
	id := strings.TrimSuffix(rest, "/revoke")
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if err != nil {
		return err
	}
	return nil
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) int {
	snap := s.Store.Current()
	if snap == nil {
		respond.Error(w, http.StatusServiceUnavailable, "config_unreadable")
		return http.StatusServiceUnavailable
	}
	if !s.loopbackConsole() {
		respond.Error(w, http.StatusForbidden, "rejected")
		return http.StatusForbidden
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	username := strings.TrimSpace(in.Username)
	if username == "" {
		return invalidReason(w, "username")
	}
	if utf8.RuneCountInString(in.Password) < 8 {
		return invalidReason(w, "password")
	}
	salt, hash, err := security.HashPassword(in.Password)
	if err != nil {
		return invalidReason(w, "password")
	}
	var saved struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	err = s.Host.Update(func(cfg *state.Config) error {
		if _, ok := cfg.UserByName(username); ok {
			return &state.ValidationError{Reason: "duplicate_username"}
		}
		id, err := state.FreshID(state.UserIDs(*cfg))
		if err != nil {
			return err
		}
		cfg.Users = append(cfg.Users, state.User{
			ID:           id,
			Username:     username,
			PasswordSalt: salt,
			PasswordHash: hash,
			Sessions:     []state.Session{},
		})
		saved.ID = id
		saved.Username = username
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusCreated, saved)
	return http.StatusCreated
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) int {
	snap := s.Store.Current()
	if snap == nil {
		respond.Error(w, http.StatusServiceUnavailable, "config_unreadable")
		return http.StatusServiceUnavailable
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	username := strings.TrimSpace(in.Username)
	user, ok := snap.Config.UserByName(username)
	salt, hash := user.PasswordSalt, user.PasswordHash
	if !security.PasswordMatch(in.Password, salt, hash) || !ok {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return http.StatusUnauthorized
	}
	secret, err := state.NewSecret()
	if err != nil {
		respond.Error(w, http.StatusServiceUnavailable, "config_unreadable")
		return http.StatusServiceUnavailable
	}
	err = s.Host.Update(func(cfg *state.Config) error {
		id, err := state.FreshID(state.SessionIDs(*cfg))
		if err != nil {
			return err
		}
		for i := range cfg.Users {
			if cfg.Users[i].Username != username {
				continue
			}
			cfg.Users[i].Sessions = append(cfg.Users[i].Sessions, state.Session{
				ID:         id,
				SecretHash: security.SecretHash(secret),
			})
			return nil
		}
		return state.ErrNotFound
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusOK, map[string]string{"username": username, "secret": secret})
	return http.StatusOK
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) int {
	secret := security.Bearer(r)
	err := s.Host.Update(func(cfg *state.Config) error {
		for ui := range cfg.Users {
			sessions := cfg.Users[ui].Sessions
			for i := range sessions {
				if security.SecretHashMatch(secret, sessions[i].SecretHash) != 1 {
					continue
				}
				cfg.Users[ui].Sessions = append(sessions[:i], sessions[i+1:]...)
				return nil
			}
		}
		return state.ErrNotFound
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.NoContent(w)
	return http.StatusNoContent
}

func (s *Server) loopbackConsole() bool {
	if s.Host == nil {
		return false
	}
	st := s.Host.Status()
	if !st.Console.Listening || st.Console.Effective == "" {
		return false
	}
	host, _, err := net.SplitHostPort(st.Console.Effective)
	if err != nil {
		return false
	}
	return security.IsLoopbackHost(host)
}

func invalidReason(w http.ResponseWriter, reason string) int {
	respond.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "reason": reason})
	return http.StatusBadRequest
}

func badJSON(w http.ResponseWriter) int {
	respond.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "reason": "json"})
	return http.StatusBadRequest
}

func methodNotAllowed(w http.ResponseWriter) int {
	respond.Error(w, http.StatusMethodNotAllowed, "method_not_allowed")
	return http.StatusMethodNotAllowed
}

func notFound(w http.ResponseWriter) int {
	respond.Error(w, http.StatusNotFound, "not_found")
	return http.StatusNotFound
}

func writeErr(w http.ResponseWriter, err error) int {
	switch {
	case errors.Is(err, state.ErrNotFound):
		respond.Error(w, http.StatusNotFound, "not_found")
		return http.StatusNotFound
	case errors.Is(err, state.ErrInUse):
		respond.Error(w, http.StatusConflict, "in_use")
		return http.StatusConflict
	case errors.Is(err, state.ErrUnreadable):
		respond.Error(w, http.StatusServiceUnavailable, "config_unreadable")
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrListen):
		respond.Error(w, http.StatusServiceUnavailable, "listen")
		return http.StatusServiceUnavailable
	}
	if ve, ok := state.AsValidation(err); ok {
		respond.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "reason": ve.Reason})
		return http.StatusBadRequest
	}
	respond.JSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "reason": "rejected"})
	return http.StatusBadRequest
}
