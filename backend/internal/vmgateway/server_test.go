package vmgateway

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
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
