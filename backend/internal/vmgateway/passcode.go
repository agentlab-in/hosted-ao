package vmgateway

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// passcodeFileName is the single file LoadPasscodeStore/GeneratePasscode
// persist under a pair-mode Config.PasscodeDir. Only the hash is ever
// written to disk; the plaintext passcode exists only in memory, for the
// instant between generation and being handed to the caller (a provisioning
// command that prints it once, or Rotate's return value), and this package
// never persists it anywhere.
const passcodeFileName = "passcode.hash"

// passcodeHashHexLen is the length of mobilebridge.HashPassword's
// hex-encoded SHA-256 output. readPasscodeHash uses it to recognise a
// corrupt file (anything else found on disk) instead of silently treating
// garbage as a hash no real passcode will ever match.
const passcodeHashHexLen = 64

// PasscodeStore holds a pair-mode gateway's current passcode hash in memory,
// loaded from and persisted to a single file (passcode.hash) under a
// pair-mode Config.PasscodeDir. It never holds the plaintext passcode.
//
// LoadPasscodeStore requires the file to already exist and be well-formed.
// Per docs/adr/0003-pair-mode-gateway.md, a gateway that starts with no
// usable passcode would be an open door, so a missing or corrupt store fails
// loudly at startup rather than falling back to "generate one now" (which
// would silently start authenticating every request against a passcode
// nobody has ever seen) or "accept everything" (the actual open door).
// Provisioning (ao setup-vm --pair, batch 3) creates the file with
// GeneratePasscode before the gateway process that loads it is ever started.
type PasscodeStore struct {
	dir  string
	hash atomic.Pointer[string]
}

// LoadPasscodeStore reads the persisted passcode hash from dir and returns a
// store ready to verify requests against it. See the PasscodeStore doc for
// why a missing or corrupt file is a fatal error here rather than a fallback.
func LoadPasscodeStore(dir string) (*PasscodeStore, error) {
	hash, err := readPasscodeHash(dir)
	if err != nil {
		return nil, err
	}
	s := &PasscodeStore{dir: dir}
	s.hash.Store(&hash)
	return s, nil
}

// currentHash returns the passcode hash currently in effect. Safe for
// concurrent use with Rotate: the pointer swap in Rotate is atomic, so a
// request in flight sees either the old or the new hash in full, never a
// torn value.
func (s *PasscodeStore) currentHash() string {
	if p := s.hash.Load(); p != nil {
		return *p
	}
	return ""
}

// Rotate generates a fresh passcode, persists its hash to disk, and swaps
// the store's in-memory hash so the very next request checked against it,
// from any client including ones already connected, is verified against the
// new value and fails if it still presents the old one. It returns the new
// plaintext passcode exactly once; printing it is the caller's job (a
// rotate command, not built in this change), matching Connect Mobile's own
// rotate semantics: dropping every connected client.
func (s *PasscodeStore) Rotate() (string, error) {
	plaintext, hash, err := newPasscodeHash()
	if err != nil {
		return "", err
	}
	if err := writePasscodeHash(s.dir, hash); err != nil {
		return "", err
	}
	s.hash.Store(&hash)
	return plaintext, nil
}

// GeneratePasscode creates a brand-new passcode and persists its hash under
// dir, for first-run provisioning where no store has been loaded yet (ao
// setup-vm --pair, batch 3): LoadPasscodeStore fails loudly against an empty
// directory by design, so provisioning calls this instead, before the
// gateway process that will later call LoadPasscodeStore ever starts. It
// returns the plaintext passcode exactly once; the caller prints it, this
// package does not.
func GeneratePasscode(dir string) (string, error) {
	plaintext, hash, err := newPasscodeHash()
	if err != nil {
		return "", err
	}
	if err := writePasscodeHash(dir, hash); err != nil {
		return "", err
	}
	return plaintext, nil
}

// newPasscodeHash generates a fresh plaintext passcode and its hash, without
// touching disk. It reuses mobilebridge.GeneratePassword rather than a
// second alphanumeric generator: the spec for both is identical (8
// characters, alphanumeric, drawn from a cryptographically secure source),
// and mobilebridge is already a dependency of this package for
// HashPassword/PasswordMatches.
func newPasscodeHash() (plaintext, hash string, err error) {
	plaintext, err = mobilebridge.GeneratePassword()
	if err != nil {
		return "", "", fmt.Errorf("generate passcode: %w", err)
	}
	return plaintext, mobilebridge.HashPassword(plaintext), nil
}

func passcodeHashPath(dir string) string {
	return filepath.Join(dir, passcodeFileName)
}

// readPasscodeHash reads and validates the persisted hash at dir, failing
// loudly, rather than falling back to any default, on either a missing file
// or one that cannot be a real mobilebridge.HashPassword output.
func readPasscodeHash(dir string) (string, error) {
	path := passcodeHashPath(dir)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf(
				"pair mode: no passcode found at %s: provision this box first (ao setup-vm --pair), which generates one and prints it once",
				path)
		}
		return "", fmt.Errorf("read passcode store %s: %w", path, err)
	}
	hash := strings.TrimSpace(string(b))
	if !isPasscodeHash(hash) {
		return "", fmt.Errorf(
			"pair mode: passcode store %s is corrupt (not a %d-character hex sha-256 hash): delete it and re-provision with ao setup-vm --pair to regenerate",
			path, passcodeHashHexLen)
	}
	return hash, nil
}

func isPasscodeHash(s string) bool {
	if len(s) != passcodeHashHexLen {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// writePasscodeHash persists hash to dir atomically (temp file + rename),
// mirroring mobilebridge.Save's own persistence shape for the same reason: a
// crash mid-write must never leave a torn, "corrupt" file behind, only ever
// the old hash or the new one.
func writePasscodeHash(dir, hash string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create passcode dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".passcode-*.tmp")
	if err != nil {
		return fmt.Errorf("create passcode temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod passcode temp file: %w", err)
	}
	if _, err := tmp.WriteString(hash); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write passcode temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close passcode temp file: %w", err)
	}
	if err := os.Rename(tmpName, passcodeHashPath(dir)); err != nil {
		return fmt.Errorf("persist passcode store: %w", err)
	}
	return nil
}

// passcodeLockoutLimit and passcodeLockoutCooldown mirror the numbers
// internal/httpd/lan_listener.go uses to construct the daemon's own LAN
// listener lockout (newLockout(5, time.Minute, time.Now)): the same wire
// shape (a bearer passcode) and the same threat model (a hostile device on
// the network guessing) get the same throttle. Vars rather than consts only
// so a test can shrink the cooldown instead of sleeping out a full minute,
// mirroring bareRequestLogWindow in proxy.go.
var (
	passcodeLockoutLimit    = 5
	passcodeLockoutCooldown = time.Minute
)

// passcodeLockout throttles pair-mode passcode guessing per source address.
// It is modelled on, but is a separate implementation from, the lockout in
// internal/httpd/auth.go: that file belongs to the daemon, which this
// task's hard constraints forbid modifying or reaching into the unexported
// helpers of, so the same per-source throttle shape is reimplemented here
// rather than shared.
type passcodeLockout struct {
	mu       sync.Mutex
	limit    int
	cooldown time.Duration
	now      func() time.Time
	fails    map[string]int
	until    map[string]time.Time
}

func newPasscodeLockout(limit int, cooldown time.Duration, now func() time.Time) *passcodeLockout {
	if now == nil {
		now = time.Now
	}
	return &passcodeLockout{
		limit: limit, cooldown: cooldown, now: now,
		fails: map[string]int{}, until: map[string]time.Time{},
	}
}

func (l *passcodeLockout) blocked(src string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.until[src]
	if !ok {
		return false
	}
	if l.now().Before(t) {
		return true
	}
	// Cooldown elapsed: clear it and the fail counter so this source starts a
	// fresh window, mirroring internal/httpd/auth.go's lockout.blocked (see
	// its comment for why: otherwise the very next failure would re-lock for
	// another full cooldown, and a source that keeps retrying would never
	// recover).
	delete(l.until, src)
	delete(l.fails, src)
	return false
}

func (l *passcodeLockout) fail(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[src]++
	if l.fails[src] >= l.limit {
		l.until[src] = l.now().Add(l.cooldown)
	}
}

func (l *passcodeLockout) reset(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, src)
	delete(l.until, src)
}

// passcodeSourceKey identifies the caller for lockout purposes: the
// request's remote IP, stripped of its port. Mirrors sourceKey in
// internal/httpd/auth.go for the same reason that function exists (a
// per-source, not global, lockout), reimplemented rather than imported
// because that function is unexported in a package this task must not
// modify or reach into.
func passcodeSourceKey(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// requirePasscode is pair mode's credential check, the branch NewHandler
// takes instead of requireToken when Config.Mode is ModePair. It rejects any
// request whose token, read by extractToken (the same helper requireToken
// uses, so /mux and the SSE routes' gateway-cookie transport keep working
// unchanged in pair mode too), does not match the store's current passcode
// hash, comparing constant-time via mobilebridge.PasswordMatches per
// docs/adr/0003-pair-mode-gateway.md. A per-source lockout throttles
// repeated failures without penalizing any other source, and a successful
// auth resets that source's failure count.
func requirePasscode(store *PasscodeStore, lock *passcodeLockout, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			src := passcodeSourceKey(r)
			if lock.blocked(src) {
				tooManyRequests(w, r)
				return
			}
			token, _ := extractToken(r)
			if !mobilebridge.PasswordMatches(store.currentHash(), token) {
				log.Warn("vm gateway: passcode rejected", "path", r.URL.Path, "source", src)
				lock.fail(src)
				unauthorized(w, r)
				return
			}
			lock.reset(src)
			next.ServeHTTP(w, r)
		})
	}
}

func tooManyRequests(w http.ResponseWriter, r *http.Request) {
	envelope.WriteAPIError(w, r, http.StatusTooManyRequests, "too_many_requests", "LOCKED_OUT",
		"too many failed attempts; try again shortly", nil)
}
