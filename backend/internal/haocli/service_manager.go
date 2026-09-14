package haocli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	haoDaemonUnit    = "ao-daemon.service"
	haoGatewayUnit   = "ao-gateway.service"
	maxJournalLines  = 200
	maxJournalOutput = 64 << 10
)

var canonicalServiceUnits = map[string]string{
	"daemon":  haoDaemonUnit,
	"gateway": haoGatewayUnit,
}

type serviceCommandSystem interface {
	CheckPrivilege(context.Context, bool, io.Reader) error
	Run(context.Context, bool, bool, io.Reader, string, ...string) error
	Output(context.Context, string, ...string) (string, error)
}

type systemServiceCommands struct{ systemSetupExecution }

func (systemServiceCommands) Output(ctx context.Context, executable string, argv ...string) (string, error) {
	cmd := exec.CommandContext(ctx, executable, argv...)
	cmd.Stdin = nil
	output := boundedBuffer{remaining: maxJournalOutput}
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	return strings.TrimSpace(output.String()), err
}

// ServiceManagerSupport describes whether HAO can operate services on this host.
// Manual contains safe commands for the selected canonical units when automation
// is unavailable.
type ServiceManagerSupport struct {
	Supported bool     `json:"supported"`
	Manager   string   `json:"manager"`
	Reason    string   `json:"reason,omitempty"`
	Manual    []string `json:"manual,omitempty"`
}

// ManagedServiceState contains the bounded systemd facts needed by later HAO
// init and lifecycle commands.
type ManagedServiceState struct {
	Component string `json:"component"`
	Unit      string `json:"unit"`
	Loaded    bool   `json:"loaded"`
	Enabled   bool   `json:"enabled"`
	Active    bool   `json:"active"`
	SubState  string `json:"subState,omitempty"`
}

// ServiceChange records a mutation owned by one invocation. Rollback only
// reverses these entries, never state that existed before the invocation.
type ServiceChange struct {
	Component string `json:"component"`
	Operation string `json:"operation"`
}

type ServiceOperationResult struct {
	Completed []ServiceChange `json:"completed"`
	Rollback  []ServiceChange `json:"rollback"`
}

type haoServiceManager struct {
	observer       Observer
	commands       serviceCommandSystem
	timeout        time.Duration
	targetUser     UserObservation
	nonInteractive bool
	input          io.Reader
	systemctl      string
	journalctl     string
}

func discoverServiceManager(ctx context.Context, deps Deps, target UserObservation, nonInteractive bool) (*haoServiceManager, ServiceManagerSupport) {
	deps = deps.withDefaults()
	components := []string{"daemon", "gateway"}
	manual := manualServiceCommands(components)
	goos, _ := deps.Observer.Platform()
	if goos != "linux" {
		return nil, ServiceManagerSupport{Manager: "manual", Reason: "systemd service management is supported only on Linux", Manual: manual}
	}
	distribution, err := deps.Observer.Distribution()
	if err != nil || distribution != "ubuntu" {
		return nil, ServiceManagerSupport{Manager: "manual", Reason: "managed services require Ubuntu systemd", Manual: manual}
	}
	if target.Name == "" || target.UID <= 0 || target.Home == "" || !filepath.IsAbs(target.Home) {
		return nil, ServiceManagerSupport{Manager: "manual", Reason: "a target unprivileged user is required", Manual: manual}
	}
	systemctl, err := deps.Observer.LookPath("systemctl")
	if err != nil || systemctl != "/usr/bin/systemctl" {
		return nil, ServiceManagerSupport{Manager: "manual", Reason: "canonical /usr/bin/systemctl is unavailable", Manual: manual}
	}
	probeCtx, cancel := boundedContext(ctx, deps.Timeout)
	defer cancel()
	if _, err := deps.Observer.Run(probeCtx, systemctl, "show-environment"); err != nil {
		return nil, ServiceManagerSupport{Manager: "manual", Reason: "systemd is installed but unavailable", Manual: manual}
	}
	journalctl := ""
	if path, err := deps.Observer.LookPath("journalctl"); err == nil && path == "/usr/bin/journalctl" {
		journalctl = path
	}
	return &haoServiceManager{observer: deps.Observer, commands: systemServiceCommands{}, timeout: deps.Timeout, targetUser: target, nonInteractive: nonInteractive, input: deps.In, systemctl: systemctl, journalctl: journalctl}, ServiceManagerSupport{Supported: true, Manager: "systemd"}
}

func manualServiceCommands(components []string) []string {
	units, err := serviceUnits(components)
	if err != nil {
		return nil
	}
	return []string{
		"sudo /usr/bin/systemctl start " + strings.Join(units, " "),
		"sudo /usr/bin/systemctl stop " + strings.Join(reverseStrings(units), " "),
		"/usr/bin/journalctl --no-pager --lines 200 --unit " + units[0],
	}
}

func serviceUnits(components []string) ([]string, error) {
	if len(components) == 0 || len(components) > len(canonicalServiceUnits) {
		return nil, errors.New("service selection must contain one or two managed components")
	}
	seen := map[string]bool{}
	units := make([]string, 0, len(components))
	for _, component := range components {
		unit, ok := canonicalServiceUnits[component]
		if !ok || seen[component] {
			return nil, fmt.Errorf("component %q is not a canonical managed service", component)
		}
		seen[component] = true
		units = append(units, unit)
	}
	return units, nil
}

func orderedComponents(components []string, reverse bool) ([]string, error) {
	if _, err := serviceUnits(components); err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, component := range components {
		wanted[component] = true
	}
	order := []string{"daemon", "gateway"}
	if reverse {
		order = []string{"gateway", "daemon"}
	}
	selected := make([]string, 0, len(components))
	for _, component := range order {
		if wanted[component] {
			selected = append(selected, component)
		}
	}
	return selected, nil
}

func (m *haoServiceManager) Status(ctx context.Context, components []string) ([]ManagedServiceState, error) {
	ordered, err := orderedComponents(components, false)
	if err != nil {
		return nil, err
	}
	states := make([]ManagedServiceState, 0, len(ordered))
	for _, component := range ordered {
		probeCtx, cancel := boundedContext(ctx, m.timeout)
		output, runErr := m.observer.Run(probeCtx, m.systemctl, "show", "--no-pager", "--property=LoadState", "--property=UnitFileState", "--property=ActiveState", "--property=SubState", canonicalServiceUnits[component])
		timedOut := probeCtx.Err() != nil
		cancel()
		if runErr != nil {
			if timedOut || errors.Is(runErr, context.DeadlineExceeded) {
				return nil, fmt.Errorf("status for %s timed out: %w", component, context.DeadlineExceeded)
			}
			return nil, fmt.Errorf("status for %s failed: %w", component, runErr)
		}
		values := parseSystemdProperties(output)
		states = append(states, ManagedServiceState{Component: component, Unit: canonicalServiceUnits[component], Loaded: values["LoadState"] == "loaded", Enabled: systemdEnabled(values["UnitFileState"]), Active: values["ActiveState"] == "active", SubState: values["SubState"]})
	}
	return states, nil
}

func parseSystemdProperties(output string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func systemdEnabled(state string) bool {
	return state == "enabled" || state == "enabled-runtime" || state == "static"
}

func (m *haoServiceManager) Activate(ctx context.Context, components []string) (ServiceOperationResult, error) {
	ordered, err := orderedComponents(components, false)
	if err != nil {
		return ServiceOperationResult{}, err
	}
	states, err := m.Status(ctx, ordered)
	if err != nil {
		return ServiceOperationResult{}, err
	}
	needsMutation := false
	for _, state := range states {
		needsMutation = needsMutation || !state.Enabled || !state.Active
	}
	if needsMutation {
		if err := m.checkPrivilege(ctx); err != nil {
			return ServiceOperationResult{}, err
		}
	}
	result := ServiceOperationResult{Completed: []ServiceChange{}, Rollback: []ServiceChange{}}
	for i, component := range ordered {
		state := states[i]
		if !state.Enabled {
			if err := m.mutate(ctx, "enable", component); err != nil {
				return m.rollback(result, err)
			}
			result.Completed = append(result.Completed, ServiceChange{Component: component, Operation: "enable"})
		}
		if !state.Active {
			if err := m.mutate(ctx, "start", component); err != nil {
				return m.rollback(result, err)
			}
			result.Completed = append(result.Completed, ServiceChange{Component: component, Operation: "start"})
		}
	}
	return result, nil
}

func (m *haoServiceManager) Start(ctx context.Context, components []string) (ServiceOperationResult, error) {
	return m.applyOrdered(ctx, components, false, "start")
}

func (m *haoServiceManager) Stop(ctx context.Context, components []string) (ServiceOperationResult, error) {
	return m.applyOrdered(ctx, components, true, "stop")
}

func (m *haoServiceManager) Disable(ctx context.Context, components []string) (ServiceOperationResult, error) {
	return m.applyOrdered(ctx, components, true, "disable")
}

func (m *haoServiceManager) Restart(ctx context.Context, components []string) (ServiceOperationResult, error) {
	result, err := m.Stop(ctx, components)
	if err != nil {
		return result, err
	}
	started, err := m.applyOrdered(ctx, components, false, "start")
	result.Completed = append(result.Completed, started.Completed...)
	result.Rollback = append(result.Rollback, started.Rollback...)
	return result, err
}

func (m *haoServiceManager) applyOrdered(ctx context.Context, components []string, reverse bool, operation string) (ServiceOperationResult, error) {
	ordered, err := orderedComponents(components, reverse)
	if err != nil {
		return ServiceOperationResult{}, err
	}
	if err := m.checkPrivilege(ctx); err != nil {
		return ServiceOperationResult{}, err
	}
	result := ServiceOperationResult{Completed: []ServiceChange{}, Rollback: []ServiceChange{}}
	for _, component := range ordered {
		if err := m.mutate(ctx, operation, component); err != nil {
			return result, err
		}
		result.Completed = append(result.Completed, ServiceChange{Component: component, Operation: operation})
	}
	return result, nil
}

func (m *haoServiceManager) Journal(ctx context.Context, component string, lines int) (string, error) {
	if _, err := serviceUnits([]string{component}); err != nil {
		return "", err
	}
	if m.journalctl == "" {
		return "", errors.New("canonical /usr/bin/journalctl is unavailable")
	}
	if lines < 1 || lines > maxJournalLines {
		return "", fmt.Errorf("journal lines must be between 1 and %d", maxJournalLines)
	}
	probeCtx, cancel := boundedContext(ctx, m.timeout)
	defer cancel()
	output, err := m.commands.Output(probeCtx, m.journalctl, "--no-pager", "--lines", fmt.Sprintf("%d", lines), "--output", "short-iso-precise", "--unit", canonicalServiceUnits[component])
	if err != nil {
		if probeCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("journal discovery timed out: %w", context.DeadlineExceeded)
		}
		return "", fmt.Errorf("journal discovery failed: %w", err)
	}
	return output, nil
}

func (m *haoServiceManager) checkPrivilege(ctx context.Context) error {
	privilegeCtx, cancel := boundedContext(ctx, m.timeout)
	defer cancel()
	if err := m.commands.CheckPrivilege(privilegeCtx, m.nonInteractive, m.input); err != nil {
		return commandError{Code: "privilege_required", Message: "service operation requires narrowly scoped administrator privileges", Operation: "manage services", Remediation: "approve the service-manager operation, or run the reported manual command", Details: map[string]any{"targetUser": m.targetUser.Name}, ExitStatus: 3, Cause: errPrivilegeRefused}
	}
	return nil
}

func (m *haoServiceManager) mutate(ctx context.Context, operation, component string) error {
	unit, ok := canonicalServiceUnits[component]
	if !ok {
		return errors.New("refusing non-canonical managed service")
	}
	mutationCtx, cancel := boundedContext(ctx, m.timeout)
	defer cancel()
	if err := m.commands.Run(mutationCtx, true, m.nonInteractive, m.input, m.systemctl, operation, unit); err != nil {
		if mutationCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%s %s timed out: %w", operation, component, context.DeadlineExceeded)
		}
		if errors.Is(err, errPrivilegeRefused) {
			return commandError{Code: "privilege_required", Message: "service operation privilege was refused", Operation: operation + " service", Remediation: "approve the service-manager operation, or run it manually", Details: map[string]any{"component": component}, ExitStatus: 3, Cause: errPrivilegeRefused}
		}
		return fmt.Errorf("%s %s failed: %w", operation, component, err)
	}
	return nil
}

func (m *haoServiceManager) rollback(result ServiceOperationResult, failure error) (ServiceOperationResult, error) {
	for i := len(result.Completed) - 1; i >= 0; i-- {
		change := result.Completed[i]
		inverse := map[string]string{"start": "stop", "enable": "disable"}[change.Operation]
		if inverse == "" {
			continue
		}
		if err := m.mutate(context.Background(), inverse, change.Component); err != nil {
			return result, commandError{Code: "rollback_required", Message: "service activation failed and rollback was incomplete", Operation: "activate services", Remediation: "inspect service status and logs before retrying", Details: map[string]any{"completed": result.Completed, "rollback": result.Rollback, "diagnostic": safeDiagnostic(err)}, ExitStatus: 1, Cause: failure}
		}
		result.Rollback = append(result.Rollback, ServiceChange{Component: change.Component, Operation: inverse})
	}
	return result, failure
}

func reverseStrings(values []string) []string {
	reversed := append([]string(nil), values...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}
