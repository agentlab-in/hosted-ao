package haocli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/haocontract"
)

const observationSchemaVersion = 1

// Observation is one stable desired-versus-observed status fact.
type Observation struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Desired     bool   `json:"desired"`
	Version     string `json:"version,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
	Remediation string `json:"remediation,omitempty"`
	Drift       bool   `json:"drift"`
}

// StatusReport is the versioned machine-readable status document.
type StatusReport struct {
	SchemaVersion    int           `json:"schemaVersion"`
	Machine          string        `json:"machine"`
	Mode             string        `json:"mode"`
	ConfigPath       string        `json:"configPath"`
	ConfigVersion    int           `json:"configVersion"`
	HAOVersion       string        `json:"haoVersion"`
	DesiredAOVersion string        `json:"desiredAoVersion"`
	Observations     []Observation `json:"observations"`
	StrictFailure    bool          `json:"strictFailure"`
}

func newStatusCommand(deps Deps, opts *options) *cobra.Command {
	var strict bool
	cmd := &cobra.Command{Use: "status", Short: "Show desired and observed machine state", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := buildStatus(cmd.Context(), deps, opts.configPath)
			if err != nil {
				return err
			}
			if opts.json {
				err = writeJSON(cmd.OutOrStdout(), haocontractRedact(report))
			} else {
				err = writeStatusReport(cmd, report)
			}
			if err != nil {
				return err
			}
			if strict && report.StrictFailure {
				return commandError{Code: "strict_failure", ExitStatus: 1, Silent: true}
			}
			return nil
		}}
	cmd.Flags().BoolVar(&strict, "strict", false, "exit 1 for proven unhealthy or drifted desired state")
	return cmd
}

func buildStatus(ctx context.Context, deps Deps, explicit string) (StatusReport, error) {
	path, object, err := loadConfig(deps, explicit)
	if err != nil {
		return StatusReport{}, err
	}
	runFile, err := deps.RunFile()
	if err != nil {
		return StatusReport{}, operationalError("resolve daemon discovery path", err)
	}
	report := StatusReport{SchemaVersion: observationSchemaVersion, Machine: configString(object, "machine", "name"), Mode: configString(object, "mode"), ConfigPath: path, ConfigVersion: 1, HAOVersion: Version, DesiredAOVersion: configString(object, "components", "aoVersion")}
	d := observeDaemon(ctx, deps.Observer, runFile, deps.Timeout)
	desiredService := configBool(object, "service", "enabled")
	if !desiredService && d.State == "unavailable" {
		d.State, d.Evidence = "disabled", "daemon is absent and service is intentionally disabled"
	}
	report.Observations = append(report.Observations, Observation{ID: "ao.daemon", Status: d.State, Desired: desiredService, Evidence: d.Evidence})
	if d.Doctor != nil {
		failed := d.Doctor.Failures > 0 || !d.Doctor.OK
		status := "healthy"
		if failed {
			status = "unhealthy"
		}
		report.Observations = append(report.Observations, Observation{ID: "ao.readiness", Status: status, Desired: true, Evidence: fmt.Sprintf("AO doctor reported %d failing check(s)", d.Doctor.Failures)})
	} else {
		report.Observations = append(report.Observations, Observation{ID: "ao.readiness", Status: "unknown", Desired: true, Evidence: "AO doctor report unavailable"})
	}
	report.Observations = append(report.Observations, toolObservation(ctx, deps, "tool.git", "git", true, "--version"))
	profileGitHub := configString(object, "workflow", "profile") == "github"
	report.Observations = append(report.Observations, toolObservation(ctx, deps, "tool.gh", "gh", profileGitHub, "--version"))
	harness := configString(object, "harness", "id")
	report.Observations = append(report.Observations, toolObservation(ctx, deps, "harness."+harness, harnessBinary(harness), true, "--version"))
	if report.Mode == "pair" {
		report.Observations = append(report.Observations, Observation{ID: "gateway", Status: "unknown", Desired: desiredService, Evidence: "gateway service state is not available through a supported read-only contract"})
	} else {
		report.Observations = append(report.Observations, Observation{ID: "gateway", Status: "disabled", Desired: false})
	}
	for i := range report.Observations {
		o := &report.Observations[i]
		if o.Desired && (o.Status == "unhealthy" || o.Status == "unavailable" || o.Drift) {
			report.StrictFailure = true
		}
	}
	sort.SliceStable(report.Observations, func(i, j int) bool { return report.Observations[i].ID < report.Observations[j].ID })
	return report, nil
}

func toolObservation(ctx context.Context, deps Deps, id, binary string, desired bool, args ...string) Observation {
	if !desired {
		return Observation{ID: id, Status: "disabled", Desired: false}
	}
	path, err := deps.Observer.LookPath(binary)
	if err != nil {
		return Observation{ID: id, Status: "unavailable", Desired: true, Evidence: binary + " was not found on PATH"}
	}
	probeCtx, cancel := boundedContext(ctx, deps.Timeout)
	defer cancel()
	out, err := deps.Observer.Run(probeCtx, path, args...)
	if err != nil {
		if probeCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return Observation{ID: id, Status: "unknown", Desired: true, Evidence: "version probe timed out"}
		}
		return Observation{ID: id, Status: "unhealthy", Desired: true, Evidence: "version probe failed"}
	}
	return Observation{ID: id, Status: "healthy", Desired: true, Version: safeVersion(out)}
}

func harnessBinary(id string) string {
	if id == "claude-code" {
		return "claude"
	}
	return id
}

func safeVersion(value string) string {
	line := strings.Split(strings.TrimSpace(value), "\n")[0]
	if len(line) > 128 {
		line = line[:128]
	}
	return safeDiagnostic(fmt.Errorf("%s", line))
}

func writeStatusReport(cmd *cobra.Command, report StatusReport) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "Machine: %s (%s)\nConfig: v%d %s\nhao: %s\nDesired AO: %s\n", redactedString(report.Machine), redactedString(report.Mode), report.ConfigVersion, redactedString(report.ConfigPath), redactedString(report.HAOVersion), redactedString(report.DesiredAOVersion)); err != nil {
		return err
	}
	for _, observation := range report.Observations {
		version := ""
		if observation.Version != "" {
			version = " (" + observation.Version + ")"
		}
		if _, err := fmt.Fprintf(out, "%-20s %s%s", observation.ID+":", observation.Status, version); err != nil {
			return err
		}
		if observation.Evidence != "" {
			if _, err := fmt.Fprintf(out, " — %s", redactedString(observation.Evidence)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	return nil
}

func haocontractRedact(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return map[string]any{"schemaVersion": observationSchemaVersion}
	}
	var object map[string]any
	if json.Unmarshal(data, &object) != nil {
		return map[string]any{"schemaVersion": observationSchemaVersion}
	}
	return haocontract.Redact(object)
}

func redactedString(value string) string {
	redacted, _ := haocontract.Redact(value).(string)
	return redacted
}
