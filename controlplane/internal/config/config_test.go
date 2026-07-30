package config

import (
	"strings"
	"testing"
)

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

func TestLoad_DefaultsAppliedWhenCredentialsSet(t *testing.T) {
	t.Setenv("AO_SH_G_CLIENT_ID", "client-id")
	t.Setenv("AO_SH_G_CLIENT_SECRET", "client-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("ListenAddr = %q, want default %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if cfg.DataDir != DefaultDataDir {
		t.Errorf("DataDir = %q, want default %q", cfg.DataDir, DefaultDataDir)
	}
	if cfg.PublicOrigin != DefaultPublicOrigin {
		t.Errorf("PublicOrigin = %q, want default %q", cfg.PublicOrigin, DefaultPublicOrigin)
	}
	if cfg.GoogleClientID != "client-id" {
		t.Errorf("GoogleClientID = %q, want %q", cfg.GoogleClientID, "client-id")
	}
	if cfg.GoogleClientSecret != "client-secret" {
		t.Errorf("GoogleClientSecret = %q, want %q", cfg.GoogleClientSecret, "client-secret")
	}
}

func TestLoad_ListenAddrOverrideHonored(t *testing.T) {
	t.Setenv("AO_SH_G_CLIENT_ID", "client-id")
	t.Setenv("AO_SH_G_CLIENT_SECRET", "client-secret")
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
			t.Setenv("AO_SH_G_CLIENT_ID", "client-id")
			t.Setenv("AO_SH_G_CLIENT_SECRET", "client-secret")
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
	t.Setenv("AO_SH_G_CLIENT_ID", "client-id")
	t.Setenv("AO_SH_G_CLIENT_SECRET", "client-secret")
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
