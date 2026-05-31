package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

const probeTimeout = 2 * time.Second

type statusOptions struct {
	json bool
}

type daemonStatus struct {
	State     string     `json:"state"`
	PID       int        `json:"pid,omitempty"`
	Port      int        `json:"port,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	Uptime    string     `json:"uptime,omitempty"`
	RunFile   string     `json:"runFile"`
	DataDir   string     `json:"dataDir"`
	Health    string     `json:"health,omitempty"`
	Ready     string     `json:"ready,omitempty"`
	Error     string     `json:"error,omitempty"`
}

func newStatusCommand(ctx *commandContext) *cobra.Command {
	var opts statusOptions
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show AO daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := ctx.inspectDaemon(cmd.Context())
			if err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), st)
			}
			return writeStatus(cmd, st)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output status as JSON")
	return cmd
}

func (c *commandContext) inspectDaemon(ctx context.Context) (daemonStatus, error) {
	cfg, err := config.Load()
	if err != nil {
		return daemonStatus{}, err
	}
	st := daemonStatus{State: "stopped", RunFile: cfg.RunFilePath, DataDir: cfg.DataDir}

	info, err := runfile.Read(cfg.RunFilePath)
	if err != nil {
		return daemonStatus{}, err
	}
	if info == nil {
		return st, nil
	}

	st.PID = info.PID
	st.Port = info.Port
	startedAt := info.StartedAt
	st.StartedAt = &startedAt
	st.Uptime = formatUptime(c.deps.Now().Sub(info.StartedAt))

	if !c.deps.ProcessAlive(info.PID) {
		st.State = "stale"
		st.Error = "run-file points to a dead process"
		return st, nil
	}

	health, err := c.readProbe(ctx, info.Port, "healthz")
	if err != nil {
		st.State = "unhealthy"
		st.Error = err.Error()
		return st, nil
	}
	st.Health = health

	ready, err := c.readProbe(ctx, info.Port, "readyz")
	if err != nil {
		st.State = "not_ready"
		st.Error = err.Error()
		return st, nil
	}
	st.Ready = ready
	if ready == "ready" {
		st.State = "ready"
		return st, nil
	}
	st.State = "not_ready"
	return st, nil
}

func (c *commandContext) readProbe(ctx context.Context, port int, path string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, fmt.Sprintf("http://%s:%d/%s", config.LoopbackHost, port, path), nil)
	if err != nil {
		return "", err
	}
	resp, err := c.deps.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("%s: decode response: %w", path, err)
	}
	if body.Status == "" {
		return "", fmt.Errorf("%s: missing status", path)
	}
	return body.Status, nil
}

func writeStatus(cmd *cobra.Command, st daemonStatus) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "AO daemon: %s\n", st.State); err != nil {
		return err
	}
	if st.PID != 0 {
		if _, err := fmt.Fprintf(out, "  pid: %d\n", st.PID); err != nil {
			return err
		}
	}
	if st.Port != 0 {
		if _, err := fmt.Fprintf(out, "  port: %d\n", st.Port); err != nil {
			return err
		}
	}
	if st.StartedAt != nil && !st.StartedAt.IsZero() {
		if _, err := fmt.Fprintf(out, "  started: %s\n", st.StartedAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if st.Uptime != "" {
		if _, err := fmt.Fprintf(out, "  uptime: %s\n", st.Uptime); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "  run file: %s\n", st.RunFile); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "  data dir: %s\n", st.DataDir); err != nil {
		return err
	}
	if st.Health != "" {
		if _, err := fmt.Fprintf(out, "  healthz: %s\n", st.Health); err != nil {
			return err
		}
	}
	if st.Ready != "" {
		if _, err := fmt.Fprintf(out, "  readyz: %s\n", st.Ready); err != nil {
			return err
		}
	}
	if st.Error != "" {
		if _, err := fmt.Fprintf(out, "  error: %s\n", st.Error); err != nil {
			return err
		}
	}
	return nil
}

func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}
