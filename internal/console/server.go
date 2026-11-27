package console

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
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
	// Traffic, when set, reports the gateway's live in-flight traffic. Nil
	// leaves the usage page with an empty snapshot; history is never read.
	Traffic func() TrafficSnapshot
	// ResetSeat, when set, clears a seat's quota refusal and drain memory so
	// the user can ask for it again without restarting the process.
	ResetSeat func(scope, seat string)
}

// TrafficSnapshot is the console's view of current gateway traffic.
// ActiveRequests, BytesIn, and BytesOut are since the process started or the
// reader's last reset. At is when the snapshot was taken.
type TrafficSnapshot struct {
	ActiveRequests int       `json:"activeRequests"`
	BytesIn        int64     `json:"bytesIn"`
	BytesOut       int64     `json:"bytesOut"`
	At             time.Time `json:"at"`
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
	case "/groups":
		switch method {
		case http.MethodGet:
			return s.listGroups(w)
		case http.MethodPost:
			return s.createGroup(w, r)
		default:
			return methodNotAllowed(w)
		}
	case "/seats":
		if method != http.MethodGet {
			return methodNotAllowed(w)
		}
		return s.listSeats(w)
	case "/seats/reset":
		if method != http.MethodPost {
			return methodNotAllowed(w)
		}
		return s.resetSeat(w, r)
	case "/auto-groups":
		switch method {
		case http.MethodGet:
			return s.getAutoGroups(w)
		case http.MethodPut:
			return s.putAutoGroups(w, r)
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
	case "/usage/heatmap":
		if method != http.MethodGet {
			return methodNotAllowed(w)
		}
		return s.usageHeatmap(w, r)
	case "/usage/requests.csv":
		if method != http.MethodGet {
			return methodNotAllowed(w)
		}
		return s.usageCSV(w, r)
	case "/usage/traffic":
		if method != http.MethodGet {
			return methodNotAllowed(w)
		}
		return s.usageTraffic(w)
	case "/usage/requests/content":
		if method != http.MethodGet {
			return methodNotAllowed(w)
		}
		return s.usageContent(w, r)
	case "/usage/clear":
		if method != http.MethodPost {
			return methodNotAllowed(w)
		}
		return s.clearUsage(w, r)
	case "/pricing":
		switch method {
		case http.MethodGet:
			return s.getPricing(w)
		case http.MethodPut:
			return s.putPricing(w, r)
		default:
			return methodNotAllowed(w)
		}
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
	if id, ok := oneID(path, "/tokens/"); ok {
		if method != http.MethodPut {
			return methodNotAllowed(w)
		}
		return s.putToken(w, r, id)
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
	if id, ok := groupIDPath(path); ok {
		switch method {
		case http.MethodPut:
			return s.putGroup(w, r, id)
		case http.MethodDelete:
			return s.deleteGroup(w, id)
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
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Protocol        string            `json:"protocol"`
	BaseURL         string            `json:"baseUrl"`
	Models          []string          `json:"models"`
	Info            []state.ModelInfo `json:"info"`
	CredentialSet   bool              `json:"credentialSet"`
	KeyCount        int               `json:"keyCount"`
	MaxConcurrent   int               `json:"maxConcurrent,omitempty"`
	MaxRPM          int               `json:"maxRPM,omitempty"`
	QueueSlots      int               `json:"queueSlots,omitempty"`
	QueueWaitMS     int               `json:"queueWaitMS,omitempty"`
	Multiplier      *float64          `json:"multiplier,omitempty"`
	KeyMultipliers  []float64         `json:"keyMultipliers,omitempty"`
	BalanceURL      string            `json:"balanceUrl,omitempty"`
	BalanceField    string            `json:"balanceField,omitempty"`
	BalanceManual   string            `json:"balanceManual,omitempty"`
	BalanceCurrency string            `json:"balanceCurrency,omitempty"`
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
		ID:              p.ID,
		Name:            p.Name,
		Protocol:        p.Protocol,
		BaseURL:         p.BaseURL,
		Models:          models,
		Info:            info,
		CredentialSet:   len(keys) > 0,
		KeyCount:        len(keys),
		MaxConcurrent:   p.MaxConcurrent,
		MaxRPM:          p.MaxRPM,
		QueueSlots:      p.QueueSlots,
		QueueWaitMS:     p.QueueWaitMS,
		Multiplier:      p.Multiplier,
		KeyMultipliers:  p.KeyMultipliers,
		BalanceURL:      p.BalanceURL,
		BalanceField:    p.BalanceField,
		BalanceManual:   p.BalanceManual,
		BalanceCurrency: p.BalanceCurrency,
	}
}

func keyMultiplierDefault(p state.Provider) float64 {
	if p.Multiplier != nil {
		return *p.Multiplier
	}
	return 1
}

// fitKeyMultipliers keeps a configured per-key list aligned with the keys.
// An empty list means no key overrides, so the provider multiplier applies.
func fitKeyMultipliers(mults []float64, count int, fill float64) []float64 {
	if len(mults) == 0 || count <= 0 {
		return nil
	}
	out := make([]float64, count)
	copy(out, mults)
	for i := len(mults); i < count; i++ {
		out[i] = fill
	}
	return out
}

func removeKeyMultiplier(mults []float64, index int) []float64 {
	if len(mults) == 0 {
		return nil
	}
	out := make([]float64, 0, len(mults)-1)
	for i, value := range mults {
		if i != index {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
		Name            string            `json:"name"`
		Protocol        string            `json:"protocol"`
		BaseURL         string            `json:"baseUrl"`
		Models          []string          `json:"models"`
		Info            []state.ModelInfo `json:"info"`
		Credential      string            `json:"credential"`
		Credentials     []string          `json:"credentials"`
		MaxConcurrent   int               `json:"maxConcurrent"`
		MaxRPM          int               `json:"maxRPM"`
		QueueSlots      int               `json:"queueSlots"`
		QueueWaitMS     int               `json:"queueWaitMS"`
		Multiplier      *float64          `json:"multiplier"`
		KeyMultipliers  []float64         `json:"keyMultipliers"`
		BalanceURL      string            `json:"balanceUrl"`
		BalanceField    string            `json:"balanceField"`
		BalanceManual   string            `json:"balanceManual"`
		BalanceCurrency string            `json:"balanceCurrency"`
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
			MaxConcurrent: in.MaxConcurrent, MaxRPM: in.MaxRPM,
			QueueSlots: in.QueueSlots, QueueWaitMS: in.QueueWaitMS,
			Multiplier: in.Multiplier, KeyMultipliers: in.KeyMultipliers,
			BalanceURL: in.BalanceURL, BalanceField: in.BalanceField,
			BalanceManual: in.BalanceManual, BalanceCurrency: in.BalanceCurrency,
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
		Name            string             `json:"name"`
		Protocol        string             `json:"protocol"`
		BaseURL         string             `json:"baseUrl"`
		Models          []string           `json:"models"`
		Info            *[]state.ModelInfo `json:"info"`
		Credential      *string            `json:"credential"`
		Credentials     *[]string          `json:"credentials"`
		AddCredentials  []string           `json:"addCredentials"`
		MaxConcurrent   *int               `json:"maxConcurrent"`
		MaxRPM          *int               `json:"maxRPM"`
		QueueSlots      *int               `json:"queueSlots"`
		QueueWaitMS     *int               `json:"queueWaitMS"`
		Multiplier      *float64           `json:"multiplier"`
		KeyMultipliers  *[]float64         `json:"keyMultipliers"`
		BalanceURL      *string            `json:"balanceUrl"`
		BalanceField    *string            `json:"balanceField"`
		BalanceManual   *string            `json:"balanceManual"`
		BalanceCurrency *string            `json:"balanceCurrency"`
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
			if in.Multiplier != nil {
				cfg.Providers[i].Multiplier = in.Multiplier
			}
			if in.MaxConcurrent != nil {
				cfg.Providers[i].MaxConcurrent = *in.MaxConcurrent
			}
			if in.MaxRPM != nil {
				cfg.Providers[i].MaxRPM = *in.MaxRPM
			}
			if in.QueueSlots != nil {
				cfg.Providers[i].QueueSlots = *in.QueueSlots
			}
			if in.QueueWaitMS != nil {
				cfg.Providers[i].QueueWaitMS = *in.QueueWaitMS
			}
			if in.BalanceURL != nil {
				cfg.Providers[i].BalanceURL = *in.BalanceURL
			}
			if in.BalanceField != nil {
				cfg.Providers[i].BalanceField = *in.BalanceField
			}
			if in.BalanceManual != nil {
				cfg.Providers[i].BalanceManual = *in.BalanceManual
			}
			if in.BalanceCurrency != nil {
				cfg.Providers[i].BalanceCurrency = *in.BalanceCurrency
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
			if in.KeyMultipliers != nil {
				cfg.Providers[i].KeyMultipliers = append([]float64(nil), (*in.KeyMultipliers)...)
			} else if len(cfg.Providers[i].KeyMultipliers) > 0 {
				cfg.Providers[i].KeyMultipliers = fitKeyMultipliers(
					cfg.Providers[i].KeyMultipliers,
					len(cfg.Providers[i].Keys()),
					keyMultiplierDefault(cfg.Providers[i]),
				)
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
			cfg.Providers[i].KeyMultipliers = removeKeyMultiplier(cfg.Providers[i].KeyMultipliers, index)
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
		for _, group := range cfg.Groups {
			for _, ref := range group.Members {
				member, ok := cfg.ResolveGroupMember(ref)
				if ok && !member.Nested && member.ProviderID == id {
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
		if _, ok := state.AsValidation(err); ok {
			return writeErr(w, err)
		}
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
		if _, ok := state.AsValidation(err); ok {
			return writeErr(w, err)
		}
		return badJSON(w)
	}
	var saved state.Route
	err = s.Host.Update(func(cfg *state.Config) error {
		for i := range cfg.Routes {
			if cfg.Routes[i].ID != id {
				continue
			}
			cfg.Routes[i].ModelMatch = in.ModelMatch
			cfg.Routes[i].Routing = in.Routing
			cfg.Routes[i].Weights = in.Weights
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

func (s *Server) listGroups(w http.ResponseWriter) int {
	snap := s.Store.Current()
	respond.JSON(w, http.StatusOK, snap.Config.Groups)
	return http.StatusOK
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) int {
	body, err := readBody(w, r)
	if err != nil {
		return badJSON(w)
	}
	group, present, err := decodeGroup(body)
	if err != nil {
		return badJSON(w)
	}
	if group.ID == "" || strings.TrimSpace(group.ID) != group.ID {
		return invalidReason(w, "group")
	}
	var saved state.Group
	err = s.Host.Update(func(cfg *state.Config) error {
		if _, found := cfg.GroupByID(group.ID); found {
			return &state.ValidationError{Reason: "duplicate_id"}
		}
		if !present {
			group.EligibleSet = false
			group.Eligible = nil
		}
		cfg.Groups = append(cfg.Groups, group)
		saved = group
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusCreated, saved)
	return http.StatusCreated
}

func (s *Server) putGroup(w http.ResponseWriter, r *http.Request, id string) int {
	body, err := readBody(w, r)
	if err != nil {
		return badJSON(w)
	}
	in, present, err := decodeGroup(body)
	if err != nil {
		return badJSON(w)
	}
	if in.ID != "" && in.ID != id {
		return invalidReason(w, "group")
	}
	var saved state.Group
	err = s.Host.Update(func(cfg *state.Config) error {
		for i := range cfg.Groups {
			if cfg.Groups[i].ID != id {
				continue
			}
			cfg.Groups[i].Members = in.Members
			cfg.Groups[i].Routing = in.Routing
			cfg.Groups[i].Weights = in.Weights
			if present {
				cfg.Groups[i].EligibleSet = in.EligibleSet
				cfg.Groups[i].Eligible = in.Eligible
			}
			saved = cfg.Groups[i]
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

func (s *Server) deleteGroup(w http.ResponseWriter, id string) int {
	err := s.Host.Update(func(cfg *state.Config) error {
		for _, group := range cfg.Groups {
			for _, ref := range group.Members {
				member, ok := cfg.ResolveGroupMember(ref)
				if ok && member.Nested && member.GroupID == id {
					return state.ErrInUse
				}
			}
		}
		for i := range cfg.Groups {
			if cfg.Groups[i].ID == id {
				cfg.Groups = append(cfg.Groups[:i], cfg.Groups[i+1:]...)
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

type seatView struct {
	Seat           string `json:"seat"`
	Kind           string `json:"kind"`
	ProviderID     string `json:"providerId,omitempty"`
	ProviderName   string `json:"providerName,omitempty"`
	KeyIndex       int    `json:"keyIndex,omitempty"`
	KeyFingerprint string `json:"keyFingerprint,omitempty"`
	Account        string `json:"account,omitempty"`
	AccountName    string `json:"accountName,omitempty"`
}

func (s *Server) listSeats(w http.ResponseWriter) int {
	snap := s.Store.Current()
	var out []seatView
	for _, provider := range snap.Config.Providers {
		for i, key := range provider.Keys() {
			out = append(out, seatView{
				Seat:           state.ProviderSeatKey(provider.ID, key),
				Kind:           "key",
				ProviderID:     provider.ID,
				ProviderName:   provider.Name,
				KeyIndex:       i,
				KeyFingerprint: state.KeyFingerprint(key),
			})
		}
	}
	if s.Logins != nil {
		accounts := login.Discover(s.Logins())
		for _, account := range accounts.Accounts {
			if !account.Readable || account.Plugin {
				continue
			}
			out = append(out, seatView{
				Seat:        state.LoginSeatKey(account.ID),
				Kind:        "login",
				Account:     account.ID,
				AccountName: account.Name,
			})
		}
	}
	if out == nil {
		out = []seatView{}
	}
	respond.JSON(w, http.StatusOK, out)
	return http.StatusOK
}

func (s *Server) resetSeat(w http.ResponseWriter, r *http.Request) int {
	var in struct {
		Scope string `json:"scope"`
		Seat  string `json:"seat"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	if in.Seat == "" ||
		(!strings.HasPrefix(in.Seat, "provider/") && !strings.HasPrefix(in.Seat, "login/")) {
		return invalidReason(w, "seat")
	}
	if s.ResetSeat == nil {
		respond.Error(w, http.StatusServiceUnavailable, "unavailable")
		return http.StatusServiceUnavailable
	}
	s.ResetSeat(in.Scope, in.Seat)
	respond.NoContent(w)
	return http.StatusNoContent
}

func (s *Server) getAutoGroups(w http.ResponseWriter) int {
	snap := s.Store.Current()
	respond.JSON(w, http.StatusOK, map[string]bool{"on": !snap.Config.NoAutoGroups})
	return http.StatusOK
}

func (s *Server) putAutoGroups(w http.ResponseWriter, r *http.Request) int {
	var in struct {
		On bool `json:"on"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	err := s.Host.Update(func(cfg *state.Config) error {
		cfg.NoAutoGroups = !in.On
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	return s.getAutoGroups(w)
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
	ID          string             `json:"id"`
	Mask        string             `json:"mask"`
	Revoked     bool               `json:"revoked"`
	Models      []string           `json:"models,omitempty"`
	SpendLimit  *float64           `json:"spendLimit,omitempty"`
	SpendPeriod string             `json:"spendPeriod,omitempty"`
	Limits      []state.TokenLimit `json:"limits,omitempty"`
	IssuerID    string             `json:"issuerId,omitempty"`
}

type tokenCreated struct {
	ID          string             `json:"id"`
	Secret      string             `json:"secret"`
	Mask        string             `json:"mask"`
	Revoked     bool               `json:"revoked"`
	Models      []string           `json:"models,omitempty"`
	SpendLimit  *float64           `json:"spendLimit,omitempty"`
	SpendPeriod string             `json:"spendPeriod,omitempty"`
	Limits      []state.TokenLimit `json:"limits,omitempty"`
	IssuerID    string             `json:"issuerId,omitempty"`
}

func (s *Server) listTokens(w http.ResponseWriter) int {
	snap := s.Store.Current()
	out := make([]tokenView, len(snap.Config.GatewayTokens))
	for i, t := range snap.Config.GatewayTokens {
		out[i] = tokenView{
			ID: t.ID, Mask: security.Mask(t.Secret), Revoked: t.Revoked, Models: t.Models,
			SpendLimit: t.SpendLimit, SpendPeriod: t.SpendPeriod, Limits: t.Limits, IssuerID: t.IssuerID,
		}
	}
	respond.JSON(w, http.StatusOK, out)
	return http.StatusOK
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) int {
	var in struct {
		Models      []string           `json:"models"`
		SpendLimit  *float64           `json:"spendLimit"`
		SpendPeriod string             `json:"spendPeriod"`
		Limits      []state.TokenLimit `json:"limits"`
		IssuerID    string             `json:"issuerId"`
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
		token := state.GatewayToken{
			ID: id, Secret: secret, Models: in.Models, SpendLimit: in.SpendLimit,
			SpendPeriod: in.SpendPeriod, Limits: in.Limits, IssuerID: in.IssuerID,
		}
		if token.SpendLimit != nil && token.SpendPeriod == "" {
			token.SpendPeriod = "30d"
		}
		cfg.GatewayTokens = append(cfg.GatewayTokens, token)
		created = tokenCreated{
			ID: id, Secret: secret, Mask: security.Mask(secret), Models: token.Models,
			SpendLimit: token.SpendLimit, SpendPeriod: token.SpendPeriod, Limits: token.Limits,
			IssuerID: token.IssuerID,
		}
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusCreated, created)
	return http.StatusCreated
}

func (s *Server) putToken(w http.ResponseWriter, r *http.Request, id string) int {
	var in struct {
		Models      *[]string           `json:"models"`
		Limits      *[]state.TokenLimit `json:"limits"`
		SpendLimit  *float64            `json:"spendLimit"`
		SpendPeriod *string             `json:"spendPeriod"`
		Revoked     *bool               `json:"revoked"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	var saved tokenView
	err := s.Host.Update(func(cfg *state.Config) error {
		for i := range cfg.GatewayTokens {
			if cfg.GatewayTokens[i].ID != id {
				continue
			}
			if in.Models != nil {
				cfg.GatewayTokens[i].Models = append([]string(nil), (*in.Models)...)
			}
			if in.Limits != nil {
				cfg.GatewayTokens[i].Limits = append([]state.TokenLimit(nil), (*in.Limits)...)
			}
			if in.SpendLimit != nil {
				cfg.GatewayTokens[i].SpendLimit = in.SpendLimit
			}
			if in.SpendPeriod != nil {
				cfg.GatewayTokens[i].SpendPeriod = *in.SpendPeriod
			}
			if in.Revoked != nil {
				cfg.GatewayTokens[i].Revoked = *in.Revoked
			}
			t := cfg.GatewayTokens[i]
			saved = tokenView{
				ID: t.ID, Mask: security.Mask(t.Secret), Revoked: t.Revoked, Models: t.Models,
				SpendLimit: t.SpendLimit, SpendPeriod: t.SpendPeriod, Limits: t.Limits, IssuerID: t.IssuerID,
			}
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
	req, ok := usageQueryOf(w, r)
	if !ok {
		return http.StatusBadRequest
	}
	recs, cfg, ok := s.visibleUsage(w, req)
	if !ok {
		return http.StatusOK
	}
	now := time.Now()
	engine := price.Engine{Catalog: s.Prices, Config: cfg}
	sum := state.Summarize(recs, state.SummarizeOptions{
		Period: req.period,
		Now:    now,
		Price: func(rec state.AttemptRecord) (float64, bool) {
			return engine.Cost(r.Context(), rec)
		},
	})
	total := len(recs)
	state.SortUsageNewestFirst(recs)
	page := pageUsage(recs, req.offset, req.limit)
	body := map[string]any{
		"status":   "ok",
		"records":  page,
		"total":    total,
		"summary":  sum,
		"balances": s.providerBalances(r.Context()),
		"quotas":   s.subscriptionQuotas(r.Context()),
		"timezone": now.Location().String(),
	}
	if req.period != "" {
		body["period"] = req.period
	}
	respond.JSON(w, http.StatusOK, body)
	return http.StatusOK
}

type usageRequest struct {
	filter state.UsageFilter
	period string
	offset int
	limit  int
}

func usageQueryOf(w http.ResponseWriter, r *http.Request) (usageRequest, bool) {
	q := r.URL.Query()
	req := usageRequest{
		filter: state.UsageFilter{
			RouteID:     q.Get("routeId"),
			ProviderID:  q.Get("providerId"),
			Agent:       q.Get("agent"),
			Model:       q.Get("model"),
			CallerKeyID: q.Get("callerKeyId"),
			Source:      q.Get("source"),
		},
		period: q.Get("period"),
	}
	if req.filter.CallerKeyID == "" {
		req.filter.CallerKeyID = q.Get("tokenId")
	}
	var err error
	req.filter.Start, err = parseTime(q.Get("start"))
	if err != nil {
		badJSON(w)
		return usageRequest{}, false
	}
	req.filter.End, err = parseTime(q.Get("end"))
	if err != nil {
		badJSON(w)
		return usageRequest{}, false
	}
	if req.period != "" {
		start, ok := state.PeriodStart(req.period, time.Now())
		if !ok {
			badJSON(w)
			return usageRequest{}, false
		}
		if !start.IsZero() && (req.filter.Start == nil || req.filter.Start.Before(start)) {
			req.filter.Start = &start
		}
	}
	req.offset, err = queryInt(q.Get("offset"))
	if err != nil {
		badJSON(w)
		return usageRequest{}, false
	}
	req.limit, err = queryInt(q.Get("limit"))
	if err != nil {
		badJSON(w)
		return usageRequest{}, false
	}
	return req, true
}

func queryInt(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, errors.New("usage query")
	}
	return n, nil
}

func (s *Server) visibleUsage(w http.ResponseWriter, req usageRequest) ([]state.AttemptRecord, state.Config, bool) {
	recs, cfg, err := s.usagePageRows(req)
	if err != nil {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "unknown"})
		return nil, state.Config{}, false
	}
	return recs, cfg, true
}

// usagePageRows reads the filtered window and drops rows the page has been
// told to hide. A ledger read failure returns the error unchanged so callers
// can choose the status their representation needs.
func (s *Server) usagePageRows(req usageRequest) ([]state.AttemptRecord, state.Config, error) {
	recs, err := s.Store.ReadUsage(req.filter)
	if err != nil {
		return nil, state.Config{}, err
	}
	cfg := state.Config{}
	if snap := s.Store.Current(); snap != nil {
		cfg = snap.Config
	}
	visible := make([]state.AttemptRecord, 0, len(recs))
	for _, rec := range recs {
		if !cfg.HiddenOnUsagePage(rec) {
			visible = append(visible, rec)
		}
	}
	return visible, cfg, nil
}

func pageUsage(recs []state.AttemptRecord, offset, limit int) []state.AttemptRecord {
	if offset >= len(recs) {
		return []state.AttemptRecord{}
	}
	rest := recs[offset:]
	if limit > 0 && limit < len(rest) {
		rest = rest[:limit]
	}
	out := make([]state.AttemptRecord, len(rest))
	copy(out, rest)
	return out
}

type usageHeatDay struct {
	Date       string  `json:"date"`
	Calls      int     `json:"calls"`
	Failed     int     `json:"failed"`
	Canceled   int     `json:"canceled"`
	In         int     `json:"in"`
	Out        int     `json:"out"`
	CacheRead  int     `json:"cacheRead"`
	CacheWrite int     `json:"cacheWrite"`
	Reasoning  int     `json:"reasoning"`
	Cost       float64 `json:"cost"`
	Unpriced   int     `json:"unpriced"`
}

func (s *Server) usageHeatmap(w http.ResponseWriter, r *http.Request) int {
	req, ok := usageQueryOf(w, r)
	if !ok {
		return http.StatusBadRequest
	}
	recs, cfg, ok := s.visibleUsage(w, req)
	if !ok {
		return http.StatusOK
	}
	now := time.Now()
	loc := now.Location()
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -(53*7 - 1))
	engine := price.Engine{Catalog: s.Prices, Config: cfg}
	type cell struct {
		calls, failed, canceled        int
		in, out, cacheRead, cacheWrite int
		reasoning, unpriced            int
		cost                           float64
	}
	cells := map[string]*cell{}
	for _, rec := range recs {
		if !rec.InTotals() {
			continue
		}
		at := rec.Started.In(loc)
		day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, loc)
		if day.Before(start) || day.After(today) {
			continue
		}
		key := day.Format("2006-01-02")
		item := cells[key]
		if item == nil {
			item = &cell{}
			cells[key] = item
		}
		if !rec.HandedOn {
			item.calls++
			switch {
			case rec.Canceled():
				item.canceled++
			case rec.Failed():
				item.failed++
			}
		}
		counts := rec.Counts()
		item.in += counts.In
		item.out += counts.Out
		item.cacheRead += counts.CacheRead
		item.cacheWrite += counts.CacheWrite
		item.reasoning += counts.Reasoning
		cost, priced := engine.Cost(r.Context(), rec)
		switch {
		case priced:
			item.cost += cost
		case counts.In+counts.Out > 0:
			item.unpriced++
		}
	}
	days := make([]usageHeatDay, 0, 53*7)
	for at := start; !at.After(today); at = at.AddDate(0, 0, 1) {
		key := at.Format("2006-01-02")
		item := cells[key]
		if item == nil {
			item = &cell{}
		}
		days = append(days, usageHeatDay{
			Date: key, Calls: item.calls, Failed: item.failed, Canceled: item.canceled,
			In: item.in, Out: item.out, CacheRead: item.cacheRead, CacheWrite: item.cacheWrite,
			Reasoning: item.reasoning, Cost: round6(item.cost), Unpriced: item.unpriced,
		})
	}
	respond.JSON(w, http.StatusOK, map[string]any{"status": "ok", "days": days})
	return http.StatusOK
}

func (s *Server) usageCSV(w http.ResponseWriter, r *http.Request) int {
	req, ok := usageQueryOf(w, r)
	if !ok {
		return http.StatusBadRequest
	}
	recs, cfg, err := s.usagePageRows(req)
	if errors.Is(err, state.ErrUsageUnknown) || err != nil {
		respond.Error(w, http.StatusServiceUnavailable, "usage_unknown")
		return http.StatusServiceUnavailable
	}
	state.SortUsageNewestFirst(recs)
	recs = pageUsage(recs, req.offset, req.limit)
	engine := price.Engine{Catalog: s.Prices, Config: cfg}
	var buf bytes.Buffer
	out := csv.NewWriter(&buf)
	header := []string{
		"started", "finished", "routeId", "route", "providerId", "providerKeyId",
		"model", "agent", "callerKeyId", "callerIssuerId", "session", "status",
		"outcome", "ms", "ttft_ms", "first_text_ms", "queue_ms", "retry_ms",
		"bytes_in", "bytes_out", "in", "out", "cacheRead", "cacheWrite",
		"cacheWrite1h", "reasoning", "cost", "unpriced", "handedOn", "tries",
		"source", "operation",
	}
	_ = out.Write(header)
	for _, rec := range recs {
		counts := rec.Counts()
		cost, unpriced := "", ""
		if got, priced := engine.Cost(r.Context(), rec); priced {
			cost = strconv.FormatFloat(got, 'f', 6, 64)
		} else if rec.Cost == nil && counts.In+counts.Out > 0 {
			unpriced = "1"
		}
		status := ""
		if rec.Status != 0 {
			status = strconv.Itoa(rec.Status)
		}
		_ = out.Write([]string{
			rec.Started.Format(time.RFC3339), rec.Finished.Format(time.RFC3339),
			rec.RouteID, rec.Route, rec.ProviderID, rec.ProviderKeyID,
			rec.Model, rec.AgentName(), rec.CallerKey(), rec.CallerIssuerID,
			rec.Session, status, rec.Outcome, strconv.Itoa(rec.MS),
			strconv.Itoa(rec.TTFTMS), strconv.Itoa(rec.FirstTextMS),
			strconv.Itoa(rec.QueueMS), strconv.Itoa(rec.RetryMS),
			strconv.FormatInt(rec.BytesIn, 10), strconv.FormatInt(rec.BytesOut, 10),
			strconv.Itoa(counts.In), strconv.Itoa(counts.Out), strconv.Itoa(counts.CacheRead),
			strconv.Itoa(counts.CacheWrite), strconv.Itoa(counts.CacheWrite1H),
			strconv.Itoa(counts.Reasoning), cost, unpriced,
			strconv.FormatBool(rec.HandedOn), strconv.Itoa(rec.Tries), rec.Source, rec.Operation,
		})
	}
	out.Flush()
	if err := out.Error(); err != nil {
		respond.Error(w, http.StatusInternalServerError, "rejected")
		return http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="usage.csv"`)
	_, _ = w.Write(buf.Bytes())
	return http.StatusOK
}

// usageTraffic reports the in-flight request count and byte counters the
// process has observed. A nil Traffic hook means no live source is wired;
// the shape is still complete and this handler never reads the ledger.
func (s *Server) usageTraffic(w http.ResponseWriter) int {
	live := s.Traffic != nil
	snapshot := TrafficSnapshot{}
	if live {
		snapshot = s.Traffic()
	}
	if snapshot.At.IsZero() {
		snapshot.At = time.Now().UTC()
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"live":           live,
		"activeRequests": snapshot.ActiveRequests,
		"bytesIn":        snapshot.BytesIn,
		"bytesOut":       snapshot.BytesOut,
		"at":             snapshot.At,
	})
	return http.StatusOK
}

// usageContent serves the archived turns of one row's session. The usage
// line never holds prompt or reply text. agent is echoed for the page's
// context; the conversation archive is keyed by session only.
func (s *Server) usageContent(w http.ResponseWriter, r *http.Request) int {
	q := r.URL.Query()
	session := strings.TrimSpace(q.Get("session"))
	if session == "" {
		return invalidReason(w, "session")
	}
	from, err := parseTime(q.Get("from"))
	if err != nil {
		return badJSON(w)
	}
	to, err := parseTime(q.Get("to"))
	if err != nil {
		return badJSON(w)
	}
	turns, err := s.Store.ReadConversation(session)
	if errors.Is(err, state.ErrConversationUnknown) || err != nil {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "unknown"})
		return http.StatusOK
	}
	out := make([]state.TurnRecord, 0, len(turns))
	for _, turn := range turns {
		if from != nil && turn.At.Before(*from) {
			continue
		}
		if to != nil && turn.At.After(*to) {
			continue
		}
		out = append(out, turn)
	}
	respond.JSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"agent":   strings.TrimSpace(q.Get("agent")),
		"session": session,
		"turns":   out,
	})
	return http.StatusOK
}

func (s *Server) clearUsage(w http.ResponseWriter, r *http.Request) int {
	var in struct {
		ProviderID string     `json:"providerId"`
		Model      string     `json:"model"`
		Before     *time.Time `json:"before"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	providerID := strings.TrimSpace(in.ProviderID)
	model := strings.TrimSpace(in.Model)
	if providerID == "" || model == "" {
		return invalidReason(w, "usage_hide")
	}
	before := time.Now().UTC()
	if in.Before != nil {
		before = in.Before.UTC()
	}
	if s.Host == nil {
		respond.Error(w, http.StatusServiceUnavailable, "unsupported")
		return http.StatusServiceUnavailable
	}
	hide := state.UsageHide{ProviderID: providerID, Model: model, Before: before}
	err := s.Host.Update(func(cfg *state.Config) error {
		for i := range cfg.UsageHidden {
			if cfg.UsageHidden[i].ProviderID != providerID || cfg.UsageHidden[i].Model != model {
				continue
			}
			if before.After(cfg.UsageHidden[i].Before) {
				cfg.UsageHidden[i].Before = before
			}
			return nil
		}
		cfg.UsageHidden = append(cfg.UsageHidden, hide)
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	respond.JSON(w, http.StatusOK, map[string]any{"status": "ok", "hidden": hide})
	return http.StatusOK
}

func (s *Server) getPricing(w http.ResponseWriter) int {
	cfg := state.Config{}
	if snap := s.Store.Current(); snap != nil {
		cfg = snap.Config
	}
	prices := append([]state.PriceRule{}, cfg.Prices...)
	peaks := append([]state.PeakWindow{}, cfg.Peaks...)
	respond.JSON(w, http.StatusOK, map[string]any{"prices": prices, "peaks": peaks})
	return http.StatusOK
}

func (s *Server) putPricing(w http.ResponseWriter, r *http.Request) int {
	var in struct {
		Prices *[]state.PriceRule  `json:"prices"`
		Peaks  *[]state.PeakWindow `json:"peaks"`
	}
	if err := decode(w, r, &in); err != nil {
		return badJSON(w)
	}
	if s.Host == nil {
		respond.Error(w, http.StatusServiceUnavailable, "unsupported")
		return http.StatusServiceUnavailable
	}
	err := s.Host.Update(func(cfg *state.Config) error {
		if in.Prices != nil {
			cfg.Prices = append([]state.PriceRule(nil), (*in.Prices)...)
		}
		if in.Peaks != nil {
			cfg.Peaks = append([]state.PeakWindow(nil), (*in.Peaks)...)
		}
		return nil
	})
	if err != nil {
		return writeErr(w, err)
	}
	return s.getPricing(w)
}

func round6(v float64) float64 {
	const scale = 1e6
	if v >= 0 {
		return float64(int64(v*scale+0.5)) / scale
	}
	return float64(int64(v*scale-0.5)) / scale
}

func (s *Server) providerBalances(ctx context.Context) []balance.Reading {
	if s == nil || s.Store == nil || s.Store.Current() == nil {
		return []balance.Reading{}
	}
	consoleListen := ""
	if s.Host != nil {
		consoleListen = s.Host.Status().Console.Effective
	}
	return balance.ForProviders(ctx, nil, s.Store.Current().Config.Providers, consoleListen)
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
		Routing     string              `json:"routing"`
		Weights     map[string]int      `json:"weights"`
		Eligible    *[]state.ErrorClass `json:"eligible"`
	}
	if err := dec.Decode(&in); err != nil {
		return state.Route{}, false, err
	}
	route := state.Route{ModelMatch: in.ModelMatch, ProviderIDs: in.ProviderIDs, Routing: in.Routing, Weights: in.Weights}
	if route.ModelMatch == "" || strings.TrimSpace(route.ModelMatch) != route.ModelMatch || strings.Contains(route.ModelMatch, ",") {
		return state.Route{}, false, &state.ValidationError{Reason: "route_model"}
	}
	_, present := raw["eligible"]
	if present && in.Eligible != nil {
		route.EligibleSet = true
		route.Eligible = *in.Eligible
	}
	return route, present && in.Eligible != nil, nil
}

func decodeGroup(body []byte) (state.Group, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return state.Group{}, false, err
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	var in struct {
		ID       string              `json:"id"`
		Members  []string            `json:"members"`
		Routing  string              `json:"routing"`
		Weights  map[string]int      `json:"weights"`
		Eligible *[]state.ErrorClass `json:"eligible"`
	}
	if err := dec.Decode(&in); err != nil {
		return state.Group{}, false, err
	}
	group := state.Group{ID: in.ID, Members: in.Members, Routing: in.Routing, Weights: in.Weights}
	_, present := raw["eligible"]
	if present && in.Eligible != nil {
		group.EligibleSet = true
		group.Eligible = *in.Eligible
	}
	return group, present && in.Eligible != nil, nil
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

func groupIDPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/groups/")
	if !ok || rest == "" || strings.HasSuffix(rest, "/") {
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
