package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Usage record sources. An empty source is a call the gateway itself handled.
// Reported calls are shown on the usage page but do not consume a gateway
// token's limit. Probe, MCP, and search lines are not part of any total.
const (
	SourceReported = "reported"
	SourceProbe    = "probe"
	SourceMCP      = "mcp"
	SourceSearch   = "search"

	// CanceledText is the agent-side cancellation message. A 499 with this
	// text is a cancellation, not a failure. A gateway timeout is a failure.
	CanceledText = "the agent canceled the request"
)

// AttemptRecord is one gateway attempt. Usage is the provider's own object.
// A nil Usage is unknown and is omitted, not stored as zero.
//
// RouteID is the id shared by every attempt of one user request. Route is the
// configured route id; older lines put it in RouteID and leave Route empty.
// Model, Session, Caller, and Protocol describe the caller-visible call.
// TokenID is the older name for CallerKeyID. CallerKeyID is the gateway
// token's id, never its secret. ProviderKeyID is a fingerprint of the
// upstream key, also never the secret.
//
// Status is the upstream HTTP status when this attempt received one. Zero
// means there was none. MS is the span from Started to Finished in
// milliseconds. Zero means under a millisecond, or unknown.
//
// In, Out, CacheRead, CacheWrite, CacheWrite1H, and Reasoning are set together
// when the usage object had token counts. In does not include cache read or
// cache write. An older line without those fields still parses.
type AttemptRecord struct {
	RouteID         string          `json:"routeId"`
	Route           string          `json:"route,omitempty"`
	ProviderID      string          `json:"providerId"`
	Outcome         string          `json:"outcome"`
	Status          int             `json:"status,omitempty"`
	Started         time.Time       `json:"started"`
	Finished        time.Time       `json:"finished"`
	MS              int             `json:"ms,omitempty"`
	Usage           json.RawMessage `json:"usage,omitempty"`
	Model           string          `json:"model,omitempty"`
	Session         string          `json:"session,omitempty"`
	TokenID         string          `json:"tokenId,omitempty"`
	CallerKeyID     string          `json:"callerKeyId,omitempty"`
	CallerIssuerID  string          `json:"callerIssuerId,omitempty"`
	ProviderKeyID   string          `json:"providerKeyId,omitempty"`
	ProviderAccount string          `json:"providerAccount,omitempty"`
	Caller          string          `json:"caller,omitempty"`
	Agent           string          `json:"agent,omitempty"`
	Protocol        string          `json:"protocol,omitempty"`
	Operation       string          `json:"operation,omitempty"`
	Source          string          `json:"source,omitempty"`
	Err             string          `json:"err,omitempty"`
	ErrType         string          `json:"errType,omitempty"`
	Tries           int             `json:"tries,omitempty"`
	HandedOn        bool            `json:"handedOn,omitempty"`
	Rejected        bool            `json:"rejected,omitempty"`
	Cost            *float64        `json:"cost,omitempty"`
	TTFTMS          int             `json:"ttft_ms,omitempty"`
	FirstTextMS     int             `json:"first_text_ms,omitempty"`
	QueueMS         int             `json:"queue_ms,omitempty"`
	RetryMS         int             `json:"retry_ms,omitempty"`
	BytesIn         int64           `json:"bytes_in,omitempty"`
	BytesOut        int64           `json:"bytes_out,omitempty"`
	In              int             `json:"in,omitempty"`
	Out             int             `json:"out,omitempty"`
	CacheRead       int             `json:"cacheRead,omitempty"`
	CacheWrite      int             `json:"cacheWrite,omitempty"`
	CacheWrite1H    int             `json:"cacheWrite1h,omitempty"`
	Reasoning       int             `json:"reasoning,omitempty"`
	counted         bool
}

// UsageCounts is the normalized token split of one attempt.
type UsageCounts struct {
	In           int
	Out          int
	CacheRead    int
	CacheWrite   int
	CacheWrite1H int
	Reasoning    int
}

// Total is the token count used by a gateway token's token limit.
func (c UsageCounts) Total() int {
	return c.In + c.Out + c.CacheRead + c.CacheWrite
}

// UsageGroup is one agent, model, upstream key, account, caller key, or
// session in a summary. Key is the group's id. Missing ids are not grouped.
type UsageGroup struct {
	Key          string  `json:"key"`
	Calls        int     `json:"calls"`
	Failed       int     `json:"failed,omitempty"`
	Canceled     int     `json:"canceled,omitempty"`
	Retried      int     `json:"retried,omitempty"`
	In           int     `json:"in"`
	Out          int     `json:"out"`
	CacheRead    int     `json:"cacheRead"`
	CacheWrite   int     `json:"cacheWrite"`
	CacheWrite1H int     `json:"cacheWrite1h,omitempty"`
	Reasoning    int     `json:"reasoning"`
	Cost         float64 `json:"cost"`
	Unpriced     int     `json:"unpriced"`
}

// UsagePoint is one bucket of a summary series. Start is the local start of
// the hour, day, or week the point covers.
type UsagePoint struct {
	Start        time.Time `json:"start"`
	Calls        int       `json:"calls"`
	Failed       int       `json:"failed,omitempty"`
	Canceled     int       `json:"canceled,omitempty"`
	In           int       `json:"in"`
	Out          int       `json:"out"`
	CacheRead    int       `json:"cacheRead"`
	CacheWrite   int       `json:"cacheWrite"`
	CacheWrite1H int       `json:"cacheWrite1h,omitempty"`
	Reasoning    int       `json:"reasoning"`
	Cost         float64   `json:"cost"`
	Unpriced     int       `json:"unpriced"`
}

// UsageSummary is the sum of the attempt lines it was given.
// Calls counts final lines, not handed-on retries. Token counts include
// handed-on lines because the provider already billed them.
// Cost and Unpriced are filled by a reader that has a price list. Cost is US
// dollars. Groups and Series are empty until a reader asks for them.
type UsageSummary struct {
	Calls        int     `json:"calls"`
	Failed       int     `json:"failed,omitempty"`
	Canceled     int     `json:"canceled,omitempty"`
	Retried      int     `json:"retried,omitempty"`
	In           int     `json:"in"`
	Out          int     `json:"out"`
	CacheRead    int     `json:"cacheRead"`
	CacheWrite   int     `json:"cacheWrite"`
	CacheWrite1H int     `json:"cacheWrite1h,omitempty"`
	Reasoning    int     `json:"reasoning"`
	Cost         float64 `json:"cost"`
	Unpriced     int     `json:"unpriced"`

	Agents       []UsageGroup `json:"agents"`
	Models       []UsageGroup `json:"models"`
	ProviderKeys []UsageGroup `json:"providerKeys"`
	Accounts     []UsageGroup `json:"accounts"`
	CallerKeys   []UsageGroup `json:"callerKeys"`
	Sessions     []UsageGroup `json:"sessions"`
	Series       []UsagePoint `json:"series"`
}

// SummarizeOptions selects the optional parts of a summary. Period empty
// bounds nothing and leaves Series empty. Price returns the cost of one
// record; the second value is false when that record has tokens but no price.
type SummarizeOptions struct {
	Period   string
	Now      time.Time
	Location *time.Location
	Price    func(AttemptRecord) (float64, bool)
	Hide     func(AttemptRecord) bool
}

// InTotals reports whether a line belongs in a usage total. Probes, MCP, and
// search lines do not. Reported lines do; a token limit skips them separately.
func (r AttemptRecord) InTotals() bool {
	switch r.Source {
	case SourceProbe, SourceMCP, SourceSearch:
		return false
	default:
		return true
	}
}

// InLimit reports whether a line consumes a gateway token's limit.
// Cancellations, local rejections, probes, reports, MCP, and search do not.
func (r AttemptRecord) InLimit() bool {
	if r.Source != "" || r.Canceled() || r.Rejected {
		return false
	}
	if r.ProviderID == "" && r.Status >= 400 {
		return false
	}
	return true
}

// Canceled reports an agent-side cancellation. A gateway timeout with 499 is
// not a cancellation because its error text is different.
func (r AttemptRecord) Canceled() bool {
	return r.Status == 499 && (r.Err == CanceledText || r.ErrType == "agent_canceled")
}

// Failed reports a final line with an error or an HTTP status at or above 400.
// A handed-on line is a retry that was already billed; the totals skip it.
func (r AttemptRecord) Failed() bool {
	if r.Canceled() {
		return false
	}
	return r.Rejected || r.Status >= 400 || r.Err != ""
}

// Answered reports a final line that was neither a failure nor a cancellation.
func (r AttemptRecord) Answered() bool {
	return !r.HandedOn && !r.Canceled() && !r.Failed()
}

// CallerKey returns the gateway token id for this line, including the older
// tokenId field.
func (r AttemptRecord) CallerKey() string {
	if r.CallerKeyID != "" {
		return r.CallerKeyID
	}
	return r.TokenID
}

// ChargedTo reports whether this line is charged to keyID, either because
// keyID made the call or because it issued the short-lived key that did.
func (r AttemptRecord) ChargedTo(keyID string) bool {
	if keyID == "" {
		return false
	}
	return r.CallerKey() == keyID || (r.CallerIssuerID != "" && r.CallerIssuerID == keyID)
}

// AgentName returns the agent id, or the caller name for an older line.
func (r AttemptRecord) AgentName() string {
	if r.Agent != "" {
		return r.Agent
	}
	return r.Caller
}

type attemptJSON struct {
	RouteID         string          `json:"routeId"`
	Route           string          `json:"route,omitempty"`
	ProviderID      string          `json:"providerId"`
	Outcome         string          `json:"outcome"`
	Status          int             `json:"status,omitempty"`
	Started         time.Time       `json:"started"`
	Finished        time.Time       `json:"finished"`
	MS              int             `json:"ms,omitempty"`
	Usage           json.RawMessage `json:"usage,omitempty"`
	Model           string          `json:"model,omitempty"`
	Session         string          `json:"session,omitempty"`
	TokenID         string          `json:"tokenId,omitempty"`
	CallerKeyID     string          `json:"callerKeyId,omitempty"`
	CallerIssuerID  string          `json:"callerIssuerId,omitempty"`
	ProviderKeyID   string          `json:"providerKeyId,omitempty"`
	ProviderAccount string          `json:"providerAccount,omitempty"`
	Caller          string          `json:"caller,omitempty"`
	Agent           string          `json:"agent,omitempty"`
	Protocol        string          `json:"protocol,omitempty"`
	Operation       string          `json:"operation,omitempty"`
	Source          string          `json:"source,omitempty"`
	Err             string          `json:"err,omitempty"`
	ErrType         string          `json:"errType,omitempty"`
	Tries           int             `json:"tries,omitempty"`
	HandedOn        bool            `json:"handedOn,omitempty"`
	Rejected        bool            `json:"rejected,omitempty"`
	Cost            *float64        `json:"cost,omitempty"`
	TTFTMS          int             `json:"ttft_ms,omitempty"`
	FirstTextMS     int             `json:"first_text_ms,omitempty"`
	QueueMS         int             `json:"queue_ms,omitempty"`
	RetryMS         int             `json:"retry_ms,omitempty"`
	BytesIn         int64           `json:"bytes_in,omitempty"`
	BytesOut        int64           `json:"bytes_out,omitempty"`
	In              *int            `json:"in,omitempty"`
	Out             *int            `json:"out,omitempty"`
	CacheRead       *int            `json:"cacheRead,omitempty"`
	CacheWrite      *int            `json:"cacheWrite,omitempty"`
	CacheWrite1H    *int            `json:"cacheWrite1h,omitempty"`
	Reasoning       *int            `json:"reasoning,omitempty"`
}

// MarshalJSON omits a missing or null usage object.
// When the line has token counts, the count fields are written, including zero.
func (r AttemptRecord) MarshalJSON() ([]byte, error) {
	aux := attemptJSON{
		RouteID: r.RouteID, Route: r.Route, ProviderID: r.ProviderID, Outcome: r.Outcome,
		Status: r.Status, Started: r.Started, Finished: r.Finished, MS: r.MS,
		Usage: r.Usage, Model: r.Model, Session: r.Session, TokenID: r.TokenID,
		CallerKeyID: r.CallerKeyID, CallerIssuerID: r.CallerIssuerID,
		ProviderKeyID: r.ProviderKeyID, ProviderAccount: r.ProviderAccount,
		Caller: r.Caller, Agent: r.Agent, Protocol: r.Protocol, Operation: r.Operation,
		Source: r.Source, Err: r.Err, ErrType: r.ErrType, Tries: r.Tries,
		HandedOn: r.HandedOn, Rejected: r.Rejected, Cost: r.Cost,
		TTFTMS: r.TTFTMS, FirstTextMS: r.FirstTextMS, QueueMS: r.QueueMS, RetryMS: r.RetryMS,
		BytesIn: r.BytesIn, BytesOut: r.BytesOut,
	}
	if len(aux.Usage) == 0 || bytes.Equal(bytes.TrimSpace(aux.Usage), []byte("null")) {
		aux.Usage = nil
	}
	if r.counted || r.hasCounts() {
		in, out, cacheRead, cacheWrite, oneHour := r.In, r.Out, r.CacheRead, r.CacheWrite, r.CacheWrite1H
		reasoning := r.Reasoning
		aux.In, aux.Out = &in, &out
		aux.CacheRead, aux.CacheWrite, aux.CacheWrite1H = &cacheRead, &cacheWrite, &oneHour
		aux.Reasoning = &reasoning
	}
	return json.Marshal(aux)
}

// UnmarshalJSON keeps an older line that has no count fields.
func (r *AttemptRecord) UnmarshalJSON(data []byte) error {
	var aux attemptJSON
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = AttemptRecord{
		RouteID: aux.RouteID, Route: aux.Route, ProviderID: aux.ProviderID, Outcome: aux.Outcome,
		Status: aux.Status, Started: aux.Started, Finished: aux.Finished, MS: aux.MS,
		Usage: aux.Usage, Model: aux.Model, Session: aux.Session, TokenID: aux.TokenID,
		CallerKeyID: aux.CallerKeyID, CallerIssuerID: aux.CallerIssuerID,
		ProviderKeyID: aux.ProviderKeyID, ProviderAccount: aux.ProviderAccount,
		Caller: aux.Caller, Agent: aux.Agent, Protocol: aux.Protocol, Operation: aux.Operation,
		Source: aux.Source, Err: aux.Err, ErrType: aux.ErrType, Tries: aux.Tries,
		HandedOn: aux.HandedOn, Rejected: aux.Rejected, Cost: aux.Cost,
		TTFTMS: aux.TTFTMS, FirstTextMS: aux.FirstTextMS, QueueMS: aux.QueueMS, RetryMS: aux.RetryMS,
		BytesIn: aux.BytesIn, BytesOut: aux.BytesOut,
	}
	if aux.In != nil || aux.Out != nil || aux.CacheRead != nil || aux.CacheWrite != nil ||
		aux.CacheWrite1H != nil || aux.Reasoning != nil {
		r.counted = true
		r.In, r.Out = nzi(aux.In), nzi(aux.Out)
		r.CacheRead, r.CacheWrite, r.CacheWrite1H = nzi(aux.CacheRead), nzi(aux.CacheWrite), nzi(aux.CacheWrite1H)
		r.Reasoning = nzi(aux.Reasoning)
	}
	return nil
}

// UsageFilter selects attempt records. Zero fields are unconstrained.
// RouteID matches either the per-request id or the configured route id.
// CallerKeyID matches callerKeyId and the older tokenId.
type UsageFilter struct {
	RouteID     string
	ProviderID  string
	TokenID     string
	CallerKeyID string
	Agent       string
	Model       string
	Source      string
	Start       *time.Time
	End         *time.Time
}

func (s *Store) usageFile() string {
	return filepath.Join(s.paths.UsageDir, "attempts.jsonl")
}

// AppendUsage adds one JSON line. It does not rewrite the config file.
// A usage object with token counts is stored as in, out, cacheRead,
// cacheWrite, cacheWrite1h, and reasoning.
func (s *Store) AppendUsage(rec AttemptRecord) error {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	now := time.Now().UTC()
	if rec.Started.IsZero() {
		rec.Started = now
	}
	if rec.Finished.IsZero() {
		rec.Finished = now
	}
	rec.prepare()
	if err := os.MkdirAll(s.paths.UsageDir, 0o700); err != nil {
		s.usageIndex.reset()
		return err
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	path := s.usageFile()
	if !s.usageIndex.tailClean {
		trimmed, err := trimUsageTail(path)
		if err != nil {
			s.usageIndex.reset()
			return err
		}
		if trimmed {
			s.usageIndex.reset()
		}
		s.usageIndex.tailClean = true
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		s.usageIndex.reset()
		return err
	}
	defer f.Close()
	n, err := f.Write(append(b, '\n'))
	if err != nil {
		s.usageIndex.reset()
		return err
	}
	s.usageIndex.appended(rec, int64(n))
	s.usageIndex.tailClean = true
	return nil
}

// ReadUsage returns records, or ErrUsageUnknown when the store cannot be read
// or a complete line cannot be parsed. A missing file is an empty list, not unknown.
// A trailing partial line with no newline is skipped and is not an error; the
// next append removes that fragment before writing a fresh line.
func (s *Store) ReadUsage(f UsageFilter) ([]AttemptRecord, error) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	recs, err := s.readUsageLocked()
	if err != nil {
		return nil, err
	}
	out := make([]AttemptRecord, 0, len(recs))
	for _, rec := range recs {
		if matchUsage(rec, f) {
			out = append(out, rec)
		}
	}
	return out, nil
}

// SortUsageNewestFirst orders records by start time, newest first. Equal
// starts keep their file order.
func SortUsageNewestFirst(recs []AttemptRecord) {
	sort.SliceStable(recs, func(i, j int) bool {
		return recs[i].Started.After(recs[j].Started)
	})
}

// completeUsageLines drops a trailing partial line that was not finished with a newline.
func completeUsageLines(data []byte) []byte {
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return data
	}
	if i := bytes.LastIndexByte(data, '\n'); i >= 0 {
		return data[:i+1]
	}
	return nil
}

func matchUsage(rec AttemptRecord, f UsageFilter) bool {
	if f.RouteID != "" && rec.RouteID != f.RouteID && rec.Route != f.RouteID {
		return false
	}
	if f.ProviderID != "" && rec.ProviderID != f.ProviderID {
		return false
	}
	key := f.CallerKeyID
	if key == "" {
		key = f.TokenID
	}
	if key != "" && rec.CallerKey() != key {
		return false
	}
	if f.Agent != "" && rec.AgentName() != f.Agent {
		return false
	}
	if f.Model != "" && rec.Model != f.Model {
		return false
	}
	if f.Source != "" && rec.Source != f.Source {
		return false
	}
	if f.Start != nil && rec.Started.Before(*f.Start) {
		return false
	}
	if f.End != nil && rec.Started.After(*f.End) {
		return false
	}
	return true
}

// PeriodStart is the inclusive start of a usage window in now's location.
// today is local midnight. 7d is six calendar days earlier. 30d is twenty-nine calendar days earlier.
// all is the zero time. Any other period is not a window.
func PeriodStart(period string, now time.Time) (time.Time, bool) {
	loc := now.Location()
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	switch period {
	case "today":
		return day, true
	case "7d":
		return day.AddDate(0, 0, -6), true
	case "30d":
		return day.AddDate(0, 0, -29), true
	case "all":
		return time.Time{}, true
	default:
		return time.Time{}, false
	}
}

// SummarizeUsage adds the lines it is given to a total and to its groups.
// Cost stays zero and Unpriced stays zero until a reader with a price list
// uses Summarize.
func SummarizeUsage(recs []AttemptRecord) UsageSummary {
	return Summarize(recs, SummarizeOptions{})
}

// Summarize adds the lines it is given that fall in the period's window.
// A line with no stored counts uses its usage object. Probe, MCP, and search
// lines are left out before any total.
func Summarize(recs []AttemptRecord, opts SummarizeOptions) UsageSummary {
	sum := UsageSummary{
		Agents: []UsageGroup{}, Models: []UsageGroup{}, ProviderKeys: []UsageGroup{},
		Accounts: []UsageGroup{}, CallerKeys: []UsageGroup{}, Sessions: []UsageGroup{},
		Series: []UsagePoint{},
	}
	agents := map[string]*UsageGroup{}
	models := map[string]*UsageGroup{}
	providerKeys := map[string]*UsageGroup{}
	accounts := map[string]*UsageGroup{}
	callerKeys := map[string]*UsageGroup{}
	sessions := map[string]*UsageGroup{}
	points := map[time.Time]*UsagePoint{}
	loc := opts.Location
	if loc == nil {
		loc = opts.Now.Location()
		if opts.Now.IsZero() {
			loc = time.Local
		}
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	now = now.In(loc)
	windowStart, windowed := PeriodStart(opts.Period, now)

	for _, rec := range recs {
		if !rec.InTotals() {
			continue
		}
		if opts.Hide != nil && opts.Hide(rec) {
			continue
		}
		if opts.Period != "" && windowed && !windowStart.IsZero() && rec.Started.Before(windowStart) {
			continue
		}
		calls := 1
		if rec.HandedOn {
			calls = 0
		}
		failed, canceled, retried := 0, 0, 0
		if calls > 0 {
			switch {
			case rec.Canceled():
				canceled = 1
			case rec.Failed():
				failed = 1
			case rec.Tries > 1:
				retried = 1
			}
		}
		counts := rec.Counts()
		cost, unpriced := priceOf(rec, counts, opts.Price)
		addTotal(&sum, calls, failed, canceled, retried, counts, cost, unpriced)
		addGroup(agents, rec.AgentName(), calls, failed, canceled, retried, counts, cost, unpriced)
		addGroup(models, rec.Model, calls, failed, canceled, retried, counts, cost, unpriced)
		addGroup(providerKeys, rec.ProviderKeyID, calls, failed, canceled, retried, counts, cost, unpriced)
		addGroup(accounts, rec.ProviderAccount, calls, failed, canceled, retried, counts, cost, unpriced)
		addGroup(callerKeys, rec.CallerKey(), calls, failed, canceled, retried, counts, cost, unpriced)
		if rec.Source != SourceReported {
			addGroup(sessions, rec.Session, calls, failed, canceled, retried, counts, cost, unpriced)
		}
		if opts.Period != "" {
			key := seriesBucket(rec.Started, opts.Period, loc)
			point := points[key]
			if point == nil {
				point = &UsagePoint{Start: key}
				points[key] = point
			}
			addPoint(point, calls, failed, canceled, counts, cost, unpriced)
		}
	}
	sum.Agents = sortedGroups(agents)
	sum.Models = sortedGroups(models)
	sum.ProviderKeys = sortedGroups(providerKeys)
	sum.Accounts = sortedGroups(accounts)
	sum.CallerKeys = sortedGroups(callerKeys)
	sum.Sessions = sortedGroups(sessions)
	if opts.Period != "" {
		sum.Series = seriesPoints(opts.Period, now, loc, points)
	}
	sum.Cost = round6(sum.Cost)
	return sum
}

func priceOf(rec AttemptRecord, c UsageCounts, fn func(AttemptRecord) (float64, bool)) (float64, int) {
	if rec.Cost != nil {
		return *rec.Cost, 0
	}
	if c.In+c.Out == 0 {
		return 0, 0
	}
	if fn == nil {
		return 0, 0
	}
	cost, ok := fn(rec)
	if !ok {
		return 0, 1
	}
	return cost, 0
}

func addTotal(sum *UsageSummary, calls, failed, canceled, retried int, c UsageCounts, cost float64, unpriced int) {
	sum.Calls += calls
	sum.Failed += failed
	sum.Canceled += canceled
	sum.Retried += retried
	sum.In += c.In
	sum.Out += c.Out
	sum.CacheRead += c.CacheRead
	sum.CacheWrite += c.CacheWrite
	sum.CacheWrite1H += c.CacheWrite1H
	sum.Reasoning += c.Reasoning
	sum.Cost += cost
	sum.Unpriced += unpriced
}

func addGroup(groups map[string]*UsageGroup, key string, calls, failed, canceled, retried int, c UsageCounts, cost float64, unpriced int) {
	if key == "" {
		return
	}
	group := groups[key]
	if group == nil {
		group = &UsageGroup{Key: key}
		groups[key] = group
	}
	group.Calls += calls
	group.Failed += failed
	group.Canceled += canceled
	group.Retried += retried
	group.In += c.In
	group.Out += c.Out
	group.CacheRead += c.CacheRead
	group.CacheWrite += c.CacheWrite
	group.CacheWrite1H += c.CacheWrite1H
	group.Reasoning += c.Reasoning
	group.Cost += cost
	group.Unpriced += unpriced
}

func addPoint(point *UsagePoint, calls, failed, canceled int, c UsageCounts, cost float64, unpriced int) {
	point.Calls += calls
	point.Failed += failed
	point.Canceled += canceled
	point.In += c.In
	point.Out += c.Out
	point.CacheRead += c.CacheRead
	point.CacheWrite += c.CacheWrite
	point.CacheWrite1H += c.CacheWrite1H
	point.Reasoning += c.Reasoning
	point.Cost += cost
	point.Unpriced += unpriced
}

func sortedGroups(groups map[string]*UsageGroup) []UsageGroup {
	out := make([]UsageGroup, 0, len(groups))
	for _, group := range groups {
		group.Cost = round6(group.Cost)
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func seriesBucket(at time.Time, period string, loc *time.Location) time.Time {
	local := at.In(loc)
	switch period {
	case "today":
		return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, loc)
	case "7d", "30d":
		return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	case "all":
		day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		offset := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -offset)
	default:
		return local
	}
}

// seriesPoints fills every bucket from the window start through now. A
// record before today's midnight therefore cannot appear in today's series.
func seriesPoints(period string, now time.Time, loc *time.Location, points map[time.Time]*UsagePoint) []UsagePoint {
	start, ok := PeriodStart(period, now)
	if !ok {
		return []UsagePoint{}
	}
	if period == "all" {
		if len(points) == 0 {
			return []UsagePoint{}
		}
		first := time.Time{}
		for at := range points {
			if first.IsZero() || at.Before(first) {
				first = at
			}
		}
		start = first
	}
	end := seriesBucket(now, period, loc)
	var out []UsagePoint
	for at := seriesBucket(start, period, loc); !at.After(end); {
		point := points[at]
		if point == nil {
			point = &UsagePoint{Start: at}
		}
		point.Cost = round6(point.Cost)
		out = append(out, *point)
		switch period {
		case "today":
			at = at.Add(time.Hour)
		case "7d", "30d":
			at = at.AddDate(0, 0, 1)
		case "all":
			at = at.AddDate(0, 0, 7)
		default:
			return []UsagePoint{}
		}
		if len(out) > 10000 {
			break
		}
	}
	if out == nil {
		out = []UsagePoint{}
	}
	return out
}

func round6(v float64) float64 {
	const scale = 1e6
	if v >= 0 {
		return float64(int64(v*scale+0.5)) / scale
	}
	return float64(int64(v*scale-0.5)) / scale
}

// Counts returns the stored token split, or the split parsed from Usage when the line has none.
func (r AttemptRecord) Counts() UsageCounts {
	if r.counted || r.hasCounts() {
		return UsageCounts{
			In: r.In, Out: r.Out, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite,
			CacheWrite1H: r.CacheWrite1H, Reasoning: r.Reasoning,
		}
	}
	c, ok := CountsFromUsage(r.Usage)
	if !ok {
		return UsageCounts{}
	}
	return c
}

// hasCounts reports whether the exported fields already carry a split.
// A line read from disk sets counted; a record constructed by a caller does not.
func (r AttemptRecord) hasCounts() bool {
	return r.In != 0 || r.Out != 0 || r.CacheRead != 0 || r.CacheWrite != 0 ||
		r.CacheWrite1H != 0 || r.Reasoning != 0
}

func (r *AttemptRecord) prepare() {
	if r.Status == 0 {
		r.Status = statusFromOutcome(r.Outcome)
	}
	if r.MS == 0 && !r.Started.IsZero() && !r.Finished.IsZero() && !r.Finished.Before(r.Started) {
		ms := r.Finished.Sub(r.Started) / time.Millisecond
		if ms > 0 && ms < 1<<31 {
			r.MS = int(ms)
		}
	}
	if r.CacheWrite1H > 0 && r.CacheWrite1H > r.CacheWrite {
		r.CacheWrite1H = r.CacheWrite
	}
	if r.counted || r.hasCounts() {
		r.counted = true
		return
	}
	if len(bytes.TrimSpace(r.Usage)) == 0 {
		return
	}
	c, ok := CountsFromUsage(r.Usage)
	if !ok {
		return
	}
	r.In, r.Out = c.In, c.Out
	r.CacheRead, r.CacheWrite = c.CacheRead, c.CacheWrite
	r.CacheWrite1H, r.Reasoning = c.CacheWrite1H, c.Reasoning
	r.counted = true
}

func statusFromOutcome(outcome string) int {
	n, ok := strings.CutPrefix(outcome, "http_")
	if !ok || n == "" {
		return 0
	}
	status, err := strconv.Atoi(n)
	if err != nil || status < 100 || status > 599 {
		return 0
	}
	return status
}

type usageShape struct {
	PromptTokens             *int                    `json:"prompt_tokens"`
	CompletionTokens         *int                    `json:"completion_tokens"`
	InputTokens              *int                    `json:"input_tokens"`
	OutputTokens             *int                    `json:"output_tokens"`
	PromptTokenCount         *int                    `json:"promptTokenCount"`
	CandidatesTokenCount     *int                    `json:"candidatesTokenCount"`
	CacheReadInputTokens     *int                    `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int                    `json:"cache_creation_input_tokens"`
	CachedContentTokenCount  *int                    `json:"cachedContentTokenCount"`
	ThoughtsTokenCount       *int                    `json:"thoughtsTokenCount"`
	PromptDetails            *cachedDetails          `json:"prompt_tokens_details"`
	CompletionDetails        *reasoningDetails       `json:"completion_tokens_details"`
	InputDetails             *cachedDetails          `json:"input_tokens_details"`
	OutputDetails            *reasoningDetails       `json:"output_tokens_details"`
	CacheCreation            *cacheCreationBreakdown `json:"cache_creation"`
}

type cachedDetails struct {
	CachedTokens *int `json:"cached_tokens"`
}

type reasoningDetails struct {
	ReasoningTokens *int `json:"reasoning_tokens"`
}

type cacheCreationBreakdown struct {
	FiveMinute *int `json:"ephemeral_5m_input_tokens"`
	OneHour    *int `json:"ephemeral_1h_input_tokens"`
}

// CountsFromUsage splits a provider usage object.
// OpenAI and Gemini prompt totals include cached tokens, and those are removed from In.
// Anthropic input_tokens is already the uncached input. A total-only object is not a count.
func CountsFromUsage(raw json.RawMessage) (UsageCounts, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return UsageCounts{}, false
	}
	var doc usageShape
	if json.Unmarshal(raw, &doc) != nil || !doc.known() {
		return UsageCounts{}, false
	}
	var c UsageCounts
	switch {
	case doc.anthropic():
		c.In = nzi(doc.InputTokens)
		c.Out = nzi(doc.OutputTokens)
		c.CacheRead = nzi(doc.CacheReadInputTokens)
		c.CacheWrite = doc.cacheWrite()
		c.CacheWrite1H = doc.cacheWrite1h()
	case doc.PromptTokens != nil || doc.CompletionTokens != nil:
		c.CacheRead = nzi(detailCached(doc.PromptDetails))
		c.In = sub(nzi(doc.PromptTokens), c.CacheRead)
		c.Out = nzi(doc.CompletionTokens)
		c.Reasoning = nzi(detailReasoning(doc.CompletionDetails))
	case doc.PromptTokenCount != nil || doc.CandidatesTokenCount != nil || doc.CachedContentTokenCount != nil || doc.ThoughtsTokenCount != nil:
		c.CacheRead = nzi(doc.CachedContentTokenCount)
		c.In = sub(nzi(doc.PromptTokenCount), c.CacheRead)
		c.Out = nzi(doc.CandidatesTokenCount)
		c.Reasoning = nzi(doc.ThoughtsTokenCount)
	default:
		c.CacheRead = nzi(detailCached(doc.InputDetails))
		c.In = sub(nzi(doc.InputTokens), c.CacheRead)
		c.Out = nzi(doc.OutputTokens)
		c.Reasoning = nzi(detailReasoning(doc.OutputDetails))
	}
	if c.CacheWrite1H > c.CacheWrite {
		c.CacheWrite1H = c.CacheWrite
	}
	return c, true
}

func (d usageShape) anthropic() bool {
	return d.CacheReadInputTokens != nil || d.CacheCreationInputTokens != nil || d.CacheCreation != nil
}

func (d usageShape) known() bool {
	return d.PromptTokens != nil || d.CompletionTokens != nil || d.InputTokens != nil || d.OutputTokens != nil ||
		d.PromptTokenCount != nil || d.CandidatesTokenCount != nil || d.anthropic() ||
		d.CachedContentTokenCount != nil || d.ThoughtsTokenCount != nil
}

func (d usageShape) cacheWrite() int {
	if d.CacheCreationInputTokens != nil {
		return *d.CacheCreationInputTokens
	}
	if d.CacheCreation == nil {
		return 0
	}
	return nzi(d.CacheCreation.FiveMinute) + nzi(d.CacheCreation.OneHour)
}

func (d usageShape) cacheWrite1h() int {
	if d.CacheCreation == nil {
		return 0
	}
	oneHour := nzi(d.CacheCreation.OneHour)
	if oneHour > d.cacheWrite() {
		return d.cacheWrite()
	}
	return oneHour
}

func detailCached(d *cachedDetails) *int {
	if d == nil {
		return nil
	}
	return d.CachedTokens
}

func detailReasoning(d *reasoningDetails) *int {
	if d == nil {
		return nil
	}
	return d.ReasoningTokens
}

func nzi(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

func sub(total, part int) int {
	if part > total {
		return 0
	}
	return total - part
}
