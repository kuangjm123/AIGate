package state

import (
	"errors"
	"math"
	"net"
	"strings"

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
	if err := validateModelInfo(cfg); err != nil {
		return err
	}
	return nil
}

func validateUnique(cfg Config) error {
	groups := [][]string{
		ProviderIDs(cfg),
		RouteIDs(cfg),
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
	}
	return nil
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
