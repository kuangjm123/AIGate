package state

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// AttemptRecord is one gateway attempt. Usage is the provider's own object.
// A nil Usage is unknown and is omitted, not stored as zero.
// Model, Session, TokenID, Caller, and Protocol describe the caller-visible call.
// TokenID is the gateway token's id, never its secret.
// Caller is the application name for a local magpie key, or the client word when the local key is not one.
// Status is the upstream HTTP status when this attempt received one. Zero means there was none.
// MS is the span from Started to Finished in milliseconds. Zero means under a millisecond, or unknown.
// In, Out, CacheRead, CacheWrite, and Reasoning are set together when the usage object had token counts.
// In does not include cache read or cache write. An older line without those fields still parses.
type AttemptRecord struct {
	RouteID    string          `json:"routeId"`
	ProviderID string          `json:"providerId"`
	Outcome    string          `json:"outcome"`
	Status     int             `json:"status,omitempty"`
	Started    time.Time       `json:"started"`
	Finished   time.Time       `json:"finished"`
	MS         int             `json:"ms,omitempty"`
	Usage      json.RawMessage `json:"usage,omitempty"`
	Model      string          `json:"model,omitempty"`
	Session    string          `json:"session,omitempty"`
	TokenID    string          `json:"tokenId,omitempty"`
	Caller     string          `json:"caller,omitempty"`
	Protocol   string          `json:"protocol,omitempty"`
	In         int             `json:"in,omitempty"`
	Out        int             `json:"out,omitempty"`
	CacheRead  int             `json:"cacheRead,omitempty"`
	CacheWrite int             `json:"cacheWrite,omitempty"`
	Reasoning  int             `json:"reasoning,omitempty"`
	counted    bool
}

// UsageCounts is the normalized token split of one attempt.
type UsageCounts struct {
	In         int
	Out        int
	CacheRead  int
	CacheWrite int
	Reasoning  int
}

// UsageSummary is the sum of the attempt lines it was given.
// Calls is the number of lines, not a merged request.
// Cost and Unpriced are filled by the reader that has a price list. Cost is US dollars.
type UsageSummary struct {
	Calls      int     `json:"calls"`
	In         int     `json:"in"`
	Out        int     `json:"out"`
	CacheRead  int     `json:"cacheRead"`
	CacheWrite int     `json:"cacheWrite"`
	Reasoning  int     `json:"reasoning"`
	Cost       float64 `json:"cost"`
	Unpriced   int     `json:"unpriced"`
}

type attemptJSON struct {
	RouteID    string          `json:"routeId"`
	ProviderID string          `json:"providerId"`
	Outcome    string          `json:"outcome"`
	Status     int             `json:"status,omitempty"`
	Started    time.Time       `json:"started"`
	Finished   time.Time       `json:"finished"`
	MS         int             `json:"ms,omitempty"`
	Usage      json.RawMessage `json:"usage,omitempty"`
	Model      string          `json:"model,omitempty"`
	Session    string          `json:"session,omitempty"`
	TokenID    string          `json:"tokenId,omitempty"`
	Caller     string          `json:"caller,omitempty"`
	Protocol   string          `json:"protocol,omitempty"`
	In         *int            `json:"in,omitempty"`
	Out        *int            `json:"out,omitempty"`
	CacheRead  *int            `json:"cacheRead,omitempty"`
	CacheWrite *int            `json:"cacheWrite,omitempty"`
	Reasoning  *int            `json:"reasoning,omitempty"`
}

// MarshalJSON omits a missing or null usage object.
// When the line has token counts, the five count fields are written, including zero.
func (r AttemptRecord) MarshalJSON() ([]byte, error) {
	aux := attemptJSON{
		RouteID: r.RouteID, ProviderID: r.ProviderID, Outcome: r.Outcome, Status: r.Status,
		Started: r.Started, Finished: r.Finished, MS: r.MS,
		Usage: r.Usage, Model: r.Model, Session: r.Session, TokenID: r.TokenID,
		Caller: r.Caller, Protocol: r.Protocol,
	}
	if len(aux.Usage) == 0 || bytes.Equal(bytes.TrimSpace(aux.Usage), []byte("null")) {
		aux.Usage = nil
	}
	if r.counted {
		in, out, cacheRead, cacheWrite, reasoning := r.In, r.Out, r.CacheRead, r.CacheWrite, r.Reasoning
		aux.In, aux.Out, aux.CacheRead, aux.CacheWrite, aux.Reasoning = &in, &out, &cacheRead, &cacheWrite, &reasoning
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
		RouteID: aux.RouteID, ProviderID: aux.ProviderID, Outcome: aux.Outcome, Status: aux.Status,
		Started: aux.Started, Finished: aux.Finished, MS: aux.MS,
		Usage: aux.Usage, Model: aux.Model, Session: aux.Session, TokenID: aux.TokenID,
		Caller: aux.Caller, Protocol: aux.Protocol,
	}
	if aux.In != nil || aux.Out != nil || aux.CacheRead != nil || aux.CacheWrite != nil || aux.Reasoning != nil {
		r.counted = true
		r.In, r.Out = nzi(aux.In), nzi(aux.Out)
		r.CacheRead, r.CacheWrite, r.Reasoning = nzi(aux.CacheRead), nzi(aux.CacheWrite), nzi(aux.Reasoning)
	}
	return nil
}

// UsageFilter selects attempt records. Zero fields are unconstrained.
type UsageFilter struct {
	RouteID    string
	ProviderID string
	TokenID    string
	Start      *time.Time
	End        *time.Time
}

func (s *Store) usageFile() string {
	return filepath.Join(s.paths.UsageDir, "attempts.jsonl")
}

// AppendUsage adds one JSON line. It does not rewrite the config file.
// A usage object with token counts is stored as in, out, cacheRead, cacheWrite, and reasoning.
func (s *Store) AppendUsage(rec AttemptRecord) error {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	rec.prepare()
	if err := os.MkdirAll(s.paths.UsageDir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.usageFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// ReadUsage returns records, or ErrUsageUnknown when the store cannot be read
// or a complete line cannot be parsed. A missing file is an empty list, not unknown.
// A trailing partial line with no newline is left for the next read and is not an error.
func (s *Store) ReadUsage(f UsageFilter) ([]AttemptRecord, error) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	path := s.usageFile()
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			info, statErr := os.Stat(s.paths.UsageDir)
			if statErr == nil && !info.IsDir() {
				return nil, ErrUsageUnknown
			}
			if statErr != nil && !os.IsNotExist(statErr) {
				return nil, ErrUsageUnknown
			}
			return []AttemptRecord{}, nil
		}
		return nil, ErrUsageUnknown
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		return nil, ErrUsageUnknown
	}
	data := completeUsageLines(raw)
	var out []AttemptRecord
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec AttemptRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, ErrUsageUnknown
		}
		if !matchUsage(rec, f) {
			continue
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, ErrUsageUnknown
	}
	if out == nil {
		out = []AttemptRecord{}
	}
	return out, nil
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
	if f.RouteID != "" && rec.RouteID != f.RouteID {
		return false
	}
	if f.ProviderID != "" && rec.ProviderID != f.ProviderID {
		return false
	}
	if f.TokenID != "" && rec.TokenID != f.TokenID {
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

// SummarizeUsage adds the lines it is given. A line with no stored counts uses its usage object.
func SummarizeUsage(recs []AttemptRecord) UsageSummary {
	var sum UsageSummary
	sum.Calls = len(recs)
	for _, rec := range recs {
		c := rec.Counts()
		sum.In += c.In
		sum.Out += c.Out
		sum.CacheRead += c.CacheRead
		sum.CacheWrite += c.CacheWrite
		sum.Reasoning += c.Reasoning
	}
	return sum
}

// Counts returns the stored token split, or the split parsed from Usage when the line has none.
func (r AttemptRecord) Counts() UsageCounts {
	if r.counted {
		return UsageCounts{In: r.In, Out: r.Out, CacheRead: r.CacheRead, CacheWrite: r.CacheWrite, Reasoning: r.Reasoning}
	}
	c, ok := CountsFromUsage(r.Usage)
	if !ok {
		return UsageCounts{}
	}
	return c
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
	if r.counted || len(bytes.TrimSpace(r.Usage)) == 0 {
		return
	}
	c, ok := CountsFromUsage(r.Usage)
	if !ok {
		return
	}
	r.In, r.Out = c.In, c.Out
	r.CacheRead, r.CacheWrite, r.Reasoning = c.CacheRead, c.CacheWrite, c.Reasoning
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
