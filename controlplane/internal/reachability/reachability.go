// Package reachability serves the off-box port check `ao setup-vm` runs
// during preflight: a cloud firewall is invisible from inside the VM, so
// confirming that 80 and 443 accept connections needs a host that is not that
// VM, and this control plane is the one such host setup-vm already depends on.
//
// # This endpoint is an SSRF primitive
//
// It is a public service that makes an outbound TCP connection to a
// caller-supplied target. Written naively it is a port scanner and an
// internal-network probe running from inside our own cloud account, with
// 169.254.169.254 (the instance metadata endpoint) as the first thing anyone
// points it at. The controls, all of which are load bearing:
//
//   - The hostname is resolved first and every resolved address is checked
//     against denyReason; the connection then goes to the address that was
//     checked, not to the name, so a second DNS answer cannot redirect it.
//   - Only ports 80 and 443, the two the CLI asks about. There is no port
//     parameter that reaches the dialer.
//   - Connect and close. No TLS handshake, no read, no redirect to follow, and
//     nothing about what answered is reported back beyond the boolean.
//   - A short per-connect timeout, and three rate limits: per client, per
//     target, and one global cap, because this endpoint turns one small
//     request into outbound connections.
//
// The endpoint is unauthenticated. `ao setup-vm` calls it during preflight,
// before the device-code binding that gives the VM any credential at all, so
// there is nothing it could present. The rate limits are therefore the whole
// budget, and they are set low.
package reachability

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/api"
)

const (
	// connectTimeout bounds one TCP connect. A filtered port is the slow case
	// and it fails by timing out, so this plus dnsTimeout is what the caller
	// waits for: two ports plus resolution stays inside the 10s budget
	// setup-vm gives the whole request.
	connectTimeout = 2 * time.Second
	dnsTimeout     = 3 * time.Second

	// rateWindow and the three allowances below meter this endpoint. One
	// `ao setup-vm` run calls it once, so a handful per minute is generous for
	// the real caller and small for anyone using it as a scanner. globalPerWindow
	// is the ceiling on outbound connections regardless of who asks: the per
	// client limit alone cannot bound it, because the client key is only as
	// good as the address the request arrives with.
	rateWindow         = time.Minute
	clientPerWindow    = 6
	targetPerWindow    = 6
	globalPerWindow    = 60
	globalRateLimitKey = "*"

	// maxHostLength is the longest DNS name that can exist, per RFC 1035.
	maxHostLength = 253
)

// probePorts are the only ports this service will connect to, in the order
// they are reported. The CLI asks about exactly these two.
var probePorts = []uint16{80, 443}

// Service owns the reachability endpoint. resolve and dial are fields so tests
// can drive the refusal paths without DNS or a network.
type Service struct {
	resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	dial    func(ctx context.Context, addr string) (net.Conn, error)

	clients *window
	targets *window
	global  *window
}

// NewService builds the reachability service with the real resolver and
// dialer.
func NewService() *Service {
	dialer := &net.Dialer{Timeout: connectTimeout}
	return &Service{
		resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", addr)
		},
		clients: newWindow(clientPerWindow, rateWindow),
		targets: newWindow(targetPerWindow, rateWindow),
		global:  newWindow(globalPerWindow, rateWindow),
	}
}

// Register wires the reachability endpoint onto mux. This is the one call the
// rest of the control plane needs to know about.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/reachability", s.handleReachability)
}

// response is the body `ao setup-vm` parses: a map of port to whether a TCP
// connect from here succeeded. The contract is set by the CLI, which reads
// {"ports":{"80":true,"443":false}} and treats a port it did not get back as
// unknown rather than closed. Nothing else is reported: what answered on the
// port is the caller's business and none of ours to relay.
type response struct {
	Ports map[string]bool `json:"ports"`
}

func (s *Service) handleReachability(w http.ResponseWriter, r *http.Request) {
	host, ok := normalizeHost(r.URL.Query().Get("host"))
	if !ok {
		api.WriteError(w, http.StatusBadRequest, "invalid_request", "host must be a hostname or IP address")
		return
	}
	ports, err := parsePorts(r.URL.Query().Get("ports"))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	// Metered before the resolver runs: DNS is outbound work too, and the
	// global window is what bounds this endpoint as an amplifier.
	if !s.global.allow(globalRateLimitKey) || !s.clients.allow(clientKey(r)) || !s.targets.allow(host) {
		api.WriteError(w, http.StatusTooManyRequests, "slow_down", "too many reachability checks, try again in a minute")
		return
	}

	resolveCtx, cancel := context.WithTimeout(r.Context(), dnsTimeout)
	defer cancel()
	addrs, err := s.resolve(resolveCtx, host)
	if err != nil || len(addrs) == 0 {
		api.WriteError(w, http.StatusBadRequest, "invalid_request", "host could not be resolved")
		return
	}

	target, refusal := selectTarget(addrs)
	if refusal != "" {
		// Worth a line: a caller pointing this at internal space is the abuse
		// case the endpoint exists inside of. The host is logged, the client is
		// not, since behind the proxy it is a header.
		log.Printf("reachability: refused %q, it %s", host, refusal)
		api.WriteError(w, http.StatusBadRequest, "invalid_request", "host "+refusal+", which this check will not connect to")
		return
	}

	open := make(map[string]bool, len(ports))
	for _, port := range ports {
		open[strconv.Itoa(int(port))] = s.connects(r.Context(), target, port)
	}
	api.WriteJSON(w, http.StatusOK, response{Ports: open})
}

// connects reports whether a TCP connection to target completes. It dials the
// validated address, never the hostname, so the destination cannot differ from
// the one selectTarget approved. The connection is closed immediately: nothing
// is written, nothing is read, and no TLS handshake is attempted, so a port
// that answers gives up no banner and this never becomes a content fetcher.
func (s *Service) connects(ctx context.Context, target netip.Addr, port uint16) bool {
	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	conn, err := s.dial(dialCtx, netip.AddrPortFrom(target, port).String())
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// normalizeHost validates the caller's target and returns it in the canonical
// form used both for resolution and as the per-target rate limit key.
//
// This is input validation only. It is not a security boundary: whether the
// target is allowed is decided on the resolved address, in denyReason.
func normalizeHost(raw string) (string, bool) {
	host := strings.TrimSuffix(strings.TrimSpace(raw), ".")
	if host == "" || len(host) > maxHostLength {
		return "", false
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		// A zone ("fe80::1%eth0") only names a local interface, so a target
		// carrying one is never a public VM.
		if addr.Zone() != "" {
			return "", false
		}
		return addr.Unmap().String(), true
	}

	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
			default:
				return "", false
			}
		}
	}
	return strings.ToLower(host), true
}

// parsePorts reads the CLI's `ports` parameter. It is a filter over probePorts
// and never a way to name a new one: anything outside 80 and 443 is refused
// rather than silently dropped, so a caller cannot mistake this endpoint's
// answer for a general port scan it did not get.
func parsePorts(raw string) ([]uint16, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return probePorts, nil
	}

	asked := make(map[uint16]bool, len(probePorts))
	for _, field := range strings.Split(raw, ",") {
		port, err := strconv.ParseUint(strings.TrimSpace(field), 10, 16)
		if err != nil || !slices.Contains(probePorts, uint16(port)) {
			return nil, errOnlyProbePorts
		}
		asked[uint16(port)] = true
	}

	// Emitted in probePorts order rather than the caller's, so the answer is
	// the same shape however the query was written.
	ports := make([]uint16, 0, len(asked))
	for _, port := range probePorts {
		if asked[port] {
			ports = append(ports, port)
		}
	}
	return ports, nil
}

// errOnlyProbePorts is the single answer for every unsupported ports value: a
// caller learns that this endpoint checks 80 and 443, and nothing about what
// else it might have been willing to try.
var errOnlyProbePorts = errors.New("ports may only name 80 and 443")

// clientKey is the per-client rate limit key.
//
// In production this service listens on loopback behind Caddy, so RemoteAddr
// is always the proxy and the real caller is the last entry of
// X-Forwarded-For, which the proxy appends itself. That entry is only trusted
// when the request actually arrived over loopback; on a directly exposed
// listener the header is caller-controlled and is ignored.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !peer.IsLoopback() {
		return host
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.LastIndex(fwd, ","); i >= 0 {
			fwd = fwd[i+1:]
		}
		if forwarded := strings.TrimSpace(fwd); forwarded != "" {
			return forwarded
		}
	}
	return host
}
