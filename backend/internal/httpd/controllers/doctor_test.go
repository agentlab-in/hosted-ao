package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		// AO_DATA_DIR set to the home directory itself: no trailing separator,
		// so a prefix-only rewrite misses it entirely.
		{"home itself", home, home, "~"},
		{"home itself mid-sentence", "dataDir=" + home + " port=3001", home, "dataDir=~ port=3001"},
		{"home at the end of a sentence", "database not created yet in " + home, home, "database not created yet in ~"},
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

// TestDoctorReportWithholdsGitHubIdentity is the privacy property M-C names:
// this body crosses the network to whoever holds a token for the machine, and
// the GitHub login is the identity this machine acts as while the scope list is
// the exact capability of the credential sitting on it. `ao doctor` locally
// still reports both; only the projection drops them.
func TestDoctorReportWithholdsGitHubIdentity(t *testing.T) {
	report, body := doctorResponse(t, []doctor.Check{{
		Level: doctor.Pass, Section: doctor.SectionGitHub, Name: "github-token",
		Message:       "AO_GITHUB_TOKEN token valid for octocat (scopes: repo, workflow, read:org)",
		PublicMessage: "AO_GITHUB_TOKEN token valid",
	}})

	for _, secret := range []string{"octocat", "read:org", "workflow", "scopes"} {
		if strings.Contains(body, secret) {
			t.Errorf("response leaks %q: %s", secret, body)
		}
	}
	if len(report.Checks) != 1 || report.Checks[0].Message != "AO_GITHUB_TOKEN token valid" {
		t.Fatalf("checks = %+v, want the public message served instead", report.Checks)
	}
	// Still a usable readiness answer: name, level, and "the token is good".
	if report.Checks[0].Name != "github-token" || report.Checks[0].Level != "PASS" {
		t.Errorf("check = %+v, want the check itself intact", report.Checks[0])
	}
}

// TestDoctorReportWithholdsMessagesWhenHomeIsUnknown: redaction is the only
// privacy control on this route, and it cannot run without a home directory to
// rewrite. Failing open silently (which is what discarding the UserHomeDir
// error did) is the failure mode worth designing against, so the messages are
// withheld and say so.
func TestDoctorReportWithholdsMessagesWhenHomeIsUnknown(t *testing.T) {
	unsetHomeEnv(t)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		t.Skipf("home directory still resolves to %q on this platform", home)
	}

	report, body := doctorResponse(t, []doctor.Check{{
		Level: doctor.Pass, Section: doctor.SectionCore, Name: "sqlite",
		Message:     "/home/ubuntu/.ao/ao.db (4096 bytes)",
		Remediation: "ao start",
	}})

	if strings.Contains(body, "/home/ubuntu") {
		t.Fatalf("response leaks an unredactable path: %s", body)
	}
	check := report.Checks[0]
	if !strings.Contains(check.Message, "withheld") {
		t.Errorf("message = %q, want it to say the message was withheld and why", check.Message)
	}
	// The readiness answer itself survives: this route exists to report it.
	if check.Name != "sqlite" || check.Level != "PASS" || check.Remediation != "ao start" {
		t.Errorf("check = %+v, want name, level, and remediation kept", check)
	}
}

// TestDoctorReportIsCachedAcrossABurst: one report costs six subprocesses, a
// temp-file write probe, and an authenticated call to the GitHub API with this
// machine's own token. The desktop machine card polls this on a timer, so
// without the TTL a burst multiplies all of it and burns the operator's own
// GitHub rate limit.
func TestDoctorReportIsCachedAcrossABurst(t *testing.T) {
	var runs int32
	now := time.Now()
	c := &DoctorController{
		Checks: func(context.Context) []doctor.Check {
			atomic.AddInt32(&runs, 1)
			return []doctor.Check{{Level: doctor.Pass, Section: doctor.SectionCore, Name: "daemon", Message: "serving this request"}}
		},
		Now: func() time.Time { return now },
	}
	r := chi.NewRouter()
	c.Register(r)

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/doctor", http.NoBody))
			if rec.Code != http.StatusOK {
				t.Errorf("GET /doctor = %d", rec.Code)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("probe runs = %d, want 1: a burst inside the TTL must run the probes once", got)
	}

	// Past the TTL the machine is probed again, so a fix the user just applied
	// shows up rather than being cached away.
	now = now.Add(reportTTL + time.Second)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/doctor", http.NoBody))
	if got := atomic.LoadInt32(&runs); got != 2 {
		t.Fatalf("probe runs = %d, want 2 once the TTL has passed", got)
	}
}

// TestDoctorReportRunOutlivesTheFirstCaller: every request queued behind the
// mutex is waiting on one run, so the client that happened to trigger it
// hanging up must not cancel the probes the rest of them are getting.
func TestDoctorReportRunOutlivesTheFirstCaller(t *testing.T) {
	c := &DoctorController{Checks: func(ctx context.Context) []doctor.Check {
		level := doctor.Pass
		if ctx.Err() != nil {
			level = doctor.Fail
		}
		return []doctor.Check{{Level: level, Section: doctor.SectionCore, Name: "daemon", Message: "serving this request"}}
	}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checks := c.checks(ctx)
	if len(checks) != 1 || checks[0].Level != doctor.Pass {
		t.Fatalf("checks = %+v, want the run detached from the cancelled request", checks)
	}
}

func unsetHomeEnv(t *testing.T) {
	t.Helper()
	// t.Setenv restores on cleanup; "" is what UserHomeDir treats as unset.
	for _, key := range []string{"HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH"} {
		t.Setenv(key, "")
	}
}
