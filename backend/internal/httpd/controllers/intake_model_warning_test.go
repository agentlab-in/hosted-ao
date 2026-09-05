package controllers_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/modelcatalog"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
)

func TestIntakeModelDiagnosticsHideRealConfigPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("QWEN_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"private-test-token":`), 0600); err != nil {
		t.Fatal(err)
	}
	svc := agentsvc.NewWithDeps(agentsvc.Deps{Discoverer: modelcatalog.Discoverer{}})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Agents: svc}, httpd.ControlDeps{}))
	defer srv.Close()
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/agents/qwen/models"},
		{http.MethodPost, "/api/v1/agents/qwen/models/refresh"},
		{http.MethodPost, "/api/v1/agents/qwen/models/refresh?revalidate=true"},
	} {
		body, status, _ := doRequest(t, srv, route.method, route.path, "")
		if status != http.StatusOK {
			t.Fatalf("%s %s: %d %s", route.method, route.path, status, body)
		}
		for _, forbidden := range []string{root, "settings.json", "private-test-token"} {
			if strings.Contains(string(body), forbidden) {
				t.Fatalf("model diagnostics exposed %q: %s", forbidden, body)
			}
		}
		var response ports.AgentModelCatalog
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		if response.Warning == "" || !response.Stale || !response.RefreshRecommended {
			t.Fatalf("lost diagnostic state: %#v", response)
		}
	}
}

func TestIntakeModelDiagnosticsHidePersistedWarning(t *testing.T) {
	for _, warning := range []string{"", "read /private/old-home/config.yaml: private-test-token", "Authorization: Bearer fixture-secret-token", "provider response: private-account@example.test", "command stderr: /Users/private-user/config", "line one\npassword=fixture-only\nline three"} {
		catalog := &fakeAgentCatalog{models: ports.AgentModelCatalog{AgentID: "qwen", Warning: warning}}
		log := slog.New(slog.NewTextHandler(io.Discard, nil))
		srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Agents: catalog}, httpd.ControlDeps{}))
		body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/qwen/models", "")
		srv.Close()
		if status != http.StatusOK {
			t.Fatalf("cached models: %d %s", status, body)
		}
		if strings.Contains(string(body), "/private/") || strings.Contains(string(body), "private-test-token") {
			t.Fatalf("cached warning leaked: %s", body)
		}
		var response ports.AgentModelCatalog
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		expected := ""
		if warning != "" {
			expected = "Some model catalog information is unavailable."
		}
		if response.Warning != expected {
			t.Fatalf("warning=%q, want fixed safe shape %q", response.Warning, expected)
		}
		if catalog.models.Warning != warning {
			t.Fatal("serialization mutated cached diagnostics")
		}
	}
}
