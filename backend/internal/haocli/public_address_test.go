package haocli

import (
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

// stubPublicIPProbes points the public-address probes at local servers so the
// detection logic is testable without live GCP or a public internet round trip.
func stubPublicIPProbes(t *testing.T, gcpURL string, echoEndpoints []string) {
	t.Helper()
	restoreGCP := gcpMetadataExternalIPURL
	restoreEcho := publicIPEchoEndpoints
	gcpMetadataExternalIPURL = gcpURL
	publicIPEchoEndpoints = echoEndpoints
	t.Cleanup(func() {
		gcpMetadataExternalIPURL = restoreGCP
		publicIPEchoEndpoints = restoreEcho
	})
}

func TestDefaultMachinePublicIPsPrefersGCPMetadata(t *testing.T) {
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "missing Metadata-Flavor", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("34.63.28.158\n"))
	}))
	defer meta.Close()
	// This echo endpoint must never be consulted: GCP metadata answered first.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("203.0.113.5"))
	}))
	defer echo.Close()
	stubPublicIPProbes(t, meta.URL, []string{echo.URL})

	got := defaultMachinePublicIPs()
	if len(got) != 1 || got[0] != "34.63.28.158" {
		t.Fatalf("defaultMachinePublicIPs() = %v, want [34.63.28.158]", got)
	}
}

func TestDefaultMachinePublicIPsFallsBackToEcho(t *testing.T) {
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer meta.Close()
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("203.0.113.9"))
	}))
	defer echo.Close()
	stubPublicIPProbes(t, meta.URL, []string{echo.URL})

	got := defaultMachinePublicIPs()
	if len(got) != 1 || got[0] != "203.0.113.9" {
		t.Fatalf("defaultMachinePublicIPs() = %v, want [203.0.113.9]", got)
	}
}

func TestDefaultMachinePublicIPsReturnsNothingWhenNoProberAnswers(t *testing.T) {
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer meta.Close()
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer echo.Close()
	stubPublicIPProbes(t, meta.URL, []string{echo.URL})

	if got := defaultMachinePublicIPs(); len(got) != 0 {
		t.Fatalf("defaultMachinePublicIPs() = %v, want none", got)
	}
}

func TestDefaultMachinePublicIPsRejectsPrivateAnswer(t *testing.T) {
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer meta.Close()
	// A prober answering with a private address is not this box's reachable
	// address (a far-side NAT or captive portal) and must be discarded.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("10.0.0.1"))
	}))
	defer echo.Close()
	stubPublicIPProbes(t, meta.URL, []string{echo.URL})

	if got := defaultMachinePublicIPs(); len(got) != 0 {
		t.Fatalf("defaultMachinePublicIPs() = %v, want none for a private answer", got)
	}
}

func TestValidPublicIP(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"34.63.28.158", "34.63.28.158"},
		{"203.0.113.5", "203.0.113.5"},
		{"2001:db8::1", "2001:db8::1"},
		{"10.0.0.1", ""},           // private
		{"127.0.0.1", ""},          // loopback
		{"169.254.1.1", ""},        // link-local
		{"fe80::1", ""},            // link-local v6
		{"not-an-ip", ""},          // unparsable
		{"", ""},                   // empty
		{" 8.8.8.8 \n", "8.8.8.8"}, // surrounding whitespace is trimmed to the canonical form
	} {
		if got := validPublicIP(tc.in); got != tc.want {
			t.Errorf("validPublicIP(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMachineAddressesIncludesDetectedPublicAddress(t *testing.T) {
	stubMachinePublicIPs(t, []string{"34.63.28.158", "2001:db8::1"})
	addrs := machineAddresses(443)
	if len(addrs) == 0 || addrs[0] != "127.0.0.1:443" {
		t.Fatalf("first address=%v, want loopback first", addrs)
	}
	if !slices.Contains(addrs, "34.63.28.158:443") {
		t.Fatalf("addresses=%v missing detected public IPv4", addrs)
	}
	if !slices.Contains(addrs, "[2001:db8::1]:443") {
		t.Fatalf("addresses=%v missing detected public IPv6 (bracketed)", addrs)
	}
}

func TestPairProvisionCarriesDetectedPublicAddressInCertIPsSAN(t *testing.T) {
	certDir := provisionedPairEnv(t)
	stubMachinePublicIPs(t, []string{"34.63.28.158"})

	provision, err := provisionPairIdentity(443, false)
	if err != nil {
		t.Fatal(err)
	}
	if provision.identity == nil || !slices.Contains(provision.identity.Addresses, "34.63.28.158:443") {
		t.Fatalf("identity addresses=%v missing detected public address", provision.identity.Addresses)
	}

	cert, err := vmgateway.LoadOrCreatePairCertificate(certDir)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	var sanIPs []string
	for _, ip := range leaf.IPAddresses {
		sanIPs = append(sanIPs, ip.String())
	}
	if !slices.Contains(sanIPs, "34.63.28.158") {
		t.Fatalf("cert IP SANs=%v missing the detected public address; Chromium would reject a bare-IP connection at it", sanIPs)
	}
}
