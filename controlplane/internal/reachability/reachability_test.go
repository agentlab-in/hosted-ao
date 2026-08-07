package reachability

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"
)

// fakeConn stands in for a connected socket. It records reads so a test can
// assert this service never touches the body of whatever answered.
type fakeConn struct {
	net.Conn
	reads  int
	closed bool
}

func (c *fakeConn) Read(b []byte) (int, error) { c.reads++; return 0, io.EOF }
func (c *fakeConn) Close() error               { c.closed = true; return nil }

// recorder captures what a request made the service do off-box.
type recorder struct {
	resolved []string
	dialed   []string
	conns    []*fakeConn
	open     map[string]bool
}

// newTestService builds a Service whose resolver answers with addrs and whose
// dialer succeeds only for the "ip:port" strings in open, so every path can be
// exercised without DNS or a network.
func newTestService(addrs []netip.Addr, open map[string]bool) (*Service, *recorder) {
	rec := &recorder{open: open}
	return &Service{
		resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			rec.resolved = append(rec.resolved, host)
			if len(addrs) == 0 {
				return nil, errors.New("no such host")
			}
			return addrs, nil
		},
		dial: func(_ context.Context, addr string) (net.Conn, error) {
			rec.dialed = append(rec.dialed, addr)
			if !rec.open[addr] {
				return nil, errors.New("connection refused")
			}
			conn := &fakeConn{}
			rec.conns = append(rec.conns, conn)
			return conn, nil
		},
		clients: newWindow(clientPerWindow, rateWindow),
		targets: newWindow(targetPerWindow, rateWindow),
		global:  newWindow(globalPerWindow, rateWindow),
	}, rec
}

// probe issues one request the way ao setup-vm does.
func probe(s *Service, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/reachability?"+query, nil)
	req.RemoteAddr = "203.0.113.9:54321"
	w := httptest.NewRecorder()
	s.handleReachability(w, req)
	return w
}

func hostQuery(host string) string {
	return url.Values{"host": {host}, "ports": {"80,443"}}.Encode()
}

func addrs(t *testing.T, raw ...string) []netip.Addr {
	t.Helper()
	out := make([]netip.Addr, 0, len(raw))
	for _, s := range raw {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		out = append(out, addr)
	}
	return out
}

// TestRefusesBlockedAddressClasses is the test that matters on this endpoint.
// One case per blocked class, each asserting the request is refused *and* that
// no connection was attempted, so a later refactor that loosens one predicate
// cannot quietly reopen a class.
func TestRefusesBlockedAddressClasses(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		// The one an SSRF goes for first. Also covered by the link-local
		// predicate, and listed in blockedPrefixes as well, on purpose.
		{"cloud metadata", "169.254.169.254"},
		{"cloud metadata as IPv4-mapped IPv6", "::ffff:169.254.169.254"},
		{"loopback IPv4", "127.0.0.1"},
		{"loopback IPv4 outside 127.0.0.1", "127.10.20.30"},
		{"loopback IPv6", "::1"},
		{"link-local IPv6", "fe80::1"},
		{"RFC 1918 10/8", "10.0.0.5"},
		{"RFC 1918 172.16/12", "172.16.0.5"},
		{"RFC 1918 192.168/16", "192.168.1.5"},
		{"unique local IPv6", "fd00::1"},
		{"carrier-grade NAT", "100.64.0.1"},
		{"multicast IPv4", "224.0.0.1"},
		{"multicast IPv6", "ff02::1"},
		{"interface-local multicast IPv6", "ff01::1"},
		{"unspecified IPv4", "0.0.0.0"},
		{"unspecified IPv6", "::"},
		{"this-network", "0.1.2.3"},
		{"broadcast", "255.255.255.255"},
		{"reserved 240/4", "240.1.2.3"},
		{"benchmarking", "198.18.0.1"},
		{"IETF protocol assignments IPv4", "192.0.0.1"},
		{"documentation IPv4", "192.0.2.1"},
		{"documentation IPv4 TEST-NET-2", "198.51.100.1"},
		{"documentation IPv4 TEST-NET-3", "203.0.113.1"},
		{"documentation IPv6", "2001:db8::1"},
		{"IPv4-compatible IPv6", "::7f00:1"},
		{"NAT64 well-known prefix", "64:ff9b::a9fe:a9fe"},
		{"6to4", "2002:a9fe:a9fe::1"},
		{"Teredo", "2001::a9fe:a9fe"},
		{"IPv6 discard-only", "100::1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := newTestService(addrs(t, tc.addr), nil)

			w := probe(s, hostQuery("vm.example.com"))

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, http.StatusBadRequest, w.Body.String())
			}
			if len(rec.dialed) != 0 {
				t.Fatalf("connected to %v, want no connection at all", rec.dialed)
			}
		})
	}
}

// TestRefusesMixedAnswer covers the DNS rebinding shape: a name that resolves
// to a public address alongside an internal one is refused outright, in either
// order, rather than the public one being picked out and the rest ignored.
func TestRefusesMixedAnswer(t *testing.T) {
	for _, answer := range [][]string{
		{"93.184.216.34", "169.254.169.254"},
		{"169.254.169.254", "93.184.216.34"},
	} {
		s, rec := newTestService(addrs(t, answer...), nil)

		w := probe(s, hostQuery("vm.example.com"))

		if w.Code != http.StatusBadRequest {
			t.Fatalf("answer %v: status = %d, want %d", answer, w.Code, http.StatusBadRequest)
		}
		if len(rec.dialed) != 0 {
			t.Fatalf("answer %v: connected to %v, want no connection", answer, rec.dialed)
		}
	}
}

// TestProbesResolvedAddress checks the wire contract ao setup-vm parses, and
// that the connection goes to the address that was validated rather than to
// the hostname, which is what stops a second DNS answer redirecting it.
func TestProbesResolvedAddress(t *testing.T) {
	s, rec := newTestService(addrs(t, "93.184.216.34"), map[string]bool{"93.184.216.34:80": true})

	w := probe(s, hostQuery("vm.example.com"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body struct {
		Ports map[string]bool `json:"ports"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	if len(body.Ports) != 2 || !body.Ports["80"] || body.Ports["443"] {
		t.Fatalf("ports = %v, want {80:true 443:false}", body.Ports)
	}
	if got, want := rec.dialed, []string{"93.184.216.34:80", "93.184.216.34:443"}; !slices.Equal(got, want) {
		t.Fatalf("dialed %v, want %v", got, want)
	}
	if len(rec.conns) != 1 {
		t.Fatalf("opened %d connections, want 1", len(rec.conns))
	}
	// Connect only: nothing is read off the socket, so no banner can reach the
	// caller, and it is closed straight away.
	if rec.conns[0].reads != 0 {
		t.Fatalf("read from the probed port %d times, want 0", rec.conns[0].reads)
	}
	if !rec.conns[0].closed {
		t.Fatal("probe connection was left open")
	}
}

func TestProbesIPv6TargetWithBrackets(t *testing.T) {
	s, rec := newTestService(addrs(t, "2606:2800:220:1::1"), nil)

	if w := probe(s, hostQuery("vm.example.com")); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got, want := rec.dialed, []string{"[2606:2800:220:1::1]:80", "[2606:2800:220:1::1]:443"}; !slices.Equal(got, want) {
		t.Fatalf("dialed %v, want %v", got, want)
	}
}

func TestRejectsBadRequests(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"no host", "ports=80,443"},
		{"empty host", "host=&ports=80,443"},
		{"host with a scheme", "host=" + url.QueryEscape("http://vm.example.com")},
		{"host with a port", "host=" + url.QueryEscape("vm.example.com:8080")},
		{"host with a path", "host=" + url.QueryEscape("vm.example.com/admin")},
		{"host with an IPv6 zone", "host=" + url.QueryEscape("fe80::1%25eth0")},
		{"port that is not 80 or 443", "host=vm.example.com&ports=22"},
		{"port smuggled alongside a real one", "host=vm.example.com&ports=80,22"},
		{"port out of range", "host=vm.example.com&ports=99999"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, rec := newTestService(addrs(t, "93.184.216.34"), nil)

			if w := probe(s, tc.query); w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
			}
			if len(rec.dialed) != 0 {
				t.Fatalf("connected to %v, want no connection", rec.dialed)
			}
		})
	}
}

func TestUnresolvableHostIsNotAProbe(t *testing.T) {
	s, rec := newTestService(nil, nil)

	if w := probe(s, hostQuery("nope.example.com")); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if len(rec.dialed) != 0 {
		t.Fatalf("connected to %v, want no connection", rec.dialed)
	}
}

func TestDefaultsToBothPorts(t *testing.T) {
	s, rec := newTestService(addrs(t, "93.184.216.34"), nil)

	if w := probe(s, "host=vm.example.com"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got, want := rec.dialed, []string{"93.184.216.34:80", "93.184.216.34:443"}; !slices.Equal(got, want) {
		t.Fatalf("dialed %v, want %v", got, want)
	}
}

func TestSingleRequestedPortIsHonoured(t *testing.T) {
	s, rec := newTestService(addrs(t, "93.184.216.34"), nil)

	w := probe(s, "host=vm.example.com&ports=443")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got, want := rec.dialed, []string{"93.184.216.34:443"}; !slices.Equal(got, want) {
		t.Fatalf("dialed %v, want %v", got, want)
	}
}

// TestRateLimited proves the endpoint stops making outbound connections once a
// caller is over the allowance. It is unauthenticated, so this is the whole
// budget.
func TestRateLimited(t *testing.T) {
	s, rec := newTestService(addrs(t, "93.184.216.34"), nil)

	for i := range clientPerWindow {
		if w := probe(s, hostQuery("vm.example.com")); w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i+1, w.Code)
		}
	}
	dialedWhileAllowed := len(rec.dialed)

	w := probe(s, hostQuery("vm.example.com"))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
	if len(rec.dialed) != dialedWhileAllowed {
		t.Fatalf("a refused request still connected: %v", rec.dialed[dialedWhileAllowed:])
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"vm.example.com", "vm.example.com", true},
		{" VM.Example.com. ", "vm.example.com", true},
		{"93.184.216.34", "93.184.216.34", true},
		{"::ffff:93.184.216.34", "93.184.216.34", true},
		{"2606:2800:220:1::1", "2606:2800:220:1::1", true},
		{"", "", false},
		{"vm..example.com", "", false},
		{"-vm.example.com", "", false},
		{"vm.example.com:443", "", false},
		{"fe80::1%eth0", "", false},
	}
	for _, tc := range cases {
		got, ok := normalizeHost(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("normalizeHost(%q) = %q, %v, want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestClientKeyBehindProxy: in production this listens on loopback behind
// Caddy, so every RemoteAddr is the proxy and the real caller is the last
// X-Forwarded-For entry, which Caddy appends. The header is only trusted from
// loopback, so a direct caller cannot pick its own rate limit bucket.
func TestClientKeyBehindProxy(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		forwarded  string
		want       string
	}{
		{"loopback proxy uses the appended entry", "127.0.0.1:8080", "198.51.100.7", "198.51.100.7"},
		{"a spoofed prefix does not win", "127.0.0.1:8080", "10.0.0.1, 198.51.100.7", "198.51.100.7"},
		{"no header, loopback peer", "127.0.0.1:8080", "", "127.0.0.1"},
		{"direct caller's header is ignored", "198.51.100.7:1234", "203.0.113.1", "198.51.100.7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/reachability", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if got := clientKey(req); got != tc.want {
				t.Fatalf("clientKey = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWindow(t *testing.T) {
	now := time.Now()
	w := newWindow(2, time.Minute)
	w.clock = func() time.Time { return now }

	if !w.allow("a") || !w.allow("a") {
		t.Fatal("the first two requests should be inside the allowance")
	}
	if w.allow("a") {
		t.Fatal("the third request should be refused")
	}
	if !w.allow("b") {
		t.Fatal("a different key has its own allowance")
	}

	now = now.Add(2 * time.Minute)
	if !w.allow("a") {
		t.Fatal("the window should have expired")
	}
	if len(w.seen["a"]) != 1 {
		t.Fatalf("expired requests were not dropped: %v", w.seen["a"])
	}
}

// TestWindowBoundsMemory: the keys here are caller-supplied, so past the cap a
// new key is refused rather than admitted.
func TestWindowBoundsMemory(t *testing.T) {
	now := time.Now()
	w := newWindow(1, time.Minute)
	w.clock = func() time.Time { return now }

	for i := range maxTrackedKeys {
		if !w.allow(strconv.Itoa(i)) {
			t.Fatalf("key %d should be within the cap", i)
		}
	}
	if w.allow("one too many") {
		t.Fatal("a new key past the cap should be refused")
	}

	// Once the tracked keys age out the cap releases itself, so a burst cannot
	// lock new callers out for longer than one window.
	now = now.Add(2 * time.Minute)
	if !w.allow("one too many") {
		t.Fatal("the cap should release once the window has passed")
	}
	if len(w.seen) != 1 {
		t.Fatalf("the sweep left %d keys, want 1", len(w.seen))
	}
}
