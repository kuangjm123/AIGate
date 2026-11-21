package state

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// TurnRecord is one finished model call filed under the client's session id.
// Output is the whole reply. A stream is already joined into that one string.
// A call with no session id is not a turn and is not written.
type TurnRecord struct {
	Session    string    `json:"session"`
	At         time.Time `json:"at"`
	Model      string    `json:"model,omitempty"`
	Caller     string    `json:"caller,omitempty"`
	ProviderID string    `json:"providerId,omitempty"`
	Protocol   string    `json:"protocol,omitempty"`
	Input      string    `json:"input,omitempty"`
	Output     string    `json:"output"`
}

// ConversationSummary is one dialogue, in archive order.
type ConversationSummary struct {
	Session string    `json:"session"`
	Turns   int       `json:"turns"`
	Last    time.Time `json:"last"`
}

func (s *Store) conversationDir() string {
	return filepath.Join(s.paths.UsageDir, "conversations")
}

func conversationFile(dir, session string) string {
	sum := sha256.Sum256([]byte(session))
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".jsonl")
}

// AppendTurn files one finished reply under its session.
// An empty session writes nothing. The output is stored as given.
func (s *Store) AppendTurn(rec TurnRecord) error {
	if rec.Session == "" {
		return nil
	}
	s.convMu.Lock()
	defer s.convMu.Unlock()
	dir := s.conversationDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(conversationFile(dir, rec.Session), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// ListConversations returns each dialogue. A missing directory is an empty list.
// A file that cannot be parsed is ErrConversationUnknown, not an empty dialogue.
func (s *Store) ListConversations() ([]ConversationSummary, error) {
	s.convMu.Lock()
	defer s.convMu.Unlock()
	dir := s.conversationDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []ConversationSummary{}, nil
		}
		return nil, ErrConversationUnknown
	}
	out := make([]ConversationSummary, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		turns, err := readTurns(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, ErrConversationUnknown
		}
		if len(turns) == 0 {
			continue
		}
		last := turns[len(turns)-1]
		out = append(out, ConversationSummary{Session: last.Session, Turns: len(turns), Last: last.At})
	}
	return out, nil
}

// ReadConversation returns the turns filed under session, in append order.
// A dialogue that has not been written is an empty list.
// A file that cannot be parsed is ErrConversationUnknown.
func (s *Store) ReadConversation(session string) ([]TurnRecord, error) {
	if session == "" {
		return []TurnRecord{}, nil
	}
	s.convMu.Lock()
	defer s.convMu.Unlock()
	path := conversationFile(s.conversationDir(), session)
	turns, err := readTurns(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []TurnRecord{}, nil
		}
		return nil, ErrConversationUnknown
	}
	for _, turn := range turns {
		if turn.Session != session {
			return nil, ErrConversationUnknown
		}
	}
	return turns, nil
}

func readTurns(path string) ([]TurnRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []TurnRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec TurnRecord
		if err := json.Unmarshal(line, &rec); err != nil || rec.Session == "" {
			return nil, ErrConversationUnknown
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, ErrConversationUnknown
	}
	if out == nil {
		out = []TurnRecord{}
	}
	return out, nil
}
