package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/doctor"
)

func doctorResponse(t *testing.T, checks []doctor.Check) (DoctorReportResponse, string) {
	t.Helper()
	r := chi.NewRouter()
	(&DoctorController{Checks: func(context.Context) []doctor.Check { return checks }}).Register(r)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/doctor", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /doctor = %d, body=%s", rec.Code, rec.Body.String())
	}
	var report DoctorReportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor report: %v (body %s)", err, rec.Body.String())
	}
	return report, rec.Body.String()
}

// TestDoctorReportCarriesRemediation pins what the desktop machine card reads:
// a named check, its level, and the exact command that fixes it, all intact
// across the HTTP boundary.
func TestDoctorReportCarriesRemediation(t *testing.T) {
	report, body := doctorResponse(t, []doctor.Check{
		{Level: doctor.Pass, Section: doctor.SectionTools, Name: "git", Message: "/usr/bin/git (version 2.43.0; supports worktrees)"},
		{
			Level: doctor.Warn, Section: doctor.SectionAgents, Name: "claude-auth",
			Message:     "claude not found in PATH; install the Claude Code CLI, then run `ao vm setup-harness claude`",
			Remediation: "ao vm setup-harness claude",
		},
	})

	if !report.OK || report.Failures != 0 {
		t.Fatalf("report = %+v, want ok with no failures (a WARN is not a failure)", report)
	}
	if len(report.Checks) != 2 {
		t.Fatalf("checks = %+v, want 2", report.Checks)
	}
	auth := report.Checks[1]
	if auth.Name != "claude-auth" || auth.Level != "WARN" || auth.Section != doctor.SectionAgents {
		t.Errorf("claude-auth check = %+v", auth)
	}
	if auth.Remediation != "ao vm setup-harness claude" {
		t.Errorf("remediation = %q, want the setup-harness command", auth.Remediation)
	}
	// A path outside the home directory is the diagnostic itself, so it stays.
	if !strings.Contains(body, "/usr/bin/git") {
		t.Errorf("body dropped the resolved git path: %s", body)
	}
}

// TestDoctorReportCountsFailures asserts a broken machine is still a readable
// report: the failure count is the caller's signal, not an HTTP error.
func TestDoctorReportCountsFailures(t *testing.T) {
	report, _ := doctorResponse(t, []doctor.Check{
		{Level: doctor.Fail, Section: doctor.SectionTools, Name: "git", Message: "not found in PATH"},
		{Level: doctor.Warn, Section: doctor.SectionTools, Name: "tmux", Message: "not found in PATH"},
		{Level: doctor.Pass, Section: doctor.SectionCore, Name: "daemon", Message: "serving this request"},
	})
	if report.OK || report.Failures != 1 {
		t.Fatalf("report = %+v, want not-ok with 1 failure", report)
	}
}

// TestDoctorReportHidesHomeLayout is the privacy property of this route: the
// body crosses the network to a desktop client, so it must not hand out the
// machine's home directory layout (the account name, the directories under
// it). The check messages are full of such paths locally.
func TestDoctorReportHidesHomeLayout(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory on this machine")
	}
	dataDir := filepath.Join(home, ".ao")
	_, body := doctorResponse(t, []doctor.Check{
		{Level: doctor.Pass, Section: doctor.SectionCore, Name: "config", Message: "runFile=" + filepath.Join(dataDir, "running.json") + " dataDir=" + dataDir + " port=3001"},
		{Level: doctor.Pass, Section: doctor.SectionCore, Name: "sqlite", Message: filepath.Join(dataDir, "ao.db") + " (4096 bytes)"},
	})

	if strings.Contains(body, home) {
		t.Fatalf("response leaks the home directory %q: %s", home, body)
	}
	if !strings.Contains(body, "~") {
		t.Fatalf("response dropped the redacted paths entirely: %s", body)
	}
}

func TestRedactHomePaths(t *testing.T) {
	sep := string(filepath.Separator)
	home := filepath.Join(sep+"home", "ubuntu")
	cases := []struct {
		name    string
		message string
		home    string
		want    string
	}{
		{"path under home", filepath.Join(home, ".ao", "ao.db"), home, "~" + sep + filepath.Join(".ao", "ao.db")},
		{"trailing separator on home", filepath.Join(home, ".ao"), home + sep, "~" + sep + ".ao"},
		{"path outside home", filepath.Join(sep+"usr", "bin", "git"), home, filepath.Join(sep+"usr", "bin", "git")},
		{"sibling account is not mangled", filepath.Join(sep+"home", "ubuntu2", "x"), home, filepath.Join(sep+"home", "ubuntu2", "x")},
		{"unknown home leaves the message alone", filepath.Join(home, ".ao"), "", filepath.Join(home, ".ao")},
		{"root home leaves the message alone", filepath.Join(sep+"etc", "ao"), sep, filepath.Join(sep+"etc", "ao")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactHomePaths(tc.message, tc.home); got != tc.want {
				t.Errorf("redactHomePaths(%q, %q) = %q, want %q", tc.message, tc.home, got, tc.want)
			}
		})
	}
}
