package state

import (
	"errors"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/kuangjm123/AIGate/internal/routes"
	"github.com/kuangjm123/AIGate/internal/security"
)

// ValidationError is a refused config write. Reason is a fixed code.
type ValidationError struct {
	Reason string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "invalid"
	}
	return e.Reason
}

func invalid(reason string) error {
	return &ValidationError{Reason: reason}
}

// Validate checks referential integrity, listen addresses, and provider base URLs.
// An empty base URL is allowed and is not dialed later.
func Validate(cfg Config) error {
	if err := validateUnique(cfg); err != nil {
		return err
	}
	if err := validateRefs(cfg); err != nil {
		return err
	}
	if err := validateListens(cfg); err != nil {
		return err
	}
	if err := validateBaseURLs(cfg); err != nil {
		return err
	}
	if err := validateTokens(cfg); err != nil {
		return err
	}
	if err := validateUsers(cfg); err != nil {
		return err
	}
	if err := validateClasses(cfg); err != nil {
		return err
	}
	if err := validateRouting(cfg); err != nil {
		return err
	}
	if err := validateGroups(cfg); err != nil {
		return err
	}
	if err := validateModelInfo(cfg); err != nil {
		return err
	}
	if err := validatePricing(cfg); err != nil {
		return err
	}
	if err := validateBalances(cfg); err != nil {
		return err
	}
	return nil
}

func validateUnique(cfg Config) error {
	groups := [][]string{
		ProviderIDs(cfg),
		RouteIDs(cfg),
		GroupIDs(cfg),
		PluginIDs(cfg),
		TokenIDs(cfg),
		UserIDs(cfg),
		SessionIDs(cfg),
	}
	for _, ids := range groups {
		seen := map[string]struct{}{}
		for _, id := range ids {
			if id == "" {
				return invalid("duplicate_id")
			}
			if _, ok := seen[id]; ok {
				return invalid("duplicate_id")
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

func validateRefs(cfg Config) error {
	providers := map[string]struct{}{}
	for _, p := range cfg.Providers {
		if len(p.Keys()) == 0 {
			return invalid("credential")
		}
		if p.MaxConcurrent < 0 || p.MaxRPM < 0 || p.QueueSlots < 0 || p.QueueWaitMS < 0 {
			return invalid("limit")
		}
		providers[p.ID] = struct{}{}
	}
	for _, r := range cfg.Routes {
		for _, id := range r.ProviderIDs {
			if _, ok := providers[id]; !ok {
				return invalid("missing_provider")
			}
		}
	}
	return nil
}

func validateListens(cfg Config) error {
	gw := security.EffectiveListen(cfg.Listeners.Gateway, security.DefaultGateway)
	cs := security.EffectiveListen(cfg.Listeners.Console, security.DefaultConsole)
	if err := checkAddr(cfg.Listeners.Gateway); err != nil {
		return err
	}
	if err := checkAddr(cfg.Listeners.Console); err != nil {
		return err
	}
	if _, _, err := net.SplitHostPort(gw); err != nil {
		return invalid("bad_listen")
	}
	if _, _, err := net.SplitHostPort(cs); err != nil {
		return invalid("bad_listen")
	}
	if security.SameListen(gw, cs) {
		return invalid("same_listen")
	}
	return nil
}

func checkAddr(addr string) error {
	if addr == "" {
		return nil
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return invalid("bad_listen")
	}
	return nil
}

func validateBaseURLs(cfg Config) error {
	cs := security.EffectiveListen(cfg.Listeners.Console, security.DefaultConsole)
	for _, p := range cfg.Providers {
		if p.BaseURL == "" {
			continue
		}
		switch security.CheckBaseURL(p.BaseURL, cs, "") {
		case security.BaseOK:
		default:
			return invalid("base_url")
		}
	}
	return nil
}

func validateTokens(cfg Config) error {
	for _, t := range cfg.GatewayTokens {
		if t.Secret == "" {
			return invalid("token_secret")
		}
		for _, model := range t.Models {
			if strings.TrimSpace(model) == "" {
				return invalid("model")
			}
		}
		if t.SpendLimit != nil && (*t.SpendLimit < 0 || math.IsNaN(*t.SpendLimit) || math.IsInf(*t.SpendLimit, 0)) {
			return invalid("spend_limit")
		}
		switch t.SpendPeriod {
		case "", "today", "7d", "30d", "all":
		default:
			return invalid("spend_limit")
		}
		if t.SpendPeriod != "" && t.SpendLimit == nil {
			return invalid("spend_limit")
		}
		if len(t.Limits) > 5 {
			return invalid("limit")
		}
		seen := map[string]struct{}{}
		for _, limit := range t.Limits {
			if err := validateLimit(limit); err != nil {
				return err
			}
			if limit.ID != "" {
				if _, ok := seen[limit.ID]; ok {
					return invalid("limit")
				}
				seen[limit.ID] = struct{}{}
			}
		}
	}
	return nil
}

func validateLimit(limit TokenLimit) error {
	if limit.USD != nil && (!finite(*limit.USD) || *limit.USD < 0) {
		return invalid("limit")
	}
	if limit.Tokens != nil && *limit.Tokens < 0 {
		return invalid("limit")
	}
	usd := limit.USD != nil && *limit.USD > 0
	tokens := limit.Tokens != nil && *limit.Tokens > 0
	if !usd && !tokens {
		return invalid("limit")
	}
	switch limit.Window {
	case "today", "week", "month", "all":
	case "hours":
		if limit.Hours <= 0 {
			return invalid("limit")
		}
	case "days":
		if limit.Days <= 0 {
			return invalid("limit")
		}
	default:
		return invalid("limit")
	}
	if limit.Since != nil {
		if limit.Since.IsZero() {
			return invalid("limit")
		}
		if limit.Window != "hours" && limit.Window != "days" {
			return invalid("limit")
		}
	}
	if strings.TrimSpace(limit.Provider) != "" && strings.TrimSpace(limit.Provider) != limit.Provider {
		return invalid("limit")
	}
	if strings.TrimSpace(limit.Model) != "" && strings.TrimSpace(limit.Model) != limit.Model {
		return invalid("limit")
	}
	return nil
}

func validatePricing(cfg Config) error {
	for _, p := range cfg.Providers {
		if p.Multiplier != nil && (!finite(*p.Multiplier) || *p.Multiplier < 0) {
			return invalid("multiplier")
		}
		if len(p.KeyMultipliers) > 0 && len(p.KeyMultipliers) != len(p.Keys()) {
			return invalid("multiplier")
		}
		for _, v := range p.KeyMultipliers {
			if !finite(v) || v < 0 {
				return invalid("multiplier")
			}
		}
	}
	for _, rule := range cfg.Prices {
		if strings.TrimSpace(rule.Model) == "" {
			return invalid("price")
		}
		if rule.Provider != "" && strings.TrimSpace(rule.Provider) != rule.Provider {
			return invalid("price")
		}
		fields := []*float64{rule.Input, rule.Output, rule.CacheRead, rule.CacheWrite, rule.CacheWrite1H}
		any := false
		for _, v := range fields {
			if v == nil {
				continue
			}
			any = true
			if !finite(*v) || *v < 0 {
				return invalid("price")
			}
		}
		if !any {
			return invalid("price")
		}
	}
	for _, peak := range cfg.Peaks {
		start, err := parseClock(peak.Start)
		if err != nil {
			return invalid("peak")
		}
		end, err := parseClock(peak.End)
		if err != nil {
			return invalid("peak")
		}
		if start == end {
			// A zero-length window never matches; reject it so a typo is visible.
			return invalid("peak")
		}
		if !finite(peak.Factor) || peak.Factor < 0 {
			return invalid("peak")
		}
	}
	for _, hide := range cfg.UsageHidden {
		if strings.TrimSpace(hide.ProviderID) == "" || strings.TrimSpace(hide.Model) == "" || hide.Before.IsZero() {
			return invalid("usage_hide")
		}
	}
	return nil
}

// validateBalances checks a user-supplied balance endpoint and amount path.
// A manual amount is free text; only surrounding whitespace is refused.
func validateBalances(cfg Config) error {
	for _, p := range cfg.Providers {
		if strings.TrimSpace(p.BalanceURL) != p.BalanceURL || strings.TrimSpace(p.BalanceManual) != p.BalanceManual {
			return invalid("balance")
		}
		if p.BalanceURL != "" {
			u, err := url.Parse(p.BalanceURL)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				return invalid("balance")
			}
		}
		if p.BalanceField != "" {
			for _, part := range strings.Split(p.BalanceField, ".") {
				if part == "" {
					return invalid("balance")
				}
			}
		}
	}
	return nil
}

func parseClock(s string) (int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, errors.New("clock")
	}
	hour, err := strconv.Atoi(s[:2])
	if err != nil {
		return 0, err
	}
	minute, err := strconv.Atoi(s[3:])
	if err != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, errors.New("clock")
	}
	return hour*60 + minute, nil
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func validateModelInfo(cfg Config) error {
	for _, p := range cfg.Providers {
		seen := map[string]struct{}{}
		for _, info := range p.Info {
			if info.ID == "" || !p.Lists(info.ID) {
				return invalid("model")
			}
			if _, ok := seen[info.ID]; ok {
				return invalid("model")
			}
			seen[info.ID] = struct{}{}
			switch info.Kind {
			case "", "chat", "image", "video":
			default:
				return invalid("model")
			}
			if info.Context < 0 {
				return invalid("model")
			}
		}
	}
	return nil
}

func validateUsers(cfg Config) error {
	seen := map[string]struct{}{}
	for _, u := range cfg.Users {
		name := strings.TrimSpace(u.Username)
		if name == "" || name != u.Username {
			return invalid("username")
		}
		if _, ok := seen[name]; ok {
			return invalid("duplicate_username")
		}
		seen[name] = struct{}{}
		if u.PasswordSalt == "" || u.PasswordHash == "" {
			return invalid("password")
		}
		for _, sess := range u.Sessions {
			if sess.SecretHash == "" {
				return invalid("session")
			}
		}
	}
	return nil
}

func validateRouting(cfg Config) error {
	for _, r := range cfg.Routes {
		if !routes.Known(r.Routing) {
			return invalid("routing")
		}
		if r.Routing == routes.Cheap || r.Routing == routes.Expiring {
			return invalid("routing")
		}
		for id, weight := range r.Weights {
			found := false
			for _, member := range r.ProviderIDs {
				if member == id {
					found = true
					break
				}
			}
			if !found || weight < 0 {
				return invalid("routing")
			}
		}
	}
	return nil
}

// validateGroups checks every member, the weight keys, and nested cycles.
// A group id is the request name, so it is not required to differ from a
// route's modelMatch; group lookup runs first.
func validateGroups(cfg Config) error {
	byID := make(map[string]Group, len(cfg.Groups))
	for _, g := range cfg.Groups {
		if g.ID == "" || strings.TrimSpace(g.ID) != g.ID {
			return invalid("group")
		}
		if _, duplicate := byID[g.ID]; duplicate {
			return invalid("duplicate_id")
		}
		byID[g.ID] = g
		if !routes.Known(g.Routing) {
			return invalid("routing")
		}
		seen := map[string]struct{}{}
		for _, ref := range g.Members {
			if ref == "" || strings.TrimSpace(ref) != ref {
				return invalid("group")
			}
			if _, dup := seen[ref]; dup {
				return invalid("group")
			}
			seen[ref] = struct{}{}
			if _, ok := cfg.ResolveGroupMember(ref); !ok {
				return invalid("group")
			}
		}
		for ref, weight := range g.Weights {
			if _, ok := seen[ref]; !ok || weight < 0 {
				return invalid("routing")
			}
		}
		if g.EligibleSet {
			for _, c := range g.Eligible {
				if !KnownClass(string(c)) {
					return invalid("bad_class")
				}
			}
		}
	}
	state := map[string]int{}
	var visit func(string) bool
	visit = func(id string) bool {
		switch state[id] {
		case 1:
			return false
		case 2:
			return true
		}
		state[id] = 1
		g := byID[id]
		for _, ref := range g.Members {
			member, ok := cfg.ResolveGroupMember(ref)
			if !ok {
				continue
			}
			if !member.Nested {
				continue
			}
			if !visit(member.GroupID) {
				return false
			}
		}
		state[id] = 2
		return true
	}
	for id := range byID {
		if !visit(id) {
			return invalid("group")
		}
	}
	return nil
}

func validateClasses(cfg Config) error {
	for _, r := range cfg.Routes {
		if !r.EligibleSet {
			continue
		}
		for _, c := range r.Eligible {
			if !KnownClass(string(c)) {
				return invalid("bad_class")
			}
		}
	}
	return nil
}

// AsValidation reports whether err is a ValidationError.
func AsValidation(err error) (*ValidationError, bool) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}
