package controllers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/termtheme"
)

func TestTerminalThemeRouteWritesDaemonDataDirForPTYEnvironment(t *testing.T) {
	dataDir := t.TempDir()
	router := chi.NewRouter()
	controller := &SettingsController{DataDir: dataDir}
	controller.Register(router)

	req := httptest.NewRequest(http.MethodPatch, "/settings/terminal-theme", strings.NewReader(`{"scheme":"light"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", res.Code, res.Body.String())
	}

	env := map[string]string{}
	termtheme.Apply(env, dataDir)
	if env[termtheme.EnvTheme] != "light" || env[termtheme.EnvColorFgBg] != "0;15" {
		t.Fatalf("PTY environment = %#v, want light terminal hints", env)
	}
}

func TestTerminalThemeRouteRejectsInvalidSchemeWithoutWriting(t *testing.T) {
	dataDir := t.TempDir()
	router := chi.NewRouter()
	controller := &SettingsController{DataDir: dataDir}
	controller.Register(router)

	req := httptest.NewRequest(http.MethodPatch, "/settings/terminal-theme", strings.NewReader(`{"scheme":"system"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", res.Code, res.Body.String())
	}
	if _, ok := termtheme.Read(dataDir); ok {
		t.Fatal("invalid theme was persisted")
	}
}
