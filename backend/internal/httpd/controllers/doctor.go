package controllers

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/doctor"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// DoctorController owns GET /api/v1/doctor: the health checks `ao doctor`
// runs, served so the desktop can read a remote machine's readiness. It sits
// under /api/v1 because that is what the VM gateway proxies (see
// isProxyablePath in internal/vmgateway); a doctor route anywhere else would
// be unreachable from the app.
//
// The route carries no auth of its own, like every other daemon route: the
// daemon is loopback-bound, and the gateway verifies an AO token on every
// request before proxying (ADR 0002). A second token check here would
// duplicate the gateway and diverge from it.
type DoctorController struct {
	// Checks runs the health checks. Nil runs the production set; tests
	// inject fakes rather than spawning real probes.
	Checks func(ctx context.Context) []doctor.Check
}

// Register mounts the doctor route on the supplied router.
func (c *DoctorController) Register(r chi.Router) {
	r.Get("/doctor", c.report)
}

func (c *DoctorController) report(w http.ResponseWriter, r *http.Request) {
	run := c.Checks
	if run == nil {
		run = runDoctorChecks
	}
	envelope.WriteJSON(w, http.StatusOK, doctorReport(run(r.Context())))
}

// runDoctorChecks runs the production check set. The daemon answers the daemon
// check itself: it is the process serving this request, so the CLI's run-file
// and health-probe inspection would be a roundabout way of asking whether it
// is awake.
func runDoctorChecks(ctx context.Context) []doctor.Check {
	deps := doctor.DefaultDeps()
	deps.DaemonCheck = func(context.Context) doctor.Check {
		return doctor.Check{
			Level: doctor.Pass, Section: doctor.SectionCore, Name: "daemon",
			Message: "serving this request",
		}
	}
	return doctor.Run(ctx, deps)
}

// doctorReport projects the checks onto the wire shape, counting failures the
// same way `ao doctor` does.
func doctorReport(checks []doctor.Check) DoctorReportResponse {
	home, _ := os.UserHomeDir()
	report := DoctorReportResponse{Checks: make([]DoctorCheckResponse, 0, len(checks))}
	for _, check := range checks {
		if check.Level == doctor.Fail {
			report.Failures++
		}
		report.Checks = append(report.Checks, DoctorCheckResponse{
			Level:       string(check.Level),
			Section:     check.Section,
			Name:        check.Name,
			Message:     redactHomePaths(check.Message, home),
			Remediation: redactHomePaths(check.Remediation, home),
		})
	}
	report.OK = report.Failures == 0
	return report
}

// redactHomePaths rewrites paths under the machine's home directory to "~", so
// a report that crosses the network does not hand out the home layout: the
// account name, and the directory names under it. Paths outside home (which
// git a machine resolves, where the ao binary lives) stay whole, because there
// the path is the diagnostic and it is not private.
//
// It only rewrites home as a directory prefix, never a bare mention of it, so
// a sibling account whose name merely starts with the same letters is not
// mangled. A data dir moved outside home with AO_DATA_DIR is reported in full;
// at that point the path is the finding.
func redactHomePaths(message, home string) string {
	sep := string(filepath.Separator)
	home = strings.TrimSuffix(home, sep)
	if home == "" || home == sep {
		return message
	}
	return strings.ReplaceAll(message, home+sep, "~"+sep)
}
