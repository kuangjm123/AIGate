// Package security decides bind policy, secret masking, and provider base URLs.
// It does not dial.
package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const (
	// DefaultGateway is the in-memory listen address when config does not set one.
	DefaultGateway = "127.0.0.1:3425"
	// DefaultConsole is the in-memory listen address when config does not set one.
	DefaultConsole = "127.0.0.1:8081"
)

// BaseResult is the dial decision for a provider base URL.
type BaseResult int

const (
	// BaseOK means the host may dial that origin.
	BaseOK BaseResult = iota
	// BaseUnknown means the URL is unset or cannot be read. It is not dialed.
	BaseUnknown
	// BaseRejected means the URL is a known illegal target.
	BaseRejected
)

// EffectiveListen returns configured when the user set it, otherwise fallback.
// It does not write the fallback anywhere.
func EffectiveListen(configured, fallback string) string {
	if configured != "" {
		return configured
	}
	return fallback
}

// IsLoopbackHost reports whether host is a loopback IP or the name localhost.
// It does not resolve DNS.
func IsLoopbackHost(host string) bool {
	h := normalizeHost(host)
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func isUnspecified(host string) bool {
	ip := net.ParseIP(normalizeHost(host))
	return ip != nil && ip.IsUnspecified()
}

func normalizeHost(host string) string {
	h := strings.TrimSpace(host)
	h = strings.Trim(h, "[]")
	return strings.ToLower(h)
}

// GatewayMayListen reports whether the gateway listener may open.
// Loopback always may. Any other address may only when the user has turned sharing on.
func GatewayMayListen(addr string, share bool) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if IsLoopbackHost(host) {
		return true
	}
	return share
}

// ConsoleMayListen reports whether the console listener may open.
// A non-loopback address opens only when at least one user exists.
// Loopback still opens with no user, so the first registration can happen there.
func ConsoleMayListen(addr string, userExists bool) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if IsLoopbackHost(host) {
		return true
	}
	return userExists
}

// PasswordIterations is the SHA-256 stretch used for a console password.
const PasswordIterations = 100000

// HashPassword returns a random salt and the stretched password hash, both hex.
func HashPassword(password string) (salt, sum string, err error) {
	var b [16]byte
	if _, err = rand.Read(b[:]); err != nil {
		return "", "", err
	}
	salt = hex.EncodeToString(b[:])
	return salt, stretchPassword(password, salt), nil
}

// PasswordMatch reports whether password stretches to sum for salt.
// It stretches even when the stored hash cannot match, so a missing user
// does not skip the work.
func PasswordMatch(password, salt, sum string) bool {
	if salt == "" || sum == "" {
		salt = "00000000000000000000000000000000"
		sum = strings.Repeat("0", 64)
	}
	got := stretchPassword(password, salt)
	if len(got) != len(sum) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(sum)) == 1
}

func stretchPassword(password, salt string) string {
	mac := sha256.Sum256([]byte(salt + "\x00" + password))
	for i := 1; i < PasswordIterations; i++ {
		mac = sha256.Sum256(mac[:])
	}
	return hex.EncodeToString(mac[:])
}

// SecretHash is the hex SHA-256 of a session secret. The file stores this, not the secret.
func SecretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// SecretHashMatch compares secret with a stored SecretHash.
// It returns 1 on a match and 0 otherwise.
func SecretHashMatch(secret, stored string) int {
	if secret == "" || stored == "" {
		return 0
	}
	got := SecretHash(secret)
	if len(got) != len(stored) {
		return 0
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(stored))
}

// SameListen reports whether two host:port pairs are the same bind.
// IP literals are compared in canonical form. Names are not resolved.
func SameListen(a, b string) bool {
	ca, ea := canonical(a)
	cb, eb := canonical(b)
	if ea != nil || eb != nil {
		return false
	}
	return ca == cb
}

func canonical(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	host = normalizeHost(host)
	if ip := net.ParseIP(host); ip != nil {
		return net.JoinHostPort(ip.String(), port), nil
	}
	return net.JoinHostPort(host, port), nil
}

// Mask returns a fixed mask for a non-empty secret and an empty string otherwise.
// The result does not contain any part of the secret.
func Mask(secret string) string {
	if secret == "" {
		return ""
	}
	return "••••"
}

// MatchBit compares two strings without leaking which one matched via length.
// It returns 1 when they are equal and neither is empty, otherwise 0.
func MatchBit(got, want string) int {
	if got == "" || want == "" {
		return 0
	}
	ga := sha256.Sum256([]byte(got))
	wa := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(ga[:], wa[:])
}

// SecretMatch reports whether got equals want.
func SecretMatch(got, want string) bool {
	return MatchBit(got, want) == 1
}

// Bearer returns the token after "Bearer ". It returns empty when the header
// is missing or not a bearer scheme. The caller must not log the result.
func Bearer(r *http.Request) string {
	if r == nil {
		return ""
	}
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// AccessLine is the only request line the process logs: method, path, status.
// It omits the query, headers, and body.
func AccessLine(method, path string, status int) string {
	return method + " " + path + " " + itoa(status)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// CheckBaseURL decides whether the host may dial base.
// consoleListen is host:port of the console. dialOrigin is the origin about
// to be dialed; empty means the origin of base itself.
// An unset or unreadable URL is BaseUnknown. The console origin and link-local
// addresses are BaseRejected. A loopback URL is BaseOK only when the saved
// origin and the dialed origin are the same.
func CheckBaseURL(base, consoleListen, dialOrigin string) BaseResult {
	if strings.TrimSpace(base) == "" {
		return BaseUnknown
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return BaseUnknown
	}
	saved := originOf(u)
	if saved == "" {
		return BaseUnknown
	}
	if consoleListen != "" && targetsConsole(u, consoleListen) {
		return BaseRejected
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return BaseRejected
	}
	if IsLoopbackHost(host) {
		dial := saved
		if strings.TrimSpace(dialOrigin) != "" {
			if parsed, ok := parseOrigin(dialOrigin); ok {
				dial = parsed
			} else {
				return BaseUnknown
			}
		}
		if dial != saved {
			return BaseRejected
		}
	}
	return BaseOK
}

// AllowRedirect reports whether a dialer may follow location from savedBase.
// A different origin is never followed. A relative path stays on the saved origin.
func AllowRedirect(savedBase, location string) bool {
	su, err := url.Parse(savedBase)
	if err != nil || su.Host == "" {
		return false
	}
	lu, err := url.Parse(location)
	if err != nil {
		return false
	}
	if lu.Host == "" {
		return strings.HasPrefix(location, "/")
	}
	return originOf(su) == originOf(lu)
}

func parseOrigin(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	o := originOf(u)
	return o, o != ""
}

func originOf(u *url.URL) string {
	if u == nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := normalizeHost(u.Hostname())
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		default:
			port = "80"
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func targetsConsole(u *url.URL, consoleListen string) bool {
	chost, cport, err := net.SplitHostPort(consoleListen)
	if err != nil {
		return false
	}
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	if port != cport {
		return false
	}
	bh := u.Hostname()
	if hostsSame(bh, chost) || isUnspecified(chost) {
		return true
	}
	return IsLoopbackHost(chost) && IsLoopbackHost(bh)
}

func hostsSame(a, b string) bool {
	aa := normalizeHost(a)
	bb := normalizeHost(b)
	if aa == bb {
		return true
	}
	ipa := net.ParseIP(aa)
	ipb := net.ParseIP(bb)
	return ipa != nil && ipb != nil && ipa.Equal(ipb)
}
