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

	"github.com/agentlab-in/hosted-ao/controlplane/internal/api"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/auth"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/config"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/device"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/keys"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/server"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/storage/sqlite"
	"github.com/agentlab-in/hosted-ao/controlplane/internal/tokens"
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

	// Log the resolved absolute data dir and the active kid on every boot.
	// The signing keys live under the data dir, so if a deployment change ever
	// points the service at a different path, keys.Load silently generates a
	// fresh pair and every issued token starts failing verification until each
	// VM's JWKS cache expires. A kid that changed unexpectedly across a restart
	// is the one signal that makes that diagnosable.
	activeKID, _ := km.Active()
	log.Printf("data dir %s, active signing kid %s, access token ttl %v, public origin %s",
		cfg.DataDir, activeKID, cfg.AccessTokenTTL, cfg.PublicOrigin)

	mux := server.New(db, km)

	authSvc, err := auth.NewService(db, cfg)
	if err != nil {
		log.Fatalf("init auth: %v", err)
	}
	authSvc.Register(mux)

	// One Issuer mints both audiences: machines.id for a token a VM gateway
	// will verify, and cfg.PublicOrigin for a token this service's own API
	// will. See TOKEN_CONTRACT.md, "The two audiences".
	issuer := tokens.NewIssuer(km, db, cfg.PublicOrigin, cfg.AccessTokenTTL)

	apiSvc := api.NewService(issuer)
	apiSvc.Register(mux)

	deviceSvc, err := device.NewService(db, issuer, authSvc, apiSvc.Authenticate, cfg.PublicOrigin)
	if err != nil {
		log.Fatalf("init device flow: %v", err)
	}
	deviceSvc.Register(mux)

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
