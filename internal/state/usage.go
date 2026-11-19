package state

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// AttemptRecord is one gateway attempt. Usage is the provider's own object.
// A nil Usage is unknown and is omitted, not stored as zero.
// Model, Session, TokenID, Caller, and Protocol describe the caller-visible call.
// TokenID is the gateway token's id, never its secret.
// Caller is the application name for a local magpie key, or the client word when the local key is not one.
type AttemptRecord struct {
	RouteID    string          `json:"routeId"`
	ProviderID string          `json:"providerId"`
	Outcome    string          `json:"outcome"`
	Started    time.Time       `json:"started"`
	Finished   time.Time       `json:"finished"`
	Usage      json.RawMessage `json:"usage,omitempty"`
	Model      string          `json:"model,omitempty"`
	Session    string          `json:"session,omitempty"`
	TokenID    string          `json:"tokenId,omitempty"`
	Caller     string          `json:"caller,omitempty"`
	Protocol   string          `json:"protocol,omitempty"`
}

// MarshalJSON omits a missing or null usage object.
func (r AttemptRecord) MarshalJSON() ([]byte, error) {
	type alias AttemptRecord
	a := alias(r)
	if len(a.Usage) == 0 || bytes.Equal(bytes.TrimSpace(a.Usage), []byte("null")) {
		a.Usage = nil
	}
	return json.Marshal(a)
}

// UsageFilter selects attempt records. Zero fields are unconstrained.
type UsageFilter struct {
	RouteID    string
	ProviderID string
	Start      *time.Time
	End        *time.Time
}

func (s *Store) usageFile() string {
	return filepath.Join(s.paths.UsageDir, "attempts.jsonl")
}

// AppendUsage adds one JSON line. It does not rewrite the config file.
func (s *Store) AppendUsage(rec AttemptRecord) error {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
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
// or a line cannot be parsed. A missing file is an empty list, not unknown.
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
	var out []AttemptRecord
	sc := bufio.NewScanner(file)
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

func matchUsage(rec AttemptRecord, f UsageFilter) bool {
	if f.RouteID != "" && rec.RouteID != f.RouteID {
		return false
	}
	if f.ProviderID != "" && rec.ProviderID != f.ProviderID {
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
