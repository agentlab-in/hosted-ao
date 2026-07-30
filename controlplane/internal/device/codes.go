package device

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// userCodeAlphabet is RFC 8628's suggested 20-character set: consonants
	// only, uppercase only. It has no vowels, so a generated code cannot spell
	// a word, and no digits or visually confusable letters (no 0/O, 1/I/L,
	// 5/S, 8/B), because a human reads this off a terminal and types it into a
	// browser on another device.
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	// userCodeLength is the number of characters before formatting. 20^8 is
	// about 2.6e10 possibilities, which is only safe because guessing is
	// online, requires a signed-in account, and is rate limited, and because
	// codes live for deviceCodeTTL.
	userCodeLength = 8
	// userCodeGroup is where the display hyphen goes: XXXX-XXXX.
	userCodeGroup = 4

	// deviceCodeBytes is the entropy of the device code, the bearer secret the
	// VM polls with: 32 bytes is 256 bits, so it is not guessable and needs no
	// rate limit of its own.
	deviceCodeBytes = 32

	// maxCodeGenerationAttempts bounds the retry loop that resolves a
	// user_code UNIQUE collision. A collision needs a live code to already
	// hold the drawn value, so more than a couple of retries means something
	// is wrong rather than unlucky.
	maxCodeGenerationAttempts = 5

	// maxMachineNameRunes caps machine_name on the device authorization
	// endpoint. That endpoint is unauthenticated and the row it writes is
	// permanent, so the name is attacker-chosen storage: without a cap, one
	// request writes however much the body limit allows. 128 is well past any
	// hostname or label an operator would type, and the approval page displays
	// the value, so a longer one would not render usefully anyway.
	maxMachineNameRunes = 128
)

// newUserCode returns a fresh user code in storage form (unformatted,
// uppercase) drawn from a CSPRNG. crypto/rand.Int is used rather than
// reducing a random byte modulo the alphabet length, which would bias the
// first 16 letters of a 20-letter alphabet.
func newUserCode() (string, error) {
	var b strings.Builder
	b.Grow(userCodeLength)
	alphabetLen := big.NewInt(int64(len(userCodeAlphabet)))
	for range userCodeLength {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", fmt.Errorf("generate user code: %w", err)
		}
		b.WriteByte(userCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// newDeviceCode returns a fresh device code, the high-entropy bearer secret
// the polling client holds.
func newDeviceCode() (string, error) {
	buf := make([]byte, deviceCodeBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate device code: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashCode is how a device code is stored and looked up. The plaintext is
// never written to the database, so a leak of controlplane.db hands over no
// live device codes, and the lookup compares a fixed-width digest rather than
// the secret itself.
func hashCode(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// formatUserCode renders a stored user code for display: XXXX-XXXX.
func formatUserCode(stored string) string {
	if len(stored) != userCodeLength {
		return stored
	}
	return stored[:userCodeGroup] + "-" + stored[userCodeGroup:]
}

// normalizeUserCode converts what a human typed into storage form. People
// paste the hyphen, type lowercase, and pick up a trailing space from a
// terminal copy, and none of that should be a failed attempt against the rate
// limiter. Characters outside the alphabet are dropped rather than rejected
// so that a typed "0" or "O" both normalize to a lookup that simply misses.
func normalizeUserCode(typed string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(typed)) {
		if strings.ContainsRune(userCodeAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizePublicURL turns the public URL a VM reports into the single form
// stored in machines.hostname and handed back to the polling client: an
// origin with no trailing slash, like https://vm.example.com. `ao vm serve`
// reduces that origin back to a bare hostname for the certificate whitelist
// (see backend/internal/vmgateway/config.go), so a value that is not a clean
// origin has to be rejected here rather than stored and failed on later.
func normalizePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("public_url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("public_url %q is not a URL", raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("public_url scheme %q is not http or https", u.Scheme)
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("public_url %q must be a bare origin like https://vm.example.com", raw)
	}
	if p := strings.Trim(u.Path, "/"); p != "" {
		return "", fmt.Errorf("public_url %q must be a bare origin with no path", raw)
	}
	// http is a local-development affordance only. The desktop builds its base
	// URL from this value and sends the bearer token and the Secure ao_gw_token
	// cookie to it, and `ao vm serve` only ever listens on TLS, so a plaintext
	// origin for a routable host is a machine that cannot work whose
	// registration nonetheless succeeded.
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return "", fmt.Errorf("public_url %q must use https: http is accepted only for a loopback host", raw)
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// isLoopbackHost reports whether host names this machine. "localhost" is
// matched by name because it is not an IP literal; everything else goes
// through net.IP, so 127.0.0.2 and [::1] are covered as well as 127.0.0.1.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hostOf returns the hostname of an already-normalized public URL, used as
// the default machine name when the client does not supply one.
func hostOf(publicURL string) string {
	if u, err := url.Parse(publicURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return publicURL
}

const (
	// attemptWindow and attemptsPerWindow bound how fast one signed-in
	// account may guess user codes. Both browser POSTs that read a user_code
	// out of a form are metered by it: the enter-code page, and the decision
	// POST that actually binds a machine. The device code endpoint needs no
	// equivalent because a device code has 256 bits of entropy, but a user code
	// has about 34, so those two are the oracle worth closing.
	attemptWindow     = time.Minute
	attemptsPerWindow = 10
)

// attemptLimiter is a fixed-window counter keyed by account id.
//
// ponytail: in-memory, so it bounds one process, and it keeps one small slice
// per account that has ever submitted a code. The control plane is a single
// instance today (one Caddy site on one box) and accounts are Google-verified,
// so both are fine; if it is ever replicated, move this to a table or a shared
// store, otherwise each replica grants the full allowance.
//
// Two more ceilings, so this control is not over-trusted. It is keyed by
// account, so N Google accounts buy N times the allowance (10N guesses a
// minute); the real bound on guessing is the 34 bits and the 15 minute code
// lifetime, not this. And l.seen never evicts, so it holds one entry per
// account that has ever submitted a code, bounded by the account count, which
// is small today. If either stops being true, key the window on the request's
// IP as well and sweep l.seen on a ticker.
type attemptLimiter struct {
	mu    sync.Mutex
	seen  map[string][]time.Time
	clock func() time.Time
}

func newAttemptLimiter() *attemptLimiter {
	return &attemptLimiter{seen: make(map[string][]time.Time), clock: time.Now}
}

// allow records an attempt for key and reports whether it is within the
// window's allowance.
func (l *attemptLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock()
	cutoff := now.Add(-attemptWindow)

	kept := l.seen[key][:0]
	for _, t := range l.seen[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= attemptsPerWindow {
		l.seen[key] = kept
		return false
	}
	l.seen[key] = append(kept, now)
	return true
}
