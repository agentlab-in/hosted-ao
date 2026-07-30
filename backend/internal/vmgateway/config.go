// Package vmgateway implements `ao vm serve`: a public TLS reverse-proxy
// gateway, run as its own process in front of the loopback daemon, on a
// user-owned hosted VM. It obtains a Let's Encrypt certificate for the
// configured domain, verifies the AO access token on every request against a
// cached JWKS, and proxies authenticated requests to the daemon. See
// docs/adr/0002-hosted-public-gateway.md and controlplane/TOKEN_CONTRACT.md,
// which this package must stay in sync with.
package vmgateway

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultIssuer is the fixed `iss` claim the control plane signs, per
	// TOKEN_CONTRACT.md. It is overridable only so tests (and a future
	// non-production control plane) do not have to fake this exact host.
	DefaultIssuer = "https://ao.agentlab.in"
	// defaultJWKSPath is where the control plane publishes its signing keys,
	// per the spec's "Control plane" section.
	defaultJWKSPath = "/.well-known/jwks.json"
	// DefaultHTTPAddr is the ACME HTTP-01 challenge / https-redirect listener.
	DefaultHTTPAddr = ":80"
	// DefaultHTTPSAddr is the public TLS listener.
	DefaultHTTPSAddr = ":443"
	// DefaultDaemonAddr is used only when the loopback daemon's own run-file
	// cannot be read; it matches config.DefaultPort.
	DefaultDaemonAddr = "127.0.0.1:3001"
)

// Config is the fully-resolved gateway configuration. It is immutable once
// built by Resolve.
type Config struct {
	// Domain is the single hostname this gateway serves and the only one
	// autocert will ever request a certificate for. Always a bare hostname:
	// machine.json's publicUrl is a full origin and Resolve reduces it, see
	// normalizeDomain.
	Domain string
	// MachineID is this machine's id, checked against the token's `aud`.
	MachineID string
	// AccountID is the single account allowlisted for this machine, checked
	// against the token's `sub`.
	AccountID string
	// Issuer is the expected token `iss`.
	Issuer string
	// JWKSURL is where the control plane publishes its signing keys.
	JWKSURL string
	// DaemonAddr is the loopback daemon's host:port, the reverse-proxy target.
	DaemonAddr string
	// CertDir is where the ACME certificate cache is stored, under ~/.ao.
	CertDir string
	// HTTPAddr is the ACME HTTP-01 challenge / redirect listener address.
	HTTPAddr string
	// HTTPSAddr is the public TLS listener address.
	HTTPSAddr string
}

// Options carries the raw flag values from the `ao vm serve` command, before
// defaulting against environment variables or machine.json. An empty field
// means "not set on the command line".
type Options struct {
	Domain      string
	MachineID   string
	AccountID   string
	Issuer      string
	JWKSURL     string
	DaemonAddr  string
	MachineFile string
	CertDir     string
	HTTPAddr    string
	HTTPSAddr   string
}

// Resolve builds a Config from opts, environment variables, and (for
// Domain/MachineID/AccountID, when still unset) ~/.ao/machine.json.
// Precedence: explicit flag > environment variable > machine.json > built-in
// default. dataDir is the resolved AO data directory (config.Config.DataDir)
// used for the certificate cache when --cert-dir/AO_VM_CERT_DIR is not set.
//
// Recognised variables:
//
//	AO_VM_DOMAIN        public domain               (falls back to machine.json)
//	AO_VM_MACHINE_ID    this machine's id           (falls back to machine.json)
//	AO_VM_ACCOUNT_ID    the allowlisted account id  (falls back to machine.json)
//	AO_VM_ISSUER        expected token issuer       (default DefaultIssuer)
//	AO_VM_JWKS_URL      control-plane JWKS URL      (default <issuer>/.well-known/jwks.json)
//	AO_VM_DAEMON_ADDR   loopback daemon host:port   (default DefaultDaemonAddr)
//	AO_MACHINE_FILE     machine.json path           (default ~/.ao/machine.json)
//	AO_VM_CERT_DIR      ACME cert cache dir         (default <dataDir>/vm-gateway/certs)
//	AO_VM_HTTP_ADDR     ACME challenge listener     (default DefaultHTTPAddr)
//	AO_VM_HTTPS_ADDR    public TLS listener         (default DefaultHTTPSAddr)
func Resolve(opts Options, dataDir string) (Config, error) {
	machineFilePath := firstNonEmpty(opts.MachineFile, os.Getenv("AO_MACHINE_FILE"))
	if machineFilePath == "" {
		p, err := defaultMachineFilePath()
		if err != nil {
			return Config{}, err
		}
		machineFilePath = p
	}
	mf, err := ReadMachineFile(machineFilePath)
	if err != nil {
		return Config{}, fmt.Errorf("load %s: %w", machineFilePath, err)
	}

	cfg := Config{
		Issuer:     firstNonEmpty(opts.Issuer, os.Getenv("AO_VM_ISSUER"), DefaultIssuer),
		DaemonAddr: firstNonEmpty(opts.DaemonAddr, os.Getenv("AO_VM_DAEMON_ADDR"), DefaultDaemonAddr),
		HTTPAddr:   firstNonEmpty(opts.HTTPAddr, os.Getenv("AO_VM_HTTP_ADDR"), DefaultHTTPAddr),
		HTTPSAddr:  firstNonEmpty(opts.HTTPSAddr, os.Getenv("AO_VM_HTTPS_ADDR"), DefaultHTTPSAddr),
	}

	cfg.Domain = firstNonEmpty(opts.Domain, os.Getenv("AO_VM_DOMAIN"))
	cfg.MachineID = firstNonEmpty(opts.MachineID, os.Getenv("AO_VM_MACHINE_ID"))
	cfg.AccountID = firstNonEmpty(opts.AccountID, os.Getenv("AO_VM_ACCOUNT_ID"))
	// domainSource names where Domain came from so a rejected value points at
	// the file or the flag the operator actually has to fix.
	domainSource := "--domain or AO_VM_DOMAIN"
	if cfg.Domain == "" {
		domainSource = fmt.Sprintf("publicUrl in %s", machineFilePath)
	}
	if mf != nil {
		cfg.Domain = firstNonEmpty(cfg.Domain, mf.PublicURL)
		cfg.MachineID = firstNonEmpty(cfg.MachineID, mf.MachineID)
		cfg.AccountID = firstNonEmpty(cfg.AccountID, mf.AccountID)
	}

	cfg.JWKSURL = firstNonEmpty(opts.JWKSURL, os.Getenv("AO_VM_JWKS_URL"), cfg.Issuer+defaultJWKSPath)

	cfg.CertDir = firstNonEmpty(opts.CertDir, os.Getenv("AO_VM_CERT_DIR"))
	if cfg.CertDir == "" {
		if strings.TrimSpace(dataDir) == "" {
			return Config{}, errors.New("resolve cert dir: no data directory available")
		}
		cfg.CertDir = filepath.Join(dataDir, "vm-gateway", "certs")
	}

	var missing []string
	if cfg.Domain == "" {
		missing = append(missing, "domain (--domain, AO_VM_DOMAIN, or machine.json)")
	}
	if cfg.MachineID == "" {
		missing = append(missing, "machine id (--machine-id, AO_VM_MACHINE_ID, or machine.json)")
	}
	if cfg.AccountID == "" {
		missing = append(missing, "account id (--account-id, AO_VM_ACCOUNT_ID, or machine.json)")
	}
	if len(missing) > 0 {
		hint := fmt.Sprintf("%s not found and no override given", machineFilePath)
		if mf != nil {
			hint = "machine.json found but incomplete"
		}
		return Config{}, fmt.Errorf("ao vm serve: missing required configuration: %s (%s)", strings.Join(missing, ", "), hint)
	}

	domain, err := normalizeDomain(cfg.Domain, domainSource)
	if err != nil {
		return Config{}, err
	}
	cfg.Domain = domain

	if _, _, err := net.SplitHostPort(cfg.DaemonAddr); err != nil {
		return Config{}, fmt.Errorf("invalid daemon address %q: %w", cfg.DaemonAddr, err)
	}

	return cfg, nil
}

// normalizeDomain reduces a configured domain to the bare hostname
// autocert.HostWhitelist requires. machine.json's publicUrl is a full origin
// ("https://vm.example.com"), which the desktop and the control plane both
// want, but HostWhitelist runs its argument through idna.Lookup.ToASCII and,
// per its own documentation, silently ignores anything that fails. A scheme
// or a slash therefore leaves the whitelist empty, so no certificate is ever
// issued and every TLS handshake fails while the gateway logs a clean
// start. Reject here, loudly, at boot, whatever cannot be reduced to a
// hostname.
func normalizeDomain(domain, source string) (string, error) {
	domain = strings.TrimSpace(domain)
	if strings.Contains(domain, "://") {
		u, err := url.Parse(domain)
		if err != nil || u.Hostname() == "" {
			return "", fmt.Errorf("invalid public url %q (from %s): expected an origin like https://vm.example.com", domain, source)
		}
		domain = u.Hostname()
	}
	if domain == "" || strings.ContainsAny(domain, ":/") {
		return "", fmt.Errorf("invalid domain %q (from %s): expected a bare hostname like vm.example.com", domain, source)
	}
	return domain, nil
}

func defaultMachineFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve machine file path: %w", err)
	}
	return filepath.Join(home, ".ao", "machine.json"), nil
}

// firstNonEmpty returns the first non-empty (after trimming) candidate, or ""
// if all are empty.
func firstNonEmpty(candidates ...string) string {
	for _, c := range candidates {
		if strings.TrimSpace(c) != "" {
			return c
		}
	}
	return ""
}
