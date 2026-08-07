package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setRequired sets every variable Load requires, so each test only has to
// supply the one it is exercising.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("AO_SH_G_CLIENT_ID", "client-id")
	t.Setenv("AO_SH_G_CLIENT_SECRET", "client-secret")
	t.Setenv("DATA_DIR", t.TempDir())
}

func TestLoad_MissingGoogleCredentials(t *testing.T) {
	tests := []struct {
		name       string
		clientID   string
		secret     string
		wantErrHas string
	}{
		{
			name:       "both unset",
			clientID:   "",
			secret:     "",
			wantErrHas: "AO_SH_G_CLIENT_ID",
		},
		{
			name:       "client id unset",
			clientID:   "",
			secret:     "secret",
			wantErrHas: "AO_SH_G_CLIENT_ID",
		},
		{
			name:       "client secret unset",
			clientID:   "client-id",
			secret:     "",
			wantErrHas: "AO_SH_G_CLIENT_SECRET",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATA_DIR", t.TempDir())
			t.Setenv("AO_SH_G_CLIENT_ID", tt.clientID)
			t.Setenv("AO_SH_G_CLIENT_SECRET", tt.secret)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() = nil error, want error containing %q", tt.wantErrHas)
			}
			if !strings.Contains(err.Error(), tt.wantErrHas) {
				t.Fatalf("Load() error = %q, want it to mention %q", err.Error(), tt.wantErrHas)
			}
		})
	}
}

func TestLoad_MissingDataDir(t *testing.T) {
	t.Setenv("AO_SH_G_CLIENT_ID", "client-id")
	t.Setenv("AO_SH_G_CLIENT_SECRET", "client-secret")
	t.Setenv("DATA_DIR", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() with DATA_DIR unset = nil error, want error")
	}
	if !strings.Contains(err.Error(), "DATA_DIR") {
		t.Fatalf("Load() error = %q, want it to mention DATA_DIR", err.Error())
	}
}

// A relative DATA_DIR must not stay relative: the EdDSA signing keys live
// inside it, and a service manager that does not set WorkingDirectory would
// otherwise resolve it somewhere else and silently generate a fresh key pair.
func TestLoad_DataDirResolvedToAbsolutePath(t *testing.T) {
	t.Setenv("AO_SH_G_CLIENT_ID", "client-id")
	t.Setenv("AO_SH_G_CLIENT_SECRET", "client-secret")
	t.Setenv("DATA_DIR", "./data")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("DataDir = %q, want an absolute path", cfg.DataDir)
	}
	if filepath.Base(cfg.DataDir) != "data" {
		t.Errorf("DataDir = %q, want it to end in %q", cfg.DataDir, "data")
	}
}

func TestLoad_DefaultsAppliedWhenCredentialsSet(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("ListenAddr = %q, want default %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if cfg.PublicOrigin != DefaultPublicOrigin {
		t.Errorf("PublicOrigin = %q, want default %q", cfg.PublicOrigin, DefaultPublicOrigin)
	}
	if cfg.AccessTokenTTL != DefaultAccessTokenTTL {
		t.Errorf("AccessTokenTTL = %v, want default %v", cfg.AccessTokenTTL, DefaultAccessTokenTTL)
	}
	if cfg.GoogleClientID != "client-id" {
		t.Errorf("GoogleClientID = %q, want %q", cfg.GoogleClientID, "client-id")
	}
	if cfg.GoogleClientSecret != "client-secret" {
		t.Errorf("GoogleClientSecret = %q, want %q", cfg.GoogleClientSecret, "client-secret")
	}
}

func TestLoad_ListenAddrOverrideHonored(t *testing.T) {
	setRequired(t)
	t.Setenv("LISTEN_ADDR", "0.0.0.0:9090")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.ListenAddr != "0.0.0.0:9090" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, "0.0.0.0:9090")
	}
}

func TestLoad_InvalidListenAddrRejected(t *testing.T) {
	tests := []struct {
		name string
		addr string
	}{
		{name: "no port", addr: "127.0.0.1"},
		{name: "not an address", addr: "not-an-address"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("LISTEN_ADDR", tt.addr)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() with LISTEN_ADDR=%q = nil error, want error", tt.addr)
			}
			if !strings.Contains(err.Error(), "LISTEN_ADDR") {
				t.Fatalf("Load() error = %q, want it to mention LISTEN_ADDR", err.Error())
			}
		})
	}
}

func TestLoad_DataDirAndPublicOriginOverrideHonored(t *testing.T) {
	setRequired(t)
	t.Setenv("DATA_DIR", "/tmp/cp-data")
	t.Setenv("PUBLIC_ORIGIN", "https://example.test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.DataDir != "/tmp/cp-data" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/tmp/cp-data")
	}
	if cfg.PublicOrigin != "https://example.test" {
		t.Errorf("PublicOrigin = %q, want %q", cfg.PublicOrigin, "https://example.test")
	}
}

// A trailing slash on PUBLIC_ORIGIN must not survive into the config: it ends
// up in the `iss` claim, which the VM gateway pins and compares with !=, so
// one extra byte would reject every token on every VM.
func TestLoad_PublicOriginTrailingSlashesStripped(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "one slash", raw: "https://ao.agentlab.in/", want: "https://ao.agentlab.in"},
		{name: "several slashes", raw: "https://ao.agentlab.in///", want: "https://ao.agentlab.in"},
		{name: "already clean", raw: "https://ao.agentlab.in", want: "https://ao.agentlab.in"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("PUBLIC_ORIGIN", tt.raw)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if cfg.PublicOrigin != tt.want {
				t.Errorf("PublicOrigin = %q, want %q", cfg.PublicOrigin, tt.want)
			}
		})
	}
}

func TestLoad_AccessTokenTTLOverrideHonored(t *testing.T) {
	for _, raw := range []string{"10m", "15m", "30m"} {
		t.Run(raw, func(t *testing.T) {
			setRequired(t)
			t.Setenv("ACCESS_TOKEN_TTL", raw)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			want, err := time.ParseDuration(raw)
			if err != nil {
				t.Fatalf("ParseDuration(%q): %v", raw, err)
			}
			if cfg.AccessTokenTTL != want {
				t.Errorf("AccessTokenTTL = %v, want %v", cfg.AccessTokenTTL, want)
			}
		})
	}
}

func TestLoad_InvalidAccessTokenTTLRejected(t *testing.T) {
	setRequired(t)
	t.Setenv("ACCESS_TOKEN_TTL", "not-a-duration")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() = nil error, want error")
	}
	if !strings.Contains(err.Error(), "ACCESS_TOKEN_TTL") {
		t.Fatalf("Load() error = %q, want it to mention ACCESS_TOKEN_TTL", err.Error())
	}
}

// An access token is never checked against a revocation list, so its TTL is
// the whole revocation window. Anything outside the range the spec allows has
// to fail at boot rather than quietly minting long-lived tokens.
func TestLoad_OutOfRangeAccessTokenTTLRejected(t *testing.T) {
	tests := []struct {
		name string
		ttl  string
	}{
		{name: "well below the minimum", ttl: "30s"},
		{name: "just below the minimum", ttl: "9m59s"},
		{name: "just above the maximum", ttl: "30m1s"},
		{name: "fat fingered hours for minutes", ttl: "720h"},
		{name: "zero", ttl: "0s"},
		{name: "negative", ttl: "-15m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("ACCESS_TOKEN_TTL", tt.ttl)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() with ACCESS_TOKEN_TTL=%q = nil error, want error", tt.ttl)
			}
			if !strings.Contains(err.Error(), "ACCESS_TOKEN_TTL") {
				t.Fatalf("Load() error = %q, want it to mention ACCESS_TOKEN_TTL", err.Error())
			}
		})
	}
}
