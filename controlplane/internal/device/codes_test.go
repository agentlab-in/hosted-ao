package device

import (
	"strings"
	"testing"
	"time"
)

func TestNewUserCode_ShapeAndAlphabet(t *testing.T) {
	seen := make(map[string]bool)
	for range 200 {
		code, err := newUserCode()
		if err != nil {
			t.Fatalf("newUserCode() unexpected error: %v", err)
		}
		if len(code) != userCodeLength {
			t.Fatalf("code %q has length %d, want %d", code, len(code), userCodeLength)
		}
		for _, r := range code {
			if !strings.ContainsRune(userCodeAlphabet, r) {
				t.Fatalf("code %q contains %q, which is outside the unambiguous alphabet", code, r)
			}
		}
		seen[code] = true
	}
	// A CSPRNG over 20^8 will not repeat in 200 draws. A stuck or seeded
	// generator would.
	if len(seen) != 200 {
		t.Errorf("200 draws produced %d distinct codes, want 200", len(seen))
	}
}

func TestUserCodeAlphabet_ExcludesVowelsDigitsAndDuplicates(t *testing.T) {
	// No digits: a human cannot then confuse 0 with O, 1 with I or L, 5 with
	// S, or 8 with B, because only one of each pair is ever printed. No
	// vowels: a generated code cannot spell a word.
	for _, r := range "AEIOU0123456789" {
		if strings.ContainsRune(userCodeAlphabet, r) {
			t.Errorf("alphabet contains %q, want no vowels and no digits", r)
		}
	}
	seen := make(map[rune]bool, len(userCodeAlphabet))
	for _, r := range userCodeAlphabet {
		if r < 'A' || r > 'Z' {
			t.Errorf("alphabet contains %q, want uppercase letters only", r)
		}
		if seen[r] {
			t.Errorf("alphabet repeats %q, which skews the draw toward it", r)
		}
		seen[r] = true
	}
}

func TestNormalizeUserCode(t *testing.T) {
	tests := []struct{ typed, want string }{
		{"WDJB-MJHT", "WDJBMJHT"},
		{"wdjb-mjht", "WDJBMJHT"},
		{"  WDJB MJHT \n", "WDJBMJHT"},
		{"WDJBMJHT", "WDJBMJHT"},
		{"", ""},
		{"----", ""},
		{"WDJB-MJH0", "WDJBMJH"}, // a typed zero is dropped, so the lookup misses
	}
	for _, tt := range tests {
		if got := normalizeUserCode(tt.typed); got != tt.want {
			t.Errorf("normalizeUserCode(%q) = %q, want %q", tt.typed, got, tt.want)
		}
	}
}

func TestFormatUserCode(t *testing.T) {
	if got := formatUserCode("WDJBMJHT"); got != "WDJB-MJHT" {
		t.Errorf("formatUserCode() = %q, want WDJB-MJHT", got)
	}
	if got := formatUserCode("SHORT"); got != "SHORT" {
		t.Errorf("formatUserCode() on a non-code = %q, want it returned unchanged", got)
	}
}

func TestNormalizePublicURL(t *testing.T) {
	ok := []struct{ raw, want string }{
		{"https://vm.example.com", "https://vm.example.com"},
		{"https://vm.example.com/", "https://vm.example.com"},
		{"vm.example.com", "https://vm.example.com"},
		{"https://VM.Example.COM", "https://vm.example.com"},
		{"  https://vm.example.com  ", "https://vm.example.com"},
		// http is the local-development affordance, so it is accepted for a
		// loopback host and nothing else.
		{"http://127.0.0.1:8443", "http://127.0.0.1:8443"},
		{"http://localhost:8443", "http://localhost:8443"},
		{"http://[::1]:8443", "http://[::1]:8443"},
	}
	for _, tt := range ok {
		got, err := normalizePublicURL(tt.raw)
		if err != nil {
			t.Errorf("normalizePublicURL(%q) unexpected error: %v", tt.raw, err)
			continue
		}
		if got != tt.want {
			t.Errorf("normalizePublicURL(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}

	// Anything `ao vm serve` cannot reduce to a bare hostname has to be
	// rejected here rather than stored and failed on at certificate time.
	bad := []string{
		"",
		"   ",
		"ftp://vm.example.com",
		"https://vm.example.com/path",
		"https://user:pw@vm.example.com",
		"https://vm.example.com?x=1",
		"https://vm.example.com#frag",
		"https://",
		// Plaintext for a routable host: the desktop would send the bearer
		// token and the Secure gateway cookie to it in the clear, and
		// `ao vm serve` only ever listens on TLS, so the registration would
		// succeed and produce a machine that cannot work.
		"http://vm.example.com",
		"http://vm.example.com:8443",
		"http://203.0.113.10",
		"http://localhost.evil.example.com",
		// A port on a routable host: `ao vm serve` drops it and listens on :443,
		// so storing it hands the desktop an address the gateway never answers.
		// See TestNormalizePublicURLRejectsPort.
		"https://vm.example.com:8443",
		"vm.example.com:8443",
	}
	for _, raw := range bad {
		if got, err := normalizePublicURL(raw); err == nil {
			t.Errorf("normalizePublicURL(%q) = %q, want an error", raw, got)
		}
	}
}

// A port in public_url is the one malformed value that used to be accepted and
// stored intact, because `ao vm serve` reduces the origin to a bare hostname
// (normalizeDomain in backend/internal/vmgateway/config.go) and then listens on
// :443. The machine registers, the desktop calls :8443, the gateway answers on
// :443, and the machine shows Offline with nothing in the gateway log. The two
// normalizers live in separate Go modules and cannot share code, so this test
// and TestNormalizeDomain_PortIsDroppedNotCarried on the gateway side are what
// keep them in agreement.
func TestNormalizePublicURLRejectsPort(t *testing.T) {
	for _, raw := range []string{"https://vm.example.com:8443", "https://vm.example.com:443", "vm.example.com:8443"} {
		got, err := normalizePublicURL(raw)
		if err == nil {
			t.Errorf("normalizePublicURL(%q) = %q, want an error naming --https-addr", raw, got)
			continue
		}
		// The operator's other option is a gateway told to listen there, so the
		// error has to name the flag that would do it.
		if !strings.Contains(err.Error(), "--https-addr") {
			t.Errorf("normalizePublicURL(%q) error = %q, want it to name --https-addr", raw, err)
		}
	}

	// The loopback development origins keep their port: http is only accepted
	// for loopback, and no gateway ever serves one, so there is no second side
	// to disagree with.
	for _, raw := range []string{"http://127.0.0.1:8443", "http://localhost:8443", "http://[::1]:8443"} {
		if got, err := normalizePublicURL(raw); err != nil || got != raw {
			t.Errorf("normalizePublicURL(%q) = %q, %v; want it kept unchanged", raw, got, err)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"localhost", "LocalHost", "127.0.0.1", "127.0.0.2", "::1"} {
		if !isLoopbackHost(host) {
			t.Errorf("isLoopbackHost(%q) = false, want true", host)
		}
	}
	// "localhost." and a name that merely ends in localhost are other people's
	// hosts, and 0.0.0.0 is not loopback.
	for _, host := range []string{"", "vm.example.com", "notlocalhost", "localhost.example.com", "0.0.0.0", "8.8.8.8"} {
		if isLoopbackHost(host) {
			t.Errorf("isLoopbackHost(%q) = true, want false", host)
		}
	}
}

func TestAttemptLimiter_BlocksAfterTheAllowanceAndRecovers(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	l := newAttemptLimiter()
	l.clock = func() time.Time { return now }

	for i := range attemptsPerWindow {
		if !l.allow("account-a") {
			t.Fatalf("attempt %d denied, want the first %d allowed", i+1, attemptsPerWindow)
		}
	}
	if l.allow("account-a") {
		t.Error("attempt past the allowance was allowed, want it denied")
	}

	// A different account is not affected by another account's attempts.
	if !l.allow("account-b") {
		t.Error("a second account was denied by the first account's attempts")
	}

	now = now.Add(attemptWindow + time.Second)
	if !l.allow("account-a") {
		t.Error("attempt after the window elapsed was denied, want it allowed")
	}
}
