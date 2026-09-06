package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// This documents why the generic launch-ensure HTTP route needs the same
// remote control boundary as targeted Codex auth operations.
func TestIntakeLaunchReadinessRequestsCodexTokenRefresh(t *testing.T) {
	for _, purpose := range []domain.AgentReadinessPurpose{domain.AgentReadinessPurposeDisplay, domain.AgentReadinessPurposeLaunch} {
		t.Run(string(purpose), func(t *testing.T) {
			var reads atomic.Int32
			var refreshed atomic.Bool
			factory := &fakeCodexAccountFactory{open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
				return &fakeCodexAccountClient{readFn: func(_ context.Context, refresh bool) (ports.CodexAccountObservation, error) {
					reads.Add(1)
					refreshed.Store(refresh)
					email := "private-account@example.test"
					return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}, nil
				}}, nil
			}}
			manager := newTestCodexAccountManager(t, factory, nil)
			record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT})
			manager.active.AccountID = record.Snapshot.ID
			manager.bootstrapped = true
			manager.bootstrapOnce.Do(func() { close(manager.bootstrapDone) })
			adapter := &readinessTestAgent{
				resolve: func(context.Context) (string, error) { return "/private/agent-bin/codex", nil },
				auth: func(context.Context) (ports.AgentAuthStatus, error) {
					t.Error("structured Codex check was bypassed")
					return ports.AgentAuthStatusUnknown, nil
				},
			}
			harnesses := []agentregistry.HarnessAgent{readinessHarness("codex", "Codex", adapter)}
			svc := newService(harnesses, nil, nil, nil)
			svc.codexAccounts = manager
			svc.readiness = newReadinessCoordinator(readinessCoordinatorConfig{Agents: harnesses, AuthenticationCheck: svc.structuredCodexAuthentication})
			// Empty IDs are the public request's select-all form.
			result, err := svc.EnsureReadiness(context.Background(), nil, purpose)
			if err != nil {
				t.Fatal(err)
			}
			if reads.Load() != 1 || refreshed.Load() != (purpose == domain.AgentReadinessPurposeLaunch) {
				t.Fatalf("purpose %s reads=%d refresh=%v", purpose, reads.Load(), refreshed.Load())
			}
			beforeResolve := adapter.resolveCalls.Load()
			cached, err := svc.CachedReadiness(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.List(context.Background()); err != nil {
				t.Fatal(err)
			}
			if reads.Load() != 1 || adapter.resolveCalls.Load() != beforeResolve {
				t.Fatal("cached diagnostics performed native work")
			}
			for _, value := range []Readiness{result, cached} {
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range []string{"/private/", "private-account", record.Snapshot.ID} {
					if strings.Contains(string(raw), forbidden) {
						t.Fatalf("readiness leaked private account detail: %s", raw)
					}
				}
			}
		})
	}
}
