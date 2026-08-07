package controllers

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/doctor"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// reportTTL is how long one run of the checks answers every request. A report
// costs six subprocesses, a temp-file write probe, and one authenticated call
// to the GitHub API with this machine's own token, so an uncached route lets a
// burst multiply all of it and burn the operator's GitHub rate limit. The
// desktop machine card polls this on a timer, so caching is not optional; the
// window is short enough that a fix the user just applied still shows up on
// their next look.
const reportTTL = 10 * time.Second

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
	// Now is the clock the cache ages against. Nil means time.Now.
	Now func() time.Time

	// ponytail: one mutex around the whole run, so a burst waits on the first
	// caller's probes instead of each starting its own. Move to a
	// single-flight that serves the stale report while a refresh runs if a
	// slow probe ever makes the wait itself the problem.
	mu       sync.Mutex
	cached   []doctor.Check
	cachedAt time.Time
}

// Register mounts the doctor route on the supplied router.
func (c *DoctorController) Register(r chi.Router) {
	r.Get("/doctor", c.report)
}

func (c *DoctorController) report(w http.ResponseWriter, r *http.Request) {
	envelope.WriteJSON(w, http.StatusOK, doctorReport(c.checks(r.Context())))
}

// checks returns the current report, running the probes only when the cached
// one has aged out.
func (c *DoctorController) checks(ctx context.Context) []doctor.Check {
	run := c.Checks
	if run == nil {
		run = runDoctorChecks
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cached != nil && now().Sub(c.cachedAt) < reportTTL {
		return c.cached
	}
	// Detached from this request: every caller queued behind the mutex is
	// waiting on this one run, so the first client hanging up must not cancel
	// the probes the rest of them are getting.
	c.cached = run(context.WithoutCancel(ctx))
	c.cachedAt = now()
	return c.cached
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

// homeUnresolvedMessage replaces every check message when the machine's home
// directory cannot be resolved. See doctorReport.
const homeUnresolvedMessage = "message withheld: this machine's home directory could not be resolved, " +
	"so paths in this message could not be redacted"

// doctorReport projects the checks onto the wire shape, counting failures the
// same way `ao doctor` does.
//
// Two things are dropped on the way out. A check that carries a PublicMessage
// says more locally than a remote caller should be told (the github-token
// check names the GitHub login and the token's scopes), so the remote-safe
// message replaces it. And when the home directory cannot be resolved at all,
// redaction cannot run, so messages are withheld rather than sent unredacted:
// the only privacy control on this route failing open, silently, is the
// failure mode worth designing against.
func doctorReport(checks []doctor.Check) DoctorReportResponse {
	home, homeErr := os.UserHomeDir()
	report := DoctorReportResponse{Checks: make([]DoctorCheckResponse, 0, len(checks))}
	for _, check := range checks {
		if check.Level == doctor.Fail {
			report.Failures++
		}
		message := check.Message
		if check.PublicMessage != "" {
			message = check.PublicMessage
		}
		out := DoctorCheckResponse{
			Level:       string(check.Level),
			Section:     check.Section,
			Name:        check.Name,
			Message:     redactHomePaths(message, home),
			Remediation: redactHomePaths(check.Remediation, home),
		}
		if homeErr != nil || strings.TrimSpace(home) == "" {
			// Level, Name, Section and Remediation stay: they are the machine's
			// readiness, which is what this route exists for, and Remediation is
			// a fixed command. Only the free text, which is where paths live, is
			// held back.
			out.Message = homeUnresolvedMessage
		}
		report.Checks = append(report.Checks, out)
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
// It rewrites the home directory as a prefix and as a whole path (a data dir
// set to home itself), but never a name that merely starts with the same
// letters, so a sibling account like /home/ubuntu2 is left alone. A data dir
// moved outside home with AO_DATA_DIR is reported in full; at that point the
// path is the finding.
//
// An empty home means redaction could not run. Callers must treat that as a
// reason to withhold the message, not to send it: this function cannot, since
// it has nothing to rewrite with.
func redactHomePaths(message, home string) string {
	sep := string(filepath.Separator)
	home = strings.TrimSuffix(home, sep)
	if home == "" || home == sep {
		return message
	}

	var b strings.Builder
	rest := message
	for {
		i := strings.Index(rest, home)
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i])
		rest = rest[i+len(home):]
		if rest == "" || !continuesName(rest[0]) {
			b.WriteString("~")
		} else {
			b.WriteString(home)
		}
	}
}

// continuesName reports whether c could be the next character of the same
// directory name. It is what separates /home/ubuntu/.ao and "/home/ubuntu (4
// bytes)", both of which name the home directory, from /home/ubuntu2, which
// names a different account.
func continuesName(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return c == '-' || c == '_' || c == '.'
}
