// Package config loads the control plane's runtime configuration. The
// control plane is a public-facing service (behind Caddy on the same box)
// that brokers Google OAuth and issues tokens, so unlike the desktop daemon
// it has required secrets: it must fail loudly at boot rather than silently
// running without credentials it needs later.
package config

import (
	"fmt"
	"net"
	"os"
	"time"
)

const (
	// DefaultListenAddr is the address the control plane binds when
	// LISTEN_ADDR is unset. Caddy fronts it in production.
	DefaultListenAddr = "127.0.0.1:8080"
	// DefaultDataDir is the SQLite data directory used when DATA_DIR is
	// unset, relative to the process's working directory. This service
	// manages its own data directory independently of the desktop app's
	// ~/.ao; it must never reference that path.
	DefaultDataDir = "./data"
	// DefaultPublicOrigin is the origin used when PUBLIC_ORIGIN is unset.
	DefaultPublicOrigin = "https://ao.agentlab.in"
	// DefaultAccessTokenTTL is the access token lifetime used when
	// ACCESS_TOKEN_TTL is unset. The spec allows this to move between 10 and
	// 30 minutes after measuring refresh chatter, which is why it is
	// configurable rather than hardcoded in the tokens package.
	DefaultAccessTokenTTL = 15 * time.Minute
)

// Config is the fully resolved control plane configuration. It is immutable
// once built by Load.
type Config struct {
	// ListenAddr is the host:port the HTTP server binds.
	ListenAddr string
	// DataDir is the directory holding the SQLite database.
	DataDir string
	// PublicOrigin is the origin this service is served from. Later batches
	// use it to build the OAuth redirect URI and the `iss` claim on issued
	// access tokens; this skeleton only loads and validates it.
	PublicOrigin string
	// GoogleClientID is the OAuth web client id from the Google Cloud
	// console. Required; Load fails if it is empty.
	GoogleClientID string
	// GoogleClientSecret is the OAuth web client secret. Required; Load
	// fails if it is empty.
	GoogleClientSecret string
	// AccessTokenTTL is the lifetime of issued access tokens.
	AccessTokenTTL time.Duration
}

// Load resolves configuration from the environment, applying defaults for
// optional values and failing loudly for required ones. It returns an error
// if AO_SH_G_CLIENT_ID or AO_SH_G_CLIENT_SECRET is missing, or if LISTEN_ADDR
// is present but malformed, so a misconfigured deployment never starts
// serving traffic it cannot correctly authenticate.
//
// Recognised variables:
//
//	LISTEN_ADDR            bind address                (default 127.0.0.1:8080)
//	DATA_DIR               SQLite data directory       (default ./data)
//	PUBLIC_ORIGIN          public origin for redirect URIs and the iss claim (default https://ao.agentlab.in)
//	ACCESS_TOKEN_TTL       access token lifetime, a Go duration (default 15m)
//	AO_SH_G_CLIENT_ID      Google OAuth client id      (required)
//	AO_SH_G_CLIENT_SECRET  Google OAuth client secret  (required)
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:     DefaultListenAddr,
		DataDir:        DefaultDataDir,
		PublicOrigin:   DefaultPublicOrigin,
		AccessTokenTTL: DefaultAccessTokenTTL,
	}

	if raw := os.Getenv("LISTEN_ADDR"); raw != "" {
		if _, _, err := net.SplitHostPort(raw); err != nil {
			return Config{}, fmt.Errorf("invalid LISTEN_ADDR %q: %w", raw, err)
		}
		cfg.ListenAddr = raw
	}

	if raw := os.Getenv("DATA_DIR"); raw != "" {
		cfg.DataDir = raw
	}

	if raw := os.Getenv("PUBLIC_ORIGIN"); raw != "" {
		cfg.PublicOrigin = raw
	}

	if raw := os.Getenv("ACCESS_TOKEN_TTL"); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid ACCESS_TOKEN_TTL %q: %w", raw, err)
		}
		cfg.AccessTokenTTL = ttl
	}

	cfg.GoogleClientID = os.Getenv("AO_SH_G_CLIENT_ID")
	if cfg.GoogleClientID == "" {
		return Config{}, fmt.Errorf("AO_SH_G_CLIENT_ID is required: set it to the Google OAuth client id (see controlplane/.env.example)")
	}

	cfg.GoogleClientSecret = os.Getenv("AO_SH_G_CLIENT_SECRET")
	if cfg.GoogleClientSecret == "" {
		return Config{}, fmt.Errorf("AO_SH_G_CLIENT_SECRET is required: set it to the Google OAuth client secret (see controlplane/.env.example)")
	}

	return cfg, nil
}
