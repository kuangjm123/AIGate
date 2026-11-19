package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

const (
	hookTimeout       = time.Second
	middlewareTimeout = 250 * time.Millisecond
	bodyLimit         = 32 << 20
	captureLimit      = 8 << 20
)

var (
	errBadBody = errors.New("bad body")
	errTools   = errors.New("tools")
	errHook    = errors.New("hook")
)

// CallContext is what a hook or middleware may see. It has no secrets.
type CallContext struct {
	Protocol Protocol `json:"protocol"`
	Model    string   `json:"model"`
	Stream   bool     `json:"stream"`
	Path     string   `json:"path"`
	Origin   string   `json:"origin"`
	Agent    string   `json:"agent"`
	Session  string   `json:"session"`
	Kind     string   `json:"kind"`
}

// Reject is a middleware decision to stop this request.
type Reject struct {
	Status  int
	Message string
}

// RequestHook adds context before routing and observes the reply afterward.
// It cannot reject the request. A failure, a timeout, or a change to the model,
// the stream flag, or the tool declaration restores the previous body.
// A hook that failed or was rolled back is not told about the reply.
type RequestHook interface {
	OnRequest(ctx context.Context, body json.RawMessage, call CallContext) (json.RawMessage, error)
	OnResponse(ctx context.Context, reply json.RawMessage, call CallContext, success bool)
}

// Middleware rewrites or rejects a request, rewrites one provider attempt,
// and may rewrite a finished JSON reply. A failure leaves the bytes unchanged.
// OnTry starts from the body left by OnRequest, not from the previous provider.
type Middleware interface {
	OnRequest(ctx context.Context, body json.RawMessage, call CallContext) (json.RawMessage, *Reject)
	OnTry(ctx context.Context, body json.RawMessage, call CallContext, providerID string, protocol Protocol) json.RawMessage
	OnReply(ctx context.Context, status int, body json.RawMessage, call CallContext) (int, json.RawMessage)
}

// EventEditor rewrites one streamed JSON event. drop omits the event.
// A nil out keeps the original bytes.
type EventEditor interface {
	EditEvent(data []byte) (out []byte, drop bool)
}

// SetConsoleListen records the console address a provider URL must not target.
func (s *Server) SetConsoleListen(addr string) {
	if s == nil {
		return
	}
	s.consoleListen.Store(addr)
}

func (s *Server) consoleAddr() string {
	if s == nil {
		return ""
	}
	v := s.consoleListen.Load()
	addr, _ := v.(string)
	return addr
}

func (s *Server) serveModel(w http.ResponseWriter, r *http.Request, snap *state.Snapshot, proto Protocol, admitted admission) int {
	body, tooBig := readBody(r.Body)
	if tooBig {
		writeProtocolError(w, proto, http.StatusRequestEntityTooLarge, "too_large", "too_large")
		return http.StatusRequestEntityTooLarge
	}
	parsed, err := Parse(proto, r, body)
	if err != nil {
		writeProtocolError(w, proto, http.StatusBadRequest, "bad_request", "bad_request")
		return http.StatusBadRequest
	}
	call := callContext(r, parsed)
	ancillary := call.Kind == "title" || call.Kind == "summary" || call.Kind == "compact"
	accepted := append([]byte(nil), parsed.Raw...)
	var told []RequestHook
	if !ancillary {
		var hookErr error
		accepted, told, hookErr = s.runHooks(r.Context(), proto, r, parsed, accepted, call)
		if hookErr != nil {
			return http.StatusBadGateway
		}
		var status int
		var rejected bool
		accepted, status, rejected = s.runMiddleware(r.Context(), proto, accepted, call, w)
		if rejected {
			return status
		}
		again, perr := Parse(proto, r, accepted)
		if perr != nil {
			writeProtocolError(w, proto, http.StatusBadRequest, "bad_request", "bad_request")
			return http.StatusBadRequest
		}
		parsed = again
		call.Model = parsed.Model
		call.Stream = parsed.Stream
	}
	meta := attemptMeta{Model: parsed.Model, Session: call.Session, TokenID: admitted.tokenID, Caller: admitted.caller, Protocol: string(proto)}
	if code := gateModel(snap.Config, parsed.Model, admitted.allow); code != "" {
		status := http.StatusNotFound
		message := code
		if code == CodeModelDenied {
			status = http.StatusForbidden
		}
		if code == CodeNotChat {
			status = http.StatusBadRequest
			message = "模型不走对话接口"
		}
		if code == CodeHidden {
			message = "模型已隐藏"
		}
		writeProtocolError(w, proto, status, code, message)
		return status
	}
	route, ids := candidates(snap.Config, parsed.Model)
	if hasMedia(accepted) {
		ids = withoutBlind(snap.Config, parsed.Model, ids)
		if len(ids) == 0 {
			writeProtocolError(w, proto, http.StatusBadRequest, CodeMedia, "模型不接受图片或视频")
			return http.StatusBadRequest
		}
	}
	if !ancillary {
		ids = s.stickFirst(call.Session, ids)
	}
	if route.id == "" && len(ids) == 0 && !route.found {
		writeProtocolError(w, proto, http.StatusNotFound, CodeNoRoute, CodeNoRoute)
		return http.StatusNotFound
	}
	if len(ids) == 0 {
		writeProtocolError(w, proto, http.StatusBadGateway, CodeNoProvider, CodeNoProvider)
		return http.StatusBadGateway
	}
	var last codeResult
	dialed := false
	for _, providerID := range ids {
		if err := r.Context().Err(); err != nil {
			return last.statusOr(http.StatusBadGateway)
		}
		started := time.Now().UTC()
		prov, pok := snap.Config.Provider(providerID)
		if !pok {
			s.note(snap, meta, route.id, providerID, CodeUnknown, started, nil)
			writeProtocolError(w, proto, http.StatusBadGateway, CodeUnknown, CodeUnknown)
			return http.StatusBadGateway
		}
		dest, known := providerProtocol(prov.Protocol, proto)
		if !known || strings.TrimSpace(prov.Credential) == "" {
			s.note(snap, meta, route.id, providerID, CodeUnknown, started, nil)
			writeProtocolError(w, proto, http.StatusBadGateway, CodeUnknown, CodeUnknown)
			return http.StatusBadGateway
		}
		switch security.CheckBaseURL(prov.BaseURL, s.consoleAddr(), "") {
		case security.BaseOK:
		case security.BaseUnknown:
			s.note(snap, meta, route.id, providerID, CodeUnknown, started, nil)
			writeProtocolError(w, proto, http.StatusBadGateway, CodeUnknown, CodeUnknown)
			return http.StatusBadGateway
		default:
			s.note(snap, meta, route.id, providerID, CodeBaseURL, started, nil)
			writeProtocolError(w, proto, http.StatusBadGateway, CodeBaseURL, CodeBaseURL)
			return http.StatusBadGateway
		}
		upModel := upstreamModel(snap.Config, parsed.Model, providerID)
		outbound, err := outboundBody(parsed, accepted, dest, upModel)
		if err != nil {
			continue
		}
		if !ancillary {
			outbound = s.rewriteTry(r.Context(), outbound, call, prov.ID, dest)
		}
		up, err := s.postProvider(r.Context(), prov, dest, upModel, parsed.Stream, outbound)
		if err != nil && r.Context().Err() != nil {
			s.note(snap, meta, route.id, providerID, CodeUnknown, started, nil)
			return last.statusOr(http.StatusBadGateway)
		}
		dialed = true
		if err != nil {
			last = s.fail(w, snap, proto, route.allows, meta, route.id, providerID, string(state.ClassUpstreamUnavailable), started, nil)
			if last.stopped {
				return last.status
			}
			continue
		}
		outcome := classifyStatus(up.StatusCode)
		if outcome != "" || up.StatusCode < 200 || up.StatusCode >= 300 {
			limited, _ := readLimited(up.Body, captureLimit)
			up.Body.Close()
			usage := usageObject(limited)
			class := outcome
			if class == "" {
				s.note(snap, meta, route.id, providerID, "http_"+itoa(up.StatusCode), started, usage)
				writeUpstreamOrError(w, proto, up.StatusCode, limited)
				return up.StatusCode
			}
			last = s.fail(w, snap, proto, route.allows, meta, route.id, providerID, class, started, usage)
			if last.stopped {
				return last.status
			}
			continue
		}
		s.remember(call.Session, providerID)
		status, usage := s.writeReply(w, r, up, proto, dest, parsed, call, told, ancillary)
		outcome = OutcomeSuccess
		if status < 200 || status >= 300 {
			outcome = string(state.ClassUpstreamUnavailable)
		}
		s.note(snap, meta, route.id, providerID, outcome, started, usage)
		return status
	}
	if !dialed {
		writeProtocolError(w, proto, http.StatusBadGateway, "unsupported", "unsupported")
		return http.StatusBadGateway
	}
	if last.status == 0 {
		writeProtocolError(w, proto, http.StatusBadGateway, CodeNoProvider, CodeNoProvider)
		return http.StatusBadGateway
	}
	return last.status
}

type routePick struct {
	id     string
	allows func(string) bool
	found  bool
}

func candidates(cfg state.Config, model string) (routePick, []string) {
	if route, ok := cfg.RouteByModel(model); ok {
		ids := append([]string(nil), route.ProviderIDs...)
		return routePick{id: route.ID, found: true, allows: func(class string) bool {
			return route.Allows(state.ErrorClass(class))
		}}, ids
	}
	allows := func(class string) bool {
		return class == string(state.ClassUpstreamUnavailable)
	}
	if providerID, name, ok := cfg.MatchCatalog(model); ok {
		if provider, pok := cfg.Provider(providerID); pok && provider.ChatModel(name) {
			return routePick{allows: allows}, []string{providerID}
		}
		return routePick{allows: allows}, nil
	}
	var ids []string
	for _, provider := range cfg.Providers {
		if provider.ChatModel(model) {
			ids = append(ids, provider.ID)
		}
	}
	return routePick{allows: allows}, ids
}

type attemptMeta struct {
	Model    string
	Session  string
	TokenID  string
	Caller   string
	Protocol string
}

func (s *Server) note(snap *state.Snapshot, meta attemptMeta, routeID, providerID, outcome string, started time.Time, usage json.RawMessage) {
	if s == nil || s.Store == nil {
		return
	}
	_ = s.Store.AppendUsage(state.AttemptRecord{
		RouteID:    routeID,
		ProviderID: providerID,
		Outcome:    outcome,
		Started:    started,
		Finished:   time.Now().UTC(),
		Usage:      cloneRaw(usage),
		Model:      meta.Model,
		Session:    meta.Session,
		TokenID:    meta.TokenID,
		Caller:     meta.Caller,
		Protocol:   meta.Protocol,
	})
}

type codeResult struct {
	status  int
	stopped bool
}

func (c codeResult) statusOr(fallback int) int {
	if c.status == 0 {
		return fallback
	}
	return c.status
}

func (s *Server) fail(w http.ResponseWriter, snap *state.Snapshot, proto Protocol, allows func(string) bool, meta attemptMeta, routeID, providerID, class string, started time.Time, usage json.RawMessage) codeResult {
	s.note(snap, meta, routeID, providerID, class, started, usage)
	if allows != nil && allows(class) {
		return codeResult{status: statusFor(class)}
	}
	writeProtocolError(w, proto, statusFor(class), class, class)
	return codeResult{status: statusFor(class), stopped: true}
}

func classifyStatus(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return string(state.ClassAuthFailure)
	case http.StatusTooManyRequests:
		return string(state.ClassRateLimit)
	case http.StatusPaymentRequired:
		return string(state.ClassEmptyBalance)
	}
	if status == http.StatusRequestTimeout || status >= 500 {
		return string(state.ClassUpstreamUnavailable)
	}
	return ""
}

func (s *Server) postProvider(ctx context.Context, prov state.Provider, proto Protocol, model string, stream bool, body []byte) (*http.Response, error) {
	target, err := endpoint(prov.BaseURL, proto, model, stream)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+prov.Credential)
	switch proto {
	case ProtoAnthropic:
		req.Header.Set("x-api-key", prov.Credential)
		req.Header.Set("anthropic-version", "2023-06-01")
	case ProtoGemini:
		req.Header.Set("x-goog-api-key", prov.Credential)
	}
	client := s.httpClient(prov.BaseURL)
	return client.Do(req)
}

func (s *Server) httpClient(base string) *http.Client {
	transport := http.DefaultTransport
	if s != nil && s.Transport != nil {
		transport = s.Transport
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 || !security.AllowRedirect(base, req.URL.String()) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func (s *Server) writeReply(w http.ResponseWriter, r *http.Request, up *http.Response, inbound, dest Protocol, parsed Parsed, call CallContext, told []RequestHook, ancillary bool) (int, json.RawMessage) {
	defer up.Body.Close()
	stream := parsed.Stream || strings.Contains(strings.ToLower(up.Header.Get("Content-Type")), "text/event-stream")
	if stream {
		return s.writeStream(w, r.Context(), up, inbound, dest, parsed.Model, call, told, ancillary), nil
	}
	body, err := readLimited(up.Body, bodyLimit)
	if err != nil {
		writeProtocolError(w, inbound, http.StatusBadGateway, string(state.ClassUpstreamUnavailable), string(state.ClassUpstreamUnavailable))
		s.observe(told, call, nil, false)
		return http.StatusBadGateway, nil
	}
	status := up.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	out := body
	if dest != inbound {
		out = renderReply(inbound, parsed.Model, readReply(dest, body))
	}
	if !ancillary {
		status, out = s.rewriteReply(r.Context(), status, out, call)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(out)
	s.observe(told, call, out, len(out) <= captureLimit)
	return status, usageObject(body)
}

func (s *Server) writeStream(w http.ResponseWriter, ctx context.Context, up *http.Response, inbound, dest Protocol, model string, call CallContext, told []RequestHook, ancillary bool) int {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	var captured bytes.Buffer
	success := true
	editors := s.editors()
	if dest == inbound && len(editors) == 0 {
		err := copyFlush(w, up.Body, &captured, flush)
		if err != nil && !errors.Is(err, io.EOF) {
			success = false
		}
		s.observe(told, call, captureBytes(captured.Bytes(), success), success && captured.Len() <= captureLimit && err == nil)
		return http.StatusOK
	}
	var text strings.Builder
	err := readSSE(up.Body, func(data []byte) error {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return writeSSE(w, clientEvent(inbound, model, text.String(), true), flush)
		}
		piece := eventPieces(dest, data)
		if dest == inbound {
			out := data
			drop := false
			for _, editor := range editors {
				next, omit := editor.EditEvent(out)
				if omit {
					drop = true
					break
				}
				if next != nil {
					out = next
				}
			}
			if drop {
				return nil
			}
			return writeSSE(w, out, flush)
		}
		if piece.empty() {
			return nil
		}
		text.WriteString(piece.text)
		for _, event := range clientPieces(inbound, model, piece) {
			if err := writeSSE(w, event, flush); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, io.EOF) {
		success = false
	} else if dest != inbound {
		_ = writeSSE(w, clientEvent(inbound, model, text.String(), true), flush)
	}
	if ancillary {
		return http.StatusOK
	}
	if dest == inbound {
		s.observe(told, call, nil, false)
		return http.StatusOK
	}
	reply := clientBody(inbound, model, text.String())
	s.observe(told, call, reply, success && len(reply) <= captureLimit)
	return http.StatusOK
}

func (s *Server) editors() []EventEditor {
	var out []EventEditor
	for _, item := range s.Middleware {
		if editor, ok := item.(EventEditor); ok && editor != nil {
			out = append(out, editor)
		}
	}
	return out
}

func (s *Server) runHooks(ctx context.Context, proto Protocol, r *http.Request, parsed Parsed, body []byte, call CallContext) ([]byte, []RequestHook, error) {
	var told []RequestHook
	for _, hook := range s.Hooks {
		if hook == nil {
			continue
		}
		next, err := callWithTimeout(ctx, hookTimeout, func(ctx context.Context) (json.RawMessage, error) {
			return hook.OnRequest(ctx, json.RawMessage(body), call)
		})
		if ctx.Err() != nil {
			return body, told, ctx.Err()
		}
		if err != nil {
			continue
		}
		if len(bytes.TrimSpace(next)) == 0 {
			told = append(told, hook)
			continue
		}
		again, perr := Parse(proto, r, next)
		if perr != nil || !sameShape(parsed, again) {
			continue
		}
		body = append([]byte(nil), next...)
		parsed = again
		call.Model = parsed.Model
		call.Stream = parsed.Stream
		told = append(told, hook)
	}
	return body, told, nil
}

func (s *Server) runMiddleware(ctx context.Context, proto Protocol, body []byte, call CallContext, w http.ResponseWriter) ([]byte, int, bool) {
	for _, item := range s.Middleware {
		if item == nil {
			continue
		}
		type decision struct {
			body   json.RawMessage
			reject *Reject
		}
		ch := make(chan decision, 1)
		go func(item Middleware) {
			defer func() {
				if recover() != nil {
					ch <- decision{}
				}
			}()
			mctx, cancel := context.WithTimeout(ctx, middlewareTimeout)
			defer cancel()
			next, reject := item.OnRequest(mctx, json.RawMessage(body), call)
			ch <- decision{body: next, reject: reject}
		}(item)
		select {
		case <-ctx.Done():
			return body, 499, true
		case got := <-ch:
			if got.reject != nil {
				status := got.reject.Status
				if status == 0 {
					status = http.StatusBadRequest
				}
				writeProtocolError(w, proto, status, "rejected", got.reject.Message)
				return body, status, true
			}
			if json.Valid(got.body) && len(got.body) > 0 && got.body[0] == '{' {
				body = append([]byte(nil), got.body...)
			}
		case <-time.After(middlewareTimeout + 50*time.Millisecond):
		}
	}
	return body, 0, false
}

func (s *Server) rewriteTry(ctx context.Context, body []byte, call CallContext, providerID string, proto Protocol) []byte {
	current := append([]byte(nil), body...)
	for _, item := range s.Middleware {
		if item == nil {
			continue
		}
		func() {
			defer func() { _ = recover() }()
			mctx, cancel := context.WithTimeout(ctx, middlewareTimeout)
			defer cancel()
			next := item.OnTry(mctx, json.RawMessage(current), call, providerID, proto)
			if json.Valid(next) && len(next) > 0 && next[0] == '{' {
				current = append([]byte(nil), next...)
			}
		}()
	}
	return current
}

func (s *Server) rewriteReply(ctx context.Context, status int, body []byte, call CallContext) (int, []byte) {
	current := append([]byte(nil), body...)
	for _, item := range s.Middleware {
		if item == nil {
			continue
		}
		func() {
			defer func() { _ = recover() }()
			mctx, cancel := context.WithTimeout(ctx, middlewareTimeout)
			defer cancel()
			nextStatus, next := item.OnReply(mctx, status, json.RawMessage(current), call)
			if json.Valid(next) && len(next) > 0 && (next[0] == '{' || next[0] == '[') {
				current = append([]byte(nil), next...)
				if nextStatus != 0 {
					status = nextStatus
				}
			}
		}()
	}
	return status, current
}

func (s *Server) observe(hooks []RequestHook, call CallContext, reply []byte, success bool) {
	if len(hooks) == 0 {
		return
	}
	saved := append([]byte(nil), reply...)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
		defer cancel()
		for _, hook := range hooks {
			if hook == nil {
				continue
			}
			func() {
				defer func() { _ = recover() }()
				hook.OnResponse(ctx, json.RawMessage(saved), call, success)
			}()
		}
	}()
}

func (s *Server) stickFirst(session string, ids []string) []string {
	if session == "" || len(ids) < 2 {
		return ids
	}
	s.stickyMu.Lock()
	id := s.sticky[session]
	s.stickyMu.Unlock()
	if id == "" {
		return ids
	}
	out := make([]string, 0, len(ids))
	found := false
	for _, candidate := range ids {
		if candidate == id {
			found = true
			continue
		}
		out = append(out, candidate)
	}
	if !found {
		return ids
	}
	return append([]string{id}, out...)
}

func (s *Server) remember(session, providerID string) {
	if session == "" || providerID == "" {
		return
	}
	s.stickyMu.Lock()
	defer s.stickyMu.Unlock()
	if s.sticky == nil {
		s.sticky = map[string]string{}
	}
	s.sticky[session] = providerID
}

func callContext(r *http.Request, parsed Parsed) CallContext {
	return CallContext{
		Protocol: parsed.Protocol,
		Model:    parsed.Model,
		Stream:   parsed.Stream,
		Path:     r.URL.Path,
		Origin:   requestOrigin(r),
		Agent:    strings.TrimSpace(r.Header.Get("x-aigate-agent")),
		Session:  sessionHeader(r),
		Kind:     kindHeader(r),
	}
}

func sessionHeader(r *http.Request) string {
	for _, name := range []string{"x-session-id", "x-claude-code-session-id", "x-conversation-id", "conversation-id"} {
		if v := strings.TrimSpace(r.Header.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

func kindHeader(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("x-aigate-kind")); v != "" {
		return v
	}
	if strings.TrimSpace(r.Header.Get("x-claude-code-agent-id")) != "" {
		return "subagent"
	}
	if strings.Contains(strings.ToLower(r.Header.Get("x-openai-subagent")), "collab_spawn") {
		return "subagent"
	}
	return ""
}

func requestOrigin(r *http.Request) string {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" {
		return "remote"
	}
	host := r.RemoteAddr
	if split, _, err := net.SplitHostPort(host); err == nil {
		host = split
	}
	if security.IsLoopbackHost(host) {
		return "local"
	}
	return "remote"
}

func callWithTimeout(parent context.Context, timeout time.Duration, fn func(context.Context) (json.RawMessage, error)) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	type result struct {
		body json.RawMessage
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if recover() != nil {
				ch <- result{err: errHook}
			}
		}()
		body, err := fn(ctx)
		ch <- result{body: body, err: err}
	}()
	select {
	case got := <-ch:
		if errors.Is(got.err, context.DeadlineExceeded) && parent.Err() == nil {
			return nil, errHook
		}
		return got.body, got.err
	case <-ctx.Done():
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		return nil, errHook
	}
}

func readBody(src io.Reader) ([]byte, bool) {
	if src == nil {
		return nil, false
	}
	b, err := readLimited(src, bodyLimit+1)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, false
	}
	if len(b) > bodyLimit {
		return nil, true
	}
	return b, false
}

func readLimited(src io.Reader, limit int) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(src, int64(limit)+1))
	if len(b) > limit {
		return b[:limit], io.ErrUnexpectedEOF
	}
	if err != nil {
		return b, err
	}
	return b, nil
}

func usageObject(body []byte) json.RawMessage {
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return nil
	}
	raw := bytes.TrimSpace(doc["usage"])
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if raw[0] != '{' {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func writeUpstreamOrError(w http.ResponseWriter, proto Protocol, status int, body []byte) {
	if json.Valid(body) && len(bytes.TrimSpace(body)) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	writeProtocolError(w, proto, status, "upstream", "upstream")
}

func copyFlush(dst io.Writer, src io.Reader, captured *bytes.Buffer, flush func()) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			if captured != nil && captured.Len() < captureLimit {
				remain := captureLimit - captured.Len()
				if n > remain {
					n = remain
				}
				_, _ = captured.Write(buf[:n])
			}
			flush()
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func readSSE(src io.Reader, fn func([]byte) error) error {
	var data bytes.Buffer
	rest := make([]byte, 0)
	buf := make([]byte, 4096)
	flushEvent := func() error {
		if data.Len() == 0 {
			return nil
		}
		err := fn(data.Bytes())
		data.Reset()
		return err
	}
	for {
		n, err := src.Read(buf)
		rest = append(rest, buf[:n]...)
		for {
			i := bytes.IndexByte(rest, '\n')
			if i < 0 {
				break
			}
			line := rest[:i]
			rest = rest[i+1:]
			line = bytes.TrimRight(line, "\r")
			if len(line) == 0 {
				if ferr := flushEvent(); ferr != nil {
					return ferr
				}
				continue
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				piece := bytes.TrimPrefix(line, []byte("data:"))
				piece = bytes.TrimPrefix(piece, []byte(" "))
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.Write(piece)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return flushEvent()
			}
			return err
		}
	}
}

func writeSSE(w io.Writer, data []byte, flush func()) error {
	if len(data) == 0 {
		return nil
	}
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if _, err := w.Write([]byte("\n\n")); err != nil {
		return err
	}
	flush()
	return nil
}

func captureBytes(b []byte, success bool) []byte {
	if !success {
		return nil
	}
	return b
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
