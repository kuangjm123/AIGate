package state

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewID returns a 128-bit random id. Ids are not drawn from a recycle pool.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// NewSecret returns a 256-bit random secret for a gateway token.
func NewSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// FreshID returns a new id that is not in used.
func FreshID(used []string) (string, error) {
	seen := make(map[string]struct{}, len(used))
	for _, id := range used {
		seen[id] = struct{}{}
	}
	for i := 0; i < 5; i++ {
		id, err := NewID()
		if err != nil {
			return "", err
		}
		if _, ok := seen[id]; !ok {
			return id, nil
		}
	}
	return "", fmt.Errorf("id space exhausted")
}
