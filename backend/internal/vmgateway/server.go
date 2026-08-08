package vmgateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// Server owns the gateway's two listeners: HTTPAddr for the ACME HTTP-01
// challenge (and a redirect to https for everything else), and HTTPSAddr for
// TLS, serving the handler passed to NewServer. Both share one
// autocert.Manager scoped to exactly the configured domain, so ao vm serve
// can never be tricked into requesting a certificate for an arbitrary Host
// header sent to this machine's IP.
type Server struct {
	cfg *Config
	log *slog.Logger

	httpSrv  *http.Server
	httpsSrv *http.Server
}

// NewServer builds a Server. It does not bind any socket yet; call Run.
func NewServer(cfg Config, handler http.Handler, log *slog.Logger) (*Server, error) {
	if cfg.Domain == "" {
		return nil, errors.New("vm gateway: a domain is required")
	}
	if cfg.CertDir == "" {
		return nil, errors.New("vm gateway: a certificate cache directory is required")
	}
	// Fail fast, before ever binding a port, if the cache directory cannot be
	// created (e.g. an unwritable AO_DATA_DIR override).
	if err := os.MkdirAll(cfg.CertDir, 0o700); err != nil {
		return nil, fmt.Errorf("create cert dir: %w", err)
	}

	manager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cfg.CertDir),
		HostPolicy: autocert.HostWhitelist(cfg.Domain),
	}

	log = loggerOrDefault(log)
	// net/http writes its own internal errors (failed TLS handshakes,
	// autocert/ACME failures such as a blocked :80, a rate-limited Let's
	// Encrypt account, or an unroutable DNS record) to ErrorLog, which
	// defaults to log.Default() on raw stderr rather than this process's
	// structured logger. Without this, certificate breakage on a
	// systemd-managed, internet-facing gateway is invisible to anyone reading
	// slog output. Warn (not Error) because most of what lands here is
	// ordinary internet noise (a client resetting a handshake mid-flight) that
	// happens to be routed through the same hook as a real ACME failure; the
	// gateway has no way to tell them apart from the message alone.
	errLog := slog.NewLogLogger(log.Handler(), slog.LevelWarn)
	s := &Server{cfg: &cfg, log: log}
	s.httpSrv = &http.Server{
		Addr: cfg.HTTPAddr,
		// nil fallback: every non-challenge request on :80 is redirected to
		// https, never served plaintext.
		Handler:           manager.HTTPHandler(nil),
		ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout reclaims the goroutine and fd of a keep-alive connection
		// that finished a request and then never sends another one, an
		// internet-facing listener's other slowloris shape besides a slow
		// header. 2 minutes is generous for normal REST reuse (the renderer's
		// client keeps reissuing requests far more often than that) while still
		// bounding an attacker who opens many connections and lets them sit
		// idle. WriteTimeout and ReadTimeout are deliberately left unset: both
		// set an absolute deadline on the connection before the handler runs,
		// and net/http does not clear it on Hijack, so either one would also
		// cut off the /mux WebSocket tunnel (hijacked wholesale by
		// httputil.ReverseProxy's upgrade handling) and the SSE routes'
		// long-lived response writes once the deadline elapsed, regardless of
		// whether the stream was still healthy. IdleTimeout only fires between
		// requests, never during one already being served, so it cannot do that.
		IdleTimeout: 2 * time.Minute,
		ErrorLog:    errLog,
	}
	s.httpsSrv = &http.Server{
		Addr:              cfg.HTTPSAddr,
		Handler:           handler,
		TLSConfig:         manager.TLSConfig(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          errLog,
	}
	return s, nil
}

// Run serves both listeners until ctx is cancelled (SIGINT/SIGTERM, via
// signal.NotifyContext in the caller), then performs a graceful shutdown of
// both bounded by shutdownTimeout. It blocks until both have stopped. If
// either listener fails on its own (for example the configured port is
// already in use), Run shuts down the other and returns that error.
func (s *Server) Run(ctx context.Context, shutdownTimeout time.Duration) error {
	serveErr := make(chan error, 2)

	go func() {
		s.log.Info("vm gateway: ACME challenge listener starting", "addr", s.cfg.HTTPAddr)
		serveErr <- serveOrNil(s.httpSrv.ListenAndServe())
	}()
	go func() {
		s.log.Info("vm gateway: TLS listener starting", "addr", s.cfg.HTTPSAddr, "domain", s.cfg.Domain)
		// TLSConfig on the server already carries the autocert manager's
		// GetCertificate, so no cert/key files are passed here.
		serveErr <- serveOrNil(s.httpsSrv.ListenAndServeTLS("", ""))
	}()

	consumed := 0
	var runErr error
	select {
	case err := <-serveErr:
		consumed++
		runErr = err
	case <-ctx.Done():
		s.log.Info("vm gateway: shutdown signal received, draining connections", "timeout", shutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := s.httpSrv.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shut down http listener: %w", err)
	}
	if err := s.httpsSrv.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shut down https listener: %w", err)
	}

	// Both goroutines are guaranteed to unblock and send exactly once, each,
	// once Shutdown has been called on their server; drain whichever ones
	// this call hasn't already consumed so neither goroutine leaks.
	for ; consumed < 2; consumed++ {
		if err := <-serveErr; err != nil && runErr == nil {
			runErr = err
		}
	}

	if runErr == nil {
		s.log.Info("vm gateway: stopped cleanly")
	}
	return runErr
}

// serveOrNil turns the ErrServerClosed a clean Shutdown produces into
// success, matching net/http.Server.ListenAndServe's documented contract.
func serveOrNil(err error) error {
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func loggerOrDefault(log *slog.Logger) *slog.Logger {
	if log != nil {
		return log
	}
	return slog.Default()
}
