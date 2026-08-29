package haocli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// DiagnosticCheck is one stable, independently executable doctor result.
type DiagnosticCheck struct {
	ID          string `json:"id"`
	Severity    string `json:"severity"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
	Remediation string `json:"remediation,omitempty"`
}

// DoctorReport is the versioned machine-readable diagnostic document.
type DoctorReport struct {
	SchemaVersion     int               `json:"schemaVersion"`
	Machine           string            `json:"machine"`
	Mode              string            `json:"mode"`
	Checks            []DiagnosticCheck `json:"checks"`
	Failures          int               `json:"failures"`
	ExecutionFailures int               `json:"executionFailures"`
}

func newDoctorCommand(deps Deps, opts *options) *cobra.Command {
	return &cobra.Command{Use: "doctor", Short: "Run read-only machine diagnostics", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := buildDoctor(cmd.Context(), deps, opts.configPath)
			if err != nil {
				return err
			}
			if opts.json {
				err = writeJSON(cmd.OutOrStdout(), haocontractRedact(report))
			} else {
				err = writeDoctorReport(cmd, report)
			}
			if err != nil {
				return err
			}
			if report.Failures > 0 || report.ExecutionFailures > 0 {
				return commandError{Code: "doctor_failed", ExitStatus: 1, Silent: true}
			}
			return nil
		}}
}

func buildDoctor(ctx context.Context, deps Deps, explicit string) (DoctorReport, error) {
	path, object, err := loadConfig(deps, explicit)
	if err != nil {
		return DoctorReport{}, err
	}
	stateRoot, err := deps.StateDir()
	if err != nil {
		return DoctorReport{}, operationalError("resolve state root", err)
	}
	report := DoctorReport{SchemaVersion: observationSchemaVersion, Machine: configString(object, "machine", "name"), Mode: configString(object, "mode")}
	add := func(check DiagnosticCheck) {
		report.Checks = append(report.Checks, check)
		if check.Status == "fail" {
			report.Failures++
		}
		if check.Status == "error" {
			report.ExecutionFailures++
		}
	}

	goos, arch := deps.Observer.Platform()
	if supportedPlatform(goos, arch) {
		add(passCheck("host.platform", goos+"/"+arch+" is supported"))
	} else {
		add(DiagnosticCheck{ID: "host.platform", Severity: "warning", Status: "unsupported", Evidence: goos + "/" + arch + " is not a supported managed platform", Remediation: "use a supported Linux or macOS amd64/arm64 host"})
	}
	checkPath := func(id, target string, private bool) {
		info, statErr := deps.Observer.Stat(target)
		if statErr != nil {
			status := "error"
			severity := "error"
			if errors.Is(statErr, os.ErrNotExist) {
				status, severity = "fail", "error"
			}
			add(DiagnosticCheck{ID: id, Severity: severity, Status: status, Evidence: "could not inspect " + target, Remediation: "ensure the path exists and is readable by the target user"})
			return
		}
		if !info.Owner {
			add(DiagnosticCheck{ID: id, Severity: "error", Status: "fail", Evidence: "path is not owned by the current target user", Remediation: "correct ownership outside hao, then retry"})
			return
		}
		if private && info.Mode.Perm()&0o077 != 0 {
			add(DiagnosticCheck{ID: id, Severity: "error", Status: "fail", Evidence: fmt.Sprintf("permissions %04o are broader than owner-only", info.Mode.Perm()), Remediation: "restrict the path to the target user"})
			return
		}
		add(passCheck(id, "ownership and permissions are acceptable"))
	}
	checkPath("state.root", stateRoot, false)
	checkPath("config.file", path, true)
	if available, diskErr := deps.Observer.Disk(stateRoot); diskErr != nil {
		add(errorCheck("host.disk", "disk availability probe failed"))
	} else if available < 512*1024*1024 {
		add(DiagnosticCheck{ID: "host.disk", Severity: "error", Status: "fail", Evidence: fmt.Sprintf("%d bytes available", available), Remediation: "free at least 512 MiB on the state volume"})
	} else {
		add(passCheck("host.disk", fmt.Sprintf("%d bytes available", available)))
	}

	add(anyToolCheck(deps, "host.package-manager", []string{"apt-get", "dnf", "yum", "brew", "apk"}, "install a supported package manager or manage dependencies manually"))
	add(serviceManagerCheck(deps, goos))
	add(diagnosticFromObservation(toolObservation(ctx, deps, "tool.git", "git", true, "--version")))
	github := configString(object, "workflow", "profile") == "github"
	add(diagnosticFromObservation(toolObservation(ctx, deps, "tool.gh", "gh", github, "--version")))
	harness := configString(object, "harness", "id")
	add(diagnosticFromObservation(toolObservation(ctx, deps, "harness."+harness, harnessBinary(harness), true, "--version")))

	runFile, runFileErr := deps.RunFile()
	d := daemonProbe{State: "unknown", Evidence: "daemon discovery path could not be resolved"}
	if runFileErr == nil {
		d = observeDaemon(ctx, deps.Observer, runFile, deps.Timeout)
	}
	if d.State == "healthy" {
		add(passCheck("ao.daemon", "loopback health and readiness succeeded"))
	} else if d.State == "unavailable" && !configBool(object, "service", "enabled") {
		add(DiagnosticCheck{ID: "ao.daemon", Severity: "info", Status: "disabled", Evidence: "daemon is absent and service is intentionally disabled"})
	} else {
		add(DiagnosticCheck{ID: "ao.daemon", Severity: "error", Status: mapObservationStatus(d.State), Evidence: d.Evidence, Remediation: "inspect the AO daemon service and logs"})
	}
	if d.Doctor == nil {
		add(DiagnosticCheck{ID: "ao.doctor", Severity: "warning", Status: "unknown", Evidence: "AO doctor endpoint is unavailable", Remediation: "restore daemon reachability and rerun hao doctor"})
	} else {
		for i, check := range d.Doctor.Checks {
			status, severity := "unknown", "warning"
			if check.Level == "WARN" {
				status, severity = "warn", "warning"
			}
			if check.Level == "FAIL" {
				status, severity = "fail", "error"
			}
			if check.Level == "PASS" {
				status, severity = "pass", "info"
			}
			add(DiagnosticCheck{ID: fmt.Sprintf("ao.doctor.%03d.%s", i+1, stableID(check.Name)), Severity: severity, Status: status, Evidence: check.Message, Remediation: check.Remediation})
		}
	}
	if report.Mode == "pair" {
		port := configInt(object, "pair", "listenPort")
		probeCtx, cancel := boundedContext(ctx, deps.Timeout)
		available, portErr := deps.Observer.PortAvailable(probeCtx, "127.0.0.1", port)
		cancel()
		if portErr != nil {
			add(errorCheck("gateway.port", "port availability probe failed"))
		} else if !available {
			add(DiagnosticCheck{ID: "gateway.port", Severity: "error", Status: "fail", Evidence: fmt.Sprintf("configured port %d is already in use", port), Remediation: "stop the conflicting listener or choose another pair port"})
		} else {
			add(passCheck("gateway.port", fmt.Sprintf("configured port %d is available", port)))
		}
	} else {
		add(DiagnosticCheck{ID: "gateway.port", Severity: "info", Status: "disabled", Evidence: "gateway is disabled in local mode"})
	}
	sort.SliceStable(report.Checks, func(i, j int) bool { return report.Checks[i].ID < report.Checks[j].ID })
	return report, nil
}

func supportedPlatform(goos, arch string) bool {
	return (goos == "linux" || goos == "darwin") && (arch == "amd64" || arch == "arm64")
}
func passCheck(id, evidence string) DiagnosticCheck {
	return DiagnosticCheck{ID: id, Severity: "info", Status: "pass", Evidence: evidence}
}
func errorCheck(id, evidence string) DiagnosticCheck {
	return DiagnosticCheck{ID: id, Severity: "error", Status: "error", Evidence: evidence, Remediation: "inspect the reported condition and retry"}
}
func anyToolCheck(deps Deps, id string, names []string, remediation string) DiagnosticCheck {
	for _, name := range names {
		if path, err := deps.Observer.LookPath(name); err == nil {
			return passCheck(id, filepath.Base(path)+" is available")
		}
	}
	return DiagnosticCheck{ID: id, Severity: "warning", Status: "unsupported", Evidence: "no supported implementation found", Remediation: remediation}
}
func serviceManagerCheck(deps Deps, goos string) DiagnosticCheck {
	var manager string
	switch goos {
	case "linux":
		manager = "systemctl"
	case "darwin":
		manager = "launchctl"
	default:
		return DiagnosticCheck{ID: "host.service-manager", Severity: "warning", Status: "unsupported", Evidence: "no supported service manager is defined for " + goos, Remediation: "manage services manually on this platform"}
	}
	if path, err := deps.Observer.LookPath(manager); err == nil {
		return passCheck("host.service-manager", filepath.Base(path)+" is available")
	}
	return DiagnosticCheck{ID: "host.service-manager", Severity: "warning", Status: "unsupported", Evidence: manager + " is unavailable", Remediation: "manage services manually on this platform"}
}
func diagnosticFromObservation(o Observation) DiagnosticCheck {
	status, severity := mapObservationStatus(o.Status), "info"
	if status == "fail" {
		severity = "error"
	}
	if status == "unknown" || status == "unsupported" {
		severity = "warning"
	}
	return DiagnosticCheck{ID: o.ID, Severity: severity, Status: status, Evidence: firstNonempty(o.Evidence, o.Version), Remediation: o.Remediation}
}
func mapObservationStatus(status string) string {
	switch status {
	case "healthy":
		return "pass"
	case "disabled":
		return "disabled"
	case "unknown":
		return "unknown"
	case "unsupported":
		return "unsupported"
	default:
		return "fail"
	}
}
func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "no additional evidence"
}
func stableID(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func writeDoctorReport(cmd *cobra.Command, report DoctorReport) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "HAO doctor: %s (%s)\n", redactedString(report.Machine), redactedString(report.Mode)); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(out, "%-28s %-11s %s\n", check.ID+":", check.Status, redactedString(check.Evidence)); err != nil {
			return err
		}
		if check.Remediation != "" {
			if _, err := fmt.Fprintf(out, "  remediation: %s\n", redactedString(check.Remediation)); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintf(out, "Failures: %d; execution failures: %d\n", report.Failures, report.ExecutionFailures)
	return err
}
