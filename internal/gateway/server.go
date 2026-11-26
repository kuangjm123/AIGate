package gateway

import (
	"log"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/kuangjm123/AIGate/internal/console"
	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/respond"
	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

// Server is the gateway port.
// Hooks and Middleware are in-process values the host passes in.
// Plugin rows in config are not loaded. How a plugin is loaded is open.
type Server struct {
	Store         *state.Store
	Log           *log.Logger
	Hooks         []RequestHook
	Middleware    []Middleware
	Transport     http.RoundTripper
	consoleListen atomic.Value
	stickyMu      sync.Mutex
	sticky        map[string]string
	// Login reads subscription sign-ins for catalog and dial. Nil reads none.
	Login func() login.Machine
	// Prices reads list prices for a gateway token's spend cap. Nil prices nothing.
	Prices price.Pricer
}

// ServeHTTP rejects console routes, requires a gateway token, then serves a
// dialect or fails closed when the path is not one.
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
	if console.IsConsolePath(r.URL.Path) {
		respond.Error(w, http.StatusForbidden, CodeRejected)
		return http.StatusForbidden
	}
	snap := (*state.Snapshot)(nil)
	if s.Store != nil {
		snap = s.Store.Current()
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/hello" {
		writeJSON(w, http.StatusOK, map[string]string{"name": "aigate"})
		return http.StatusOK
	}
	if snap == nil {
		respond.Error(w, http.StatusServiceUnavailable, CodeConfigUnreadable)
		return http.StatusServiceUnavailable
	}
	admitted, ok := admit(r, snap.Config)
	if !ok {
		respond.Error(w, http.StatusUnauthorized, string(state.ClassAuthFailure))
		return http.StatusUnauthorized
	}
	proto, isDialect := Classify(r)
	if !isDialect {
		respond.Error(w, http.StatusServiceUnavailable, CodeNoDialect)
		return http.StatusServiceUnavailable
	}
	if r.Method == http.MethodGet {
		writeCatalog(w, proto, snap.Config, admitted.allow, r.URL.Query().Get("kind"), s.loginEntries(r.URL.Query().Get("kind")))
		return http.StatusOK
	}
	return s.serveModel(w, r, snap, proto, admitted)
}
