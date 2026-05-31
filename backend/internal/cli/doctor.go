package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type doctorLevel string

const (
	doctorPass doctorLevel = "PASS"
	doctorWarn doctorLevel = "WARN"
	doctorFail doctorLevel = "FAIL"
)

type doctorCheck struct {
	Level   doctorLevel
	Name    string
	Message string
}

func newDoctorCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run local AO health checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			checks := ctx.runDoctor(cmd.Context())
			for _, check := range checks {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", check.Level, check.Name, check.Message); err != nil {
					return err
				}
			}
			var failures int
			for _, check := range checks {
				if check.Level == doctorFail {
					failures++
				}
			}
			if failures > 0 {
				return fmt.Errorf("doctor found %d failing check(s)", failures)
			}
			return nil
		},
	}
}

func (c *commandContext) runDoctor(ctx context.Context) []doctorCheck {
	checks := []doctorCheck{}

	cfg, err := config.Load()
	if err != nil {
		return append(checks, doctorCheck{Level: doctorFail, Name: "config", Message: err.Error()})
	}
	checks = append(checks, doctorCheck{
		Level: doctorPass, Name: "config",
		Message: fmt.Sprintf("runFile=%s dataDir=%s port=%d", cfg.RunFilePath, cfg.DataDir, cfg.Port),
	})

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		checks = append(checks, doctorCheck{Level: doctorFail, Name: "data-dir", Message: err.Error()})
	} else {
		checks = append(checks, doctorCheck{Level: doctorPass, Name: "data-dir", Message: cfg.DataDir})
	}

	store, err := sqlite.Open(cfg.DataDir)
	if err != nil {
		checks = append(checks, doctorCheck{Level: doctorFail, Name: "sqlite", Message: err.Error()})
	} else {
		_ = store.Close()
		checks = append(checks, doctorCheck{Level: doctorPass, Name: "sqlite", Message: "opened database and applied migrations"})
	}

	st, err := c.inspectDaemon(ctx)
	if err != nil {
		checks = append(checks, doctorCheck{Level: doctorFail, Name: "daemon", Message: err.Error()})
	} else {
		level := doctorPass
		switch st.State {
		case "stale", "not_ready":
			level = doctorWarn
		case "unhealthy":
			level = doctorFail
		}
		msg := st.State
		if st.PID != 0 {
			msg = fmt.Sprintf("%s pid=%d port=%d", msg, st.PID, st.Port)
		}
		if st.Error != "" {
			msg += " (" + st.Error + ")"
		}
		checks = append(checks, doctorCheck{Level: level, Name: "daemon", Message: msg})
	}

	checks = append(checks, c.checkTool("git", true))
	checks = append(checks, c.checkTool("tmux", false))
	checks = append(checks, c.checkTool("zellij", false))
	return checks
}

func (c *commandContext) checkTool(name string, required bool) doctorCheck {
	path, err := c.deps.LookPath(name)
	if err == nil {
		return doctorCheck{Level: doctorPass, Name: name, Message: path}
	}
	if required {
		return doctorCheck{Level: doctorFail, Name: name, Message: "not found in PATH"}
	}
	return doctorCheck{Level: doctorWarn, Name: name, Message: "not found in PATH"}
}
