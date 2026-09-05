package agentauth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type intakePlanResolver struct{ fail bool }

func (r intakePlanResolver) ResolveAgentBinary(context.Context, string) (string, error) {
	if r.fail {
		return "", errors.New("private-token at /private/credential-home/agent")
	}
	return "/private/credential-home/agent", nil
}

func TestIntakePlansDoNotLaunchOrExposeResolverDetails(t *testing.T) {
	for _, fail := range []bool{false, true} {
		opener := &recordingTerminalOpener{}
		svc := NewWithAgentResolver(nil, intakePlanResolver{fail: fail}, opener)
		result := svc.Plans(context.Background())
		if len(result) != len(plans) {
			t.Fatalf("got %d plans, want %d", len(result), len(plans))
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"/private/", "private-token", "terminalInput"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("plans exposed resolver details: %s", raw)
			}
		}
		if opener.calls != 0 {
			t.Fatalf("Plans opened %d terminals", opener.calls)
		}
		for i, resultPlan := range result {
			if resultPlan.DisplayCommand != plans[i].DisplayCommand || resultPlan.Guidance != plans[i].Guidance || resultPlan.DocumentationURL != plans[i].DocumentationURL {
				t.Fatalf("plan %s description changed during discovery", resultPlan.AgentID)
			}
		}
	}
}
