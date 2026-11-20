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

	"github.com/kuangjm123/aiconv"

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
		if !known || len(prov.Keys()) == 0 {
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
			usage := usageObject(dest, limited)
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
		status, usage, output, complete := s.writeReply(w, r, up, proto, dest, parsed, call, told, ancillary)
		outcome = OutcomeSuccess
		if status < 200 || status >= 300 {
			outcome = string(state.ClassUpstreamUnavailable)
		}
		s.note(snap, meta, route.id, providerID, outcome, started, usage)
		if s.Store != nil && complete && !ancillary && call.Session != "" && status >= 200 && status < 300 {
			_ = s.Store.AppendTurn(state.TurnRecord{
				Session:    call.Session,
				At:         time.Now().UTC(),
				Model:      meta.Model,
				Caller:     meta.Caller,
				ProviderID: providerID,
				Protocol:   meta.Protocol,
				Input:      lastUserText(parsed.Messages),
				Output:     output,
			})
		}
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
	keys := prov.Keys()
	if len(keys) == 0 {
		keys = []string{""}
	}
	var last *http.Response
	for i, key := range keys {
		resp, err := s.postKey(ctx, prov, proto, model, stream, body, key)
		if err != nil {
			if last != nil {
				last.Body.Close()
			}
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
			if last != nil {
				last.Body.Close()
			}
			return resp, nil
		}
		if last != nil {
			last.Body.Close()
		}
		last = resp
		if i == len(keys)-1 {
			return resp, nil
		}
	}
	return last, nil
}

func (s *Server) postKey(ctx context.Context, prov state.Provider, proto Protocol, model string, stream bool, body []byte, key string) (*http.Response, error) {
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
	applyProviderAuth(req, proto, key)
	return s.httpClient(prov.BaseURL).Do(req)
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

func (s *Server) writeReply(w http.ResponseWriter, r *http.Request, up *http.Response, inbound, dest Protocol, parsed Parsed, call CallContext, told []RequestHook, ancillary bool) (int, json.RawMessage, string, bool) {
	defer up.Body.Close()
	stream := parsed.Stream || strings.Contains(strings.ToLower(up.Header.Get("Content-Type")), "text/event-stream")
	if stream {
		status, usage, output, complete := s.writeStream(w, r.Context(), up, inbound, dest, parsed.Model, call, told, ancillary)
		return status, usage, output, complete
	}
	body, err := readLimited(up.Body, bodyLimit)
	if err != nil {
		writeProtocolError(w, inbound, http.StatusBadGateway, string(state.ClassUpstreamUnavailable), string(state.ClassUpstreamUnavailable))
		s.observe(told, call, nil, false)
		return http.StatusBadGateway, nil, "", false
	}
	status := up.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	out := body
	if dest != inbound {
		converted, cerr := aiconv.ConvertReply(aiconv.Protocol(dest), aiconv.Protocol(inbound), body, &aiconv.Options{Model: parsed.Model})
		if cerr != nil {
			writeProtocolError(w, inbound, http.StatusBadGateway, string(state.ClassUpstreamUnavailable), cerr.Error())
			s.observe(told, call, nil, false)
			return http.StatusBadGateway, usageObject(dest, body), "", false
		}
		out = converted
	}
	if !ancillary {
		status, out = s.rewriteReply(r.Context(), status, out, call)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(out)
	complete := status >= 200 && status < 300 && len(out) <= captureLimit
	s.observe(told, call, out, complete)
	output := ""
	if complete {
		output = replyText(inbound, out)
	}
	return status, usageObject(dest, body), output, complete
}

func (s *Server) writeStream(w http.ResponseWriter, ctx context.Context, up *http.Response, inbound, dest Protocol, model string, call CallContext, told []RequestHook, ancillary bool) (int, json.RawMessage, string, bool) {
	if dest != inbound && inbound == ProtoOllama {
		w.Header().Set("Content-Type", "application/x-ndjson")
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
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
	usage := usageScan{proto: dest}
	editors := s.editors()
	if dest == inbound && len(editors) == 0 {
		truncated, err := copyFlush(io.MultiWriter(&usage, w), up.Body, &captured, flush)
		usage.finish()
		if err != nil && !errors.Is(err, io.EOF) {
			success = false
		}
		complete := success && !truncated && err == nil
		output := ""
		if complete {
			output = joinedStreamText(dest, captured.Bytes())
		}
		s.observe(told, call, captureBytes(captured.Bytes(), success), complete)
		if ancillary {
			return http.StatusOK, usage.object(), "", false
		}
		return http.StatusOK, usage.object(), output, complete
	}
	if dest != inbound {
		return s.convertStream(w, up, inbound, dest, model, call, told, ancillary, flush)
	}
	var text strings.Builder
	err := readSSE(up.Body, func(data []byte) error {
		usage.see(data)
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return writeSSE(w, clientEvent(inbound, model, text.String(), true), flush)
		}
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
		text.WriteString(deltaText(inbound, out))
		return writeSSE(w, out, flush)
	})
	if err != nil && !errors.Is(err, io.EOF) {
		success = false
	}
	complete := success && text.Len() <= captureLimit
	output := ""
	if complete && !ancillary {
		output = text.String()
	}
	if ancillary {
		return http.StatusOK, usage.object(), "", false
	}
	s.observe(told, call, nil, false)
	return http.StatusOK, usage.object(), output, complete
}

func (s *Server) convertStream(w http.ResponseWriter, up *http.Response, inbound, dest Protocol, model string, call CallContext, told []RequestHook, ancillary bool, flush func()) (int, json.RawMessage, string, bool) {
	pr, pw := io.Pipe()
	usage := usageScan{proto: dest}
	var captured capWriter
	captured.limit = captureLimit
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := io.Copy(io.MultiWriter(&usage, &captured, pw), up.Body)
		usage.finish()
		_ = pw.CloseWithError(err)
	}()
	streamErr := aiconv.ConvertStream(aiconv.Protocol(dest), aiconv.Protocol(inbound), pr, flushWriter{w: w, flush: flush}, &aiconv.Options{Model: model})
	_ = pr.Close()
	<-done
	if ancillary {
		return http.StatusOK, usage.object(), "", false
	}
	if streamErr != nil || captured.trunc {
		s.observe(told, call, nil, false)
		return http.StatusOK, usage.object(), "", false
	}
	output := spokenText(dest, captured.buf.Bytes())
	observed, err := aiconv.ConvertReply(aiconv.Protocol(dest), aiconv.Protocol(inbound), captured.buf.Bytes(), &aiconv.Options{Model: model})
	if err != nil || len(observed) > captureLimit {
		observed = nil
	}
	s.observe(told, call, observed, observed != nil)
	return http.StatusOK, usage.object(), output, true
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

// usageObject is the provider's token-count object on a whole JSON reply.
// A missing object is nil. It is not stored as zero.
func usageObject(proto Protocol, body []byte) json.RawMessage {
	return eventUsage(proto, body)
}

// eventUsage reads one provider event. Chat and responses use usage.
// A responses event nests it under response. Gemini names it usageMetadata.
// Anthropic puts input counts on message.usage and output counts on usage;
// both are returned so the caller can lay the later object over the earlier one.
func eventUsage(proto Protocol, data []byte) json.RawMessage {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(data, &doc) != nil {
		return nil
	}
	switch proto {
	case ProtoGemini:
		if u := objectField(doc["usageMetadata"]); u != nil {
			return u
		}
		return objectField(doc["usage"])
	case ProtoResponses:
		if u := nestedUsage(doc["response"]); u != nil {
			return u
		}
		return objectField(doc["usage"])
	case ProtoAnthropic:
		u := nestedUsage(doc["message"])
		if top := objectField(doc["usage"]); top != nil {
			u = mergeUsage(u, top)
		}
		return u
	default:
		return objectField(doc["usage"])
	}
}

func nestedUsage(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	return objectField(doc["usage"])
}

func objectField(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) || raw[0] != '{' {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func mergeUsage(base, next json.RawMessage) json.RawMessage {
	if len(next) == 0 {
		return base
	}
	if len(base) == 0 {
		return append(json.RawMessage(nil), next...)
	}
	var a, b map[string]json.RawMessage
	if json.Unmarshal(base, &a) != nil || json.Unmarshal(next, &b) != nil {
		return append(json.RawMessage(nil), next...)
	}
	for k, v := range b {
		a[k] = append(json.RawMessage(nil), v...)
	}
	out, err := json.Marshal(a)
	if err != nil {
		return append(json.RawMessage(nil), next...)
	}
	return out
}

// usageScan keeps the provider usage seen on a stream.
// Chat, responses, and Gemini keep the last object. Anthropic lays each
// later object over the earlier one, because input and output arrive apart.
type usageScan struct {
	proto Protocol
	raw   json.RawMessage
	data  bytes.Buffer
	rest  []byte
}

func (s *usageScan) Write(p []byte) (int, error) {
	s.feed(p, false)
	return len(p), nil
}

func (s *usageScan) finish() {
	s.feed(nil, true)
}

func (s *usageScan) feed(p []byte, eof bool) {
	if len(p) > 0 {
		s.rest = append(s.rest, p...)
	}
	for {
		i := bytes.IndexByte(s.rest, '\n')
		if i < 0 {
			break
		}
		line := s.rest[:i]
		s.rest = s.rest[i+1:]
		s.line(line)
	}
	if !eof {
		return
	}
	if len(s.rest) > 0 {
		s.line(s.rest)
		s.rest = nil
	}
	s.flushEvent()
}

func (s *usageScan) line(line []byte) {
	line = bytes.TrimRight(line, "\r")
	if len(line) == 0 {
		s.flushEvent()
		return
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	piece := bytes.TrimPrefix(line, []byte("data:"))
	piece = bytes.TrimPrefix(piece, []byte(" "))
	if s.data.Len() > 0 {
		s.data.WriteByte('\n')
	}
	_, _ = s.data.Write(piece)
}

func (s *usageScan) flushEvent() {
	if s.data.Len() == 0 {
		return
	}
	s.see(s.data.Bytes())
	s.data.Reset()
}

func (s *usageScan) see(event []byte) {
	if !bytes.Contains(event, []byte("usage")) {
		return
	}
	next := eventUsage(s.proto, event)
	if next == nil {
		return
	}
	if s.proto == ProtoAnthropic {
		s.raw = mergeUsage(s.raw, next)
		return
	}
	s.raw = next
}

func (s *usageScan) object() json.RawMessage {
	if len(s.raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), s.raw...)
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

func copyFlush(dst io.Writer, src io.Reader, captured *bytes.Buffer, flush func()) (bool, error) {
	buf := make([]byte, 32*1024)
	truncated := false
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return truncated, werr
			}
			if captured != nil && captured.Len() < captureLimit {
				remain := captureLimit - captured.Len()
				if n > remain {
					truncated = true
					n = remain
				}
				_, _ = captured.Write(buf[:n])
			} else if captured != nil {
				truncated = true
			}
			flush()
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return truncated, nil
			}
			return truncated, err
		}
	}
}

func joinedStreamText(proto Protocol, raw []byte) string {
	var b strings.Builder
	_ = readSSE(bytes.NewReader(raw), func(data []byte) error {
		if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			return nil
		}
		b.WriteString(deltaText(proto, data))
		return nil
	})
	return b.String()
}

func lastUserText(messages []turn) string {
	text := ""
	for _, msg := range messages {
		if msg.Role == "user" && msg.Text != "" {
			text = msg.Text
		}
	}
	return text
}

type flushWriter struct {
	w     io.Writer
	flush func()
}

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if n > 0 && f.flush != nil {
		f.flush()
	}
	return n, err
}

type capWriter struct {
	buf   bytes.Buffer
	limit int
	trunc bool
}

func (c *capWriter) Write(p []byte) (int, error) {
	if c.buf.Len() >= c.limit {
		c.trunc = true
		return len(p), nil
	}
	remain := c.limit - c.buf.Len()
	if len(p) > remain {
		c.trunc = true
		_, _ = c.buf.Write(p[:remain])
		return len(p), nil
	}
	_, _ = c.buf.Write(p)
	return len(p), nil
}

func spokenText(proto Protocol, raw []byte) string {
	if text := joinedStreamText(proto, raw); text != "" {
		return text
	}
	reply, err := aiconv.DecodeReply(aiconv.Protocol(proto), raw)
	if err != nil {
		return replyText(proto, raw)
	}
	var b strings.Builder
	for _, part := range reply.Parts {
		if part.Kind == aiconv.Text {
			b.WriteString(part.Text)
		}
	}
	return b.String()
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
