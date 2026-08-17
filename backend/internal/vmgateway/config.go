// Package vmgateway implements `ao vm serve`: a public TLS reverse-proxy
// gateway, run as its own process in front of the loopback daemon. It has two
// mutually exclusive configurations, selected by Config.Mode. ModeHosted runs
// on a user-owned hosted VM: it obtains a Let's Encrypt certificate for the
// configured domain, verifies the AO access token on every request against a
// cached JWKS, and proxies authenticated requests to the daemon. See
// docs/adr/0002-hosted-public-gateway.md and TOKEN_CONTRACT.md in agentlab-in/ao-controlplane,
// which this package must stay in sync with. ModePair runs on a box the user
// owns on a network they trust, reached by bare IP: it presents a long-lived
// self-signed certificate instead of one from a CA, so there is no domain and
// no control plane involved. See docs/adr/0003-pair-mode-gateway.md. Pair
// mode's credential check (a passcode, replacing the JWT) is a separate,
// later change; this package only carries the certificate and configuration
// halves of pair mode so far.
package vmgateway

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
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

// Mode selects which of the two mutually exclusive gateway configurations
// Resolve built. NewServer and Server.Run branch on it to decide whether to
// run autocert and bind the ACME challenge listener at all.
type Mode string

const (
	// ModeHosted is the default: ACME via autocert.Manager scoped to Domain,
	// and (outside this package) machine-audience JWT verification against
	// the control plane's JWKS. See docs/adr/0002-hosted-public-gateway.md.
	ModeHosted Mode = "hosted"
	// ModePair is a box on a network the user trusts, reached by bare IP: a
	// long-lived self-signed certificate persisted under the state root
	// (LoadOrCreatePairCertificate) instead of one from a CA, and no contact
	// with the control plane at all. Passcode auth, replacing the JWT check,
	// is added in a later change. See docs/adr/0003-pair-mode-gateway.md.
	ModePair Mode = "pair"
)

// Config is the fully-resolved gateway configuration. It is immutable once
// built by Resolve.
type Config struct {
	// Mode selects hosted vs pair configuration; see the Mode type doc.
	// Fields documented as hosted-only are always zero-valued when Mode is
	// ModePair, and Resolve rejects any attempt to set them alongside pair
	// mode rather than silently ignoring them.
	Mode Mode
	// Domain is the single hostname this gateway serves and the only one
	// autocert will ever request a certificate for. Always a bare hostname:
	// machine.json's publicUrl is a full origin and Resolve reduces it, see
	// normalizeDomain. Hosted-only: always empty in pair mode.
	Domain string
	// MachineID is this machine's id. In hosted mode it is checked against
	// the token's `aud`. Set in both modes when available; pair mode does not
	// require it.
	MachineID string
	// AccountID is the single account allowlisted for this machine, checked
	// against the token's `sub`. Hosted-only: always empty in pair mode,
	// which has no account at all.
	AccountID string
	// Issuer is the expected token `iss`. Hosted-only: always empty in pair
	// mode, which never verifies a JWT.
	Issuer string
	// JWKSURL is where the control plane publishes its signing keys.
	// Hosted-only: always empty in pair mode, which never contacts a control
	// plane.
	JWKSURL string
	// DaemonAddr is the loopback daemon's host:port, the reverse-proxy target.
	// Used by both modes.
	DaemonAddr string
	// CertDir is where certificate material is stored: the ACME cache
	// directory in hosted mode (autocert.DirCache), or the persisted
	// self-signed certificate and key in pair mode
	// (LoadOrCreatePairCertificate). The default location differs by mode;
	// see Resolve.
	CertDir string
	// HTTPAddr is the ACME HTTP-01 challenge / redirect listener address.
	// Hosted-only: always empty in pair mode, which binds only HTTPSAddr —
	// there is no ACME challenge to answer.
	HTTPAddr string
	// HTTPSAddr is the public TLS listener address. Used by both modes.
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
	// Pair requests ModePair instead of ModeHosted. See AO_VM_PAIR.
	Pair bool
}

// Resolve builds a Config from opts, environment variables, and (for
// Domain/MachineID/AccountID, when still unset) ~/.ao/hosted/machine.json.
// Precedence: explicit flag > environment variable > machine.json > built-in
// default. dataDir is the resolved AO data directory (config.Config.DataDir)
// used for the hosted-mode ACME certificate cache when --cert-dir/AO_VM_CERT_DIR
// is not set; pair mode ignores it (see the AO_VM_CERT_DIR entry below).
//
// opts.Pair (or AO_VM_PAIR) selects ModePair instead of ModeHosted. The two
// modes are mutually exclusive: Resolve rejects, at this call rather than at
// first request, any hosted-only field (domain, account id, issuer, JWKS URL,
// the ACME listener address) explicitly configured alongside pair mode.
//
// Recognised variables:
//
//	AO_VM_PAIR          request pair mode instead of hosted (off|on, default off)
//	AO_VM_DOMAIN        public domain               (falls back to machine.json; hosted only)
//	AO_VM_MACHINE_ID    this machine's id           (falls back to machine.json)
//	AO_VM_ACCOUNT_ID    the allowlisted account id  (falls back to machine.json; hosted only)
//	AO_VM_ISSUER        expected token issuer       (default DefaultIssuer; hosted only)
//	AO_VM_JWKS_URL      control-plane JWKS URL      (default <issuer>/.well-known/jwks.json; hosted only)
//	AO_VM_DAEMON_ADDR   loopback daemon host:port   (default DefaultDaemonAddr)
//	AO_MACHINE_FILE     machine.json path           (default ~/.ao/hosted/machine.json)
//	AO_VM_CERT_DIR      certificate storage dir     (default <dataDir>/vm-gateway/certs in
//	                                                 hosted mode, <state root>/vm-gateway/pair-cert
//	                                                 in pair mode)
//	AO_VM_HTTP_ADDR     ACME challenge listener     (default DefaultHTTPAddr; hosted only)
//	AO_VM_HTTPS_ADDR    public TLS listener         (default DefaultHTTPSAddr)
func Resolve(opts Options, dataDir string) (Config, error) {
	pairMode, err := resolvePairMode(opts)
	if err != nil {
		return Config{}, err
	}

	machineFilePath := firstNonEmpty(opts.MachineFile, os.Getenv("AO_MACHINE_FILE"))
	if machineFilePath == "" {
		p, err := DefaultMachineFilePath()
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
		Mode:       ModeHosted,
		DaemonAddr: firstNonEmpty(opts.DaemonAddr, os.Getenv("AO_VM_DAEMON_ADDR"), DefaultDaemonAddr),
		HTTPSAddr:  firstNonEmpty(opts.HTTPSAddr, os.Getenv("AO_VM_HTTPS_ADDR"), DefaultHTTPSAddr),
	}
	cfg.MachineID = firstNonEmpty(opts.MachineID, os.Getenv("AO_VM_MACHINE_ID"))
	if mf != nil {
		cfg.MachineID = firstNonEmpty(cfg.MachineID, mf.MachineID)
	}
	if err := validateHostPort("daemon address", cfg.DaemonAddr); err != nil {
		return Config{}, err
	}
	if err := validateHostPort("https listener address", cfg.HTTPSAddr); err != nil {
		return Config{}, err
	}

	if pairMode {
		return resolvePair(cfg, opts)
	}

	cfg.Issuer = firstNonEmpty(opts.Issuer, os.Getenv("AO_VM_ISSUER"), DefaultIssuer)
	cfg.HTTPAddr = firstNonEmpty(opts.HTTPAddr, os.Getenv("AO_VM_HTTP_ADDR"), DefaultHTTPAddr)
	cfg.Domain = firstNonEmpty(opts.Domain, os.Getenv("AO_VM_DOMAIN"))
	cfg.AccountID = firstNonEmpty(opts.AccountID, os.Getenv("AO_VM_ACCOUNT_ID"))
	// domainSource names where Domain came from so a rejected value points at
	// the file or the flag the operator actually has to fix.
	domainSource := "--domain or AO_VM_DOMAIN"
	if cfg.Domain == "" {
		domainSource = fmt.Sprintf("publicUrl in %s", machineFilePath)
	}
	if mf != nil {
		cfg.Domain = firstNonEmpty(cfg.Domain, mf.PublicURL)
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

	// HTTPAddr gets the same check DaemonAddr/HTTPSAddr already had above (for
	// the same reason: net.Listen on a colon-less value like "80", the
	// AO_VM_HTTP_ADDR=80 mistake, passes straight through Resolve and only
	// fails later, deep inside http.Server.ListenAndServe, with a raw
	// "missing port in address" net error and no indication which flag or
	// variable caused it). It only applies here because pair mode never sets
	// HTTPAddr at all.
	if err := validateHostPort("http listener address", cfg.HTTPAddr); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// resolvePairMode reports whether pair mode was requested, by flag or by
// AO_VM_PAIR. Unlike the other AO_VM_* variables this one is a strict on/off
// toggle (mirroring config.Load's AO_TELEMETRY_* toggles): an unrecognised
// value fails loudly rather than silently being treated as "not pair mode",
// because guessing wrong here means the gateway silently starts in the wrong
// mode with the wrong trust model.
func resolvePairMode(opts Options) (bool, error) {
	if opts.Pair {
		return true, nil
	}
	raw, ok := os.LookupEnv("AO_VM_PAIR")
	if !ok || strings.TrimSpace(raw) == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "on", "yes":
		return true, nil
	case "0", "false", "off", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid AO_VM_PAIR %q: must be off|on", raw)
	}
}

// resolvePair finishes building a ModePair Config from the mode-agnostic
// fields Resolve has already filled in on cfg. Pair mode has no domain, no
// control-plane URL, and no JWKS (see docs/adr/0003-pair-mode-gateway.md), so
// any hosted-only field explicitly configured here is rejected loudly rather
// than silently ignored: it means either the wrong flags were copied onto
// this box, or --pair itself was a mistake, and either way the operator needs
// to see it now, not discover it as a confusing runtime failure later.
func resolvePair(cfg Config, opts Options) (Config, error) {
	cfg.Mode = ModePair

	var mixed []string
	if firstNonEmpty(opts.Domain, os.Getenv("AO_VM_DOMAIN")) != "" {
		mixed = append(mixed, "--domain/AO_VM_DOMAIN")
	}
	if firstNonEmpty(opts.AccountID, os.Getenv("AO_VM_ACCOUNT_ID")) != "" {
		mixed = append(mixed, "--account-id/AO_VM_ACCOUNT_ID")
	}
	if firstNonEmpty(opts.Issuer, os.Getenv("AO_VM_ISSUER")) != "" {
		mixed = append(mixed, "--issuer/AO_VM_ISSUER")
	}
	if firstNonEmpty(opts.JWKSURL, os.Getenv("AO_VM_JWKS_URL")) != "" {
		mixed = append(mixed, "--jwks-url/AO_VM_JWKS_URL")
	}
	if firstNonEmpty(opts.HTTPAddr, os.Getenv("AO_VM_HTTP_ADDR")) != "" {
		mixed = append(mixed, "--http-addr/AO_VM_HTTP_ADDR")
	}
	if len(mixed) > 0 {
		return Config{}, fmt.Errorf(
			"ao vm serve --pair: %s not allowed in pair mode (pair mode has no domain, no control-plane URL, no JWKS, and no ACME challenge listener; hosted mode and pair mode are mutually exclusive)",
			strings.Join(mixed, ", "))
	}

	cfg.CertDir = firstNonEmpty(opts.CertDir, os.Getenv("AO_VM_CERT_DIR"))
	if cfg.CertDir == "" {
		stateDir, err := config.DefaultStateDir()
		if err != nil {
			return Config{}, fmt.Errorf("resolve pair cert dir: %w", err)
		}
		// Unlike the hosted ACME cache (under dataDir, see Resolve above), the
		// pair-mode certificate is identity, not disposable cache: losing it
		// forces every paired client to re-pair (see
		// docs/adr/0003-pair-mode-gateway.md), so it must survive an
		// AO_DATA_DIR change the same way machine.json does. It therefore
		// defaults under the state root rather than the data dir, mirroring
		// the asymmetry DefaultMachineFilePath's comment explains for the same
		// reason.
		cfg.CertDir = filepath.Join(stateDir, "vm-gateway", "pair-cert")
	}

	return cfg, nil
}

// validateHostPort rejects addr unless it splits into a host and a port, the
// shape every net.Listen/http.Server.Addr field here requires. label names
// the field in the error so it points at what the operator has to fix.
func validateHostPort(label, addr string) error {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("invalid %s %q: %w", label, addr, err)
	}
	return nil
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
//
// A port in the origin is dropped, not carried into HTTPSAddr: the listener
// moves only with --https-addr/AO_VM_HTTPS_ADDR. That stays safe because the
// other side of the publicUrl contract refuses to store an origin with a port
// (normalizePublicURL in controlplane/internal/device/codes.go), so the desktop
// can only ever be pointed at the port this gateway actually listens on. The
// two normalizers are in separate Go modules, so the agreement is pinned by a
// test on each side rather than shared code; see
// TestNormalizeDomain_PortIsDroppedNotCarried.
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

// DefaultMachineFilePath is where machine.json lives when AO_MACHINE_FILE and
// --machine-file are both unset: ~/.ao/hosted/machine.json, the AO state root,
// NOT the AO_DATA_DIR-resolved data dir that CertDir above uses.
//
// That asymmetry is deliberate, and it is the answer to a review finding that
// read it as a bug. AO_DATA_DIR moves durable data (the SQLite database, the
// ACME cert cache). machine.json is this machine's binding identity and sits
// beside running.json, which has the same shape: pinned to the state root and
// moved only by its own override (AO_RUN_FILE there, AO_MACHINE_FILE here).
// Deriving it from the data dir instead would move the file the gateway reads
// without moving the file `ao setup-vm` writes (setupPlan.MachineFile is
// <state root>/machine.json regardless of AO_DATA_DIR), so an operator who set
// AO_DATA_DIR would get a gateway looking in a place nothing ever writes.
//
// It is exported so `ao whoami` resolves the same path from the same line of
// code: the two must name the same file, or whoami confidently reports a
// binding the gateway never sees.
func DefaultMachineFilePath() (string, error) {
	stateDir, err := config.DefaultStateDir()
	if err != nil {
		return "", fmt.Errorf("resolve machine file path: %w", err)
	}
	return filepath.Join(stateDir, "machine.json"), nil
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
