package vmgateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// Server owns the gateway's listeners, serving the handler passed to
// NewServer. In hosted mode (Config.Mode == ModeHosted) that is two
// listeners: HTTPAddr for the ACME HTTP-01 challenge (and a redirect to https
// for everything else), and HTTPSAddr for TLS. Both share one
// autocert.Manager scoped to exactly the configured domain, so ao vm serve
// can never be tricked into requesting a certificate for an arbitrary Host
// header sent to this machine's IP. In pair mode (ModePair) there is no ACME
// HTTP-01 challenge to answer, so httpSrv is never built: Server binds only
// the TLS listener, presenting the long-lived self-signed certificate from
// LoadOrCreatePairCertificate.
type Server struct {
	cfg *Config
	log *slog.Logger

	httpSrv  *http.Server // nil in pair mode
	httpsSrv *http.Server
}

// NewServer builds a Server. It does not bind any socket yet; call Run.
func NewServer(cfg Config, handler http.Handler, log *slog.Logger) (*Server, error) {
	if cfg.CertDir == "" {
		return nil, errors.New("vm gateway: a certificate directory is required")
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
	// gateway has no way to tell them apart from the message alone. Applies in
	// both modes: a pair-mode gateway is just as internet-reachable as a
	// hosted one, TLS noise and all.
	errLog := slog.NewLogLogger(log.Handler(), slog.LevelWarn)
	s := &Server{cfg: &cfg, log: log}

	if cfg.Mode == ModePair {
		cert, err := LoadOrCreatePairCertificate(cfg.CertDir)
		if err != nil {
			return nil, fmt.Errorf("pair mode certificate: %w", err)
		}
		s.httpsSrv = &http.Server{
			Addr:              cfg.HTTPSAddr,
			Handler:           handler,
			TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}},
			ReadHeaderTimeout: 10 * time.Second,
			// See the IdleTimeout/ErrorLog comments on the hosted-mode
			// httpsSrv below; the same slowloris and log-visibility reasoning
			// applies unchanged to the pair-mode TLS listener.
			IdleTimeout: 2 * time.Minute,
			ErrorLog:    errLog,
		}
		return s, nil
	}

	if cfg.Domain == "" {
		return nil, errors.New("vm gateway: a domain is required")
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

// Run serves the gateway's listener(s) until ctx is cancelled (SIGINT/SIGTERM,
// via signal.NotifyContext in the caller), then performs a graceful shutdown
// bounded by shutdownTimeout. It blocks until every listener has stopped. In
// hosted mode that is two listeners (ACME challenge and TLS); in pair mode,
// where httpSrv is nil because there is no ACME challenge to answer, it is
// the TLS listener alone. If a listener fails on its own (for example the
// configured port is already in use), Run shuts down the other, if any, and
// returns that error.
func (s *Server) Run(ctx context.Context, shutdownTimeout time.Duration) error {
	listeners := 1
	if s.httpSrv != nil {
		listeners = 2
	}
	serveErr := make(chan error, listeners)

	if s.httpSrv != nil {
		go func() {
			s.log.Info("vm gateway: ACME challenge listener starting", "addr", s.cfg.HTTPAddr)
			serveErr <- serveOrNil(s.httpSrv.ListenAndServe())
		}()
	}
	go func() {
		s.log.Info("vm gateway: TLS listener starting", "addr", s.cfg.HTTPSAddr, "mode", s.cfg.Mode, "domain", s.cfg.Domain)
		// TLSConfig on the server already carries either the autocert
		// manager's GetCertificate (hosted mode) or the fixed pair-mode
		// certificate (pair mode), so no cert/key files are passed here.
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
	if s.httpSrv != nil {
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil && runErr == nil {
			runErr = fmt.Errorf("shut down http listener: %w", err)
		}
	}
	if err := s.httpsSrv.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shut down https listener: %w", err)
	}

	// Every goroutine is guaranteed to unblock and send exactly once, each,
	// once Shutdown has been called on their server; drain whichever ones
	// this call hasn't already consumed so none of them leak.
	for ; consumed < listeners; consumed++ {
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
