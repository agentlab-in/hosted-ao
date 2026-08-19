// Package pairstring implements the ao-pair:// pairing-string codec, the
// single credential a box prints and the desktop app pastes to add a
// machine. The grammar (single source of truth, see
// docs/superpowers/specs/2026-08-19-seamless-machine-onboarding-design.md):
//
//	ao-pair://v1/<addr>[,<addr>...]#<fp>:<passcode>
//
//	addr     = host:port, host is IPv4, a bracketed IPv6 literal, or a DNS
//	           name; port is required and explicit, 1-65535
//	fp       = 64 lowercase hex chars, the SHA-256 of the gateway cert's DER
//	passcode = 8 chars, [A-Za-z0-9]
//
// A parser rejects: an unknown version segment, zero addresses, an address
// without an explicit port, a port outside 1-65535, a fingerprint that is
// not exactly 64 lowercase hex chars, a passcode that is not exactly 8
// alphanumeric chars, and any username, query string, or extra fragment.
//
// Build and Validate are tested against vectors.json, the golden-vector
// contract this package shares with the TypeScript parser (Task 3 of the
// phase-1 pairing-string plan). Do not fork or hand-edit that file's cases
// without updating both sides.
//
// Build takes addrs already ordered by the caller (private-before-public
// ordering is the emitter's job, upstream of this package) and does not
// reorder them.
package pairstring

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	scheme         = "ao-pair://"
	version        = "v1/"
	fingerprintLen = 64
	passcodeLen    = 8
)

// Build assembles a pairing string from already-validated, already-ordered
// inputs. It validates every input against the grammar and returns an error
// naming the first violation instead of building a malformed string.
func Build(addrs []string, fpHex, passcode string) (string, error) {
	if len(addrs) == 0 {
		return "", errors.New("pairstring: at least one address is required")
	}
	for _, addr := range addrs {
		if err := validateAddr(addr); err != nil {
			return "", fmt.Errorf("pairstring: invalid address %q: %w", addr, err)
		}
	}
	if err := validateFingerprint(fpHex); err != nil {
		return "", fmt.Errorf("pairstring: %w", err)
	}
	if err := validatePasscode(passcode); err != nil {
		return "", fmt.Errorf("pairstring: %w", err)
	}
	return scheme + version + strings.Join(addrs, ",") + "#" + fpHex + ":" + passcode, nil
}

// Validate parses a full pairing string against the grammar and returns the
// first violation found, or nil if s is well formed. It is exported for
// reuse by commands like `ao pair show --check` that need to sanity-check a
// string without also holding the fields Build would need.
func Validate(s string) error {
	rest, ok := strings.CutPrefix(s, scheme)
	if !ok {
		return fmt.Errorf("pairstring: missing %q scheme", scheme)
	}
	rest, ok = strings.CutPrefix(rest, version)
	if !ok {
		return errors.New("pairstring: unknown or missing version segment (want v1)")
	}

	if n := strings.Count(rest, "#"); n != 1 {
		return fmt.Errorf("pairstring: expected exactly one '#' fragment separator, found %d", n)
	}
	addrPart, tail, _ := strings.Cut(rest, "#")

	if addrPart == "" {
		return errors.New("pairstring: at least one address is required")
	}
	for _, addr := range strings.Split(addrPart, ",") {
		if err := validateAddr(addr); err != nil {
			return fmt.Errorf("pairstring: invalid address %q: %w", addr, err)
		}
	}

	if n := strings.Count(tail, ":"); n != 1 {
		return fmt.Errorf("pairstring: expected exactly one ':' between fingerprint and passcode, found %d", n)
	}
	fpHex, passcode, _ := strings.Cut(tail, ":")
	if err := validateFingerprint(fpHex); err != nil {
		return fmt.Errorf("pairstring: %w", err)
	}
	if err := validatePasscode(passcode); err != nil {
		return fmt.Errorf("pairstring: %w", err)
	}
	return nil
}

// Fingerprint returns the 64-char lowercase hex SHA-256 of cert.Raw, the
// canonical fingerprint format this package's fp field uses.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// validateAddr checks one host:port pair against the grammar: an explicit
// port in 1-65535, a non-empty host, and no username. net.SplitHostPort
// already understands bracketed IPv6 literals, so it does double duty as
// the bracket parser and the missing-port check.
func validateAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("address must include an explicit host:port: %w", err)
	}
	if host == "" {
		return errors.New("address is missing a host")
	}
	if strings.ContainsAny(host, "@#,") {
		return errors.New("address must not include a username, fragment, or extra address")
	}
	portNum, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return fmt.Errorf("port %q is not a number", port)
	}
	if portNum < 1 {
		return fmt.Errorf("port %d is outside 1-65535", portNum)
	}
	return nil
}

// validateFingerprint checks fp is exactly 64 lowercase hex chars.
func validateFingerprint(fp string) error {
	if len(fp) != fingerprintLen {
		return fmt.Errorf("fingerprint must be exactly %d hex characters, got %d", fingerprintLen, len(fp))
	}
	for _, c := range fp {
		if !isLowerHex(c) {
			return fmt.Errorf("fingerprint must be lowercase hex, got %q", c)
		}
	}
	return nil
}

// validatePasscode checks p is exactly 8 chars, each [A-Za-z0-9].
func validatePasscode(p string) error {
	if len(p) != passcodeLen {
		return fmt.Errorf("passcode must be exactly %d characters, got %d", passcodeLen, len(p))
	}
	for _, c := range p {
		if !isAlphanumeric(c) {
			return fmt.Errorf("passcode must be alphanumeric, got %q", c)
		}
	}
	return nil
}

func isLowerHex(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

func isAlphanumeric(c rune) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
