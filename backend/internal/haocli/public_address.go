package haocli

// Public-address detection for pair-mode pairing strings.
//
// machineAddresses enumerates loopback and interface addresses (LAN-private
// first, then any public address bound to a local interface). The functions
// here discover the public addresses no local interface carries: a NATed
// cloud VM's external address. On such a box the only interface address is
// private (e.g. GCP's 10.x.y.z), so a pairing string built from interface
// addresses alone cannot be pasted from a Mac outside that network. These
// probes make the string advertise an address a remote desktop can actually
// reach, without adding a cloud SDK: the GCP metadata server answers one
// link-local HTTP GET, and the plain-text public-IP echo endpoints answer
// another.
//
// Both probes are best-effort enrichments. A box with no public address, or
// one whose probers cannot be reached, simply contributes nothing, which is
// still a correct pairing string for a LAN-only box. The addresses are hints,
// never identity (docs/adr/0004-pairing-string-and-cloud-pair-scope.md), so a
// failed probe must never hang or fail a command whose whole point is printing
// what is already on disk.

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// machinePublicIPTimeout bounds each public-address probe. Short on purpose:
// the probes only add the WAN-facing address on top of whatever interface
// addresses were already found, and a slow or unreachable prober must never
// hang provisioning.
const machinePublicIPTimeout = 3 * time.Second

// gcpMetadataExternalIPURL is the GCP Compute Engine metadata endpoint that
// reports this VM's public (external) IPv4 address. It is reachable only from
// inside a GCE VM and requires the Metadata-Flavor header; no cloud SDK is
// needed. A var (not a const) so tests can point it at a local httptest
// server instead of requiring a live GCP box.
var gcpMetadataExternalIPURL = "http://metadata.google.internal/computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip"

// publicIPEchoEndpoints are the plain-text public-IP echo endpoints, tried in
// order. They report the address this box's outbound traffic egresses from,
// which for a NATed cloud VM is its external address. A var for the same
// test-injection reason as gcpMetadataExternalIPURL.
var publicIPEchoEndpoints = []string{"https://api.ipify.org", "https://ifconfig.me/ip"}

// machinePublicIPs is the address-selection seam: it returns the bare public
// IP addresses this box is reachable at from outside its own network, best
// effort, in the order they should be advertised. A package var so tests can
// inject a fixed result and never depend on the machine running the test
// suite. Ordering (loopback, then private, then public) is machineAddresses'
// job, not this function's.
var machinePublicIPs = defaultMachinePublicIPs

// defaultMachinePublicIPs is machinePublicIPs' real implementation: the GCP
// metadata external IP when this is a GCE VM, otherwise the first public-IP
// echo endpoint that answers. Returning at most one answer keeps provisioning
// to a single extra probe in the common case; the pair string only needs one
// reachable address to work.
func defaultMachinePublicIPs() []string {
	if ip := fetchExternalIP(gcpMetadataExternalIPURL, true); ip != "" {
		return []string{ip}
	}
	for _, endpoint := range publicIPEchoEndpoints {
		if ip := fetchExternalIP(endpoint, false); ip != "" {
			return []string{ip}
		}
	}
	return nil
}

// fetchExternalIP GETs endpoint, expecting a bare IP address in the body, and
// returns it when it is a usable public address. gcpFlavor adds the
// Metadata-Flavor header the GCP metadata server requires. Any failure (bad
// status, unparsable body, a private/loopback answer) returns "" rather than
// an error: the caller treats this as "no public address found", never as a
// provisioning failure.
func fetchExternalIP(endpoint string, gcpFlavor bool) string {
	ctx, cancel := context.WithTimeout(context.Background(), machinePublicIPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return ""
	}
	if gcpFlavor {
		req.Header.Set("Metadata-Flavor", "Google")
	}
	client := &http.Client{Timeout: machinePublicIPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	return validPublicIP(strings.TrimSpace(string(body)))
}

// validPublicIP returns ip's canonical form when it parses as a global-unicast
// address that is not private, loopback, or link-local, else "". A public-IP
// probe that answered with a private address (a far-side NAT, a captive portal)
// is not this box's reachable address and is discarded.
func validPublicIP(ip string) string {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil || !parsed.IsGlobalUnicast() || parsed.IsPrivate() || parsed.IsLoopback() || parsed.IsLinkLocalUnicast() {
		return ""
	}
	return parsed.String()
}
