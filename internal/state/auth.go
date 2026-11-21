package state

import "github.com/kuangjm123/AIGate/internal/security"

// TokenOK reports whether secret is a live gateway token.
// A console session is not a gateway token. An empty secret is not valid.
func (c Config) TokenOK(secret string) bool {
	_, ok := c.TokenID(secret)
	return ok
}

// TokenID returns the id of the live gateway token that matches secret.
// It does not return the secret. An unknown or revoked token is not an id.
func (c Config) TokenID(secret string) (string, bool) {
	if secret == "" {
		return "", false
	}
	id := ""
	ok := 0
	n := 0
	for _, t := range c.GatewayTokens {
		if t.Revoked || t.Secret == "" {
			continue
		}
		n++
		bit := security.MatchBit(secret, t.Secret)
		if bit == 1 {
			id = t.ID
		}
		ok |= bit
	}
	if n == 0 {
		security.MatchBit(secret, "aigate-token-pad")
	}
	if ok != 1 {
		return "", false
	}
	return id, true
}

// SessionOK reports whether secret is a live console session.
// A gateway token is not a session. No user does not leave the console open.
func (c Config) SessionOK(secret string) bool {
	if secret == "" {
		return false
	}
	ok := 0
	n := 0
	for _, u := range c.Users {
		for _, sess := range u.Sessions {
			if sess.SecretHash == "" {
				continue
			}
			n++
			ok |= security.SecretHashMatch(secret, sess.SecretHash)
		}
	}
	if n == 0 {
		security.SecretHashMatch(secret, "0000000000000000000000000000000000000000000000000000000000000000")
	}
	return ok == 1
}
