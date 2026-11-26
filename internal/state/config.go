package state

import (
	"encoding/json"
	"strings"
)

// ErrorClass is one failure the gateway keeps separate from the others.
type ErrorClass string

const (
	ClassAuthFailure         ErrorClass = "auth_failure"
	ClassRateLimit           ErrorClass = "rate_limit"
	ClassEmptyBalance        ErrorClass = "empty_balance"
	ClassUpstreamUnavailable ErrorClass = "upstream_unavailable"
)

// KnownClass reports whether class is one of the four provider classes.
func KnownClass(class string) bool {
	switch ErrorClass(class) {
	case ClassAuthFailure, ClassRateLimit, ClassEmptyBalance, ClassUpstreamUnavailable:
		return true
	default:
		return false
	}
}

// Listeners holds user-set bind addresses. Empty means the process default,
// which is applied in memory and not written back.
type Listeners struct {
	Gateway string `json:"gateway,omitempty"`
	Console string `json:"console,omitempty"`
}

// ModelInfo is catalog data for one id in Provider.Models.
// Kind empty or chat is a conversation model. image and video are not.
// Vision nil means the process does not know whether the model accepts media.
type ModelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Hidden      bool   `json:"hidden,omitempty"`
	Context     int    `json:"context,omitempty"`
	Reasoning   bool   `json:"reasoning,omitempty"`
	Vision      *bool  `json:"vision,omitempty"`
}

// Provider is a configured upstream. Protocol is stored and not interpreted.
// Name is the label shown in the console. It is not the id.
// Credential is the first upstream key, kept so an older reader still finds one.
// Credentials is every key for that same protocol. Console responses do not return either.
type Provider struct {
	ID          string      `json:"id"`
	Name        string      `json:"name,omitempty"`
	Protocol    string      `json:"protocol"`
	BaseURL     string      `json:"baseUrl"`
	Models      []string    `json:"models"`
	Info        []ModelInfo `json:"info,omitempty"`
	Credential  string      `json:"credential,omitempty"`
	Credentials []string    `json:"credentials,omitempty"`
}

// Keys returns the provider's upstream keys, credentials first, then a legacy
// credential that is not already in that list. Empty strings are left out.
func (p Provider) Keys() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(key string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	for _, key := range p.Credentials {
		add(key)
	}
	add(p.Credential)
	return out
}

// Route matches a model name to an ordered provider list.
// EligibleSet is false when the file omits eligible; the attempt loop then
// uses only upstream unavailable. An explicit empty list stays empty.
type Route struct {
	ID          string       `json:"id"`
	ModelMatch  string       `json:"modelMatch"`
	ProviderIDs []string     `json:"providerIds"`
	Eligible    []ErrorClass `json:"-"`
	EligibleSet bool         `json:"-"`
}

// EligibleOrDefault returns the classes that may fall through to the next provider.
func (r Route) EligibleOrDefault() []ErrorClass {
	if !r.EligibleSet {
		return []ErrorClass{ClassUpstreamUnavailable}
	}
	out := make([]ErrorClass, len(r.Eligible))
	copy(out, r.Eligible)
	return out
}

// Allows reports whether class may fall through on this route.
func (r Route) Allows(class ErrorClass) bool {
	for _, c := range r.EligibleOrDefault() {
		if c == class {
			return true
		}
	}
	return false
}

type routeJSON struct {
	ID          string        `json:"id"`
	ModelMatch  string        `json:"modelMatch"`
	ProviderIDs []string      `json:"providerIds"`
	Eligible    *[]ErrorClass `json:"eligible,omitempty"`
}

// MarshalJSON omits eligible when it was not set, and writes [] when it was set empty.
func (r Route) MarshalJSON() ([]byte, error) {
	aux := routeJSON{
		ID:          r.ID,
		ModelMatch:  r.ModelMatch,
		ProviderIDs: r.ProviderIDs,
	}
	if aux.ProviderIDs == nil {
		aux.ProviderIDs = []string{}
	}
	if r.EligibleSet {
		els := r.Eligible
		if els == nil {
			els = []ErrorClass{}
		}
		aux.Eligible = &els
	}
	return json.Marshal(aux)
}

// UnmarshalJSON records whether eligible was present.
func (r *Route) UnmarshalJSON(data []byte) error {
	var aux routeJSON
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.ID = aux.ID
	r.ModelMatch = aux.ModelMatch
	r.ProviderIDs = aux.ProviderIDs
	if r.ProviderIDs == nil {
		r.ProviderIDs = []string{}
	}
	if aux.Eligible != nil {
		r.EligibleSet = true
		r.Eligible = *aux.Eligible
	} else {
		r.EligibleSet = false
		r.Eligible = nil
	}
	return nil
}

// Plugin is a config record. The process does not load it.
type Plugin struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Order   int    `json:"order"`
}

// GatewayToken is a caller credential. Secret is stored in the file.
// Models, when non-empty, is the only set of catalog ids this token may see or call.
// SpendLimit, when set, is the most this token may cost in SpendPeriod, in US dollars.
// SpendPeriod is today, 7d, 30d, or all. A limit with an empty period uses 30d.
type GatewayToken struct {
	ID          string   `json:"id"`
	Secret      string   `json:"secret"`
	Revoked     bool     `json:"revoked,omitempty"`
	Models      []string `json:"models,omitempty"`
	SpendLimit  *float64 `json:"spendLimit,omitempty"`
	SpendPeriod string   `json:"spendPeriod,omitempty"`
}

// Session is one console login. SecretHash is stored; the secret is not.
type Session struct {
	ID         string `json:"id"`
	SecretHash string `json:"secretHash"`
}

// User is a console operator. The password is stored only as a salt and a hash.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	PasswordSalt string    `json:"passwordSalt"`
	PasswordHash string    `json:"passwordHash"`
	Sessions     []Session `json:"sessions"`
}

// Config is the JSON document on disk.
// Share lets the gateway open a non-loopback address. It does not change a loopback bind.
type Config struct {
	Listeners     Listeners      `json:"listeners,omitempty"`
	Share         bool           `json:"share,omitempty"`
	Providers     []Provider     `json:"providers"`
	Routes        []Route        `json:"routes"`
	Plugins       []Plugin       `json:"plugins"`
	GatewayTokens []GatewayToken `json:"gatewayTokens"`
	Users         []User         `json:"users"`
	// NoAccountDiscovery, when true, stops the process from reading coding-agent
	// sign-ins on this computer. Absent means those sign-ins are read.
	NoAccountDiscovery bool `json:"noAccountDiscovery,omitempty"`
}

func emptyConfig() Config {
	return Config{
		Providers:     []Provider{},
		Routes:        []Route{},
		Plugins:       []Plugin{},
		GatewayTokens: []GatewayToken{},
		Users:         []User{},
	}
}

func (c *Config) normalize() {
	if c.Providers == nil {
		c.Providers = []Provider{}
	}
	if c.Routes == nil {
		c.Routes = []Route{}
	}
	if c.Plugins == nil {
		c.Plugins = []Plugin{}
	}
	if c.GatewayTokens == nil {
		c.GatewayTokens = []GatewayToken{}
	}
	if c.Users == nil {
		c.Users = []User{}
	}
	for i := range c.Providers {
		if c.Providers[i].Models == nil {
			c.Providers[i].Models = []string{}
		}
	}
	for i := range c.Routes {
		if c.Routes[i].ProviderIDs == nil {
			c.Routes[i].ProviderIDs = []string{}
		}
	}
	for i := range c.Users {
		if c.Users[i].Sessions == nil {
			c.Users[i].Sessions = []Session{}
		}
	}
}

// Clone returns a deep copy. Published snapshots are not mutated in place.
func (c Config) Clone() Config { return c.clone() }

func (c Config) clone() Config {
	out := c
	out.Providers = make([]Provider, len(c.Providers))
	for i, p := range c.Providers {
		p.Models = append([]string(nil), p.Models...)
		p.Credentials = append([]string(nil), p.Credentials...)
		p.Info = cloneInfo(p.Info)
		out.Providers[i] = p
	}
	out.Routes = make([]Route, len(c.Routes))
	for i, r := range c.Routes {
		r.ProviderIDs = append([]string(nil), r.ProviderIDs...)
		r.Eligible = append([]ErrorClass(nil), r.Eligible...)
		out.Routes[i] = r
	}
	out.Plugins = append([]Plugin(nil), c.Plugins...)
	out.GatewayTokens = make([]GatewayToken, len(c.GatewayTokens))
	for i, t := range c.GatewayTokens {
		t.Models = append([]string(nil), t.Models...)
		out.GatewayTokens[i] = t
	}
	out.Users = make([]User, len(c.Users))
	for i, u := range c.Users {
		u.Sessions = append([]Session(nil), u.Sessions...)
		out.Users[i] = u
	}
	return out
}

// Provider returns the provider with id.
func (c Config) Provider(id string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// UserByName returns the user whose username equals name.
func (c Config) UserByName(name string) (User, bool) {
	for _, u := range c.Users {
		if u.Username == name {
			return u, true
		}
	}
	return User{}, false
}

// RouteByModel returns the first route whose model match equals model.
func (c Config) RouteByModel(model string) (Route, bool) {
	for _, r := range c.Routes {
		if r.ModelMatch == model {
			return r, true
		}
	}
	return Route{}, false
}

// IDs returns the ids of one object type.
func ProviderIDs(c Config) []string {
	out := make([]string, len(c.Providers))
	for i, p := range c.Providers {
		out[i] = p.ID
	}
	return out
}

// RouteIDs returns route ids.
func RouteIDs(c Config) []string {
	out := make([]string, len(c.Routes))
	for i, r := range c.Routes {
		out[i] = r.ID
	}
	return out
}

// PluginIDs returns plugin ids.
func PluginIDs(c Config) []string {
	out := make([]string, len(c.Plugins))
	for i, p := range c.Plugins {
		out[i] = p.ID
	}
	return out
}

// TokenIDs returns gateway token ids.
func TokenIDs(c Config) []string {
	out := make([]string, len(c.GatewayTokens))
	for i, t := range c.GatewayTokens {
		out[i] = t.ID
	}
	return out
}

// UserIDs returns user ids.
func UserIDs(c Config) []string {
	out := make([]string, len(c.Users))
	for i, u := range c.Users {
		out[i] = u.ID
	}
	return out
}

// SessionIDs returns every session id.
func SessionIDs(c Config) []string {
	var out []string
	for _, u := range c.Users {
		for _, s := range u.Sessions {
			out = append(out, s.ID)
		}
	}
	return out
}
