package haocli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// lifecycleReport is the machine-readable outcome of one hao start/stop/
// restart run. It never carries secrets: service facts only.
type lifecycleReport struct {
	Operation  string                 `json:"operation"`
	Components []string               `json:"components"`
	Supported  bool                   `json:"supported"`
	Manager    string                 `json:"manager"`
	Reason     string                 `json:"reason,omitempty"`
	Result     ServiceOperationResult `json:"result,omitempty"`
	States     []ManagedServiceState  `json:"states,omitempty"`
	Manual     []string               `json:"manual,omitempty"`
}

// journalReport is the machine-readable outcome of one hao logs run.
type journalReport struct {
	Component string `json:"component"`
	Unit      string `json:"unit"`
	Lines     int    `json:"lines"`
	Output    string `json:"output"`
}

// newServiceStartCommand builds `hao start [daemon|gateway]`.
func newServiceStartCommand(deps Deps, opts *options) *cobra.Command {
	return newServiceOperationCommand(deps, opts, "start")
}

func newServiceStopCommand(deps Deps, opts *options) *cobra.Command {
	return newServiceOperationCommand(deps, opts, "stop")
}

func newServiceRestartCommand(deps Deps, opts *options) *cobra.Command {
	return newServiceOperationCommand(deps, opts, "restart")
}

func newServiceOperationCommand(deps Deps, opts *options, operation string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   operation + " [daemon|gateway]",
		Short: capitalServiceVerb(operation) + " hao-managed services",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := runServiceOperation(cmd.Context(), deps, opts, operation, componentArg(args))
			if err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			return writeLifecycleReport(cmd, report)
		},
	}
	return cmd
}

func componentArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// runServiceOperation runs one start/stop/restart against the hao-managed
// daemon/gateway components selected by the config mode (both for pair, only
// the daemon for local) or by the positional component. Restrictions:
//
//   - service.enabled=false refuses start/stop/restart with a stable
//     service_disabled error; refusing is deliberate so an operator who
//     disabled service management cannot silently start listening services
//     that the next hao setup would tear down again.
//   - without a supported service manager, the operation reports the manual
//     commands instead of failing.
//   - start/restart refuse to bring up a daemon while a live daemon already
//     runs outside the hao-managed services (the desktop supervisor), because
//     two daemons would fight over the loopback port.
func runServiceOperation(ctx context.Context, deps Deps, opts *options, operation, selected string) (lifecycleReport, error) {
	path, object, err := loadConfig(deps, opts.configPath)
	if err != nil {
		return lifecycleReport{}, err
	}
	mode := configString(object, "mode")
	enabled := configBool(object, "service", "enabled")
	components, err := selectLifecycleComponents(selected, mode)
	if err != nil {
		return lifecycleReport{}, err
	}
	report := lifecycleReport{Operation: operation, Components: components}
	if !enabled {
		return report, commandError{Code: "service_disabled", Message: "service management is disabled in the hao configuration", Operation: operation + " services", Remediation: "run `hao config set service.enabled true` then `hao setup`, or operate the services manually", Details: map[string]any{"path": path}, ExitStatus: 2}
	}
	target, _ := deps.Observer.CurrentUser()
	manager, support := discoverServiceManager(ctx, deps, target, false)
	report.Supported, report.Manager, report.Reason, report.Manual = support.Supported, support.Manager, support.Reason, support.Manual
	if !support.Supported {
		report.Reason = support.Reason + "; run the reported manual commands"
		return report, nil
	}
	if operation == "start" || operation == "restart" {
		if conflict, found, err := managedDaemonConflict(ctx, deps, manager, components); err != nil {
			return report, operationalError("probe daemon supervision", err)
		} else if found {
			return report, commandError{Code: "desktop_supervised_daemon", Message: "a live AO daemon already runs outside the hao-managed services", Operation: operation + " services", Remediation: "stop the desktop-supervised daemon (or use the desktop app's own lifecycle), then retry", Details: map[string]any{"supervisor": conflict}, ExitStatus: 1}
		}
	}
	var result ServiceOperationResult
	switch operation {
	case "start":
		result, err = manager.Start(ctx, components)
	case "stop":
		result, err = manager.Stop(ctx, components)
	case "restart":
		result, err = manager.Restart(ctx, components)
	default:
		return report, commandError{Code: "invalid_usage", Message: "unknown service operation", Operation: operation, ExitStatus: 2}
	}
	if err != nil {
		return report, err
	}
	report.Result = result
	states, err := manager.Status(ctx, components)
	if err != nil {
		return report, operationalError("read service status after "+operation, err)
	}
	report.States = states
	return report, nil
}

func selectLifecycleComponents(selected, mode string) ([]string, error) {
	if selected != "" {
		if _, ok := canonicalServiceUnits[selected]; !ok {
			return nil, commandError{Code: "invalid_usage", Message: "service component must be daemon or gateway", Operation: "manage services", Remediation: "run `hao " + selected + "` without a component, or pass daemon or gateway", Details: map[string]any{"component": selected}, ExitStatus: 2}
		}
		return []string{selected}, nil
	}
	return defaultLifecycleComponents(mode), nil
}

// defaultLifecycleComponents derives the managed component set from the
// configuration mode: pair manages the daemon and the gateway, local manages
// only the daemon. The gateway has no meaning without pair provisioning.
func defaultLifecycleComponents(mode string) []string {
	if mode == "pair" {
		return []string{"daemon", "gateway"}
	}
	return []string{"daemon"}
}

func newServiceLogsCommand(deps Deps, opts *options) *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "logs [daemon|gateway]",
		Short: "Read recent journal output for a hao-managed service",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := runLogs(cmd.Context(), deps, opts, componentArg(args), lines)
			if err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\n", report.Output); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&lines, "lines", maxJournalLines, fmt.Sprintf("number of recent journal lines (1-%d)", maxJournalLines))
	return cmd
}

// runLogs reads journal output for one hao-managed component. It is read-only
// and therefore allowed even when service.enabled is false; what it refuses is
// a host without a journal (the operation would fail the same way using the
// manual command, so it reports the manual journal command instead).
func runLogs(ctx context.Context, deps Deps, opts *options, selected string, lines int) (journalReport, error) {
	path, object, err := loadConfig(deps, opts.configPath)
	if err != nil {
		return journalReport{}, err
	}
	components, err := selectLifecycleComponents(selected, configString(object, "mode"))
	if err != nil {
		return journalReport{}, err
	}
	if lines < 1 || lines > maxJournalLines {
		return journalReport{}, commandError{Code: "invalid_usage", Message: fmt.Sprintf("--lines must be between 1 and %d", maxJournalLines), Operation: "read logs", Remediation: "pass --lines within 1-200", Details: map[string]any{"lines": lines}, ExitStatus: 2}
	}
	component := components[0]
	target, _ := deps.Observer.CurrentUser()
	manager, support := discoverServiceManager(ctx, deps, target, false)
	if !support.Supported {
		remediation := "run the reported manual journal command"
		if len(support.Manual) > 0 {
			remediation = "run the manual journal command: " + support.Manual[len(support.Manual)-1]
		}
		return journalReport{}, commandError{Code: "service_unsupported", Message: "the host has no supported service journal", Operation: "read logs", Remediation: remediation, Details: map[string]any{"manual": support.Manual, "path": path, "reason": support.Reason}, ExitStatus: 1}
	}
	output, err := manager.Journal(ctx, component, lines)
	if err != nil {
		return journalReport{}, operationalError("read service journal", err)
	}
	return journalReport{Component: component, Unit: canonicalServiceUnits[component], Lines: lines, Output: output}, nil
}

func writeLifecycleReport(cmd *cobra.Command, report lifecycleReport) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "%s: %s\n", report.Operation, redactedString(joinComponents(report.Components))); err != nil {
		return err
	}
	if !report.Supported {
		if _, err := fmt.Fprintf(out, "  not automated: %s\n", redactedString(report.Reason)); err != nil {
			return err
		}
		for _, manual := range report.Manual {
			if _, err := fmt.Fprintf(out, "  manual: %s\n", redactedString(manual)); err != nil {
				return err
			}
		}
		return nil
	}
	for _, change := range report.Result.Completed {
		if _, err := fmt.Fprintf(out, "  %s %s\n", change.Operation, canonicalServiceUnits[change.Component]); err != nil {
			return err
		}
	}
	for _, state := range report.States {
		label := "not active"
		if state.Active {
			label = "active"
		}
		if _, err := fmt.Fprintf(out, "  %s: %s (%s)\n", state.Component, label, redactedString(state.SubState)); err != nil {
			return err
		}
	}
	return nil
}

func joinComponents(components []string) string {
	joined := ""
	for i, component := range components {
		if i > 0 {
			joined += ", "
		}
		joined += component
	}
	return joined
}

func capitalServiceVerb(operation string) string {
	switch operation {
	case "start":
		return "Start"
	case "stop":
		return "Stop"
	case "restart":
		return "Restart"
	}
	return "Manage"
}
