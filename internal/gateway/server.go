package gateway

import (
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kuangjm123/AIGate/internal/console"
	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/respond"
	"github.com/kuangjm123/AIGate/internal/routes"
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
	// routeStrategies and routingMem back routing strategies; both are lazy.
	routingMu       sync.Mutex
	routeStrategies *routes.Builtin
	routingMem      *routingMemory
	// quota cache backs subscription seat fields. It never holds a credential
	// or a reply body.
	quotaMu   sync.Mutex
	quotaAt   time.Time
	quotaByID map[string]login.Quota
	quotaWait chan struct{}
	// Live traffic is process memory only. No ledger line is read.
	trafficActive atomic.Int64
	trafficIn     atomic.Int64
	trafficOut    atomic.Int64
	reportMu      sync.Mutex
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
	if r.Method == http.MethodPost && r.URL.Path == "/v1/magpie/usage/requests" {
		return s.reportUsage(w, r, snap, admitted)
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
	s.trafficActive.Add(1)
	defer s.trafficActive.Add(-1)
	if r.Body != nil {
		r.Body = &trafficReadCloser{ReadCloser: r.Body, n: &s.trafficIn}
	}
	tw := &trafficWriter{ResponseWriter: w, n: &s.trafficOut}
	return s.serveModel(tw, r, snap, proto, admitted)
}

// Traffic is the console's live view. It never reads the usage ledger.
func (s *Server) Traffic() console.TrafficSnapshot {
	return console.TrafficSnapshot{
		ActiveRequests: int(s.trafficActive.Load()),
		BytesIn:        s.trafficIn.Load(),
		BytesOut:       s.trafficOut.Load(),
		At:             time.Now().UTC(),
	}
}

type trafficReadCloser struct {
	io.ReadCloser
	n *atomic.Int64
}

func (r *trafficReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.n.Add(int64(n))
	}
	return n, err
}

type trafficWriter struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (w *trafficWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		w.n.Add(int64(n))
	}
	return n, err
}

func (w *trafficWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
