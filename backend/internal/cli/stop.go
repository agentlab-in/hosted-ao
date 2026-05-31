package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

const defaultStopTimeout = 10 * time.Second

type stopOptions struct {
	timeout time.Duration
	json    bool
}

func newStopCommand(ctx *commandContext) *cobra.Command {
	opts := stopOptions{timeout: defaultStopTimeout}
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the AO daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := ctx.stopDaemon(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), st)
			}
			if st.State == "stopped" {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "AO daemon stopped")
				return err
			}
			return writeStatus(cmd, st)
		},
	}
	cmd.Flags().DurationVar(&opts.timeout, "timeout", defaultStopTimeout, "How long to wait for daemon shutdown")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output stop result as JSON")
	return cmd
}

func (c *commandContext) stopDaemon(ctx context.Context, opts stopOptions) (daemonStatus, error) {
	cfg, err := config.Load()
	if err != nil {
		return daemonStatus{}, err
	}
	st, err := c.inspectDaemon(ctx)
	if err != nil {
		return daemonStatus{}, err
	}
	switch st.State {
	case "stopped":
		return st, nil
	case "stale":
		if err := runfile.Remove(cfg.RunFilePath); err != nil {
			return daemonStatus{}, err
		}
		return daemonStatus{State: "stopped", RunFile: cfg.RunFilePath, DataDir: cfg.DataDir}, nil
	}
	if !st.owned {
		if err := runfile.Remove(cfg.RunFilePath); err != nil {
			return daemonStatus{}, err
		}
		return daemonStatus{State: "stopped", RunFile: cfg.RunFilePath, DataDir: cfg.DataDir}, nil
	}

	if err := c.deps.SignalTerm(st.PID); err != nil {
		if c.deps.ProcessAlive(st.PID) {
			return daemonStatus{}, fmt.Errorf("signal daemon pid %d: %w", st.PID, err)
		}
		_ = runfile.Remove(cfg.RunFilePath)
		return daemonStatus{State: "stopped", RunFile: cfg.RunFilePath, DataDir: cfg.DataDir}, nil
	}
	return c.waitForStopped(ctx, st.PID, cfg.RunFilePath, cfg.DataDir, opts.timeout)
}

func (c *commandContext) waitForStopped(ctx context.Context, pid int, runFilePath, dataDir string, timeout time.Duration) (daemonStatus, error) {
	if timeout <= 0 {
		timeout = defaultStopTimeout
	}
	deadline := c.deps.Now().Add(timeout)
	for {
		select {
		case <-ctx.Done():
			return daemonStatus{}, ctx.Err()
		default:
		}

		info, err := runfile.Read(runFilePath)
		if err != nil {
			return daemonStatus{}, err
		}
		alive := c.deps.ProcessAlive(pid)
		if info == nil {
			return daemonStatus{State: "stopped", RunFile: runFilePath, DataDir: dataDir}, nil
		}
		if !alive {
			if err := runfile.Remove(runFilePath); err != nil {
				return daemonStatus{}, err
			}
			return daemonStatus{State: "stopped", RunFile: runFilePath, DataDir: dataDir}, nil
		}
		if !c.deps.Now().Before(deadline) {
			return daemonStatus{}, fmt.Errorf("daemon pid %d did not stop within %s", pid, timeout)
		}
		c.deps.Sleep(100 * time.Millisecond)
	}
}
