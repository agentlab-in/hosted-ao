package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/doctor"
)

// doctorReport is the `ao doctor --json` document. The checks themselves come
// from internal/doctor, which the daemon's GET /api/v1/doctor also calls; only
// the framing (ok/failures counts, text grouping) is the CLI's.
type doctorReport struct {
	OK       bool           `json:"ok"`
	Failures int            `json:"failures"`
	Checks   []doctor.Check `json:"checks"`
}

func newDoctorCommand(ctx *commandContext) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run local AO health checks",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			checks := ctx.runDoctor(cmd.Context())
			failures := 0
			for _, check := range checks {
				if check.Level == doctor.Fail {
					failures++
				}
			}

			if asJSON {
				if err := writeJSON(cmd.OutOrStdout(), doctorReport{
					OK: failures == 0, Failures: failures, Checks: checks,
				}); err != nil {
					return err
				}
			} else {
				if err := writeDoctorText(cmd, checks); err != nil {
					return err
				}
			}

			if failures > 0 {
				return fmt.Errorf("doctor found %d failing check(s)", failures)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output health checks as JSON")
	return cmd
}

func writeDoctorText(cmd *cobra.Command, checks []doctor.Check) error {
	var lastSection string
	for _, check := range checks {
		if check.Section != "" && check.Section != lastSection {
			if lastSection != "" {
				if _, err := fmt.Fprintln(cmd.OutOrStdout()); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s:\n", check.Section); err != nil {
				return err
			}
			lastSection = check.Section
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", check.Level, check.Name, check.Message); err != nil {
			return err
		}
	}
	return nil
}

func (c *commandContext) runDoctor(ctx context.Context) []doctor.Check {
	return doctor.Run(ctx, c.doctorDeps())
}

// doctorDeps hands the shared check runner the CLI's own injectable side
// effects, so `ao doctor` stays testable through Deps and the checks stay in
// one place.
func (c *commandContext) doctorDeps() doctor.Deps {
	return doctor.Deps{
		LookPath:       c.deps.LookPath,
		CommandOutput:  c.deps.CommandOutput,
		Executable:     c.deps.Executable,
		HTTPClient:     c.deps.HTTPClient,
		GitHubRESTBase: c.deps.DoctorGitHubRESTBase,
		DaemonCheck:    c.doctorDaemonCheck,
	}
}

// doctorDaemonCheck is the CLI's answer to "is the daemon up": the same
// run-file plus health-probe inspection `ao status` reports. The daemon's own
// HTTP route answers this differently, which is why the check is injected
// rather than living in internal/doctor.
func (c *commandContext) doctorDaemonCheck(ctx context.Context) doctor.Check {
	st, err := c.inspectDaemon(ctx)
	if err != nil {
		return doctor.Check{Level: doctor.Fail, Section: doctor.SectionCore, Name: "daemon", Message: err.Error()}
	}
	level := doctor.Pass
	switch st.State {
	case stateStale, stateNotReady:
		level = doctor.Warn
	case stateUnhealthy:
		level = doctor.Fail
	}
	msg := string(st.State)
	if st.PID != 0 {
		msg = fmt.Sprintf("%s pid=%d port=%d", msg, st.PID, st.Port)
	}
	if st.Error != "" {
		msg += " (" + st.Error + ")"
	}
	return doctor.Check{Level: level, Section: doctor.SectionCore, Name: "daemon", Message: msg}
}

// firstOutputLine is the shared version-probe formatter, kept under its
// original CLI name for the VM preflight that also uses it.
func firstOutputLine(out []byte) string { return doctor.FirstOutputLine(out) }
