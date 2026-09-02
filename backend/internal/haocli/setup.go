package haocli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const setupPlanSchemaVersion = 1

// SetupPrivilege describes the narrow elevated boundary a future executor needs.
type SetupPrivilege struct {
	Required bool   `json:"required"`
	Scope    string `json:"scope,omitempty"`
}

// SetupAction is a structured, shell-free future execution description.
type SetupAction struct {
	Kind       string   `json:"kind"`
	Executable string   `json:"executable,omitempty"`
	Argv       []string `json:"argv,omitempty"`
	Path       string   `json:"path,omitempty"`
	Mode       string   `json:"mode,omitempty"`
}

// SetupStep is one stable desired-versus-observed reconciliation decision.
type SetupStep struct {
	ID           string         `json:"id"`
	Component    string         `json:"component"`
	Operation    string         `json:"operation"`
	Disposition  string         `json:"disposition"`
	Privilege    SetupPrivilege `json:"privilege"`
	Reason       string         `json:"reason"`
	Evidence     string         `json:"evidence"`
	Remediation  string         `json:"remediation,omitempty"`
	Dependencies []string       `json:"dependencies,omitempty"`
	Action       *SetupAction   `json:"action,omitempty"`
}

// SetupSummary counts each disposition and reports whether the plan is complete.
type SetupSummary struct {
	Create  int  `json:"create"`
	Update  int  `json:"update"`
	NoOp    int  `json:"noOp"`
	Blocked int  `json:"blocked"`
	Ready   bool `json:"ready"`
}

// SetupPlan is the versioned deterministic output of setup dry-run.
type SetupPlan struct {
	SchemaVersion int          `json:"schemaVersion"`
	DryRun        bool         `json:"dryRun"`
	Machine       string       `json:"machine"`
	Mode          string       `json:"mode"`
	Platform      string       `json:"platform"`
	InstallPolicy string       `json:"installPolicy"`
	Steps         []SetupStep  `json:"steps"`
	Summary       SetupSummary `json:"summary"`
}

type setupDesired struct {
	Machine, Mode, AOVersion, Harness, Profile, Install string
	ServiceEnabled                                      bool
	PairPort                                            int
	StateRoot, ConfigPath                               string
}

type observedItem struct {
	State, Evidence, Version, Path string
	Mode                           os.FileMode
	UID                            int
	Owner                          bool
	IsDir                          bool
}

type setupSnapshot struct {
	OS, Arch, Distribution, DistributionState string
	Directories                               map[string]observedItem
	Artifact                                  observedItem
	Tools                                     map[string]observedItem
	PackageManager, ServiceManager            observedItem
	ServiceFiles                              map[string]observedItem
}

func newSetupCommand(deps Deps, opts *options) *cobra.Command {
	var dryRun, nonInteractive, yes bool
	var install string
	cmd := &cobra.Command{Use: "setup", Short: "Plan machine preparation", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		// This guard intentionally precedes config loading and every observation.
		if !dryRun {
			return commandError{Code: "feature_deferred", Message: "hao setup mutation is not yet supported", Remediation: "rerun with --dry-run to inspect the setup plan", ExitStatus: 2}
		}
		if install != "" && install != "missing" && install != "none" {
			return commandError{Code: "invalid_usage", Message: "--install must be missing or none", Remediation: "pass --install missing or --install none", ExitStatus: 2}
		}
		path, object, err := loadConfig(deps, opts.configPath)
		if err != nil {
			return err
		}
		desired, err := resolveSetupDesired(deps, path, object, install, nonInteractive)
		if err != nil {
			return err
		}
		plan := planSetup(desired, observeSetup(cmd.Context(), deps, desired))
		if opts.json {
			err = writeJSON(cmd.OutOrStdout(), haocontractRedact(plan))
		} else {
			err = writeSetupPlan(cmd, plan, yes)
		}
		if err != nil {
			return err
		}
		if plan.Summary.Blocked > 0 {
			return commandError{Code: "setup_blocked", ExitStatus: 1, Silent: true}
		}
		return nil
	}}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "display the setup plan without changing the machine")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "never prompt for input")
	cmd.Flags().StringVar(&install, "install", "", "dependency policy: missing or none")
	cmd.Flags().BoolVar(&yes, "yes", false, "approve the displayed plan for future setup execution")
	return cmd
}

func resolveSetupDesired(deps Deps, path string, object map[string]any, install string, nonInteractive bool) (setupDesired, error) {
	if install == "" {
		install = configString(object, "install", "dependencies")
	}
	d := setupDesired{Machine: configString(object, "machine", "name"), Mode: configString(object, "mode"), AOVersion: configString(object, "components", "aoVersion"), Harness: configString(object, "harness", "id"), Profile: configString(object, "workflow", "profile"), Install: install, ServiceEnabled: configBool(object, "service", "enabled"), PairPort: configInt(object, "pair", "listenPort"), ConfigPath: path}
	if d.Profile == "" {
		d.Profile = "general"
	}
	if nonInteractive && (d.Machine == "" || d.Harness == "" || d.AOVersion == "" || d.Install == "") {
		return setupDesired{}, commandError{Code: "invalid_usage", Message: "non-interactive setup is missing required desired state", Remediation: "provide a complete v1 configuration", ExitStatus: 2}
	}
	root, err := deps.StateDir()
	if err != nil {
		return setupDesired{}, operationalError("resolve state root", err)
	}
	d.StateRoot = root
	return d, nil
}

func observeSetup(ctx context.Context, deps Deps, desired setupDesired) setupSnapshot {
	osName, arch := deps.Observer.Platform()
	s := setupSnapshot{OS: osName, Arch: arch, Directories: map[string]observedItem{}, Tools: map[string]observedItem{}, ServiceFiles: map[string]observedItem{}}
	if distro, err := deps.Observer.Distribution(); err != nil {
		s.DistributionState = "unknown"
	} else {
		s.Distribution, s.DistributionState = distro, "known"
	}
	directories := map[string]string{"directory.state": desired.StateRoot, "directory.hao": filepath.Join(desired.StateRoot, "hao"), "directory.bin": filepath.Join(desired.StateRoot, "bin"), "directory.data": filepath.Join(desired.StateRoot, "data")}
	if desired.Mode == "pair" {
		directories["directory.gateway"] = filepath.Join(desired.StateRoot, "vm-gateway")
	}
	for id, path := range directories {
		s.Directories[id] = observeFile(deps.Observer, path)
	}
	artifactPath := filepath.Join(desired.StateRoot, "bin", "ao")
	s.Artifact = observeFile(deps.Observer, artifactPath)
	if s.Artifact.State == "present" && s.Artifact.Owner && !s.Artifact.IsDir && s.Artifact.Mode.Perm()&0o022 == 0 && s.Artifact.Mode.Perm()&0o111 != 0 {
		s.Artifact.Version, s.Artifact.State, s.Artifact.Evidence = probeVersion(ctx, deps, artifactPath)
		s.Artifact.Path = artifactPath
	}
	tools := map[string]string{"git": "git", "harness": harnessBinary(desired.Harness)}
	if desired.Profile == "github" {
		tools["gh"] = "gh"
	}
	for id, binary := range tools {
		s.Tools[id] = observeTool(ctx, deps, binary)
	}
	s.PackageManager, s.ServiceManager = observePackageManager(ctx, deps, osName), observeServiceManager(ctx, deps, osName)
	if osName == "linux" {
		s.ServiceFiles["service.daemon"] = observeFile(deps.Observer, "/etc/systemd/system/hosted-ao.service")
		if desired.Mode == "pair" {
			s.ServiceFiles["service.gateway"] = observeFile(deps.Observer, "/etc/systemd/system/hosted-ao-gateway.service")
		}
	}
	return s
}

func observeFile(obs Observer, path string) observedItem {
	info, err := obs.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return observedItem{State: "absent", Evidence: "path is absent", Path: path}
	}
	if err != nil {
		return observedItem{State: "unknown", Evidence: "path probe failed: " + safeDiagnostic(err), Path: path}
	}
	return observedItem{State: "present", Evidence: fmt.Sprintf("path exists with mode %04o and uid %d", info.Mode.Perm(), info.UID), Path: path, Mode: info.Mode, UID: info.UID, Owner: info.Owner, IsDir: info.IsDir}
}

func observeTool(ctx context.Context, deps Deps, binary string) observedItem {
	path, err := deps.Observer.LookPath(binary)
	if err != nil {
		return observedItem{State: "absent", Evidence: binary + " was not found on PATH"}
	}
	version, state, evidence := probeVersion(ctx, deps, path)
	return observedItem{State: state, Evidence: evidence, Version: version, Path: path, Owner: true}
}
func probeVersion(ctx context.Context, deps Deps, path string) (string, string, string) {
	probeCtx, cancel := boundedContext(ctx, deps.Timeout)
	defer cancel()
	out, err := deps.Observer.Run(probeCtx, path, "--version")
	if err != nil {
		if probeCtx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return "", "unknown", "version probe timed out or was canceled"
		}
		return "", "unknown", "version probe failed"
	}
	return safeVersion(out), "present", "version probe succeeded"
}
func observePackageManager(ctx context.Context, deps Deps, goos string) observedItem {
	name := ""
	if goos == "linux" {
		name = "apt-get"
	}
	if goos == "darwin" {
		name = "brew"
	}
	if name != "" {
		item := observeTool(ctx, deps, name)
		if item.State != "absent" {
			return item
		}
	}
	return observedItem{State: "absent", Evidence: "no supported package manager was found"}
}
func observeServiceManager(ctx context.Context, deps Deps, goos string) observedItem {
	if goos != "linux" {
		return observedItem{State: "unsupported", Evidence: "managed service definitions are not supported on " + goos}
	}
	path, err := deps.Observer.LookPath("systemctl")
	if err != nil {
		return observedItem{State: "absent", Evidence: "systemctl was not found"}
	}
	probeCtx, cancel := boundedContext(ctx, deps.Timeout)
	defer cancel()
	if _, err := deps.Observer.Run(probeCtx, path, "show-environment"); err != nil {
		return observedItem{State: "unknown", Evidence: "systemd usability probe failed or timed out", Path: path}
	}
	return observedItem{State: "present", Evidence: "systemd is available and usable", Path: path}
}

func planSetup(d setupDesired, s setupSnapshot) SetupPlan {
	p := SetupPlan{SchemaVersion: setupPlanSchemaVersion, DryRun: true, Machine: d.Machine, Mode: d.Mode, Platform: s.OS + "/" + s.Arch, InstallPolicy: d.Install}
	add := func(step SetupStep) { p.Steps = append(p.Steps, step) }
	platformBlocked := ""
	if s.OS == "linux" && s.DistributionState == "unknown" {
		platformBlocked = "Linux distribution could not be determined"
	} else if s.OS == "linux" && s.Distribution != "ubuntu" {
		platformBlocked = "managed setup is supported only on Ubuntu Linux"
	} else if s.OS == "windows" && d.Mode == "pair" {
		platformBlocked = "pair-mode service and gateway setup is not supported on Windows"
	} else if s.OS != "linux" && s.OS != "darwin" && s.OS != "windows" {
		platformBlocked = "platform is not supported for managed setup"
	}
	if platformBlocked == "" && ((s.OS == "windows" && s.Arch != "amd64") || (s.OS != "windows" && s.Arch != "amd64" && s.Arch != "arm64")) {
		platformBlocked = "architecture is not supported for managed setup"
	}
	if platformBlocked == "" {
		add(noopStep("host.platform", "host", "verify-platform", "platform is supported for this setup mode", s.OS+"/"+s.Arch+" "+s.Distribution))
	} else {
		add(blockedStep("host.platform", "host", "verify-platform", platformBlocked, s.OS+"/"+s.Arch+" "+s.Distribution, "use a supported platform or manage the host manually"))
	}
	for _, id := range []string{"directory.state", "directory.hao", "directory.bin", "directory.data", "directory.gateway"} {
		item, ok := s.Directories[id]
		if !ok {
			continue
		}
		step := planDirectory(id, item)
		if id == "directory.state" {
			step.Dependencies = []string{"host.platform"}
		} else {
			step.Dependencies = []string{"directory.state"}
		}
		add(step)
	}
	artifact := planArtifact(d, s.Artifact)
	artifact.Dependencies = []string{"directory.bin"}
	add(artifact)
	needsPackageManager := false
	for id, item := range s.Tools {
		if id != "harness" && item.State == "absent" && d.Install == "missing" {
			needsPackageManager = true
		}
	}
	managerStep := noopStep("host.package-manager", "package-manager", "verify-capability", "no package installation is required by the observed state", s.PackageManager.Evidence)
	if s.PackageManager.State == "present" {
		managerStep.Reason = "supported package manager is available"
	} else if needsPackageManager {
		managerStep = blockedStep("host.package-manager", "package-manager", "verify-capability", "required package installation cannot be planned safely", s.PackageManager.Evidence, "restore the supported package manager or install prerequisites manually")
	}
	managerStep.Dependencies = []string{"host.platform"}
	add(managerStep)
	for _, prereq := range []struct{ id, component string }{{"git", "git"}, {"gh", "github-cli"}, {"harness", d.Harness}} {
		if item, ok := s.Tools[prereq.id]; ok {
			step := planPrerequisite(d, s, prereq.id, prereq.component, item)
			step.Dependencies = []string{"host.platform"}
			if step.Operation == "install-package" {
				step.Dependencies = append(step.Dependencies, "host.package-manager")
			}
			add(step)
		}
	}
	add(noopStep("ao.runtime", "ao-artifact", "verify-ownership", "AO runtime and terminal implementation are owned by the AO artifact", "no separate runtime backend is machine-managed"))
	if d.Mode == "pair" {
		port := noopStep("pair.port-policy", "pair-gateway", "verify-port-policy", "configured pair port is valid; activation remains an init responsibility", fmt.Sprintf("configured port %d", d.PairPort))
		if d.PairPort < 1024 {
			port.Privilege = SetupPrivilege{Required: true, Scope: "privileged-port-preparation"}
		}
		port.Dependencies = []string{"host.platform"}
		add(port)
	}
	for _, step := range planServices(d, s) {
		add(step)
	}
	propagateBlocked(p.Steps)
	for _, step := range p.Steps {
		switch step.Disposition {
		case "create":
			p.Summary.Create++
		case "update":
			p.Summary.Update++
		case "no-op":
			p.Summary.NoOp++
		case "blocked":
			p.Summary.Blocked++
		}
	}
	p.Summary.Ready = p.Summary.Blocked == 0
	return p
}

func planDirectory(id string, item observedItem) SetupStep {
	component := strings.TrimPrefix(id, "directory.")
	if item.State == "unknown" {
		return blockedStep(id, component, "reconcile-directory", "directory state is unknown", item.Evidence, "inspect the path and its parent permissions manually")
	}
	if item.State == "absent" {
		return SetupStep{ID: id, Component: component, Operation: "create-directory", Disposition: "create", Reason: "required directory is absent", Evidence: item.Evidence, Action: &SetupAction{Kind: "directory", Path: item.Path, Mode: "0700"}}
	}
	if !item.IsDir {
		return blockedStep(id, component, "reconcile-directory", "required directory path is occupied by a non-directory", item.Evidence, "move the conflicting path outside hao, then retry")
	}
	if !item.Owner {
		return blockedStep(id, component, "reconcile-directory", "existing directory has unsafe ownership", item.Evidence, "correct ownership outside hao, then retry")
	}
	if item.Mode.Perm()&0o077 != 0 {
		return SetupStep{ID: id, Component: component, Operation: "set-directory-mode", Disposition: "update", Reason: "directory permissions are broader than owner-only", Evidence: item.Evidence, Action: &SetupAction{Kind: "file-mode", Path: item.Path, Mode: "0700"}}
	}
	return noopStep(id, component, "verify-directory", "directory ownership and permissions already match", item.Evidence)
}
func planArtifact(d setupDesired, item observedItem) SetupStep {
	if item.State == "unknown" {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "reconcile-artifact", "artifact state or version is unknown", item.Evidence, "inspect the hao-owned AO artifact manually")
	}
	action := &SetupAction{Kind: "artifact-install", Path: item.Path, Argv: []string{"ao", d.AOVersion}}
	if item.State == "absent" {
		if d.Install == "none" {
			return blockedStep("artifact.ao", "ao-gateway-artifact", "manual-install", "audit-only policy forbids planning artifact installation", item.Evidence, "install the requested hao-owned AO artifact manually, then rerun setup")
		}
		return SetupStep{ID: "artifact.ao", Component: "ao-gateway-artifact", Operation: "install-artifact", Disposition: "create", Reason: "hao-owned AO/gateway artifact is absent", Evidence: item.Evidence, Action: action}
	}
	if item.IsDir {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "reconcile-artifact", "artifact path is occupied by a directory", item.Evidence, "move the conflicting directory outside hao")
	}
	if !item.Owner {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "reconcile-artifact", "existing artifact has unsafe ownership", item.Evidence, "correct or remove the conflicting artifact outside hao")
	}
	if item.Mode.Perm()&0o022 != 0 {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "reconcile-artifact", "existing artifact is writable by group or other users", item.Evidence, "secure or remove the conflicting artifact outside hao")
	}
	if item.Mode.Perm()&0o111 == 0 {
		return SetupStep{ID: "artifact.ao", Component: "ao-gateway-artifact", Operation: "replace-artifact", Disposition: "update", Reason: "existing artifact is not executable", Evidence: item.Evidence, Action: action}
	}
	if !versionMatches(item.Version, d.AOVersion) {
		if d.Install == "none" {
			return blockedStep("artifact.ao", "ao-gateway-artifact", "manual-update", "audit-only policy forbids planning artifact replacement", "observed "+item.Version+"; desired "+d.AOVersion, "install the requested hao-owned AO artifact version manually, then rerun setup")
		}
		return SetupStep{ID: "artifact.ao", Component: "ao-gateway-artifact", Operation: "replace-artifact", Disposition: "update", Reason: "installed artifact version differs from desired version", Evidence: "observed " + item.Version + "; desired " + d.AOVersion, Action: action}
	}
	return noopStep("artifact.ao", "ao-gateway-artifact", "verify-artifact", "hao-owned AO/gateway artifact version already matches", item.Version)
}
func planPrerequisite(d setupDesired, s setupSnapshot, id, component string, item observedItem) SetupStep {
	stepID := "prerequisite." + id
	if item.State == "present" {
		return noopStep(stepID, component, "verify-prerequisite", "required prerequisite is available", item.Path+" "+item.Version)
	}
	if item.State == "unknown" {
		return blockedStep(stepID, component, "verify-prerequisite", "prerequisite availability is unknown", item.Evidence, "inspect the prerequisite manually and retry")
	}
	if d.Install == "none" {
		return blockedStep(stepID, component, "manual-install", "audit-only policy forbids planning installation", item.Evidence, "install "+component+" manually, then rerun setup")
	}
	if id == "harness" && d.Harness != "claude-code" {
		return blockedStep(stepID, component, "manual-install", "selected harness has no allowlisted installer", item.Evidence, "install the selected harness using its vendor documentation")
	}
	if s.PackageManager.State != "present" {
		return blockedStep(stepID, component, "install-prerequisite", "a supported package manager is not proven usable", s.PackageManager.Evidence, "install the prerequisite manually or restore the supported package manager")
	}
	if id == "harness" {
		return SetupStep{ID: stepID, Component: component, Operation: "install-vendor-artifact", Disposition: "create", Reason: "required allowlisted harness is absent", Evidence: item.Evidence, Action: &SetupAction{Kind: "vendor-installer", Executable: "hao-installer", Argv: []string{"install", "harness", d.Harness}}}
	}
	return SetupStep{ID: stepID, Component: component, Operation: "install-package", Disposition: "create", Privilege: SetupPrivilege{Required: true, Scope: "package-install"}, Reason: "required prerequisite is absent", Evidence: item.Evidence, Action: &SetupAction{Kind: "package-manager", Executable: s.PackageManager.Path, Argv: []string{"install", id}}}
}

func planServices(d setupDesired, s setupSnapshot) []SetupStep {
	if !d.ServiceEnabled {
		steps := []SetupStep{noopStep("service.daemon", "ao-daemon-service", "leave-disabled", "service lifecycle is intentionally disabled", "configuration service.enabled=false")}
		if d.Mode == "pair" {
			step := noopStep("service.gateway", "pair-gateway-service", "leave-disabled", "gateway service lifecycle is intentionally disabled", "configuration service.enabled=false")
			step.Dependencies = []string{"service.daemon"}
			steps = append(steps, step)
		}
		return steps
	}
	if s.OS == "darwin" && d.Mode == "local" {
		return []SetupStep{noopStep("service.daemon", "desktop-supervisor", "desktop-supervised", "macOS local daemon lifecycle is owned by Hosted AO desktop", "no launchd definition is planned")}
	}
	if s.OS != "linux" || s.Distribution != "ubuntu" || s.ServiceManager.State != "present" {
		steps := []SetupStep{blockedStep("service.daemon", "ao-daemon-service", "reconcile-definition", "supported managed service lifecycle is unavailable", s.ServiceManager.Evidence, "use Ubuntu systemd or manage the daemon manually")}
		if d.Mode == "pair" {
			step := blockedStep("service.gateway", "pair-gateway-service", "reconcile-definition", "supported managed service lifecycle is unavailable", s.ServiceManager.Evidence, "use Ubuntu systemd for pair mode")
			step.Dependencies = []string{"service.daemon"}
			steps = append(steps, step)
		}
		return steps
	}
	ids := []string{"service.daemon"}
	if d.Mode == "pair" {
		ids = append(ids, "service.gateway")
	}
	steps := make([]SetupStep, 0, len(ids))
	for _, id := range ids {
		item, component, deps := s.ServiceFiles[id], "ao-daemon-service", []string{"artifact.ao", "directory.data"}
		if id == "service.gateway" {
			component, deps = "pair-gateway-service", []string{"service.daemon", "directory.gateway", "artifact.ao"}
		}
		var step SetupStep
		switch item.State {
		case "absent":
			step = SetupStep{ID: id, Component: component, Operation: "install-definition", Disposition: "create", Privilege: SetupPrivilege{Required: true, Scope: "system-service-definition"}, Reason: "supported service definition is absent", Evidence: item.Evidence, Action: &SetupAction{Kind: "service-definition", Path: item.Path}}
		case "present":
			if item.IsDir || item.UID > 0 || item.Mode.Perm()&0o022 != 0 {
				step = blockedStep(id, component, "reconcile-definition", "existing service definition has unsafe ownership", item.Evidence, "inspect and reconcile the conflicting definition manually")
			} else {
				step = noopStep(id, component, "verify-definition", "service definition is present; setup does not enable or start it", item.Evidence)
			}
		default:
			step = blockedStep(id, component, "reconcile-definition", "service definition state is unknown", item.Evidence, "inspect the service definition manually")
		}
		step.Dependencies = deps
		steps = append(steps, step)
	}
	return steps
}

func propagateBlocked(steps []SetupStep) {
	byID := make(map[string]int, len(steps))
	for i := range steps {
		byID[steps[i].ID] = i
	}
	for i := range steps {
		if steps[i].Disposition == "blocked" {
			continue
		}
		for _, dependency := range steps[i].Dependencies {
			if j, ok := byID[dependency]; ok && steps[j].Disposition == "blocked" {
				steps[i].Disposition, steps[i].Reason, steps[i].Remediation, steps[i].Action = "blocked", "dependency "+dependency+" is blocked", "resolve the blocked dependency, then rerun setup", nil
				break
			}
		}
	}
}
func noopStep(id, component, operation, reason, evidence string) SetupStep {
	return SetupStep{ID: id, Component: component, Operation: operation, Disposition: "no-op", Reason: reason, Evidence: evidence}
}
func blockedStep(id, component, operation, reason, evidence, remediation string) SetupStep {
	return SetupStep{ID: id, Component: component, Operation: operation, Disposition: "blocked", Reason: reason, Evidence: evidence, Remediation: remediation}
}
func versionMatches(observed, desired string) bool {
	for _, field := range strings.Fields(observed) {
		if strings.TrimPrefix(field, "v") == desired {
			return true
		}
	}
	return false
}

func writeSetupPlan(cmd *cobra.Command, plan SetupPlan, yes bool) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "HAO setup plan (dry-run): %s (%s) on %s\nInstall policy: %s\n", redactedString(plan.Machine), redactedString(plan.Mode), redactedString(plan.Platform), plan.InstallPolicy); err != nil {
		return err
	}
	for _, step := range plan.Steps {
		privilege := ""
		if step.Privilege.Required {
			privilege = " [privileged:" + step.Privilege.Scope + "]"
		}
		if _, err := fmt.Fprintf(out, "%-24s %-8s %s%s — %s\n", step.ID+":", step.Disposition, step.Operation, privilege, redactedString(step.Reason)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "  evidence: %s\n", redactedString(step.Evidence)); err != nil {
			return err
		}
		if step.Remediation != "" {
			if _, err := fmt.Fprintf(out, "  remediation: %s\n", redactedString(step.Remediation)); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintf(out, "Summary: create=%d update=%d no-op=%d blocked=%d ready=%t\n", plan.Summary.Create, plan.Summary.Update, plan.Summary.NoOp, plan.Summary.Blocked, plan.Summary.Ready); err != nil {
		return err
	}
	if yes {
		_, err := fmt.Fprintln(out, "Note: --yes has no mutation effect in dry-run; it will approve the displayed plan only when setup execution exists.")
		return err
	}
	return nil
}
