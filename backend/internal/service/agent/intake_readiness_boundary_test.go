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
			factory := &fakeCodexAccountFactory{capabilities: supportedCodexAccountCapabilities(), open: func(ports.CodexAccountContext) (ports.CodexAccountClient, error) {
				return &fakeCodexAccountClient{readFn: func(_ context.Context, refresh bool) (ports.CodexAccountObservation, error) {
					reads.Add(1)
					refreshed.Store(refresh)
					email := "private-account@example.test"
					return ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT, Email: &email}, nil
				}, capacityFn: func(context.Context) (ports.CodexCapacityObservation, error) {
					return ports.CodexCapacityObservation{}, nil
				}}, nil
			}}
			manager := newTestCodexAccountManager(t, factory, nil)
			record := commitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.CodexAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.CodexAuthMethodChatGPT})
			manager.mu.Lock()
			manager.deviceAccountID = record.Snapshot.ID
			manager.deviceCredentialPresent = true
			manager.accountStoreReady = true
			manager.reconciliation = domain.CodexDeviceReconciliation{Status: domain.CodexDeviceReconciliationVerified, ActiveAccountVerified: true, ReasonCode: "verified"}
			manager.mu.Unlock()
			nativeAuthCalls := &atomic.Int32{}
			adapter := &readinessTestAgent{
				resolve: func(context.Context) (string, error) { return "/private/agent-bin/codex", nil },
				auth: func(context.Context) (ports.AgentAuthStatus, error) {
					nativeAuthCalls.Add(1)
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
			// account/read refresh is never authentication evidence: display
			// readiness answers from the structured account store, and launch
			// readiness falls back to the native probe after the protected
			// capacity call rather than trusting a credential refresh.
			if purpose == domain.AgentReadinessPurposeDisplay {
				if nativeAuthCalls.Load() != 0 {
					t.Fatal("display readiness bypassed the structured Codex check")
				}
			} else if nativeAuthCalls.Load() == 0 {
				t.Fatal("launch readiness did not fall back to the native probe")
			}
			if refreshed.Load() {
				t.Fatalf("purpose %s used account/read refresh as authentication evidence", purpose)
			}
			if reads.Load() < 1 {
				t.Fatalf("purpose %s performed no structured account read", purpose)
			}
			beforeResolve := adapter.resolveCalls.Load()
			readsBeforeCached := reads.Load()
			cached, err := svc.CachedReadiness(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.List(context.Background()); err != nil {
				t.Fatal(err)
			}
			if reads.Load() != readsBeforeCached || adapter.resolveCalls.Load() != beforeResolve {
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
