package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/doctor"
)

// The checks themselves are tested in internal/doctor, which owns them. What
// is left here is the `ao doctor` output contract: the text grouping and the
// --json document the desktop machine card reads.

// TestDoctorJSONReportsClaudeAuthRemediation pins the shape the desktop reads:
// a named check in `ao doctor --json` carrying a level and a remediation
// command.
func TestDoctorJSONReportsClaudeAuthRemediation(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitHubEnv(t)
	deps := Deps{
		LookPath:     func(string) (string, error) { return "", errors.New("missing") },
		ProcessAlive: func(int) bool { return false },
	}
	stdout, _, _ := executeCLI(t, deps, "doctor", "--json")

	var report doctorReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("unmarshal doctor --json: %v (output %q)", err, stdout)
	}
	var found *doctor.Check
	for i := range report.Checks {
		if report.Checks[i].Name == "claude-auth" {
			found = &report.Checks[i]
		}
	}
	if found == nil {
		t.Fatalf("no claude-auth check in %+v", report.Checks)
	}
	if found.Level != doctor.Warn || found.Remediation != "ao vm setup-harness claude" {
		t.Fatalf("claude-auth check = %+v, want WARN with the setup-harness remediation", *found)
	}
	if found.Section != doctor.SectionAgents {
		t.Errorf("claude-auth section = %q, want %q", found.Section, doctor.SectionAgents)
	}
}

func TestDoctorJSONOutputIsDecodable(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitHubEnv(t)
	out, errOut, err := executeCLI(t, doctorCLIDeps(), "doctor", "--json")
	if err != nil {
		t.Fatalf("doctor --json failed: %v\nstderr=%s\nstdout=%s", err, errOut, out)
	}
	var got doctorReport
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode doctor json: %v\nout=%s", err, out)
	}
	if !got.OK || len(got.Checks) == 0 {
		t.Fatalf("doctor json = %#v, want ok with checks", got)
	}
	if findDoctorCheck(t, got.Checks, "git").Section != doctor.SectionTools {
		t.Fatalf("git json check missing section: %#v", findDoctorCheck(t, got.Checks, "git"))
	}
}

func TestDoctorTextOutputIsGrouped(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitHubEnv(t)
	out, errOut, err := executeCLI(t, doctorCLIDeps(), "doctor")
	if err != nil {
		t.Fatalf("doctor failed: %v\nstderr=%s\nstdout=%s", err, errOut, out)
	}
	for _, want := range []string{"Core:\nPASS config:", "Tools:\nPASS git:", "Agent harnesses:\nWARN claude-code:", "WARN codex:", "WARN muse:", "GitHub:\nWARN github-token:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

// TestDoctorReportsDaemonState covers the one check the CLI supplies itself:
// internal/doctor cannot know how a caller learns the daemon's state, so
// `ao doctor` injects the same run-file inspection `ao status` reports.
func TestDoctorReportsDaemonState(t *testing.T) {
	setConfigEnv(t)
	clearDoctorGitHubEnv(t)
	c := &commandContext{deps: doctorCLIDeps().withDefaults()}

	check := findDoctorCheck(t, c.runDoctor(context.Background()), "daemon")
	if check.Level != doctor.Pass || check.Message != string(stateStopped) {
		t.Fatalf("daemon check = %+v, want PASS %q", check, stateStopped)
	}
}

func doctorCLIDeps() Deps {
	return Deps{
		LookPath: func(name string) (string, error) {
			switch name {
			case "git":
				return "/bin/git", nil
			case "tmux":
				return "/bin/tmux", nil
			}
			return "", errors.New("missing")
		},
		CommandOutput: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "/bin/tmux" {
				return []byte("tmux 3.3a\n"), nil
			}
			return []byte("git version 2.43.0\n"), nil
		},
		ProcessAlive: func(int) bool { return false },
	}
}

func clearDoctorGitHubEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AO_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
}

func findDoctorCheck(t *testing.T, checks []doctor.Check, name string) doctor.Check {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("doctor check %q not found in %+v", name, checks)
	return doctor.Check{}
}
