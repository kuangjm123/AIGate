package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kuangjm123/AIGate/internal/security"
	"github.com/kuangjm123/AIGate/internal/state"
)

const (
	// CodeNoRoute means the model matched no route.
	CodeNoRoute = "no_route"
	// CodeHidden means the model is in the catalog data and hidden.
	CodeHidden = "hidden"
	// CodeNotChat means the model is an image or video model on a conversation path.
	CodeNotChat = "not_chat"
	// CodeMedia means the request carries an image or video the model does not accept.
	CodeMedia = "media"
	// CodeModelDenied means this gateway token is not allowed to call the model.
	CodeModelDenied = "model_denied"
	// CodeNoProvider means the route has no provider to dial.
	CodeNoProvider = "no_provider"
	// CodeNoDialect means no model dialect is registered.
	CodeNoDialect = "no_dialect"
	// CodeRejected means the request is on the wrong port.
	CodeRejected = "rejected"
	// CodeConfigUnreadable means there is no good config snapshot.
	CodeConfigUnreadable = "config_unreadable"
	// CodeBaseURL means the provider base URL was not dialed.
	CodeBaseURL = "base_url"
	// CodeUnknown means the attempt could not be classified.
	CodeUnknown = "unknown"
	// OutcomeSuccess is the usage outcome of a completed reply.
	OutcomeSuccess = "success"
)

// Fields are the request values a plugin may read or replace.
// They do not include credentials.
type Fields struct {
	Model string
	Body  json.RawMessage
}

// View is the snapshot a plugin may see. It has no secrets.
type View struct {
	Providers []state.Provider `json:"providers"`
	Routes    []state.Route    `json:"routes"`
	Plugins   []state.Plugin   `json:"plugins"`
}

// Hook is an in-process plugin the host chooses to call.
// The process does not load hooks from config. How a plugin is loaded is open.
type Hook interface {
	BeforeDial(ctx context.Context, view View, in Fields) (Fields, string)
	AfterAttempt(ctx context.Context, view View, rec state.AttemptRecord, body []byte)
}

// DialRequest is what the host sends to the selected provider.
// The dialer is host code. A plugin does not receive this value.
type DialRequest struct {
	RouteID    string
	ProviderID string
	Credential string
	BaseURL    string
	Fields     Fields
}

// DialResponse is one upstream attempt.
// Completed means the provider finished a reply and the loop must stop.
// Otherwise Class is the provider failure, or empty for a reachability failure.
type DialResponse struct {
	Completed bool
	Class     string
	Status    int
	Body      []byte
	Usage     json.RawMessage
}

// Dialer places the upstream call. A nil dialer is not a registered dialect.
type Dialer interface {
	Dial(ctx context.Context, req DialRequest) (DialResponse, error)
}

// Result is the caller-visible outcome of Run.
// AppendFailed means a usage write failed after Body was already chosen.
type Result struct {
	Code         string
	Status       int
	Body         []byte
	AppendFailed bool
}

// PublicView copies cfg without secrets.
func PublicView(cfg state.Config) View {
	v := View{
		Providers: make([]state.Provider, len(cfg.Providers)),
		Routes:    make([]state.Route, len(cfg.Routes)),
		Plugins:   append([]state.Plugin(nil), cfg.Plugins...),
	}
	for i, p := range cfg.Providers {
		p.Models = append([]string(nil), p.Models...)
		p.Credential = ""
		v.Providers[i] = p
	}
	for i, r := range cfg.Routes {
		r.ProviderIDs = append([]string(nil), r.ProviderIDs...)
		r.Eligible = append([]state.ErrorClass(nil), r.Eligible...)
		v.Routes[i] = r
	}
	return v
}

// Run tries the route's providers one at a time.
// snap is the snapshot taken for this attempt. Run does not read the store again
// and does not hold the config lock.
// Caller-side authentication is not handled here.
func Run(ctx context.Context, snap *state.Snapshot, model, consoleListen string, dialer Dialer, hooks []Hook, appendUsage func(state.AttemptRecord) error) (Result, error) {
	if snap == nil {
		return Result{Code: CodeConfigUnreadable, Status: http.StatusServiceUnavailable}, nil
	}
	if dialer == nil {
		return Result{Code: CodeNoDialect, Status: http.StatusServiceUnavailable}, nil
	}
	route, ok := snap.Config.RouteByModel(model)
	if !ok {
		return Result{Code: CodeNoRoute, Status: http.StatusNotFound}, nil
	}
	if len(route.ProviderIDs) == 0 {
		return Result{Code: CodeNoProvider, Status: http.StatusBadGateway}, nil
	}
	view := PublicView(snap.Config)
	var last Result
	for _, providerID := range route.ProviderIDs {
		if err := ctx.Err(); err != nil {
			return last, err
		}
		started := time.Now().UTC()
		routeID := route.ID
		prov, pok := snap.Config.Provider(providerID)
		if !pok {
			res := Result{Code: CodeUnknown, Status: http.StatusBadGateway}
			res.AppendFailed = note(ctx, appendUsage, hooks, view, record(routeID, providerID, CodeUnknown, started, nil), nil)
			return res, nil
		}
		fields := Fields{Model: model}
		reject := ""
		for _, h := range hooks {
			if h == nil {
				continue
			}
			var class string
			fields, class = h.BeforeDial(ctx, view, fields)
			if class != "" {
				reject = class
				break
			}
		}
		if reject != "" {
			res := Result{Code: reject, Status: statusFor(reject)}
			res.AppendFailed = note(ctx, appendUsage, hooks, view, record(routeID, providerID, reject, started, nil), nil)
			if route.Allows(state.ErrorClass(reject)) {
				last = res
				continue
			}
			return res, nil
		}
		switch security.CheckBaseURL(prov.BaseURL, consoleListen, "") {
		case security.BaseOK:
		case security.BaseUnknown:
			res := Result{Code: CodeUnknown, Status: http.StatusBadGateway}
			res.AppendFailed = note(ctx, appendUsage, hooks, view, record(routeID, providerID, CodeUnknown, started, nil), nil)
			return res, nil
		default:
			res := Result{Code: CodeBaseURL, Status: http.StatusBadGateway}
			res.AppendFailed = note(ctx, appendUsage, hooks, view, record(routeID, providerID, CodeBaseURL, started, nil), nil)
			return res, nil
		}
		resp, err := dialer.Dial(ctx, DialRequest{
			RouteID:    routeID,
			ProviderID: providerID,
			Credential: prov.Credential,
			BaseURL:    prov.BaseURL,
			Fields:     fields,
		})
		if err != nil && ctx.Err() != nil {
			res := Result{Code: CodeUnknown, Status: http.StatusBadGateway}
			res.AppendFailed = note(ctx, appendUsage, hooks, view, record(routeID, providerID, CodeUnknown, started, nil), nil)
			return res, ctx.Err()
		}
		if err != nil {
			resp = DialResponse{Class: string(state.ClassUpstreamUnavailable)}
		}
		if resp.Completed {
			body := append([]byte(nil), resp.Body...)
			status := resp.Status
			if status == 0 {
				status = http.StatusOK
			}
			res := Result{Status: status, Body: body}
			res.AppendFailed = note(ctx, appendUsage, hooks, view, record(routeID, providerID, OutcomeSuccess, started, cloneRaw(resp.Usage)), body)
			return res, nil
		}
		class := resp.Class
		if class == "" {
			class = string(state.ClassUpstreamUnavailable)
		}
		res := Result{Code: class, Status: statusFor(class)}
		res.AppendFailed = note(ctx, appendUsage, hooks, view, record(routeID, providerID, class, started, cloneRaw(resp.Usage)), nil)
		if route.Allows(state.ErrorClass(class)) {
			last = res
			continue
		}
		return res, nil
	}
	if last.Code == "" {
		last = Result{Code: CodeNoProvider, Status: http.StatusBadGateway}
	}
	return last, nil
}

func record(routeID, providerID, outcome string, started time.Time, usage json.RawMessage) state.AttemptRecord {
	return state.AttemptRecord{
		RouteID:    routeID,
		ProviderID: providerID,
		Outcome:    outcome,
		Started:    started,
		Finished:   time.Now().UTC(),
		Usage:      usage,
	}
}

func note(ctx context.Context, appendUsage func(state.AttemptRecord) error, hooks []Hook, view View, rec state.AttemptRecord, body []byte) bool {
	failed := false
	if appendUsage != nil {
		if err := appendUsage(rec); err != nil {
			failed = true
		}
	}
	for _, h := range hooks {
		if h != nil {
			h.AfterAttempt(ctx, view, rec, body)
		}
	}
	return failed
}

func cloneRaw(m json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(m)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return append(json.RawMessage(nil), m...)
}

func statusFor(class string) int {
	switch state.ErrorClass(class) {
	case state.ClassAuthFailure:
		return http.StatusUnauthorized
	case state.ClassRateLimit:
		return http.StatusTooManyRequests
	case state.ClassEmptyBalance:
		return http.StatusPaymentRequired
	case state.ClassUpstreamUnavailable:
		return http.StatusBadGateway
	default:
		return http.StatusBadGateway
	}
}
