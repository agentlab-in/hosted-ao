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
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultListenAddr is the address the control plane binds when
	// LISTEN_ADDR is unset. Caddy fronts it in production.
	DefaultListenAddr = "127.0.0.1:8080"
	// DefaultPublicOrigin is the origin used when PUBLIC_ORIGIN is unset.
	DefaultPublicOrigin = "https://ao.agentlab.in"
	// DefaultAccessTokenTTL is the access token lifetime used when
	// ACCESS_TOKEN_TTL is unset. The spec allows this to move between 10 and
	// 30 minutes after measuring refresh chatter, which is why it is
	// configurable rather than hardcoded in the tokens package.
	DefaultAccessTokenTTL = 15 * time.Minute
	// MinAccessTokenTTL and MaxAccessTokenTTL bound ACCESS_TOKEN_TTL to the
	// range the spec allows. Nothing checks an issued access token against a
	// revocation list, so the TTL is the entire revocation window: a typo
	// like 720h would mint month-long unrevocable tokens. Load rejects
	// anything outside these bounds rather than trusting the operator.
	MinAccessTokenTTL = 10 * time.Minute
	MaxAccessTokenTTL = 30 * time.Minute
)

// Config is the fully resolved control plane configuration. It is immutable
// once built by Load.
type Config struct {
	// ListenAddr is the host:port the HTTP server binds.
	ListenAddr string
	// DataDir is the absolute path of the directory holding the SQLite
	// database and the EdDSA signing keys. Load resolves it to an absolute
	// path so it never depends on the process's working directory, which a
	// service manager may not set.
	DataDir string
	// PublicOrigin is the origin this service is served from. Later batches
	// use it to build the OAuth redirect URI and the `iss` claim on issued
	// access tokens; this skeleton only loads and validates it. Load strips
	// any trailing slash, so consumers can concatenate paths onto it and the
	// `iss` claim matches what verifiers pin byte for byte.
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
// if DATA_DIR, AO_SH_G_CLIENT_ID, or AO_SH_G_CLIENT_SECRET is missing, if
// LISTEN_ADDR is present but malformed, or if ACCESS_TOKEN_TTL is outside the
// range the spec allows, so a misconfigured deployment never starts serving
// traffic it cannot correctly authenticate.
//
// DATA_DIR is required rather than defaulted because the EdDSA signing keys
// live inside it. A working-directory-relative default (it used to be ./data)
// silently resolves somewhere else under a service manager that does not set
// WorkingDirectory, where the keys package would find no key files and
// generate a fresh pair whose kid no verifier has cached, rejecting every
// token for up to the JWKS cache lifetime and orphaning the refresh-token
// rows. An explicit path plus the boot log in cmd/controlplane makes that
// visible instead.
//
// Recognised variables:
//
//	LISTEN_ADDR            bind address                (default 127.0.0.1:8080)
//	DATA_DIR               SQLite and signing key directory, absolute path recommended (required)
//	PUBLIC_ORIGIN          public origin for redirect URIs and the iss claim (default https://ao.agentlab.in)
//	ACCESS_TOKEN_TTL       access token lifetime, a Go duration between 10m and 30m (default 15m)
//	AO_SH_G_CLIENT_ID      Google OAuth client id      (required)
//	AO_SH_G_CLIENT_SECRET  Google OAuth client secret  (required)
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:     DefaultListenAddr,
		PublicOrigin:   DefaultPublicOrigin,
		AccessTokenTTL: DefaultAccessTokenTTL,
	}

	if raw := os.Getenv("LISTEN_ADDR"); raw != "" {
		if _, _, err := net.SplitHostPort(raw); err != nil {
			return Config{}, fmt.Errorf("invalid LISTEN_ADDR %q: %w", raw, err)
		}
		cfg.ListenAddr = raw
	}

	rawDataDir := os.Getenv("DATA_DIR")
	if rawDataDir == "" {
		return Config{}, fmt.Errorf("DATA_DIR is required: set it to the directory holding the SQLite database and the EdDSA signing keys, ideally an absolute path (see controlplane/.env.example)")
	}
	dataDir, err := filepath.Abs(rawDataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve DATA_DIR %q: %w", rawDataDir, err)
	}
	cfg.DataDir = dataDir

	if raw := os.Getenv("PUBLIC_ORIGIN"); raw != "" {
		cfg.PublicOrigin = raw
	}
	// Normalize once here so every consumer sees the same string: the OAuth
	// redirect URI must match Google's registered value byte for byte, and
	// the `iss` claim must match what the VM gateway pins, which it compares
	// with !=. A trailing slash in the environment would otherwise reject
	// every token on every VM with nothing in the logs but "token rejected".
	cfg.PublicOrigin = strings.TrimRight(cfg.PublicOrigin, "/")

	if raw := os.Getenv("ACCESS_TOKEN_TTL"); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid ACCESS_TOKEN_TTL %q: %w", raw, err)
		}
		if ttl < MinAccessTokenTTL || ttl > MaxAccessTokenTTL {
			return Config{}, fmt.Errorf("ACCESS_TOKEN_TTL %v is outside the allowed range %v to %v: an access token is not checked against a revocation list, so its lifetime is the whole revocation window", ttl, MinAccessTokenTTL, MaxAccessTokenTTL)
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
