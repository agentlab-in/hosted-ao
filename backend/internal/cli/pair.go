package cli

// pair.go implements `ao pair`, the one-command path for a box the desktop
// app reaches by IP on a network the operator trusts, instead of by a public
// domain and an AO account (see docs/adr/0003-pair-mode-gateway.md and
// pairstring.go for the ao-pair:// wire format this command emits).
//
// Bare `ao pair`, with no subcommand, does one of two things depending on
// whether this box has already been provisioned for pair mode:
//
//   - not provisioned: delegates straight into `ao setup-vm --pair`'s own
//     provisioning path (same preflight, same install, same summary), so a
//     fresh box needs exactly one command either way.
//   - already provisioned: behaves exactly like `ao pair show`, since
//     provisioning never runs twice and the passcode is only ever known in
//     plaintext at the moment it was minted (only its hash is persisted).
//
// `ao pair show` never mutates anything: it reads whatever is already on
// disk and prints the address list and the certificate fingerprint, plus a
// pointer at `ao vm rotate-passcode` for a fresh, complete pairing string.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/pairstring"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

// pairPublicIPTimeout bounds the public-address probe ao pair show (and the
// pairing-string emission at mint/rotate time) uses to enrich the address
// list. Short and best effort: it only adds the WAN-facing address on top of
// whatever private addresses were already found, so a slow or unreachable
// prober must never hang a command whose whole point is reading what is
// already on disk.
const pairPublicIPTimeout = 3 * time.Second

type pairShowOptions struct {
	CertDir     string
	PasscodeDir string
}

func newPairCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Pair this box to the AO desktop app by IP and passcode, no domain or AO account required",
		Long: "ao pair is the one-command path for a box the desktop app reaches by IP on a\n" +
			"trusted network, instead of by a public domain and an AO account (see\n" +
			"docs/adr/0003-pair-mode-gateway.md). Run with no subcommand, it provisions this\n" +
			"box the first time, identical to ao setup-vm --pair, and shows this box's current\n" +
			"pairing details on every run after that, since provisioning never runs twice and\n" +
			"the passcode can only be printed again by rotating it.\n\n" +
			"ao pair show only ever reads what is already on disk and never provisions.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runPairBare(cmd)
		},
	}
	cmd.AddCommand(newPairShowCommand(ctx))
	return cmd
}

func newPairShowCommand(ctx *commandContext) *cobra.Command {
	var opts pairShowOptions
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print this box's pair-mode addresses, fingerprint, and how to get a fresh pairing string",
		Long: "ao pair show prints the addresses and certificate fingerprint of a box already\n" +
			"provisioned by ao setup-vm --pair (or bare ao pair). It never prints the\n" +
			"passcode: only its hash is persisted, so a full ao-pair:// pairing string can\n" +
			"only ever be produced again by rotating it with ao vm rotate-passcode.\n\n" +
			"This command only reads what is already on disk; it never provisions.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runPairShow(cmd, opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.CertDir, "cert-dir", "", "Pair-mode certificate directory (default under the state root)")
	flags.StringVar(&opts.PasscodeDir, "passcode-dir", "", "Pair-mode passcode hash directory (default under the state root)")
	return cmd
}

// runPairBare is bare `ao pair`'s dispatch: provision on a box that has
// never been paired, otherwise show what is already there. See the package
// doc for why this is not a usage error either way.
func (c *commandContext) runPairBare(cmd *cobra.Command) error {
	provisioned, err := c.pairIsProvisioned("")
	if err != nil {
		return err
	}
	if !provisioned {
		return c.runSetupVMPair(cmd, setupVMOptions{})
	}
	return c.runPairShow(cmd, pairShowOptions{})
}

func (c *commandContext) runPairShow(cmd *cobra.Command, opts pairShowOptions) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	passcodeDir, err := c.resolvePairPasscodeDir(opts.PasscodeDir)
	if err != nil {
		return err
	}
	if _, err := vmgateway.LoadPasscodeStore(passcodeDir); err != nil {
		return fmt.Errorf("%w (or run bare `ao pair`, which provisions this box automatically)", err)
	}

	certDir, err := c.resolvePairCertDir(opts.CertDir)
	if err != nil {
		return err
	}
	if !vmgateway.PairCertExists(certDir) {
		return fmt.Errorf(
			"pair mode: no certificate found at %s: provision this box first (ao setup-vm --pair, or bare `ao pair`)",
			certDir)
	}
	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		return err
	}
	fingerprint, err := vmgateway.PairFingerprint(cert)
	if err != nil {
		return err
	}

	addrs := pairListenAddresses(ctx, c.pairHTTPClient(), pairHTTPSAddr())
	return writeSetupText(out, renderPairShow(fingerprint, addrs))
}

// pairIsProvisioned reports whether this box already has a pair-mode
// passcode on disk, the same signal ensureSetupPasscode uses to decide
// between generating a first passcode and leaving an existing one alone.
// The passcode store is the canonical marker: ao setup-vm --pair always
// creates it and the certificate together, in that order, so a readable
// passcode store is proof the certificate exists too, without this command
// ever having to touch (and risk silently creating) the certificate itself
// just to answer a yes/no question.
func (c *commandContext) pairIsProvisioned(passcodeDirFlag string) (bool, error) {
	dir, err := c.resolvePairPasscodeDir(passcodeDirFlag)
	if err != nil {
		return false, err
	}
	_, err = vmgateway.LoadPasscodeStore(dir)
	return err == nil, nil
}

// resolvePairPasscodeDir and resolvePairCertDir mirror runVMRotatePasscode's
// own flag/environment/plan-default precedence (see vm.go), reused rather
// than duplicated so bare `ao pair`, `ao pair show`, and `ao vm
// rotate-passcode` all land on the exact same directory for the exact same
// box.
func (c *commandContext) resolvePairPasscodeDir(flag string) (string, error) {
	dir := strings.TrimSpace(flag)
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv("AO_VM_PASSCODE_DIR"))
	}
	if dir == "" {
		plan, err := c.buildSetupVMPlanPair()
		if err != nil {
			return "", err
		}
		dir = plan.PasscodeDir
	}
	return dir, nil
}

func (c *commandContext) resolvePairCertDir(flag string) (string, error) {
	dir := strings.TrimSpace(flag)
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv("AO_VM_CERT_DIR"))
	}
	if dir == "" {
		plan, err := c.buildSetupVMPlanPair()
		if err != nil {
			return "", err
		}
		dir = plan.PairCertDir
	}
	return dir, nil
}

// pairHTTPClient clones the CLI's shared HTTP client with a short timeout,
// mirroring setupHTTPClient's pattern in setupvm.go for the same reason: the
// public-IP probe talks to the internet, so the default loopback budget is
// too tight, but this probe is also only ever a nice-to-have enrichment of
// an address list, so it stays short rather than borrowing setup-vm's own
// (longer) provisioning-time budget.
func (c *commandContext) pairHTTPClient() *http.Client {
	client := *c.deps.HTTPClient
	client.Timeout = pairPublicIPTimeout
	return &client
}

// pairHTTPSAddr is the gateway's configured HTTPS listener address, the same
// precedence vmgateway.Resolve itself applies (see AO_VM_HTTPS_ADDR in
// backend/internal/vmgateway/config.go): an explicit override wins, and the
// package's own default otherwise.
func pairHTTPSAddr() string {
	if addr := strings.TrimSpace(os.Getenv("AO_VM_HTTPS_ADDR")); addr != "" {
		return addr
	}
	return vmgateway.DefaultHTTPSAddr
}

// pairInterfaceIPs lists this box's own non-loopback interface addresses,
// split into private-network ones (RFC 1918 IPv4, RFC 4193 IPv6 ULA) and
// public ones (a directly bound public IP, no NAT between it and the
// internet). A package var, overridden in tests, so address-ordering
// behavior does not depend on the network interfaces of whatever machine
// happens to run the test suite.
var pairInterfaceIPs = defaultPairInterfaceIPs

// defaultPairInterfaceIPs is pairInterfaceIPs' real implementation:
// net.Interfaces, filtered to global unicast, with loopback, link-local,
// and multicast addresses excluded. Best effort and never fatal: an
// interface this process cannot list its addresses for is simply skipped,
// since a partial list is still useful and a fatal error here would take
// down a command whose job is reading what is already on disk.
//
// Splitting private from public here, rather than filtering to private
// only, matters: a box whose only interface address is a directly bound
// public IP (no NAT) is still reachable at that address, and pairCandidateIPs
// below still has to offer it. The private/public split is what lets that
// caller prefer private addresses without ever discarding a public one.
func defaultPairInterfaceIPs() (private, public []string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.IsLoopback() || !ip.IsGlobalUnicast() {
				continue
			}
			if ip.IsPrivate() {
				private = append(private, ip.String())
			} else {
				public = append(public, ip.String())
			}
		}
	}
	sort.Strings(private)
	sort.Strings(public)
	return private, public
}

// pairPublicAddress asks the same public-IP endpoints ao setup-vm's own
// discoverPublicIP uses (setupVMPublicIPEndpoints, fetchSetupPublicIP; see
// setupvm.go) what this box's address looks like from the internet, trying
// each in turn and stopping at the first answer. Unlike discoverPublicIP
// this never returns an error: a pair-mode box on a trusted LAN is reachable
// without a public address at all, so a failed or unreachable probe is
// skipped in silence rather than reported.
func pairPublicAddress(ctx context.Context, client *http.Client) (string, bool) {
	for _, endpoint := range setupVMPublicIPEndpoints {
		ip, err := fetchSetupPublicIP(ctx, client, endpoint)
		if err == nil && ip != "" {
			return ip, true
		}
	}
	return "", false
}

// pairCandidateIPs is the single, ordered list of bare IPs this box could be
// reached at: this is the one enumeration every pair-mode command builds
// its output from, so the addresses a command displays and the addresses
// embedded in its pairing string can never diverge. Order is private
// interface addresses first (preferred: a LAN address is usually the more
// direct route), then any interface address that is itself public (a
// directly bound public IP still has to be offered, not discarded, when
// there is no private address at all), then the public-IP probe's answer if
// it found one not already listed (a NATed box's WAN-facing address, which
// no local interface carries). The ordering here is a preference, not a
// filter: nothing that could pair a client is ever left out of the list.
func pairCandidateIPs(ctx context.Context, client *http.Client) []string {
	private, public := pairInterfaceIPs()
	out := append(append([]string{}, private...), public...)
	if pub, ok := pairPublicAddress(ctx, client); ok && !slices.Contains(out, pub) {
		out = append(out, pub)
	}
	return out
}

// pairListenAddresses is pairCandidateIPs with httpsAddr's port appended to
// every address: the shape ao pair show prints and pairstring.Build needs.
// pairstring.Build takes addresses already ordered by the caller and never
// reorders them, so pairCandidateIPs' own ordering is what the desktop app
// tries first on connect.
func pairListenAddresses(ctx context.Context, client *http.Client, httpsAddr string) []string {
	return pairAddrsWithPort(pairCandidateIPs(ctx, client), httpsAddr)
}

// pairAddrsWithPort appends httpsAddr's port to every ip in ips, without
// re-enumerating or re-probing: callers that already hold an ips list
// (pairSummaryAddresses, buildPairingStringFromIPs) use this instead of
// pairListenAddresses so the public-IP probe only ever runs once per
// command invocation.
func pairAddrsWithPort(ips []string, httpsAddr string) []string {
	if len(ips) == 0 {
		return nil
	}
	port := strings.TrimPrefix(httpsAddr, ":")
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = net.JoinHostPort(ip, port)
	}
	return out
}

// pairLeafCertificate returns cert's parsed leaf, parsing it from the DER
// bytes when tls.X509KeyPair did not already populate Leaf (Go version
// dependent), since pairstring.Fingerprint needs an *x509.Certificate and
// vmgateway hands back a tls.Certificate.
func pairLeafCertificate(cert tls.Certificate) (*x509.Certificate, error) {
	if cert.Leaf != nil {
		return cert.Leaf, nil
	}
	if len(cert.Certificate) == 0 {
		return nil, errors.New("pair certificate has no DER bytes")
	}
	return x509.ParseCertificate(cert.Certificate[0])
}

// buildPairingStringFromIPs assembles the full ao-pair:// string from an
// already-enumerated ips list (see pairCandidateIPs), a freshly generated
// (or just-rotated) plaintext passcode, and cert. ok is false whenever
// there is nothing usable to build from (no address, or the certificate
// cannot be parsed); the caller falls back to its own "no address was
// found" guidance instead of failing the whole command over what is, at
// that point, just a missing enrichment.
func buildPairingStringFromIPs(ips []string, httpsAddr string, cert tls.Certificate, passcode string) (pairingString string, addrs []string, ok bool) {
	addrs = pairAddrsWithPort(ips, httpsAddr)
	if len(addrs) == 0 {
		return "", addrs, false
	}
	leaf, err := pairLeafCertificate(cert)
	if err != nil {
		return "", addrs, false
	}
	s, err := pairstring.Build(addrs, pairstring.Fingerprint(leaf), passcode)
	if err != nil {
		return "", addrs, false
	}
	return s, addrs, true
}

// buildPairingString is buildPairingStringFromIPs for a caller (ao vm
// rotate-passcode) that has no already-enumerated ips list of its own: it
// runs pairCandidateIPs itself, once, then builds from that.
func (c *commandContext) buildPairingString(ctx context.Context, cert tls.Certificate, passcode string) (pairingString string, addrs []string, ok bool) {
	ips := pairCandidateIPs(ctx, c.pairHTTPClient())
	return buildPairingStringFromIPs(ips, pairHTTPSAddr(), cert, passcode)
}

// pairSummaryAddresses is `ao setup-vm --pair`'s one enumeration point: the
// bare candidate IPs its "Addresses:" display is built from, and, when this
// run generated a fresh passcode, the pairing string built from those exact
// same IPs. Both come from a single pairCandidateIPs call (one public-IP
// probe), which is what makes it impossible for the display list and the
// string's own embedded addresses to show different addresses or counts
// for the same run.
func (c *commandContext) pairSummaryAddresses(ctx context.Context, cert tls.Certificate, passcode string, generated bool) (addrs []string, pairingString string) {
	ips := pairCandidateIPs(ctx, c.pairHTTPClient())
	if !generated {
		return ips, ""
	}
	pairingString, _, _ = buildPairingStringFromIPs(ips, pairHTTPSAddr(), cert, passcode)
	return ips, pairingString
}

// renderPairShow is the whole output of `ao pair show`: the address list,
// the certificate fingerprint (compare this, by eye, against what the
// desktop app shows on first connect), and a pointer at rotation for a
// fresh pairing string, since the passcode itself is never recoverable once
// past the moment it was minted (only its hash is stored).
func renderPairShow(fingerprint string, addrs []string) string {
	var b strings.Builder
	b.WriteString("Addresses:\n")
	if len(addrs) == 0 {
		b.WriteString("  (none found automatically; find this machine's LAN IP with `ip addr`)\n")
	} else {
		for _, addr := range addrs {
			fmt.Fprintf(&b, "  %s\n", addr)
		}
	}
	fmt.Fprintf(&b, "\nFingerprint:\n  %s\n", fingerprint)
	b.WriteString("\nPasscode: not shown, only its hash is stored; run 'ao vm rotate-passcode' for a\n")
	b.WriteString("fresh pairing string.\n")
	return b.String()
}
