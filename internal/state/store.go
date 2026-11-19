package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Snapshot is an immutable config captured at a point in time.
// Callers must not mutate it. An attempt keeps the snapshot it took.
type Snapshot struct {
	Config Config
	Hash   string
}

// Store is the config file and the published snapshot.
// Readers take mu only long enough to clone the snapshot.
// Disk writes run without holding mu.
type Store struct {
	paths   Paths
	mu      sync.RWMutex
	writeMu sync.Mutex
	usageMu sync.Mutex
	snap    *Snapshot
	hash    func([]byte) (string, error)
}

// Open returns a store for paths. It does not read the file.
func Open(p Paths) *Store {
	return &Store{paths: p, hash: defaultHash}
}

func defaultHash(b []byte) (string, error) {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Paths returns the config file and usage directory.
func (s *Store) Paths() Paths {
	return s.paths
}

// Current returns a clone of the published snapshot, or nil when there is none.
func (s *Store) Current() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snap == nil {
		return nil
	}
	return &Snapshot{Config: s.snap.Config.clone(), Hash: s.snap.Hash}
}

// Load reads the config file.
// A missing file publishes an empty config and does not create the file.
// A failed read, parse, or hash keeps the last good snapshot, moves the bad
// file aside, and does not publish an empty config. The first failed load
// leaves Current nil.
func (s *Store) Load() error {
	b, err := os.ReadFile(s.paths.ConfigFile)
	if err != nil {
		if os.IsNotExist(err) {
			s.mu.Lock()
			s.snap = &Snapshot{Config: emptyConfig()}
			s.mu.Unlock()
			return nil
		}
		return s.failLoad(err)
	}
	sum, err := s.hash(b)
	if err != nil {
		return s.failLoad(err)
	}
	cfg := emptyConfig()
	if err := json.Unmarshal(b, &cfg); err != nil {
		return s.failLoad(err)
	}
	if legacyConfig(b) {
		return s.failLoad(errors.New("legacy config"))
	}
	cfg.normalize()
	s.mu.Lock()
	s.snap = &Snapshot{Config: cfg, Hash: sum}
	s.mu.Unlock()
	return nil
}

func (s *Store) failLoad(cause error) error {
	_ = setAside(s.paths.ConfigFile)
	return fmt.Errorf("%w: %v", ErrUnreadable, cause)
}

// Update clones the current snapshot, applies fn, validates, and publishes.
// fn errors and validation errors leave the previous snapshot in place.
// A nil snapshot returns ErrUnreadable.
func (s *Store) Update(fn func(*Config) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.RLock()
	if s.snap == nil {
		s.mu.RUnlock()
		return ErrUnreadable
	}
	cfg := s.snap.Config.clone()
	s.mu.RUnlock()
	if err := fn(&cfg); err != nil {
		return err
	}
	return s.publish(cfg)
}

func (s *Store) publish(cfg Config) error {
	cfg.normalize()
	if err := Validate(cfg); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	sum, err := s.hash(b)
	if err != nil {
		return err
	}
	if err := s.atomicWrite(b); err != nil {
		return err
	}
	snap := &Snapshot{Config: cfg.clone(), Hash: sum}
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()
	return nil
}

func (s *Store) atomicWrite(b []byte) error {
	dir := filepath.Dir(s.paths.ConfigFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	if existing, err := os.ReadFile(s.paths.ConfigFile); err == nil {
		var probe Config
		if json.Unmarshal(existing, &probe) != nil {
			if err := setAside(s.paths.ConfigFile); err != nil {
				return err
			}
		}
	}
	if err := replaceFile(tmpName, s.paths.ConfigFile); err != nil {
		return err
	}
	cleanup = false
	_ = os.Chmod(s.paths.ConfigFile, 0o600)
	return nil
}

func replaceFile(tmp, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		backup := dest + ".replacing"
		_ = os.Remove(backup)
		if err := os.Rename(dest, backup); err != nil {
			return err
		}
		if err := os.Rename(tmp, dest); err != nil {
			_ = os.Rename(backup, dest)
			return err
		}
		_ = os.Remove(backup)
		return nil
	}
	return os.Rename(tmp, dest)
}

// legacyConfig reports a document that still names accounts or the old console credential.
// Those files are unreadable. They are not loaded as an empty config.
func legacyConfig(b []byte) bool {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return false
	}
	if _, ok := doc["accounts"]; ok {
		return true
	}
	if _, ok := doc["consoleCredential"]; ok {
		return true
	}
	raw, ok := doc["routes"]
	if !ok {
		return false
	}
	var routes []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &routes); err != nil {
		return false
	}
	for _, route := range routes {
		if _, ok := route["accountIds"]; ok {
			return true
		}
	}
	return false
}

func setAside(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	dest := path + ".bad"
	if _, err := os.Stat(dest); err == nil {
		dest = fmt.Sprintf("%s.%d", dest, time.Now().UnixNano())
	}
	return os.Rename(path, dest)
}
