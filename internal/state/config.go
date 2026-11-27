package state

import (
	"encoding/json"
	"strings"
	"time"
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
	// ReasoningEfforts is the model's own level names. ReasoningEffortsKnown
	// separates an empty known list (the model has no level selector) from a
	// list the process has not read yet.
	ReasoningEfforts      []string `json:"-"`
	ReasoningEffortsKnown bool     `json:"-"`
	Vision                *bool    `json:"vision,omitempty"`
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
	// MaxConcurrent caps requests in flight on one key seat. Zero means no cap.
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
	// MaxRPM caps request starts per minute on one key seat. Zero means no cap.
	MaxRPM int `json:"maxRPM,omitempty"`
	// QueueSlots caps how many requests may wait on a capped seat. Zero uses
	// the process default.
	QueueSlots int `json:"queueSlots,omitempty"`
	// QueueWaitMS caps how long a request may wait for a capped seat. Zero
	// uses the process default.
	QueueWaitMS int `json:"queueWaitMS,omitempty"`
	// Multiplier scales catalog prices for this provider. Nil or 1 is the
	// catalog price. An explicit 0 makes this provider's calls free.
	Multiplier *float64 `json:"multiplier,omitempty"`
	// KeyMultipliers scales catalog prices for each upstream key, in the same
	// order as Credentials. An absent list or a value of 1 keeps the catalog
	// price. The provider multiplier and a key multiplier are not both applied
	// to one call; the key's own value wins when it is present.
	KeyMultipliers []float64 `json:"keyMultipliers,omitempty"`
	// BalanceURL is a balance endpoint the user supplied for this provider.
	// BalanceField is the dotted JSON path of the remaining amount in its
	// reply. BalanceManual is an amount the user typed; it is shown when no
	// endpoint answers and no earlier reading is kept. BalanceCurrency
	// prefixes that amount.
	BalanceURL      string `json:"balanceUrl,omitempty"`
	BalanceField    string `json:"balanceField,omitempty"`
	BalanceManual   string `json:"balanceManual,omitempty"`
	BalanceCurrency string `json:"balanceCurrency,omitempty"`
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
// Routing is the strategy ordering the providers: empty is smart, or order,
// rotate, usage, pace, weight, free, fast, cheap, drain, expiring.
// Weights is each provider's weight for the weight strategy; absent means 1.
type Route struct {
	ID          string         `json:"id"`
	ModelMatch  string         `json:"modelMatch"`
	ProviderIDs []string       `json:"providerIds"`
	Routing     string         `json:"-"`
	Weights     map[string]int `json:"-"`
	Eligible    []ErrorClass   `json:"-"`
	EligibleSet bool           `json:"-"`
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
	ID          string         `json:"id"`
	ModelMatch  string         `json:"modelMatch"`
	ProviderIDs []string       `json:"providerIds"`
	Routing     string         `json:"routing,omitempty"`
	Weights     map[string]int `json:"weights,omitempty"`
	Eligible    *[]ErrorClass  `json:"eligible,omitempty"`
}

// MarshalJSON omits eligible when it was not set, and writes [] when it was set empty.
func (r Route) MarshalJSON() ([]byte, error) {
	aux := routeJSON{
		ID:          r.ID,
		ModelMatch:  r.ModelMatch,
		ProviderIDs: r.ProviderIDs,
		Routing:     r.Routing,
	}
	if len(r.Weights) > 0 {
		aux.Weights = r.Weights
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
	r.Routing = aux.Routing
	r.Weights = aux.Weights
	if aux.Eligible != nil {
		r.EligibleSet = true
		r.Eligible = *aux.Eligible
	} else {
		r.EligibleSet = false
		r.Eligible = nil
	}
	return nil
}

// Group orders its members for one request name. A member is a
// provider/model pair or another group. The group's id is the model name a
// caller asks for. A group with no routing inherits a strategy from an outer
// group when that strategy is inheritable; order and weight leave an inner
// group on Smart.
type Group struct {
	ID          string         `json:"-"`
	Members     []string       `json:"-"`
	Routing     string         `json:"-"`
	Weights     map[string]int `json:"-"`
	Eligible    []ErrorClass   `json:"-"`
	EligibleSet bool           `json:"-"`
	// Levels replaces every member model's level list for this group.
	// ReasoningMap rewrites a client level to the level a member sends.
	Levels       []string          `json:"-"`
	ReasoningMap map[string]string `json:"-"`
}

// EligibleOrDefault returns the classes that may move from one member to the
// next. The default matches Route: only an unavailable upstream falls through.
func (g Group) EligibleOrDefault() []ErrorClass {
	if !g.EligibleSet {
		return []ErrorClass{ClassUpstreamUnavailable}
	}
	out := make([]ErrorClass, len(g.Eligible))
	copy(out, g.Eligible)
	return out
}

// Allows reports whether class may fall through to another member.
func (g Group) Allows(class ErrorClass) bool {
	for _, c := range g.EligibleOrDefault() {
		if c == class {
			return true
		}
	}
	return false
}

type groupJSON struct {
	ID           string            `json:"id"`
	Members      []string          `json:"members"`
	Routing      string            `json:"routing,omitempty"`
	Weights      map[string]int    `json:"weights,omitempty"`
	Eligible     *[]ErrorClass     `json:"eligible,omitempty"`
	Levels       []string          `json:"levels,omitempty"`
	ReasoningMap map[string]string `json:"reasoningMap,omitempty"`
}

// MarshalJSON omits eligible when it was not set, and writes [] when it was set empty.
func (g Group) MarshalJSON() ([]byte, error) {
	aux := groupJSON{ID: g.ID, Members: g.Members, Routing: g.Routing}
	if aux.Members == nil {
		aux.Members = []string{}
	}
	if len(g.Weights) > 0 {
		aux.Weights = g.Weights
	}
	if len(g.Levels) > 0 {
		aux.Levels = g.Levels
	}
	if len(g.ReasoningMap) > 0 {
		aux.ReasoningMap = g.ReasoningMap
	}
	if g.EligibleSet {
		els := g.Eligible
		if els == nil {
			els = []ErrorClass{}
		}
		aux.Eligible = &els
	}
	return json.Marshal(aux)
}

// UnmarshalJSON records whether eligible was present.
func (g *Group) UnmarshalJSON(data []byte) error {
	var aux groupJSON
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	g.ID = aux.ID
	g.Members = aux.Members
	if g.Members == nil {
		g.Members = []string{}
	}
	g.Routing = aux.Routing
	g.Weights = aux.Weights
	g.Levels = append([]string(nil), aux.Levels...)
	g.ReasoningMap = cloneStringMap(aux.ReasoningMap)
	if len(g.Levels) == 0 && len(g.ReasoningMap) == 0 {
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) == nil {
			if value, ok := raw["reasoningLevels"]; ok {
				_ = json.Unmarshal(value, &g.Levels)
			}
			if value, ok := raw["reasoningMap"]; ok {
				_ = json.Unmarshal(value, &g.ReasoningMap)
			}
		}
	}
	if aux.Eligible != nil {
		g.EligibleSet = true
		g.Eligible = *aux.Eligible
	} else {
		g.EligibleSet = false
		g.Eligible = nil
	}
	return nil
}

// Plugin is a config record. The process does not load it.
type Plugin struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Order   int    `json:"order"`
}

// PriceRule is a user price for a provider and model. Provider empty applies
// to every provider. Model "*" applies to every model of that provider.
// Each money field is US dollars per million tokens. An explicit zero is a
// free token and wins over the catalog; a nil field falls back to the catalog.
type PriceRule struct {
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model"`
	Input        *float64 `json:"input,omitempty"`
	Output       *float64 `json:"output,omitempty"`
	CacheRead    *float64 `json:"cacheRead,omitempty"`
	CacheWrite   *float64 `json:"cacheWrite,omitempty"`
	CacheWrite1H *float64 `json:"cacheWrite1h,omitempty"`
}

// PeakWindow multiplies every price of the matching calls by Factor while the
// local wall clock is inside [Start, End). Start and End are "HH:MM". An
// empty or "*" model matches every model; an empty provider matches every
// provider. A window that wraps past midnight is allowed.
type PeakWindow struct {
	Provider string  `json:"provider,omitempty"`
	Model    string  `json:"model,omitempty"`
	Start    string  `json:"start"`
	End      string  `json:"end"`
	Factor   float64 `json:"factor"`
}

// UsageHide hides rows from the usage page at or before Before for one
// provider and model. The rows stay on disk and still count toward token
// limits.
type UsageHide struct {
	ProviderID string    `json:"providerId"`
	Model      string    `json:"model"`
	Before     time.Time `json:"before"`
}

// TokenLimit is one cap on a gateway token. USD and Tokens may both be set;
// nil or zero means that dimension is not capped. Window is today, week,
// month, all, hours, or days. Provider and Model narrow the rows counted;
// either may be empty. Hours and days use a rolling window ending now unless
// Since is set, in which case the window starts at Since.
type TokenLimit struct {
	ID       string     `json:"id,omitempty"`
	USD      *float64   `json:"usd,omitempty"`
	Tokens   *int       `json:"tokens,omitempty"`
	Window   string     `json:"window"`
	Hours    int        `json:"hours,omitempty"`
	Days     int        `json:"days,omitempty"`
	Since    *time.Time `json:"since,omitempty"`
	Provider string     `json:"provider,omitempty"`
	Model    string     `json:"model,omitempty"`
}

// GatewayToken is a caller credential. Secret is stored in the file.
// Models, when non-empty, is the only set of catalog ids this token may see or call.
// SpendLimit and SpendPeriod are the older single-dollar cap. Limits holds up
// to five caps with their own windows and scopes. IssuerID is set on a
// short-lived token and names the token that issued it.
type GatewayToken struct {
	ID          string       `json:"id"`
	Secret      string       `json:"secret"`
	Revoked     bool         `json:"revoked,omitempty"`
	Models      []string     `json:"models,omitempty"`
	SpendLimit  *float64     `json:"spendLimit,omitempty"`
	SpendPeriod string       `json:"spendPeriod,omitempty"`
	Limits      []TokenLimit `json:"limits,omitempty"`
	IssuerID    string       `json:"issuerId,omitempty"`
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
	Groups        []Group        `json:"groups,omitempty"`
	Plugins       []Plugin       `json:"plugins"`
	GatewayTokens []GatewayToken `json:"gatewayTokens"`
	Users         []User         `json:"users"`
	// Prices, Peaks, and UsageHidden are read when usage is priced or shown.
	// They are never written into attempts.jsonl.
	Prices      []PriceRule  `json:"prices,omitempty"`
	Peaks       []PeakWindow `json:"peaks,omitempty"`
	UsageHidden []UsageHide  `json:"usageHidden,omitempty"`
	// NoAccountDiscovery, when true, stops the process from reading coding-agent
	// sign-ins on this computer. Absent means those sign-ins are read.
	NoAccountDiscovery bool `json:"noAccountDiscovery,omitempty"`
	// NoAutoGroups, when true, stops a bare model name listed by more than one
	// provider from becoming an implicit group. Absent means the group is made.
	NoAutoGroups bool `json:"noAutoGroups,omitempty"`
}

func emptyConfig() Config {
	return Config{
		Providers:     []Provider{},
		Routes:        []Route{},
		Groups:        []Group{},
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
	if c.Groups == nil {
		c.Groups = []Group{}
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
	for i := range c.Groups {
		if c.Groups[i].Members == nil {
			c.Groups[i].Members = []string{}
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
		p.Multiplier = cloneFloat(p.Multiplier)
		p.KeyMultipliers = append([]float64(nil), p.KeyMultipliers...)
		out.Providers[i] = p
	}
	out.Routes = make([]Route, len(c.Routes))
	for i, r := range c.Routes {
		r.ProviderIDs = append([]string(nil), r.ProviderIDs...)
		r.Eligible = append([]ErrorClass(nil), r.Eligible...)
		if r.Weights != nil {
			weights := make(map[string]int, len(r.Weights))
			for id, weight := range r.Weights {
				weights[id] = weight
			}
			r.Weights = weights
		}
		out.Routes[i] = r
	}
	out.Groups = make([]Group, len(c.Groups))
	for i, g := range c.Groups {
		g.Members = append([]string(nil), g.Members...)
		g.Eligible = append([]ErrorClass(nil), g.Eligible...)
		g.Levels = append([]string(nil), g.Levels...)
		if g.Weights != nil {
			weights := make(map[string]int, len(g.Weights))
			for ref, weight := range g.Weights {
				weights[ref] = weight
			}
			g.Weights = weights
		}
		g.ReasoningMap = cloneStringMap(g.ReasoningMap)
		out.Groups[i] = g
	}
	out.Plugins = append([]Plugin(nil), c.Plugins...)
	out.GatewayTokens = make([]GatewayToken, len(c.GatewayTokens))
	for i, t := range c.GatewayTokens {
		t.Models = append([]string(nil), t.Models...)
		t.Limits = cloneLimits(t.Limits)
		out.GatewayTokens[i] = t
	}
	out.Users = make([]User, len(c.Users))
	for i, u := range c.Users {
		u.Sessions = append([]Session(nil), u.Sessions...)
		out.Users[i] = u
	}
	out.Prices = clonePriceRules(c.Prices)
	out.Peaks = append([]PeakWindow(nil), c.Peaks...)
	out.UsageHidden = append([]UsageHide(nil), c.UsageHidden...)
	return out
}

func cloneFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	got := *v
	return &got
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneInt(v *int) *int {
	if v == nil {
		return nil
	}
	got := *v
	return &got
}

func cloneTime(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	got := *v
	return &got
}

func cloneLimits(in []TokenLimit) []TokenLimit {
	if in == nil {
		return nil
	}
	out := make([]TokenLimit, len(in))
	for i, limit := range in {
		limit.USD = cloneFloat(limit.USD)
		limit.Tokens = cloneInt(limit.Tokens)
		limit.Since = cloneTime(limit.Since)
		out[i] = limit
	}
	return out
}

func clonePriceRules(in []PriceRule) []PriceRule {
	if in == nil {
		return nil
	}
	out := make([]PriceRule, len(in))
	for i, rule := range in {
		rule.Input = cloneFloat(rule.Input)
		rule.Output = cloneFloat(rule.Output)
		rule.CacheRead = cloneFloat(rule.CacheRead)
		rule.CacheWrite = cloneFloat(rule.CacheWrite)
		rule.CacheWrite1H = cloneFloat(rule.CacheWrite1H)
		out[i] = rule
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

// GroupByID returns the group whose id equals id.
func (c Config) GroupByID(id string) (Group, bool) {
	for _, g := range c.Groups {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

// GroupFor resolves a request name to a group. The bare id wins; group/<id>
// is accepted as an unambiguous spelling for a group id that also looks like
// a catalog id.
func (c Config) GroupFor(model string) (Group, bool) {
	if g, ok := c.GroupByID(model); ok {
		return g, true
	}
	if id, ok := strings.CutPrefix(model, "group/"); ok {
		return c.GroupByID(id)
	}
	return Group{}, false
}

// GroupMember is one resolved group member. Nested is true for a group id;
// otherwise ProviderID and Model name one listed provider model.
type GroupMember struct {
	Ref        string
	ProviderID string
	Model      string
	GroupID    string
	Nested     bool
	Fixed      string
	Allowed    []string
}

// ResolveGroupMember reads a member reference. A provider/model pair wins when
// its provider exists; group/<id> names a nested group explicitly, and a bare
// id names a nested group when no provider has that id.
func (c Config) ResolveGroupMember(ref string) (GroupMember, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return GroupMember{}, false
	}
	original := ref
	base, fixed, allowed, hasLevels := splitMemberLevels(ref)
	if hasLevels {
		ref = base
	}
	if id, ok := strings.CutPrefix(ref, "group/"); ok {
		if _, found := c.GroupByID(id); !found {
			return GroupMember{}, false
		}
		return GroupMember{Ref: original, GroupID: id, Nested: true, Fixed: fixed, Allowed: allowed}, true
	}
	if providerID, model, ok := strings.Cut(ref, "/"); ok && providerID != "" && model != "" {
		if provider, found := c.Provider(providerID); found {
			if !provider.ChatModel(model) {
				return GroupMember{}, false
			}
			return GroupMember{Ref: original, ProviderID: providerID, Model: model, Fixed: fixed, Allowed: allowed}, true
		}
	}
	if _, found := c.GroupByID(ref); found {
		return GroupMember{Ref: original, GroupID: ref, Nested: true, Fixed: fixed, Allowed: allowed}, true
	}
	return GroupMember{}, false
}

func splitMemberLevels(ref string) (string, string, []string, bool) {
	at := strings.LastIndex(ref, ":")
	if at <= 0 || at == len(ref)-1 {
		return ref, "", nil, false
	}
	suffix := ref[at+1:]
	parts := strings.Split(suffix, "+")
	levels := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "off" {
			part = "none"
		}
		switch part {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "light", "extra_high":
		default:
			return ref, "", nil, false
		}
		if !containsString(levels, part) {
			levels = append(levels, part)
		}
	}
	if len(levels) == 0 {
		return ref, "", nil, false
	}
	if len(levels) == 1 {
		return ref[:at], levels[0], nil, true
	}
	return ref[:at], "", levels, true
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
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

// GroupIDs returns group ids.
func GroupIDs(c Config) []string {
	out := make([]string, len(c.Groups))
	for i, g := range c.Groups {
		out[i] = g.ID
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
