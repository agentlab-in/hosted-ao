// Command controlplane is the hosted AO control plane: it brokers Google
// sign-in, the RFC 8628 device flow, and JWT issuance for VMs bound via
// ao setup-vm. See controlplane/README.md to run it locally and
// controlplane/TOKEN_CONTRACT.md for the token shapes it issues.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/agentlab-in/hosted-ao/controlplane/internal/config"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/keys"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/server"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/storage/sqlite"
)

// shutdownTimeout bounds graceful shutdown after SIGINT/SIGTERM.
const shutdownTimeout = 10 * time.Second

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	db, err := sqlite.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("open storage: %v", err)
	}
	defer db.Close()

	km, err := keys.Load(cfg.DataDir)
	if err != nil {
		log.Fatalf("load signing keys: %v", err)
	}

	mux := server.New(db, km)

	// LISTEN_ADDR defaults to loopback (127.0.0.1:8080): this service sits
	// behind Caddy on the same box, which terminates TLS and reverse-proxies
	// to it. See controlplane/.env.example.
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("control plane listening on %s", cfg.ListenAddr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	case <-ctx.Done():
		log.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Fatalf("graceful shutdown: %v", err)
		}
	}
}
