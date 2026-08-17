package vmgateway

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewServer_RequiresDomain(t *testing.T) {
	_, err := NewServer(Config{CertDir: t.TempDir()}, http.NotFoundHandler(), discardLogger())
	if err == nil {
		t.Fatal("expected an error when Domain is empty")
	}
}

func TestNewServer_CreatesCertDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "certs")
	_, err := NewServer(Config{
		Domain:    "vm.example.com",
		CertDir:   dir,
		HTTPAddr:  "127.0.0.1:0",
		HTTPSAddr: "127.0.0.1:0",
	}, http.NotFoundHandler(), discardLogger())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	info, statErr := os.Stat(dir)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("expected the cert cache dir to be created, stat err: %v", statErr)
	}
}

// TestNewServer_SetsIdleTimeoutNotReadOrWriteTimeout pins the slowloris fix:
// IdleTimeout must be set on both listeners (an internet-facing gateway must
// reclaim a keep-alive connection nobody is using), while ReadTimeout and
// WriteTimeout must stay unset. net/http sets both as an absolute deadline on
// the connection before the handler runs and does not clear it on Hijack, so
// either one would also cut off the /mux WebSocket tunnel (hijacked by
// httputil.ReverseProxy's upgrade handling) and long SSE responses once the
// deadline elapsed, healthy or not.
func TestNewServer_SetsIdleTimeoutNotReadOrWriteTimeout(t *testing.T) {
	srv, err := NewServer(Config{
		Domain: "vm.example.com", CertDir: t.TempDir(),
		HTTPAddr: "127.0.0.1:0", HTTPSAddr: "127.0.0.1:0",
	}, http.NotFoundHandler(), discardLogger())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	for name, s := range map[string]*http.Server{"http": srv.httpSrv, "https": srv.httpsSrv} {
		if s.IdleTimeout <= 0 {
			t.Errorf("%s server IdleTimeout = %v, want a positive bound", name, s.IdleTimeout)
		}
		if s.ReadTimeout != 0 {
			t.Errorf("%s server ReadTimeout = %v, want unset: it would also bound the hijacked /mux tunnel", name, s.ReadTimeout)
		}
		if s.WriteTimeout != 0 {
			t.Errorf("%s server WriteTimeout = %v, want unset: it would cut off a long-lived SSE stream", name, s.WriteTimeout)
		}
	}
}

// TestNewServer_ErrorLogWritesToSlog pins that net/http's own internal errors
// (a failed TLS handshake, an ACME/autocert failure) are wired to the
// gateway's structured logger instead of falling back to log.Default() on raw
// stderr, where nothing reading slog output would ever see them.
func TestNewServer_ErrorLogWritesToSlog(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	srv, err := NewServer(Config{
		Domain: "vm.example.com", CertDir: t.TempDir(),
		HTTPAddr: "127.0.0.1:0", HTTPSAddr: "127.0.0.1:0",
	}, http.NotFoundHandler(), log)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	if srv.httpSrv.ErrorLog == nil || srv.httpsSrv.ErrorLog == nil {
		t.Fatal("both servers must set ErrorLog")
	}
	srv.httpsSrv.ErrorLog.Print("simulated TLS handshake failure")

	if got := buf.String(); !strings.Contains(got, "simulated TLS handshake failure") {
		t.Errorf("slog output = %q, want the net/http ErrorLog line forwarded into it", got)
	}
}

// TestNewServer_HostedMode_BindsHTTPAndUsesAutocert pins the hosted half of
// "hosted mode still binds :80 and uses autocert, and pair mode does
// neither": httpSrv must be built, and the TLS listener's certificate must
// come from autocert's GetCertificate hook rather than a fixed certificate.
func TestNewServer_HostedMode_BindsHTTPAndUsesAutocert(t *testing.T) {
	cfg := Config{
		Mode:      ModeHosted,
		Domain:    "vm.example.com",
		CertDir:   t.TempDir(),
		HTTPAddr:  "127.0.0.1:0",
		HTTPSAddr: "127.0.0.1:0",
	}
	srv, err := NewServer(cfg, http.NotFoundHandler(), discardLogger())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if srv.httpSrv == nil {
		t.Fatal("hosted mode must bind the :80 ACME challenge listener")
	}
	if srv.httpsSrv.TLSConfig == nil || srv.httpsSrv.TLSConfig.GetCertificate == nil {
		t.Error("hosted mode must serve certificates via autocert.Manager.GetCertificate")
	}
	if len(srv.httpsSrv.TLSConfig.Certificates) != 0 {
		t.Error("hosted mode must not set a fixed certificate list; that is the pair-mode shape")
	}
}

// TestNewServer_PairMode_SkipsHTTPListenerAndAutocert pins the pair half of
// the same contract: no httpSrv at all (no :80 bind, no ACME challenge to
// answer), and the TLS listener presents a fixed, persisted certificate
// rather than going through autocert.
func TestNewServer_PairMode_SkipsHTTPListenerAndAutocert(t *testing.T) {
	cfg := Config{
		Mode:      ModePair,
		CertDir:   t.TempDir(),
		HTTPSAddr: "127.0.0.1:0",
	}
	srv, err := NewServer(cfg, http.NotFoundHandler(), discardLogger())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if srv.httpSrv != nil {
		t.Error("pair mode must not build an HTTP (:80 ACME challenge) listener")
	}
	if srv.httpsSrv == nil {
		t.Fatal("pair mode must still build the TLS listener")
	}
	if srv.httpsSrv.TLSConfig == nil || len(srv.httpsSrv.TLSConfig.Certificates) != 1 {
		t.Fatal("pair mode must present the persisted self-signed certificate directly")
	}
	if srv.httpsSrv.TLSConfig.GetCertificate != nil {
		t.Error("pair mode must not use autocert's GetCertificate hook")
	}
}

// TestNewServer_PairMode_DoesNotRequireDomain proves pair mode's
// certificate/config half does not need a Domain at all, unlike hosted mode
// (TestNewServer_RequiresDomain above).
func TestNewServer_PairMode_DoesNotRequireDomain(t *testing.T) {
	cfg := Config{Mode: ModePair, CertDir: t.TempDir(), HTTPSAddr: "127.0.0.1:0"}
	if _, err := NewServer(cfg, http.NotFoundHandler(), discardLogger()); err != nil {
		t.Fatalf("NewServer: %v", err)
	}
}

func TestNewServer_PairMode_PersistsCertificateUnderCertDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "pair-cert")
	cfg := Config{Mode: ModePair, CertDir: dir, HTTPSAddr: "127.0.0.1:0"}
	if _, err := NewServer(cfg, http.NotFoundHandler(), discardLogger()); err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	for _, name := range []string{"cert.pem", "key.pem"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to be persisted under CertDir: %v", name, err)
		}
	}
}

// TestNewServer_PairMode_SetsIdleTimeoutAndErrorLog mirrors
// TestNewServer_SetsIdleTimeoutNotReadOrWriteTimeout and
// TestNewServer_ErrorLogWritesToSlog above for the pair-mode TLS listener: a
// pair-mode gateway is just as internet-reachable as a hosted one, so it
// needs the same slowloris and log-visibility hardening.
func TestNewServer_PairMode_SetsIdleTimeoutAndErrorLog(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := Config{Mode: ModePair, CertDir: t.TempDir(), HTTPSAddr: "127.0.0.1:0"}
	srv, err := NewServer(cfg, http.NotFoundHandler(), log)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if srv.httpsSrv.IdleTimeout <= 0 {
		t.Errorf("IdleTimeout = %v, want a positive bound", srv.httpsSrv.IdleTimeout)
	}
	if srv.httpsSrv.ReadTimeout != 0 || srv.httpsSrv.WriteTimeout != 0 {
		t.Error("ReadTimeout/WriteTimeout must stay unset: they would also bound the hijacked /mux tunnel and long SSE responses")
	}
	if srv.httpsSrv.ErrorLog == nil {
		t.Fatal("pair mode must set ErrorLog")
	}
	srv.httpsSrv.ErrorLog.Print("simulated TLS handshake failure")
	if got := buf.String(); !strings.Contains(got, "simulated TLS handshake failure") {
		t.Errorf("slog output = %q, want the net/http ErrorLog line forwarded into it", got)
	}
}

// TestServer_Run_PairMode_SkipsPort80AndShutsDownCleanly is the pair-mode
// analogue of TestServer_RunShutsDownOnContextCancel below: it proves Run
// starts and cleanly stops a single (TLS-only) listener, with no :80
// goroutine to leak or fail to shut down.
func TestServer_Run_PairMode_SkipsPort80AndShutsDownCleanly(t *testing.T) {
	cfg := Config{
		Mode:      ModePair,
		CertDir:   t.TempDir(),
		HTTPSAddr: "127.0.0.1:0",
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv, err := NewServer(cfg, handler, discardLogger())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if srv.cfg.HTTPAddr != "" {
		t.Fatalf("HTTPAddr = %q, want empty in pair mode", srv.cfg.HTTPAddr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, time.Second) }()

	time.Sleep(50 * time.Millisecond) // give the TLS listener time to bind
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error on graceful shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation; the TLS listener goroutine leaked")
	}
}

// TestServer_RunShutsDownOnContextCancel exercises Run's graceful-shutdown
// path without ever completing a TLS handshake or touching the network:
// autocert only calls out to Let's Encrypt when a client actually connects
// over TLS and requests a certificate, which this test never does. It binds
// ephemeral loopback ports (no root required) purely to prove both listener
// goroutines start, then confirms Run returns cleanly once the context is
// cancelled, so neither goroutine can leak on shutdown.
func TestServer_RunShutsDownOnContextCancel(t *testing.T) {
	cfg := Config{
		Domain:    "vm.example.com",
		CertDir:   t.TempDir(),
		HTTPAddr:  "127.0.0.1:0",
		HTTPSAddr: "127.0.0.1:0",
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv, err := NewServer(cfg, handler, discardLogger())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, time.Second) }()

	time.Sleep(50 * time.Millisecond) // give both listeners time to bind
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned an error on graceful shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation; a listener goroutine leaked")
	}
}
