package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/kuangjm123/AIGate/internal/login"
)

const (
	// Claude Code asks GET /v1/models with a three-second timeout. One slow
	// subscription sign-in must not keep the whole catalog from answering.
	loginCatalogWait    = 1200 * time.Millisecond
	loginRequestWait    = 2500 * time.Millisecond
	loginSnapshotFresh  = 15 * time.Second
	loginDiscoveryLimit = 3 * time.Second
)

// loginSnapshot returns the subscription accounts the gateway can see.
// A fresh snapshot is reused. An expired one is refreshed in the background,
// and the last good snapshot answers when the refresh is slower than wait.
// With no earlier snapshot a timeout is unknown, not an empty account list.
func (s *Server) loginSnapshot(ctx context.Context, wait time.Duration) login.Snapshot {
	if s == nil || s.Login == nil {
		return login.Snapshot{Status: "off"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if wait <= 0 {
		wait = time.Millisecond
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var last login.Snapshot
	haveLast := false
	for {
		s.loginMu.Lock()
		if !s.loginAt.IsZero() {
			last = s.loginSnap
			haveLast = true
			if time.Since(s.loginAt) < loginSnapshotFresh {
				s.loginMu.Unlock()
				return last
			}
		}
		done := s.loginWait
		if done == nil {
			done = make(chan struct{})
			s.loginWait = done
			go s.discoverLogin(done)
		}
		s.loginMu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			if haveLast {
				return last
			}
			return login.Snapshot{Status: "unknown"}
		case <-timer.C:
			if haveLast {
				return last
			}
			return login.Snapshot{Status: "unknown"}
		}
	}
}

// discoverLogin runs one discovery for every waiter. The short client timeout
// applies only to this read; a subscription call still uses the long-lived
// client from the original machine.
func (s *Server) discoverLogin(done chan struct{}) {
	snap := login.Snapshot{Status: "unknown"}
	defer func() {
		if recover() != nil {
			snap = login.Snapshot{Status: "unknown"}
		}
		s.loginMu.Lock()
		s.loginSnap = snap
		s.loginAt = time.Now()
		s.loginWait = nil
		s.loginMu.Unlock()
		close(done)
	}()
	machine := s.Login()
	machine.Client = discoveryHTTPClient(machine.Client)
	snap = login.Discover(machine)
}

func discoveryHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		return &http.Client{Timeout: loginDiscoveryLimit}
	}
	clone := *base
	if clone.Timeout == 0 || clone.Timeout > loginDiscoveryLimit {
		clone.Timeout = loginDiscoveryLimit
	}
	return &clone
}
