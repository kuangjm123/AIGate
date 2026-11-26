// Package app runs one process with a gateway listener and a console listener.
// The two ports share the config snapshot and the usage store. They do not call
// each other over HTTP.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kuangjm123/AIGate/internal/console"
	"github.com/kuangjm123/AIGate/internal/gateway"
	"github.com/kuangjm123/AIGate/internal/login"
	"github.com/kuangjm123/AIGate/internal/price"
	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

// ErrSameListen means both listeners would use one host and port.
// Start leaves both closed and does not rewrite the config file.
var ErrSameListen = errors.New("same listen address")

// App is the process.
type App struct {
	store   *state.Store
	gateway *gateway.Server
	console *console.Server
	log     *log.Logger
	bindMu  sync.RWMutex
	gw      port
	cs      port
}

type port struct {
	addr string
	ln   net.Listener
	srv  *http.Server
}

// New wires the two servers around store. logger may be nil.
func New(store *state.Store, logger *log.Logger) *App {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	a := &App{store: store, log: logger}
	book := price.NewBook()
	a.gateway = &gateway.Server{Store: store, Log: logger, Prices: book}
	a.console = &console.Server{
		Store: store, Host: a, Log: logger, Prices: book, Traffic: a.gateway.Traffic,
		ResetSeat: a.gateway.ResetSeat,
		Probe: func(ctx context.Context, prov state.Provider, consoleListen string) console.ProbeResult {
			got := a.gateway.Probe(ctx, prov, consoleListen)
			return console.ProbeResult{OK: got.OK, Status: got.Status, Protocol: got.Protocol, Model: got.Model, Error: got.Error}
		},
		Detect: func(ctx context.Context, base, key, consoleListen string) (string, string) {
			got := a.gateway.Detect(ctx, base, key, consoleListen)
			return got.Protocol, got.Error
		},
		FetchModels: func(ctx context.Context, base, protocol, key, consoleListen string) ([]string, string) {
			got := a.gateway.FetchModels(ctx, base, protocol, key, consoleListen)
			return got.Models, got.Error
		},
		CheckModels: func(ctx context.Context, prov state.Provider, consoleListen string) []console.ModelReport {
			rows := a.gateway.CheckModels(ctx, prov, consoleListen)
			out := make([]console.ModelReport, len(rows))
			for i, row := range rows {
				out[i] = console.ModelReport{Model: row.Model, OK: row.OK, Status: row.Status, Error: row.Error}
			}
			return out
		},
	}
	return a
}

// Start loads config and opens the listeners.
// An unreadable config still binds the in-memory defaults so the console can
// report it, and does not write those defaults to disk.
func (a *App) Start() error {
	if err := a.store.Load(); err != nil {
		if !errors.Is(err, state.ErrUnreadable) {
			return err
		}
		a.log.Printf("config unreadable")
	}
	a.bindMu.Lock()
	defer a.bindMu.Unlock()
	if err := a.applyListeners(); err != nil {
		a.closeAll()
		return err
	}
	return nil
}

// Shutdown stops both listeners.
func (a *App) Shutdown(ctx context.Context) error {
	a.bindMu.Lock()
	defer a.bindMu.Unlock()
	var err error
	if a.gw.srv != nil {
		err = a.gw.srv.Shutdown(ctx)
	}
	if a.cs.srv != nil {
		if e := a.cs.srv.Shutdown(ctx); err == nil {
			err = e
		}
	}
	a.gw = port{}
	a.cs = port{}
	return err
}

// Update publishes a new config, then rebinds listeners when their addresses changed.
// On a rebind failure it puts the previous config back.
func (a *App) Update(fn func(*state.Config) error) error {
	a.bindMu.Lock()
	defer a.bindMu.Unlock()
	cur := a.store.Current()
	if cur == nil {
		return state.ErrUnreadable
	}
	old := cur.Config.Clone()
	if err := a.store.Update(fn); err != nil {
		return err
	}
	if err := a.applyListeners(); err != nil {
		rb := a.store.Update(func(cfg *state.Config) error {
			*cfg = old.Clone()
			return nil
		})
		if rb == nil {
			_ = a.applyListeners()
		}
		if rb != nil {
			return fmt.Errorf("%w (rollback: %v)", err, rb)
		}
		return err
	}
	return nil
}

// Status reports configured and effective listen addresses.
func (a *App) Status() console.ListenStatus {
	a.bindMu.RLock()
	defer a.bindMu.RUnlock()
	return a.statusLocked()
}

func (a *App) statusLocked() console.ListenStatus {
	var listeners state.Listeners
	if snap := a.store.Current(); snap != nil {
		listeners = snap.Config.Listeners
	}
	var st console.ListenStatus
	st.Gateway.Configured = listeners.Gateway
	if a.gw.ln != nil {
		st.Gateway.Listening = true
		st.Gateway.Effective = a.gw.ln.Addr().String()
	}
	st.Console.Configured = listeners.Console
	if a.cs.ln != nil {
		st.Console.Listening = true
		st.Console.Effective = a.cs.ln.Addr().String()
	}
	if snap := a.store.Current(); snap != nil {
		st.Share = snap.Config.Share
	}
	return st
}

// GatewayURL is the base URL of the gateway listener, or empty when it is closed.
func (a *App) GatewayURL() string {
	a.bindMu.RLock()
	defer a.bindMu.RUnlock()
	if a.gw.ln == nil {
		return ""
	}
	return "http://" + a.gw.ln.Addr().String()
}

// ConsoleURL is the base URL of the console listener, or empty when it is closed.
func (a *App) ConsoleURL() string {
	a.bindMu.RLock()
	defer a.bindMu.RUnlock()
	if a.cs.ln == nil {
		return ""
	}
	return "http://" + a.cs.ln.Addr().String()
}

func (a *App) applyListeners() error {
	var listeners state.Listeners
	users := false
	share := false
	if snap := a.store.Current(); snap != nil {
		listeners = snap.Config.Listeners
		users = len(snap.Config.Users) > 0
		share = snap.Config.Share
	}
	gw := security.EffectiveListen(listeners.Gateway, security.DefaultGateway)
	cs := security.EffectiveListen(listeners.Console, security.DefaultConsole)
	a.gateway.SetConsoleListen(cs)
	if security.SameListen(gw, cs) {
		return ErrSameListen
	}
	if security.GatewayMayListen(gw, share) {
		if err := a.ensure(&a.gw, gw, a.gateway); err != nil {
			return err
		}
	} else {
		a.gw.close()
	}
	if security.ConsoleMayListen(cs, users) {
		return a.ensure(&a.cs, cs, a.console)
	}
	a.cs.close()
	return nil
}

func (a *App) ensure(p *port, addr string, h http.Handler) error {
	if p.ln != nil && security.SameListen(p.addr, addr) {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		a.log.Printf("listen failed")
		return console.ErrListen
	}
	srv := &http.Server{Handler: h, ErrorLog: log.New(io.Discard, "", 0)}
	go func() {
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.log.Printf("listener stopped")
		}
	}()
	old := p.srv
	p.ln = ln
	p.srv = srv
	p.addr = addr
	// Shutdown waits for the request that asked for the rebind. Do that wait
	// off this stack so the handler can finish writing its response.
	if old != nil {
		go shutdownServer(old)
	}
	return nil
}

func (p *port) close() {
	if p == nil || p.srv == nil {
		return
	}
	srv := p.srv
	*p = port{}
	go shutdownServer(srv)
}

func shutdownServer(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func (a *App) closeAll() {
	a.gw.close()
	a.cs.close()
}

// EnableAccounts turns on reading subscription sign-ins from this computer.
// Tests leave it unset so they do not touch a real agent login.
func (a *App) EnableAccounts() {
	a.gateway.Login = a.loginMachine
	a.console.Logins = a.loginMachine
}

func (a *App) loginMachine() login.Machine {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	machine := login.Machine{Home: home, WorkBuddyAuth: login.DesktopAuthDir()}
	if paths := a.store.Paths(); paths.ConfigFile != "" {
		machine.WorkBuddyStore = filepath.Join(filepath.Dir(paths.ConfigFile), "workbuddy-auth.json")
	}
	if snap := a.store.Current(); snap != nil {
		machine.Off = snap.Config.NoAccountDiscovery
		for _, plugin := range snap.Config.Plugins {
			if plugin.Enabled {
				machine.PluginIDs = append(machine.PluginIDs, plugin.ID)
			}
		}
	}
	return machine
}
