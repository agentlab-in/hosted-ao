package httpd

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	agentservice "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/agentauth"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

const diagnosticCanary = "/private/intake-fixture/credential-secret"

type diagnosticAdapter struct {
	ports.Agent
	mutations atomic.Int32
	probes    atomic.Int32
	resolves  atomic.Int32
}

func (a *diagnosticAdapter) ResolveBinary(context.Context) (string, error) {
	a.resolves.Add(1)
	return diagnosticCanary, nil
}
func (a *diagnosticAdapter) AuthStatus(context.Context) (ports.AgentAuthStatus, error) {
	a.probes.Add(1)
	return ports.AgentAuthStatusUnknown, errors.New(diagnosticCanary)
}
func (a *diagnosticAdapter) GetLaunchCommand(context.Context, ports.LaunchConfig) ([]string, error) {
	a.mutations.Add(1)
	return nil, errors.New("unexpected launch")
}
func (a *diagnosticAdapter) LookPath(string) (string, error) { return diagnosticCanary, nil }
func (a *diagnosticAdapter) OpenCommandTerminal(context.Context, shellterm.OpenCommandTerminalInput) (shellterm.ShellTerminal, error) {
	a.mutations.Add(1)
	return shellterm.ShellTerminal{}, errors.New("unexpected authentication terminal")
}

// Exercise actual routers and services, with only host execution replaced by spies.
func TestIntakeRemoteDiagnosticsRealHandlers(t *testing.T) {
	for _, mode := range []string{"lan", "hosted", "pair"} {
		t.Run(mode, func(t *testing.T) {
			adapter := &diagnosticAdapter{}
			agents := agentservice.NewWithAgents([]agentregistry.HarnessAgent{{Harness: domain.AgentHarness("claude-code"), Manifest: adapters.Manifest{ID: "claude-code", Name: "Claude Code"}, Agent: adapter}})
			router := NewRouterWithControl(config.Config{}, discardLogger(), nil, APIDeps{Agents: agents, AgentAuth: agentauth.NewWithAgentResolver(adapter, agents, adapter)}, ControlDeps{})
			var calls atomic.Int32
			daemon := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode != "lan" && r.Header.Get("Authorization") != "" {
					t.Error("gateway forwarded credential")
				}
				router.ServeHTTP(w, r)
			})
			handler, credential := diagnosticBoundary(t, mode, daemon)
			type routeCase struct {
				method, path, body string
				status             int
				blocked            bool
			}
			routes := []routeCase{
				{"GET", "/api/v1/agents/auth-plans", "", 200, false},
				{"GET", "/api/v1/agents/readiness", "", 200, false},
				{"GET", "/api/v1/agents", "", 200, false},
				{"POST", "/api/v1/agents/readiness/ensure", `{"agentIds":[],"purpose":"launch"}`, 404, true},
				{"POST", "/api/v1/agents/readiness/ensure", `{"purpose":"launch"}`, 404, true},
				{"POST", "/api/v1/agents/readiness/ensure", `{"agentIds":["codex","claude-code"],"purpose":"launch"}`, 404, true},
				{"POST", "/api/v1/agents/readiness/ensure", `{"agentIds":[],"purpose":"display"}`, 404, true},
				{"POST", "/api/v1/agents/readiness/ensure", `{"agentIds":["claude-code"],"purpose":"launch"}`, 404, true},
				{"POST", "/api/v1/agents/claude-code/probe", "", 200, false},
				{"POST", "/api/v1/agents/refresh", "", 200, false},
				{"POST", "/api/v1/agents/auth-plans", "", 405, false},
				{"POST", "/api/v1/agents/claude-code/auth", "", 404, true},
				{"POST", "/api/v1/agents/claude-code/auth/child", "", 404, true},
				{"POST", "/api/v1/agents/claude-code/install", `{"operation":"reinstall"}`, 404, true},
				{"POST", "/api/v1/agents/claude-code/install/reinstall", "", 404, true},
				{"GET", "/api/v1/agents/claude-code/verify/status", "", 404, true},
				{"GET", "/api/v1/agents/installers", "", 404, true},
				{"POST", "/api/v1/agents/install-jobs/job/verify", "", 404, true},
				{"GET", "/api/v1/agents/codex/accounts/events", "", 404, true},
				{"DELETE", "/api/v1/agents/codex/accounts/account", "", 404, true},
				{"GET", "/api/v1/system/install/catalog", "", 404, true},
				{"POST", "/api/v1/system/install/cloudflared", "", 404, true},
				{"OPTIONS", "/api/v1/system/install/jobs/status", "", 404, true},
				{"GET", "/api/v1/system/installer", "", 404, false},
				{"GET", "/api/v1/agents/readiness/ensure-extra", "", 404, false},
				{"GET", "/api/v1/agents/codexapp/accounts", "", 404, false},
				{"POST", "/api/v1/agents/claude-code/authentication", "", 404, false},
				{"GET", "/api/v1/agents/claude-code/verification", "", 404, false},
			}
			for _, prefix := range []string{"/api/v1/agents/codex", "/api/v1/system/install", "/api/v1/agents/readiness/ensure"} {
				for _, suffix := range []string{"", "/", "/jobs/status/verify"} {
					for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
						routes = append(routes, routeCase{method, prefix + suffix, "", 404, true})
					}
				}
			}
			sequence := 0
			for _, route := range routes {
				for _, auth := range []string{"", "invalid", credential} {
					sequence++
					req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
					req.RemoteAddr = fmt.Sprintf("192.0.%d.%d:1234", sequence/250, sequence%250+1)
					req.Header.Set("Content-Type", "application/json")
					if auth != "" {
						req.Header.Set("Authorization", "Bearer "+auth)
					}
					before := calls.Load()
					beforeProbes := adapter.probes.Load()
					beforeResolves := adapter.resolves.Load()
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)
					want := route.status
					reached := int32(1)
					if route.blocked {
						want = 404
						reached = 0
					} else if auth != credential {
						want = 401
						reached = 0
					}
					if rec.Code != want {
						t.Fatalf("%s %s valid=%t: got %d want %d: %s", route.method, route.path, auth == credential, rec.Code, want, rec.Body)
					}
					if calls.Load()-before != reached {
						t.Fatalf("%s %s reached daemon %d times, want %d", route.method, route.path, calls.Load()-before, reached)
					}
					cachedRead := route.method == http.MethodGet && (route.path == "/api/v1/agents/readiness" || route.path == "/api/v1/agents")
					if reached == 0 || cachedRead {
						if adapter.probes.Load() != beforeProbes || adapter.resolves.Load() != beforeResolves {
							t.Fatal("rejected request or cached diagnostic invoked host work")
						}
					}
					if adapter.mutations.Load() != 0 {
						t.Fatal("remote diagnostic invoked launch/authentication")
					}
					for _, secret := range []string{diagnosticCanary, "credential-secret", "workingDir", "terminalHandle"} {
						if strings.Contains(rec.Body.String(), secret) {
							t.Fatalf("%s leaked %s", route.path, secret)
						}
					}
				}
			}
			if adapter.probes.Load() == 0 {
				t.Fatal("allowed readiness routes never exercised the real probe service")
			}
		})
	}
}

func diagnosticBoundary(t *testing.T, mode string, daemon http.Handler) (http.Handler, string) {
	t.Helper()
	if mode == "lan" {
		credential := "Abc12345"
		state := &authState{}
		state.setHash(mobilebridge.HashPassword(credential))
		return lanControlBlock(authMiddleware(state, newLockout(5, time.Minute, time.Now), nil)(daemon)), credential
	}
	server := httptest.NewServer(daemon)
	t.Cleanup(server.Close)
	addr := strings.TrimPrefix(server.URL, "http://")
	if mode == "pair" {
		dir := t.TempDir()
		credential, err := vmgateway.GeneratePasscode(dir)
		if err != nil {
			t.Fatal(err)
		}
		store, err := vmgateway.LoadPasscodeStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		handler, err := vmgateway.NewPairHandler(addr, nil, store, nil, discardLogger())
		if err != nil {
			t.Fatal(err)
		}
		return handler, credential
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	encode := func(value any) string {
		b, e := json.Marshal(value)
		if e != nil {
			t.Fatal(e)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	keys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": "fixture", "x": base64.RawURLEncoding.EncodeToString(public)}}})
	}))
	t.Cleanup(keys.Close)
	token := encode(map[string]string{"alg": "EdDSA", "kid": "fixture"}) + "." + encode(map[string]any{"iss": "https://fixture.invalid", "sub": "fixture-account", "aud": "fixture-machine", "exp": time.Now().Add(time.Hour).Unix()})
	token += "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(token)))
	handler, err := vmgateway.NewHandler(addr, nil, vmgateway.NewJWKSCache(keys.URL, keys.Client()), vmgateway.VerifyOptions{Issuer: "https://fixture.invalid", Subject: "fixture-account", Audience: "fixture-machine", Skew: vmgateway.DefaultSkew}, nil, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	return handler, token
}
