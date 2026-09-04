package haocli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	SchemaVersion int                  `json:"schemaVersion"`
	Kind          string               `json:"kind"`
	File          *SetupFileAction     `json:"file,omitempty"`
	Package       *SetupPackageAction  `json:"package,omitempty"`
	Artifact      *SetupArtifactAction `json:"artifact,omitempty"`
	Vendor        *SetupVendorAction   `json:"vendor,omitempty"`
	Service       *SetupServiceAction  `json:"service,omitempty"`
}

// SetupFileAction is the payload for one managed file operation.
type SetupFileAction struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// SetupPackageAction is the payload for an allowlisted system package operation.
type SetupPackageAction struct {
	Executable string   `json:"executable"`
	Argv       []string `json:"argv"`
	Version    string   `json:"version"`
	Source     string   `json:"source"`
	SHA256     string   `json:"sha256"`
}

// SetupArtifactAction identifies one immutable release artifact.
type SetupArtifactAction struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	Source  string `json:"source"`
	SHA256  string `json:"sha256"`
}

// SetupVendorAction identifies one immutable vendor package.
type SetupVendorAction struct {
	Executable string   `json:"executable"`
	Argv       []string `json:"argv"`
	Version    string   `json:"version"`
	Source     string   `json:"source"`
	SHA256     string   `json:"sha256"`
}

// SetupServiceAction contains a complete canonical service definition.
type SetupServiceAction struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	Version string `json:"version"`
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
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
	OS, Arch                                            string
}

type observedItem struct {
	State, Evidence, Version, Path, Source, SHA256 string
	Mode                                           os.FileMode
	UID                                            int
	Owner                                          bool
	IsDir                                          bool
	Link                                           bool
	Trusted                                        bool
}

type setupSnapshot struct {
	OS, Arch, Distribution, DistributionState string
	Directories                               map[string]observedItem
	Artifact                                  observedItem
	Tools                                     map[string]observedItem
	PackageManager, ServiceManager            observedItem
	ServiceFiles                              map[string]observedItem
	User                                      UserObservation
	UserState                                 string
}

func newSetupCommand(deps Deps, opts *options) *cobra.Command {
	var dryRun, nonInteractive, yes bool
	var install string
	cmd := &cobra.Command{Use: "setup", Short: "Plan machine preparation", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		// This guard intentionally precedes config loading and every observation.
		if !dryRun {
			return commandError{Code: "feature_deferred", Message: "hao setup mutation is not yet supported", Remediation: "rerun with --dry-run to inspect the setup plan", ExitStatus: 4}
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
		serializedPlan, err := json.Marshal(plan)
		if err != nil {
			return operationalError("serialize setup plan", err)
		}
		if _, err := consumeSetupPlanJSON(serializedPlan); err != nil {
			return operationalError("validate setup plan", err)
		}
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
	if currentUser, err := deps.Observer.CurrentUser(); err != nil {
		s.UserState = "unknown"
	} else {
		s.User, s.UserState = currentUser, "known"
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
	parentsSafe := true
	for _, id := range []string{"directory.state", "directory.bin"} {
		item := s.Directories[id]
		parentsSafe = parentsSafe && item.State == "present" && item.IsDir && !item.Link && item.Owner
	}
	if !parentsSafe && s.Artifact.State == "present" {
		s.Artifact.State, s.Artifact.Evidence = "unknown", "managed artifact ancestry is not a proven owned non-link directory chain"
	}
	if s.Artifact.State == "present" && s.Artifact.Owner && !s.Artifact.IsDir && !s.Artifact.Link && s.Artifact.Mode.Perm()&0o022 == 0 && s.Artifact.Mode.Perm()&0o111 != 0 {
		metadata, err := deps.Observer.InspectArtifact(ctx, artifactPath)
		if err != nil {
			s.Artifact.State, s.Artifact.Evidence = "unknown", "artifact provenance probe failed: "+safeDiagnostic(err)
		} else {
			s.Artifact.Version, s.Artifact.Source, s.Artifact.SHA256 = metadata.Version, metadata.Source, metadata.SHA256
			s.Artifact.Evidence = "observed hao artifact provenance from " + metadata.Source
			if deps.TrustedArtifact != nil {
				trusted, ok := deps.TrustedArtifact(osName, arch, desired.AOVersion)
				s.Artifact.Trusted = ok && matchesTrustedArtifact(metadata, trusted)
			}
		}
		s.Artifact.Path = artifactPath
	}
	tools := map[string]string{"git": "git", "harness": harnessBinary(desired.Harness)}
	if desired.Profile == "github" {
		tools["gh"] = "gh"
	}
	for id, binary := range tools {
		s.Tools[id] = observeTool(deps, binary)
	}
	s.PackageManager, s.ServiceManager = observePackageManager(deps, osName), observeServiceManager(deps, osName)
	if osName == "linux" {
		s.ServiceFiles["service.daemon"] = observeServiceFile(deps.Observer, "/etc/systemd/system/ao-daemon.service")
		if desired.Mode == "pair" {
			s.ServiceFiles["service.gateway"] = observeServiceFile(deps.Observer, "/etc/systemd/system/ao-gateway.service")
		}
	}
	return s
}

func matchesTrustedArtifact(observed, trusted ArtifactMetadata) bool {
	return trusted.Version == observed.Version && trusted.Source == observed.Source && strings.EqualFold(trusted.SHA256, observed.SHA256) && validSHA256(strings.ToLower(trusted.SHA256)) && strings.Contains(trusted.Source, "/releases/download/v"+trusted.Version+"/")
}

func observeFile(obs Observer, path string) observedItem {
	info, err := obs.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return observedItem{State: "absent", Evidence: "path is absent", Path: path}
	}
	if err != nil {
		return observedItem{State: "unknown", Evidence: "path probe failed: " + safeDiagnostic(err), Path: path}
	}
	return observedItem{State: "present", Evidence: fmt.Sprintf("path exists with mode %04o and uid %d", info.Mode.Perm(), info.UID), Path: path, Mode: info.Mode, UID: info.UID, Owner: info.Owner, IsDir: info.IsDir, Link: info.Link}
}

func observeServiceFile(obs Observer, path string) observedItem {
	item := observeFile(obs, path)
	if item.State != "present" || item.Link || item.IsDir {
		return item
	}
	data, err := obs.ReadFile(path)
	if err != nil {
		item.State, item.Evidence = "unknown", "service definition read failed: "+safeDiagnostic(err)
		return item
	}
	item.Version = string(data)
	return item
}

func observeTool(deps Deps, binary string) observedItem {
	path, err := deps.Observer.LookPath(binary)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return observedItem{State: "unknown", Evidence: binary + " path lookup failed: " + safeDiagnostic(err)}
		}
		return observedItem{State: "absent", Evidence: binary + " was not found on PATH"}
	}
	return observedItem{State: "present", Evidence: "executable path is present (not executed)", Path: path, Owner: true}
}
func observePackageManager(deps Deps, goos string) observedItem {
	name := ""
	if goos == "linux" {
		name = "apt-get"
	}
	if goos == "darwin" {
		name = "brew"
	}
	if name != "" {
		item := observeTool(deps, name)
		if item.State != "absent" {
			return item
		}
	}
	return observedItem{State: "absent", Evidence: "no supported package manager was found"}
}
func observeServiceManager(deps Deps, goos string) observedItem {
	if goos != "linux" {
		return observedItem{State: "unsupported", Evidence: "managed service definitions are not supported on " + goos}
	}
	path, err := deps.Observer.LookPath("systemctl")
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return observedItem{State: "unknown", Evidence: "systemctl path lookup failed: " + safeDiagnostic(err)}
		}
		return observedItem{State: "absent", Evidence: "systemctl was not found"}
	}
	return observedItem{State: "present", Evidence: "systemd executable is present (not executed)", Path: path}
}

func planSetup(d setupDesired, s setupSnapshot) SetupPlan {
	d.OS, d.Arch = s.OS, s.Arch
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
		return SetupStep{ID: id, Component: component, Operation: "create-directory", Disposition: "create", Reason: "required directory is absent", Evidence: item.Evidence, Action: &SetupAction{SchemaVersion: 1, Kind: "directory", File: &SetupFileAction{Path: item.Path, Mode: "0700"}}}
	}
	if !item.IsDir {
		return blockedStep(id, component, "reconcile-directory", "required directory path is occupied by a non-directory", item.Evidence, "move the conflicting path outside hao, then retry")
	}
	if item.Link {
		return blockedStep(id, component, "reconcile-directory", "managed directory path is a symbolic link", item.Evidence, "replace the link with a directory inside the HAO state root")
	}
	if !item.Owner {
		return blockedStep(id, component, "reconcile-directory", "existing directory has unsafe ownership", item.Evidence, "correct ownership outside hao, then retry")
	}
	if item.Mode.Perm()&0o077 != 0 {
		return SetupStep{ID: id, Component: component, Operation: "set-directory-mode", Disposition: "update", Reason: "directory permissions are broader than owner-only", Evidence: item.Evidence, Action: &SetupAction{SchemaVersion: 1, Kind: "file-mode", File: &SetupFileAction{Path: item.Path, Mode: "0700"}}}
	}
	return noopStep(id, component, "verify-directory", "directory ownership and permissions already match", item.Evidence)
}
func planArtifact(d setupDesired, item observedItem) SetupStep {
	if item.State == "unknown" {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "reconcile-artifact", "artifact state or version is unknown", item.Evidence, "inspect the hao-owned AO artifact manually")
	}
	if item.State == "absent" {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "manual-install", "trusted immutable release metadata is unavailable", item.Evidence, "install the requested hao-owned AO artifact manually, then rerun setup")
	}
	if item.IsDir {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "reconcile-artifact", "artifact path is occupied by a directory", item.Evidence, "move the conflicting directory outside hao")
	}
	if item.Link {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "reconcile-artifact", "managed artifact path is a symbolic link", item.Evidence, "replace the link with a regular hao-owned artifact")
	}
	if !item.Owner {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "reconcile-artifact", "existing artifact has unsafe ownership", item.Evidence, "correct or remove the conflicting artifact outside hao")
	}
	if item.Mode.Perm()&0o022 != 0 {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "reconcile-artifact", "existing artifact is writable by group or other users", item.Evidence, "secure or remove the conflicting artifact outside hao")
	}
	if item.Mode.Perm()&0o111 == 0 {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "manual-update", "trusted immutable release metadata is unavailable for replacement", item.Evidence, "replace the artifact manually from a trusted release")
	}
	if !item.Trusted || !validSHA256(strings.ToLower(item.SHA256)) || !strings.Contains(item.Source, "/releases/download/v"+item.Version+"/") {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "verify-provenance", "trusted immutable release metadata is unavailable", item.Evidence, "verify the artifact against trusted immutable release metadata")
	}
	if !versionMatches(item.Version, d.AOVersion) {
		return blockedStep("artifact.ao", "ao-gateway-artifact", "manual-update", "trusted immutable metadata for the desired artifact is unavailable", "observed "+item.Version+"; desired "+d.AOVersion, "install the requested artifact version manually from a trusted release")
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
	if id == "harness" {
		return blockedStep(stepID, component, "manual-install", "trusted immutable vendor package metadata is unavailable", item.Evidence, "install the selected harness from pinned trusted vendor metadata")
	}
	if s.PackageManager.State != "present" {
		return blockedStep(stepID, component, "install-prerequisite", "a supported package manager is not proven usable", s.PackageManager.Evidence, "install the prerequisite manually or restore the supported package manager")
	}
	return blockedStep(stepID, component, "manual-install", "trusted immutable package metadata is unavailable", item.Evidence, "install "+component+" manually from pinned trusted package metadata")
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
	if s.UserState != "known" || s.User.Name == "" || s.User.UID == 0 || s.User.Home == "" {
		steps := []SetupStep{blockedStep("service.daemon", "ao-daemon-service", "reconcile-definition", "target unprivileged user could not be determined", "user observation is "+s.UserState, "rerun as the target non-root user")}
		if d.Mode == "pair" {
			step := blockedStep("service.gateway", "pair-gateway-service", "reconcile-definition", "target unprivileged user could not be determined", "user observation is "+s.UserState, "rerun as the target non-root user")
			step.Dependencies = []string{"service.daemon"}
			steps = append(steps, step)
		}
		return steps
	}
	if !systemdInputsSafe(d, s.User) {
		steps := []SetupStep{blockedStep("service.daemon", "ao-daemon-service", "reconcile-definition", "service definition inputs contain unsupported control characters or user syntax", "canonical systemd rendering refused unsafe input", "use conventional target-user and state paths without control characters")}
		if d.Mode == "pair" {
			step := blockedStep("service.gateway", "pair-gateway-service", "reconcile-definition", "service definition inputs contain unsupported control characters or user syntax", "canonical systemd rendering refused unsafe input", "use conventional target-user and state paths without control characters")
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
		desiredContent := renderSystemdDefinition(id, d, s.User)
		digest := sha256.Sum256([]byte(desiredContent))
		action := &SetupAction{SchemaVersion: 1, Kind: "service-definition-v1", Service: &SetupServiceAction{Path: item.Path, Mode: "0644", Version: "1", Content: desiredContent, SHA256: fmt.Sprintf("%x", digest[:])}}
		switch item.State {
		case "absent":
			step = SetupStep{ID: id, Component: component, Operation: "install-definition", Disposition: "create", Privilege: SetupPrivilege{Required: true, Scope: "system-service-definition"}, Reason: "supported service definition is absent", Evidence: item.Evidence, Action: action}
		case "present":
			if item.Link || item.IsDir || item.UID > 0 || item.Mode.Perm()&0o022 != 0 {
				step = blockedStep(id, component, "reconcile-definition", "existing service definition has unsafe ownership", item.Evidence, "inspect and reconcile the conflicting definition manually")
			} else if item.Version != desiredContent {
				step = SetupStep{ID: id, Component: component, Operation: "update-definition", Disposition: "update", Privilege: SetupPrivilege{Required: true, Scope: "system-service-definition"}, Reason: "service definition content differs from desired canonical v1 content", Evidence: "canonical content mismatch", Action: action}
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

func renderSystemdDefinition(id string, d setupDesired, user UserObservation) string {
	dataDir, runFile, binary := filepath.Join(d.StateRoot, "data"), filepath.Join(d.StateRoot, "running.json"), filepath.Join(d.StateRoot, "bin", "ao")
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=Hosted AO ")
	if id == "service.gateway" {
		b.WriteString("pair gateway\nAfter=network-online.target ao-daemon.service\nRequires=ao-daemon.service\n")
	} else {
		b.WriteString("daemon\nAfter=network-online.target\n")
	}
	path := filepath.Join(user.Home, ".local", "bin") + ":" + filepath.Join(user.Home, "bin") + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	b.WriteString("\n[Service]\nType=simple\nUser=" + user.Name + "\nWorkingDirectory=" + systemdQuote(dataDir) + "\nEnvironment=" + systemdQuote("HOME="+user.Home) + "\nEnvironment=" + systemdQuote("PATH="+path) + "\n")
	if id == "service.gateway" {
		b.WriteString("Environment=" + systemdQuote("AO_VM_PAIR=on") + "\nEnvironment=" + systemdQuote("AO_VM_HTTPS_ADDR=:"+strconv.Itoa(d.PairPort)) + "\nEnvironment=" + systemdQuote("AO_VM_CERT_DIR="+filepath.Join(d.StateRoot, "vm-gateway", "pair-cert")) + "\nEnvironment=" + systemdQuote("AO_VM_PASSCODE_DIR="+filepath.Join(d.StateRoot, "vm-gateway", "pair-passcode")) + "\n")
		if d.PairPort < 1024 {
			b.WriteString("AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\n")
		}
		b.WriteString("ExecStart=" + systemdQuote(binary) + " vm serve\n")
	} else {
		b.WriteString("Environment=" + systemdQuote("AO_DATA_DIR="+dataDir) + "\nEnvironment=" + systemdQuote("AO_RUN_FILE="+runFile) + "\nExecStart=" + systemdQuote(binary) + " daemon\n")
	}
	b.WriteString("Restart=on-failure\n\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

type systemdDefinitionConsumer struct {
	Environment          map[string]string
	HasNetBindCapability bool
	ExecStart            string
}

// consumeSystemdDefinition parses the exact subset emitted by the planner and
// verifies the gateway/daemon values a later service-file writer would use.
func consumeSystemdDefinition(content string) (systemdDefinitionConsumer, error) {
	parsed := systemdDefinitionConsumer{Environment: map[string]string{}}
	ambient, bounding := false, false
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "Environment="):
			value, err := strconv.Unquote(strings.TrimPrefix(line, "Environment="))
			if err != nil {
				return parsed, fmt.Errorf("parse systemd environment: %w", err)
			}
			key, value, ok := strings.Cut(value, "=")
			if !ok || key == "" {
				return parsed, errors.New("invalid systemd environment entry")
			}
			parsed.Environment[key] = value
		case strings.HasPrefix(line, "ExecStart="):
			parsed.ExecStart = strings.TrimPrefix(line, "ExecStart=")
		case line == "AmbientCapabilities=CAP_NET_BIND_SERVICE":
			ambient = true
		case line == "CapabilityBoundingSet=CAP_NET_BIND_SERVICE":
			bounding = true
		}
	}
	if parsed.ExecStart == "" {
		return parsed, errors.New("systemd definition has no ExecStart")
	}
	pathValue := parsed.Environment["PATH"]
	if pathValue == "" {
		return parsed, errors.New("systemd definition has no controlled PATH")
	}
	for _, entry := range filepath.SplitList(pathValue) {
		if !filepath.IsAbs(entry) {
			return parsed, errors.New("systemd PATH contains a non-absolute entry")
		}
	}
	parsed.HasNetBindCapability = ambient && bounding
	if ambient != bounding {
		return parsed, errors.New("systemd network bind capability is incomplete")
	}
	if addr := parsed.Environment["AO_VM_HTTPS_ADDR"]; addr != "" {
		_, rawPort, err := net.SplitHostPort(addr)
		if err != nil {
			return parsed, fmt.Errorf("parse gateway HTTPS address: %w", err)
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return parsed, errors.New("gateway HTTPS port is invalid")
		}
		if (port < 1024) != parsed.HasNetBindCapability {
			return parsed, errors.New("gateway capability does not match HTTPS port privilege")
		}
		if !strings.HasSuffix(parsed.ExecStart, " vm serve") {
			return parsed, errors.New("gateway ExecStart does not invoke vm serve")
		}
	}
	return parsed, nil
}

func lookPathInService(name, pathValue string) (string, error) {
	if name == "" || filepath.Base(name) != name {
		return "", errors.New("service executable name must be a base name")
	}
	for _, directory := range filepath.SplitList(pathValue) {
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

var systemdUserPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func systemdInputsSafe(d setupDesired, user UserObservation) bool {
	if !systemdUserPattern.MatchString(user.Name) {
		return false
	}
	for _, value := range []string{user.Home, d.StateRoot} {
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				return false
			}
		}
	}
	return true
}

func systemdQuote(value string) string { return strconv.Quote(strings.ReplaceAll(value, "%", "%%")) }

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

// validateSetupPlan is the non-mutating consumer boundary for the future executor.
// It rejects ambiguous action variants without opening files, executing programs,
// contacting networks, or changing machine state.
func validateSetupPlan(plan SetupPlan) error {
	for _, step := range plan.Steps {
		a := step.Action
		if a == nil {
			if step.Disposition == "create" || step.Disposition == "update" {
				return fmt.Errorf("step %s is executable but has no action", step.ID)
			}
			continue
		}
		if step.Disposition != "create" && step.Disposition != "update" {
			return fmt.Errorf("step %s has an action for non-executable disposition %q", step.ID, step.Disposition)
		}
		if a.SchemaVersion != 1 {
			return fmt.Errorf("step %s has unsupported action schema", step.ID)
		}
		variants := 0
		for _, present := range []bool{a.File != nil, a.Package != nil, a.Artifact != nil, a.Vendor != nil, a.Service != nil} {
			if present {
				variants++
			}
		}
		if variants != 1 {
			return fmt.Errorf("step %s action must contain exactly one variant", step.ID)
		}
		switch a.Kind {
		case "directory", "file-mode":
			if a.File == nil || !filepath.IsAbs(a.File.Path) || (a.File.Mode != "0700" && a.File.Mode != "0644") {
				return fmt.Errorf("step %s has incomplete file action", step.ID)
			}
		case "package-manager":
			if a.Package == nil || !filepath.IsAbs(a.Package.Executable) || len(a.Package.Argv) == 0 || a.Package.Version == "" || a.Package.Version == "latest" || a.Package.Source == "" || !validSHA256(a.Package.SHA256) || !strings.Contains(a.Package.Source, a.Package.Version) || !strings.Contains(strings.Join(a.Package.Argv, "\x00"), a.Package.Version) {
				return fmt.Errorf("step %s has mutable package provenance", step.ID)
			}
		case "vendor-package":
			if a.Vendor == nil || !filepath.IsAbs(a.Vendor.Executable) || len(a.Vendor.Argv) == 0 || a.Vendor.Source == "" || a.Vendor.Version == "" || a.Vendor.Version == "latest" || !validSHA256(a.Vendor.SHA256) || !strings.Contains(a.Vendor.Source, a.Vendor.Version) || !strings.Contains(strings.Join(a.Vendor.Argv, "\x00"), "@"+a.Vendor.Version) {
				return fmt.Errorf("step %s has mutable vendor provenance", step.ID)
			}
		case "verified-release-artifact":
			if a.Artifact == nil || !filepath.IsAbs(a.Artifact.Path) || a.Artifact.Version == "" || a.Artifact.Source == "" || !validSHA256(a.Artifact.SHA256) || !strings.Contains(a.Artifact.Source, a.Artifact.Version) || strings.Contains(a.Artifact.Source, "/latest/") {
				return fmt.Errorf("step %s has incomplete artifact provenance", step.ID)
			}
		case "service-definition-v1":
			if a.Service == nil {
				return fmt.Errorf("step %s has no service definition variant", step.ID)
			}
			digest := sha256.Sum256([]byte(a.Service.Content))
			if !filepath.IsAbs(a.Service.Path) || a.Service.Mode != "0644" || a.Service.Version != "1" || a.Service.Content == "" || a.Service.SHA256 != fmt.Sprintf("%x", digest[:]) {
				return fmt.Errorf("step %s has invalid service definition contract", step.ID)
			}
			if _, err := consumeSystemdDefinition(a.Service.Content); err != nil {
				return fmt.Errorf("step %s has unusable service definition: %w", step.ID, err)
			}
		default:
			return fmt.Errorf("step %s has unsupported action kind %q", step.ID, a.Kind)
		}
	}
	return nil
}

// consumeSetupPlanJSON is the planning-only consumer boundary. It strictly
// decodes, validates, and renders every action variant without performing it.
func consumeSetupPlanJSON(data []byte) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var plan SetupPlan
	if err := decoder.Decode(&plan); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("setup plan contains trailing JSON")
	}
	if plan.SchemaVersion != setupPlanSchemaVersion || !plan.DryRun {
		return nil, errors.New("unsupported or executable setup plan")
	}
	if err := validateSetupPlan(plan); err != nil {
		return nil, err
	}
	operations := make([]string, 0)
	for _, step := range plan.Steps {
		if step.Action == nil {
			continue
		}
		a := step.Action
		switch a.Kind {
		case "verified-release-artifact":
			operations = append(operations, fmt.Sprintf("artifact source=%s version=%s sha256=%s path=%s", a.Artifact.Source, a.Artifact.Version, a.Artifact.SHA256, a.Artifact.Path))
		case "vendor-package":
			operations = append(operations, fmt.Sprintf("vendor source=%s version=%s sha256=%s", a.Vendor.Source, a.Vendor.Version, a.Vendor.SHA256))
		case "service-definition-v1":
			operations = append(operations, fmt.Sprintf("service path=%s mode=%s version=%s sha256=%s", a.Service.Path, a.Service.Mode, a.Service.Version, a.Service.SHA256))
		case "directory", "file-mode":
			operations = append(operations, fmt.Sprintf("file kind=%s path=%s mode=%s", a.Kind, a.File.Path, a.File.Mode))
		case "package-manager":
			operations = append(operations, fmt.Sprintf("package executable=%s source=%s version=%s sha256=%s argv=%q", a.Package.Executable, a.Package.Source, a.Package.Version, a.Package.SHA256, a.Package.Argv))
		}
	}
	return operations, nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
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
