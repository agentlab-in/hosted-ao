package haocli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const setupJournalSchemaVersion = 1

var (
	errSetupBusy        = errors.New("another hao setup invocation is active")
	errPrivilegeRefused = errors.New("required setup privilege was unavailable or refused")
)

// SetupExecutionOptions carries interaction policy into one approved run.
type SetupExecutionOptions struct {
	NonInteractive bool      `json:"nonInteractive"`
	Input          io.Reader `json:"-"`
	DataDir        string    `json:"-"`
}

// SetupExecutionResult reports completed, rollback, recovery, and retry facts.
type SetupExecutionResult struct {
	Status    string   `json:"status"`
	Completed []string `json:"completed"`
	Rollback  []string `json:"rollback"`
	Recovery  []string `json:"recovery"`
	Retry     string   `json:"retry"`
}

type setupExecutionSystem interface {
	Download(context.Context, string) (io.ReadCloser, error)
	CheckPrivilege(context.Context, bool, io.Reader) error
	Run(context.Context, bool, bool, io.Reader, string, ...string) error
}

type systemSetupExecution struct{}

func (systemSetupExecution) Download(ctx context.Context, source string) (io.ReadCloser, error) {
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return nil, errors.New("download source is not a valid HTTPS URL")
	}
	client := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: allowArtifactRedirect}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("artifact download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxArtifactSize {
		_ = resp.Body.Close()
		return nil, errors.New("artifact download exceeds size limit")
	}
	return resp.Body, nil
}

func allowArtifactRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 3 {
		return errors.New("artifact download exceeded redirect limit")
	}
	if len(via) == 0 || via[0].URL.Scheme != "https" || via[0].URL.Hostname() != "github.com" || !strings.HasPrefix(via[0].URL.EscapedPath(), "/agentlab-in/hosted-ao/releases/download/v") || req.URL.Scheme != "https" || req.URL.Hostname() != "release-assets.githubusercontent.com" {
		return errors.New("artifact download redirected to an untrusted host")
	}
	return nil
}

func (systemSetupExecution) CheckPrivilege(ctx context.Context, nonInteractive bool, in io.Reader) error {
	if runningAsRoot() {
		return nil
	}
	if runtime.GOOS != "linux" {
		return errPrivilegeRefused
	}
	const sudo = "/usr/bin/sudo"
	if _, err := os.Stat(sudo); err != nil {
		return errPrivilegeRefused
	}
	args := []string{"--validate"}
	if nonInteractive {
		args = []string{"--non-interactive", "--validate"}
	}
	cmd := exec.CommandContext(ctx, sudo, args...)
	cmd.Stdin = in
	cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
	cmd.Env = []string{"LANG=C", "LC_ALL=C", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	if err := cmd.Run(); err != nil {
		return errPrivilegeRefused
	}
	return nil
}

func (systemSetupExecution) Run(ctx context.Context, privileged, nonInteractive bool, in io.Reader, name string, args ...string) error {
	command, argv := name, append([]string(nil), args...)
	if privileged && !runningAsRoot() {
		const sudo = "/usr/bin/sudo"
		if _, err := os.Stat(sudo); err != nil {
			return errPrivilegeRefused
		}
		prefix := make([]string, 0, 2)
		if nonInteractive {
			prefix = append(prefix, "--non-interactive")
		}
		prefix = append(prefix, "--")
		command, argv = sudo, append(append(prefix, name), argv...)
	}
	cmd := exec.CommandContext(ctx, command, argv...)
	cmd.Stdin = in
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if privileged {
		cmd.Env = []string{"LANG=C", "LC_ALL=C", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("structured command failed: %w", err)
	}
	return nil
}

func runningAsRoot() bool {
	current, err := user.Current()
	return err == nil && current.Uid == "0"
}

type setupJournal struct {
	SchemaVersion int             `json:"schemaVersion"`
	PlanSHA256    string          `json:"planSha256"`
	State         string          `json:"state"`
	StartedAt     string          `json:"startedAt"`
	Records       []journalRecord `json:"records"`
}

type journalRecord struct {
	StepID         string `json:"stepId"`
	Kind           string `json:"kind"`
	Target         string `json:"target,omitempty"`
	Backup         string `json:"backup,omitempty"`
	ManifestTarget string `json:"manifestTarget,omitempty"`
	ManifestBackup string `json:"manifestBackup,omitempty"`
	Temporary      string `json:"temporary,omitempty"`
	PreviousMode   uint32 `json:"previousMode,omitempty"`
	Created        bool   `json:"created,omitempty"`
	BackupReady    bool   `json:"backupReady,omitempty"`
	Completed      bool   `json:"completed"`
	Reversible     bool   `json:"reversible"`
}

type setupExecutor struct {
	system         setupExecutionSystem
	stateRoot      string
	dataDir        string
	journalPath    string
	backupDir      string
	options        SetupExecutionOptions
	journal        setupJournal
	journalStarted bool
	result         SetupExecutionResult
}

func executeSetupPlan(ctx context.Context, plan SetupPlan, stateRoot string, options SetupExecutionOptions) (SetupExecutionResult, error) {
	return executeSetupPlanWithSystem(ctx, plan, stateRoot, options, systemSetupExecution{})
}

func executeSetupPlanWithSystem(ctx context.Context, plan SetupPlan, stateRoot string, options SetupExecutionOptions, system setupExecutionSystem) (SetupExecutionResult, error) {
	dataDir := options.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(stateRoot, "data")
	}
	if err := validateExecutablePlan(plan, stateRoot, dataDir); err != nil {
		return SetupExecutionResult{}, commandError{Code: "invalid_setup_plan", Message: "setup plan cannot be executed", Remediation: "rerun hao setup to produce a fresh trusted plan", Details: map[string]any{"diagnostic": safeDiagnostic(err)}, ExitStatus: 1, Cause: err}
	}
	lockName := fmt.Sprintf("hao-setup-%x.lock", sha256.Sum256([]byte(filepath.Clean(stateRoot))))
	lock, err := acquireSetupLock(filepath.Join(os.TempDir(), lockName))
	if err != nil {
		if errors.Is(err, errSetupBusy) {
			return SetupExecutionResult{}, commandError{Code: "setup_busy", Message: "another hao setup invocation is active", Remediation: "wait for the active setup to finish, then retry", Details: map[string]any{}, ExitStatus: 1, Cause: err}
		}
		return SetupExecutionResult{}, operationalError("acquire setup transaction lock", err)
	}
	defer func() { _ = releaseSetupLock(lock) }()

	e := &setupExecutor{
		system:      system,
		stateRoot:   filepath.Clean(stateRoot),
		dataDir:     filepath.Clean(dataDir),
		journalPath: filepath.Join(stateRoot, ".hao-setup-transaction.json"),
		options:     options,
		result: SetupExecutionResult{
			Completed: []string{},
			Rollback:  []string{},
			Recovery:  []string{},
		},
	}
	if planNeedsPrivilege(plan) {
		if err := system.CheckPrivilege(ctx, options.NonInteractive, options.Input); err != nil {
			return e.result, commandError{Code: "privilege_required", Message: "setup requires narrowly scoped administrator privileges", Remediation: "approve the displayed privileged operations, or arrange them manually", Details: map[string]any{"completed": []string{}, "rollback": []string{}, "retry": "hao setup --dry-run"}, ExitStatus: 3, Cause: errPrivilegeRefused}
		}
	}
	expectedRecovery := ""
	for _, step := range plan.Steps {
		if step.Action != nil && step.Action.Kind == "transaction-recovery-v1" {
			expectedRecovery = step.Action.Recovery.JournalSHA256
		}
	}
	if recovered, recoverErr := e.recover(ctx, expectedRecovery); recoverErr != nil {
		return e.result, e.failure("recover interrupted setup", recoverErr)
	} else if recovered != "" {
		e.result.Recovery = append(e.result.Recovery, recovered)
		e.result.Completed = append(e.result.Completed, "transaction.recovery")
	}
	planBytes, err := json.Marshal(plan)
	if err != nil {
		return e.result, operationalError("serialize setup transaction", err)
	}
	e.journal = setupJournal{SchemaVersion: setupJournalSchemaVersion, PlanSHA256: digestBytes(planBytes), State: "in_progress", StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	e.backupDir = filepath.Join(stateRoot, "hao", "backups", e.journal.PlanSHA256[:16]+"-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	for _, step := range plan.Steps {
		if step.Action == nil {
			continue
		}
		if step.Action.Kind == "transaction-recovery-v1" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return e.result, e.rollbackFailure(ctx, step.ID, err)
		}
		if err := e.apply(ctx, step); err != nil {
			return e.result, e.rollbackFailure(ctx, step.ID, err)
		}
		e.result.Completed = append(e.result.Completed, step.ID)
	}
	if e.journalStarted {
		e.journal.State = "completed"
		if err := e.writeJournal(); err != nil {
			return e.result, e.rollbackFailure(ctx, "transaction.commit", err)
		}
		if err := e.cleanupServiceBackups(ctx); err != nil {
			return e.result, e.failure("clean committed service backups", err)
		}
		if err := managedRemove(e.journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return e.result, e.failure("remove completed setup journal", err)
		}
		_ = syncDirectory(filepath.Dir(e.journalPath))
	}
	e.result.Status = "completed"
	e.result.Retry = "hao setup --dry-run"
	return e.result, nil
}

func validateExecutablePlan(plan SetupPlan, stateRoot, dataDir string) error {
	if plan.SchemaVersion != setupPlanSchemaVersion || plan.DryRun {
		return errors.New("plan is not an executable schema v1 plan")
	}
	if err := validateSetupPlan(plan); err != nil {
		return err
	}
	root := filepath.Clean(stateRoot)
	dataRoot := filepath.Clean(dataDir)
	if !filepath.IsAbs(root) || !filepath.IsAbs(dataRoot) {
		return errors.New("state root and data directory must be absolute")
	}
	seen := make(map[string]struct{}, len(plan.Steps))
	for _, step := range plan.Steps {
		if step.ID == "" {
			return errors.New("setup plan contains an empty step ID")
		}
		if _, duplicate := seen[step.ID]; duplicate {
			return fmt.Errorf("setup plan contains duplicate step %s", step.ID)
		}
		for _, dependency := range step.Dependencies {
			if _, ready := seen[dependency]; !ready {
				return fmt.Errorf("step %s has an unresolved or out-of-order dependency", step.ID)
			}
		}
		seen[step.ID] = struct{}{}
		if step.Action == nil {
			continue
		}
		a := step.Action
		if (a.Kind == "service-definition-v1" || a.Kind == "package-manager") != step.Privilege.Required && a.Kind != "transaction-recovery-v1" {
			return fmt.Errorf("step %s has incorrect privilege metadata", step.ID)
		}
		switch a.Kind {
		case "directory", "file-mode":
			expected := managedDirectoryPath(root, dataRoot, step.ID)
			if expected == "" || filepath.Clean(a.File.Path) != expected || (step.ID == "directory.parent" && a.Kind != "directory") {
				return fmt.Errorf("step %s targets an unmanaged file", step.ID)
			}
		case "verified-release-artifact":
			if step.ID != "artifact.ao" || filepath.Clean(a.Artifact.Path) != filepath.Join(root, "bin", "ao") {
				return fmt.Errorf("step %s targets an unmanaged artifact", step.ID)
			}
		case "service-definition-v1":
			expected := map[string]string{"service.daemon": "/etc/systemd/system/ao-daemon.service", "service.gateway": "/etc/systemd/system/ao-gateway.service"}[step.ID]
			if expected == "" || a.Service.Path != expected {
				return fmt.Errorf("step %s targets an unmanaged service definition", step.ID)
			}
		case "package-manager":
			if a.Package.Executable != "/usr/bin/apt-get" || !allowedPackageArgv(step.ID, a.Package.Argv, a.Package.Version) {
				return fmt.Errorf("step %s has a non-allowlisted package command", step.ID)
			}
		case "vendor-package":
			if step.ID != "prerequisite.harness" || a.Vendor.Executable != "/usr/bin/npm" || !allowedVendorArgv(a.Vendor.Argv, a.Vendor.Version) {
				return fmt.Errorf("step %s has a non-allowlisted vendor command", step.ID)
			}
		case "transaction-recovery-v1":
			if step.ID != "transaction.recovery" {
				return errors.New("recovery action has an invalid step")
			}
			expected, found, err := planSetupRecovery(root, dataRoot)
			if err != nil || !found || expected.Action.Recovery.JournalSHA256 != a.Recovery.JournalSHA256 || expected.Privilege.Required != step.Privilege.Required {
				return errors.New("recovery action does not match the current interrupted transaction")
			}
		}
	}
	return nil
}

func managedDirectoryPath(root, dataDir, stepID string) string {
	return map[string]string{
		"directory.parent":  filepath.Dir(root),
		"directory.state":   root,
		"directory.hao":     filepath.Join(root, "hao"),
		"directory.bin":     filepath.Join(root, "bin"),
		"directory.data":    dataDir,
		"directory.gateway": filepath.Join(root, "vm-gateway"),
	}[stepID]
}

func allowedPackageArgv(stepID string, argv []string, version string) bool {
	if len(argv) != 3 || argv[0] != "install" || argv[1] != "--yes" || argv[2] != "{verified-file}" {
		return false
	}
	expectedName := map[string]string{"prerequisite.git": "git", "prerequisite.gh": "gh"}[stepID]
	return expectedName != "" && version != ""
}

func allowedVendorArgv(argv []string, version string) bool {
	return version != "" && len(argv) == 3 && argv[0] == "install" && argv[1] == "--global" && argv[2] == "{verified-file}"
}

func planNeedsPrivilege(plan SetupPlan) bool {
	for _, step := range plan.Steps {
		if step.Action != nil && step.Privilege.Required {
			return true
		}
	}
	return false
}

func (e *setupExecutor) apply(ctx context.Context, step SetupStep) error {
	a := step.Action
	switch a.Kind {
	case "directory":
		return e.createDirectory(step.ID, a.File)
	case "file-mode":
		return e.changeMode(step.ID, a.File)
	case "verified-release-artifact":
		return e.installArtifact(ctx, step.ID, step.Disposition, a.Artifact)
	case "service-definition-v1":
		return e.installService(ctx, step.ID, step.Disposition, a.Service)
	case "package-manager":
		return e.runVerifiedPackage(ctx, step.ID, a.Kind, true, a.Package.Executable, a.Package.Argv, a.Package.Source, a.Package.SHA256)
	case "vendor-package":
		return e.runVerifiedPackage(ctx, step.ID, a.Kind, false, a.Vendor.Executable, a.Vendor.Argv, a.Vendor.Source, a.Vendor.SHA256)
	default:
		return fmt.Errorf("unsupported action kind %q", a.Kind)
	}
}

func (e *setupExecutor) createDirectory(stepID string, action *SetupFileAction) error {
	if err := ensureNoSymlinkAncestors(filepath.Dir(action.Path)); err != nil {
		return err
	}
	info, err := managedLstat(action.Path)
	if err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("directory target is occupied or linked")
		}
		if stepID == "directory.parent" {
			return nil
		}
		return managedChmod(action.Path, 0o700)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := managedMkdir(action.Path, 0o700); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(action.Path)); err != nil {
		return err
	}
	record := journalRecord{StepID: stepID, Kind: "directory", Target: action.Path, Created: true, Completed: true, Reversible: false}
	return e.record(record)
}

func (e *setupExecutor) changeMode(stepID string, action *SetupFileAction) error {
	if err := ensureNoSymlinkAncestors(action.Path); err != nil {
		return err
	}
	info, err := managedLstat(action.Path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("file mode target is unavailable or linked")
	}
	record := journalRecord{StepID: stepID, Kind: "file-mode", Target: action.Path, PreviousMode: uint32(info.Mode().Perm()), Reversible: true}
	if err := e.record(record); err != nil {
		return err
	}
	if err := managedChmod(action.Path, 0o700); err != nil {
		return err
	}
	return e.completeLastRecord()
}

func (e *setupExecutor) installArtifact(ctx context.Context, stepID, disposition string, action *SetupArtifactAction) error {
	if err := ensureNoSymlinkAncestors(action.Path); err != nil {
		return err
	}
	currentDigest, digestErr := fileDigest(action.Path, maxArtifactSize)
	binaryMatches := digestErr == nil && currentDigest == action.SHA256
	info, targetErr := managedLstat(action.Path)
	if disposition == "create" && targetErr == nil {
		if binaryMatches && info.Mode().Perm() == 0o700 && artifactManifestMatches(action) {
			return nil
		}
		return errors.New("artifact target appeared after planning; rerun setup")
	}
	if disposition == "update" && errors.Is(targetErr, os.ErrNotExist) {
		return errors.New("artifact target disappeared after planning; rerun setup")
	}
	if disposition == "update" {
		if digestErr != nil || currentDigest != action.ExpectedSHA256 {
			return errors.New("artifact changed after planning; rerun setup")
		}
		if targetErr != nil || uint32(info.Mode().Perm()) != action.ExpectedMode {
			return errors.New("artifact mode changed after planning; rerun setup")
		}
	}
	if binaryMatches && artifactManifestMatches(action) {
		return nil
	}
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return targetErr
	}
	if err := managedMkdirAll(e.backupDir, 0o700); err != nil {
		return err
	}
	temporary, err := randomTemporaryPath(filepath.Dir(action.Path), ".hao-artifact-")
	if err != nil {
		return err
	}
	record := journalRecord{StepID: stepID, Kind: "verified-release-artifact", Target: action.Path, ManifestTarget: action.Path + ".hao-manifest.json", Temporary: temporary, Reversible: true}
	if targetErr == nil {
		info, err := managedLstat(action.Path)
		if err != nil {
			return err
		}
		record.PreviousMode = uint32(info.Mode().Perm())
		record.Backup = filepath.Join(e.backupDir, safeStepName(stepID)+".binary")
		if err := copyRegularFile(action.Path, record.Backup, os.FileMode(record.PreviousMode)); err != nil {
			return err
		}
	} else {
		record.Created = true
	}
	if _, err := managedLstat(record.ManifestTarget); err == nil {
		record.ManifestBackup = filepath.Join(e.backupDir, safeStepName(stepID)+".manifest")
		if err := copyRegularFile(record.ManifestTarget, record.ManifestBackup, 0o600); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := e.record(record); err != nil {
		return err
	}
	if !binaryMatches {
		if err := downloadVerifiedArtifact(ctx, e.system, action, temporary); err != nil {
			return err
		}
	}
	if err := writeArtifactManifest(action); err != nil {
		return err
	}
	return e.completeLastRecord()
}

func downloadVerifiedArtifact(ctx context.Context, system setupExecutionSystem, action *SetupArtifactAction, tempName string) error {
	if err := downloadVerifiedFile(ctx, system, action.Source, action.SHA256, tempName, 0o700); err != nil {
		return err
	}
	if err := managedRename(tempName, action.Path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(action.Path))
}

func downloadVerifiedFile(ctx context.Context, system setupExecutionSystem, source, expectedSHA256, tempName string, mode os.FileMode) error {
	body, err := system.Download(ctx, source)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	temp, err := managedCreateExclusive(tempName, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(body, maxArtifactSize+1))
	if copyErr != nil {
		_ = temp.Close()
		return fmt.Errorf("artifact download interrupted: %w", copyErr)
	}
	if written > maxArtifactSize {
		_ = temp.Close()
		return errors.New("artifact download exceeds size limit")
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expectedSHA256 {
		_ = temp.Close()
		return fmt.Errorf("artifact digest mismatch: expected %s, received %s", expectedSHA256, actual)
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return nil
}

func writeArtifactManifest(action *SetupArtifactAction) error {
	data, err := artifactManifestBytes(action)
	if err != nil {
		return err
	}
	return atomicWriteFile(action.Path+".hao-manifest.json", data, 0o600)
}

func artifactManifestBytes(action *SetupArtifactAction) ([]byte, error) {
	data, err := json.Marshal(ArtifactMetadata{Version: action.Version, Source: action.Source, SHA256: action.SHA256})
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func artifactManifestMatches(action *SetupArtifactAction) bool {
	want, err := artifactManifestBytes(action)
	if err != nil {
		return false
	}
	file, err := openManagedRegular(action.Path+".hao-manifest.json", 1<<20)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	got, err := io.ReadAll(io.LimitReader(file, int64(len(want)+1)))
	return err == nil && bytes.Equal(got, want)
}

func (e *setupExecutor) installService(ctx context.Context, stepID, disposition string, action *SetupServiceAction) error {
	digest, digestErr := fileDigest(action.Path, 1<<20)
	info, targetErr := managedLstat(action.Path)
	if targetErr == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0) {
		return errors.New("service target is not a safe regular unlinked file")
	}
	if disposition == "create" && targetErr == nil {
		return errors.New("service target appeared after planning; rerun setup")
	}
	if disposition == "update" && errors.Is(targetErr, os.ErrNotExist) {
		return errors.New("service target disappeared after planning; rerun setup")
	}
	if disposition == "update" && !fileDigestEquals(digest, digestErr, action.ExpectedSHA256) {
		return errors.New("service definition changed after planning; rerun setup")
	}
	if disposition == "update" && targetErr == nil && uint32(info.Mode().Perm()) != action.ExpectedMode {
		return errors.New("service definition mode changed after planning; rerun setup")
	}
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return targetErr
	}
	if digestErr == nil && digest == action.SHA256 {
		return nil
	}
	targetTemp, err := randomTemporaryPath(filepath.Dir(action.Path), ".hao-service-")
	if err != nil {
		return err
	}
	record := journalRecord{StepID: stepID, Kind: "service-definition-v1", Target: action.Path, Temporary: targetTemp, Reversible: true}
	if targetErr == nil {
		record.Backup, err = randomTemporaryPath(filepath.Dir(action.Path), ".hao-backup-")
		if err != nil {
			return err
		}
	} else {
		record.Created = true
	}
	if err := e.record(record); err != nil {
		return err
	}
	if record.Backup != "" {
		if err := e.system.Run(ctx, true, e.options.NonInteractive, e.options.Input, "/usr/bin/cp", "--no-dereference", "--preserve=mode,ownership,timestamps", "--", action.Path, record.Backup); err != nil {
			return err
		}
		e.journal.Records[len(e.journal.Records)-1].BackupReady = true
		if err := e.writeJournal(); err != nil {
			return err
		}
	}
	if err := e.system.Run(ctx, true, e.options.NonInteractive, bytes.NewReader([]byte(action.Content)), "/usr/bin/tee", "--", targetTemp); err != nil {
		return err
	}
	if err := e.system.Run(ctx, true, e.options.NonInteractive, e.options.Input, "/usr/bin/chmod", "0644", "--", targetTemp); err != nil {
		return err
	}
	if err := e.system.Run(ctx, true, e.options.NonInteractive, e.options.Input, "/usr/bin/chown", "root:root", "--", targetTemp); err != nil {
		return err
	}
	if err := e.system.Run(ctx, true, e.options.NonInteractive, e.options.Input, "/usr/bin/mv", "--no-target-directory", "--", targetTemp, action.Path); err != nil {
		return err
	}
	return e.completeLastRecord()
}

func fileDigestEquals(actual string, err error, expected string) bool {
	return err == nil && actual == expected
}

func (e *setupExecutor) runVerifiedPackage(ctx context.Context, stepID, kind string, privileged bool, executable string, argv []string, source, sha256Digest string) error {
	temporary, err := randomTemporaryPath(filepath.Join(e.stateRoot, "hao"), ".hao-package-")
	if err != nil {
		return err
	}
	record := journalRecord{StepID: stepID, Kind: kind, Temporary: temporary, Reversible: false}
	if err := e.record(record); err != nil {
		return err
	}
	defer func() { _ = removeManagedIfExists(temporary) }()
	if err := downloadVerifiedFile(ctx, e.system, source, sha256Digest, temporary, 0o600); err != nil {
		return err
	}
	localArgv := append([]string(nil), argv...)
	localArgv[len(localArgv)-1] = temporary
	if err := e.system.Run(ctx, privileged, e.options.NonInteractive, e.options.Input, executable, localArgv...); err != nil {
		return err
	}
	return e.completeLastRecord()
}

func (e *setupExecutor) record(record journalRecord) error {
	e.journal.Records = append(e.journal.Records, record)
	if _, err := os.Stat(filepath.Dir(e.journalPath)); err == nil {
		e.journalStarted = true
		return e.writeJournal()
	}
	return nil
}

func (e *setupExecutor) completeLastRecord() error {
	e.journal.Records[len(e.journal.Records)-1].Completed = true
	if e.journalStarted {
		return e.writeJournal()
	}
	return nil
}

func (e *setupExecutor) writeJournal() error {
	data, err := json.Marshal(e.journal)
	if err != nil {
		return err
	}
	return atomicWriteFile(e.journalPath, append(data, '\n'), 0o600)
}

func (e *setupExecutor) rollbackFailure(ctx context.Context, stepID string, cause error) error {
	rollback, complete := e.rollback(ctx)
	e.result.Rollback = append(e.result.Rollback, rollback...)
	e.result.Status = "failed"
	e.result.Retry = "hao setup --dry-run, resolve the reported failure, then rerun hao setup"
	if e.journalStarted {
		if complete {
			e.journal.State = "rolled_back"
		}
		_ = e.writeJournal()
	}
	return e.failure("execute setup step "+stepID, cause)
}

func (e *setupExecutor) failure(operation string, cause error) error {
	diagnostic := safeDiagnostic(cause)
	retry := e.result.Retry
	if retry == "" {
		retry = "inspect the transaction journal and reported diagnostic, then rerun hao setup --dry-run"
		e.result.Retry = retry
	}
	return commandError{Code: "setup_failed", Message: operation + " failed; completed actions and rollback are reported in details", Remediation: retry, Details: map[string]any{"completed": e.result.Completed, "rollback": e.result.Rollback, "recovery": e.result.Recovery, "retry": retry, "diagnostic": diagnostic}, ExitStatus: 1, Cause: errors.New(diagnostic)}
}

func (e *setupExecutor) rollback(ctx context.Context) ([]string, bool) {
	results := make([]string, 0)
	complete := true
	for i := len(e.journal.Records) - 1; i >= 0; i-- {
		record := e.journal.Records[i]
		if (record.Kind == "package-manager" || record.Kind == "vendor-package") && record.Temporary != "" {
			_ = removeManagedIfExists(record.Temporary)
		}
		if !record.Reversible {
			continue
		}
		var err error
		switch record.Kind {
		case "file-mode":
			if err = ensureNoSymlinkAncestors(record.Target); err == nil {
				err = managedChmod(record.Target, os.FileMode(record.PreviousMode))
			}
		case "verified-release-artifact":
			if record.Temporary != "" {
				_ = removeManagedIfExists(record.Temporary)
			}
			mode := os.FileMode(record.PreviousMode)
			if record.Created {
				mode = 0o700
			}
			err = rollbackManagedFile(record.Target, record.Backup, record.Created, mode)
			if manifestErr := rollbackManagedFile(record.ManifestTarget, record.ManifestBackup, record.ManifestBackup == "", 0o600); err == nil {
				err = manifestErr
			}
		case "service-definition-v1":
			if record.Temporary != "" {
				_ = e.system.Run(ctx, true, e.options.NonInteractive, e.options.Input, "/usr/bin/rm", "--force", "--", record.Temporary)
			}
			if record.Backup != "" && record.BackupReady {
				err = e.restoreServiceBackup(ctx, record)
			} else if record.Backup != "" {
				err = e.system.Run(ctx, true, e.options.NonInteractive, e.options.Input, "/usr/bin/rm", "--force", "--", record.Backup)
			} else if record.Created {
				err = e.system.Run(ctx, true, e.options.NonInteractive, e.options.Input, "/usr/bin/rm", "--force", "--", record.Target)
			}
		}
		status := record.StepID + ": restored"
		if err != nil {
			status = record.StepID + ": rollback failed: " + safeDiagnostic(err)
			complete = false
		}
		results = append(results, status)
	}
	if complete {
		if err := e.cleanupServiceBackups(ctx); err != nil {
			results = append(results, "service backups: cleanup failed: "+safeDiagnostic(err))
			complete = false
		}
	}
	return results, complete
}

func rollbackManagedFile(target, backup string, created bool, mode os.FileMode) error {
	if backup != "" {
		return copyRegularFile(backup, target, mode)
	}
	if created {
		if err := managedRemove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(filepath.Dir(target))
	}
	return nil
}

func (e *setupExecutor) restoreServiceBackup(ctx context.Context, record journalRecord) error {
	if _, err := managedLstat(record.Backup); errors.Is(err, os.ErrNotExist) {
		return errors.New("service backup is unavailable")
	} else if err != nil {
		return err
	}
	if err := e.system.Run(ctx, true, e.options.NonInteractive, e.options.Input, "/usr/bin/cp", "--no-dereference", "--preserve=mode,ownership,timestamps", "--", record.Backup, record.Temporary); err != nil {
		return err
	}
	return e.system.Run(ctx, true, e.options.NonInteractive, e.options.Input, "/usr/bin/mv", "--no-target-directory", "--", record.Temporary, record.Target)
}

func (e *setupExecutor) cleanupServiceBackups(ctx context.Context) error {
	for _, record := range e.journal.Records {
		if record.Kind == "service-definition-v1" && record.Backup != "" {
			if err := e.system.Run(ctx, true, e.options.NonInteractive, e.options.Input, "/usr/bin/rm", "--force", "--", record.Backup); err != nil {
				return err
			}
		}
	}
	return nil
}

func planSetupRecovery(stateRoot string, dataDirs ...string) (SetupStep, bool, error) {
	dataDir := filepath.Join(stateRoot, "data")
	if len(dataDirs) > 0 && dataDirs[0] != "" {
		dataDir = dataDirs[0]
	}
	e := setupExecutor{stateRoot: filepath.Clean(stateRoot), dataDir: filepath.Clean(dataDir), journalPath: filepath.Join(stateRoot, ".hao-setup-transaction.json")}
	journal, digest, found, err := e.readJournal()
	if err != nil || !found {
		return SetupStep{}, found, err
	}
	e.journal = journal
	if err := e.validateJournal(); err != nil {
		return SetupStep{}, true, err
	}
	privileged := false
	for _, record := range journal.Records {
		privileged = privileged || record.Kind == "service-definition-v1"
	}
	return SetupStep{
		ID: "transaction.recovery", Component: "setup-transaction", Operation: "recover-interrupted-setup", Disposition: "update",
		Privilege: SetupPrivilege{Required: privileged, Scope: "system-service-definition-recovery"},
		Reason:    "an interrupted setup transaction must be rolled back before retry", Evidence: "journal sha256 " + digest,
		Action: &SetupAction{SchemaVersion: 1, Kind: "transaction-recovery-v1", Recovery: &SetupRecoveryAction{JournalSHA256: digest}},
	}, true, nil
}

func (e *setupExecutor) readJournal() (setupJournal, string, bool, error) {
	info, err := managedLstat(e.journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return setupJournal{}, "", false, nil
	}
	if err != nil {
		return setupJournal{}, "", true, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return setupJournal{}, "", true, errors.New("setup transaction journal is not a regular file")
	}
	journalFile, err := openManagedRegular(e.journalPath, 1<<20)
	if err != nil {
		return setupJournal{}, "", true, err
	}
	defer func() { _ = journalFile.Close() }()
	data, err := io.ReadAll(io.LimitReader(journalFile, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return setupJournal{}, "", true, errors.New("setup transaction journal exceeds size limit")
	}
	var journal setupJournal
	if err := json.Unmarshal(data, &journal); err != nil || journal.SchemaVersion != setupJournalSchemaVersion {
		return setupJournal{}, "", true, errors.New("setup transaction journal is invalid; inspect it manually before retrying")
	}
	return journal, digestBytes(data), true, nil
}

func (e *setupExecutor) recover(ctx context.Context, expectedDigest string) (string, error) {
	journal, digest, found, err := e.readJournal()
	if err != nil {
		return "", err
	}
	if !found {
		if expectedDigest != "" {
			return "", errors.New("approved recovery journal disappeared; rerun setup")
		}
		return "", nil
	}
	if expectedDigest == "" || digest != expectedDigest {
		return "", errors.New("setup recovery journal changed after planning; rerun setup")
	}
	e.journal = journal
	if err := e.validateJournal(); err != nil {
		return "", fmt.Errorf("setup transaction journal is unsafe: %w", err)
	}
	e.journalStarted = true
	if e.journal.State == "in_progress" {
		var complete bool
		e.result.Rollback, complete = e.rollback(ctx)
		if !complete {
			return "", errors.New("interrupted setup rollback is incomplete; retry to resume rollback")
		}
		e.journal.State = "recovered"
		if err := e.writeJournal(); err != nil {
			return "", err
		}
	}
	if e.journal.State != "recovered" && e.journal.State != "rolled_back" && e.journal.State != "completed" {
		return "", errors.New("setup transaction journal has an unsupported state")
	}
	if err := e.cleanupServiceBackups(ctx); err != nil {
		return "", err
	}
	if err := managedRemove(e.journalPath); err != nil {
		return "", err
	}
	e.journalStarted = false
	return "previous interrupted setup was rolled back; safe retry started", nil
}

func (e *setupExecutor) validateJournal() error {
	if !validSHA256(e.journal.PlanSHA256) || len(e.journal.Records) > 64 {
		return errors.New("journal identity or record count is invalid")
	}
	for _, record := range e.journal.Records {
		switch record.Kind {
		case "directory":
			if record.Target != managedDirectoryPath(e.stateRoot, e.dataDir, record.StepID) || record.Reversible || !record.Created {
				return fmt.Errorf("step %s has an invalid directory record", record.StepID)
			}
		case "file-mode":
			if record.Target != managedDirectoryPath(e.stateRoot, e.dataDir, record.StepID) || !record.Reversible || record.Created {
				return fmt.Errorf("step %s has an invalid mode record", record.StepID)
			}
		case "verified-release-artifact":
			target := filepath.Join(e.stateRoot, "bin", "ao")
			backupCoherent := !record.BackupReady && ((record.Created && record.Backup == "") || (!record.Created && record.Backup != ""))
			modeCoherent := (record.Created && record.PreviousMode == 0) || (!record.Created && record.PreviousMode&0o111 != 0 && record.PreviousMode&0o022 == 0)
			if record.StepID != "artifact.ao" || record.Target != target || record.ManifestTarget != target+".hao-manifest.json" || !record.Reversible || !backupCoherent || !modeCoherent || !temporaryPathMatches(record.Temporary, filepath.Dir(target), ".hao-artifact-") || !optionalPathBelow(record.Backup, filepath.Join(e.stateRoot, "hao", "backups")) || !optionalPathBelow(record.ManifestBackup, filepath.Join(e.stateRoot, "hao", "backups")) {
				return errors.New("artifact record contains an unmanaged path")
			}
		case "service-definition-v1":
			expected := map[string]string{"service.daemon": "/etc/systemd/system/ao-daemon.service", "service.gateway": "/etc/systemd/system/ao-gateway.service"}[record.StepID]
			backupCoherent := (record.Created && record.Backup == "" && !record.BackupReady) || (!record.Created && record.Backup != "")
			if expected == "" || record.Target != expected || !record.Reversible || !backupCoherent || !temporaryPathMatches(record.Temporary, filepath.Dir(expected), ".hao-service-") || (record.Backup != "" && !temporaryPathMatches(record.Backup, filepath.Dir(expected), ".hao-backup-")) {
				return errors.New("service record contains an unmanaged path")
			}
		case "package-manager":
			if record.Reversible || !temporaryPathMatches(record.Temporary, filepath.Join(e.stateRoot, "hao"), ".hao-package-") || (record.StepID != "prerequisite.git" && record.StepID != "prerequisite.gh") {
				return errors.New("package record has an invalid step")
			}
		case "vendor-package":
			if record.Reversible || !temporaryPathMatches(record.Temporary, filepath.Join(e.stateRoot, "hao"), ".hao-package-") || record.StepID != "prerequisite.harness" {
				return errors.New("vendor record has an invalid step")
			}
		default:
			return fmt.Errorf("step %s has an unsupported journal kind", record.StepID)
		}
	}
	return nil
}

func optionalPathBelow(path, root string) bool {
	if path == "" {
		return true
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func temporaryPathMatches(path, directory, prefix string) bool {
	if filepath.Dir(path) != directory {
		return false
	}
	name := strings.TrimPrefix(filepath.Base(path), prefix)
	if len(name) != 32 {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}

func ensureNoSymlinkAncestors(path string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return errors.New("managed path is not absolute")
	}
	current := string(os.PathSeparator)
	parts := strings.Split(strings.TrimPrefix(clean, string(os.PathSeparator)), string(os.PathSeparator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed path traverses symbolic link %s", current)
		}
	}
	return nil
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	if err := ensureNoSymlinkAncestors(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := managedLstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed write target is not a regular unlinked file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tempName, err := randomTemporaryPath(filepath.Dir(path), ".hao-write-")
	if err != nil {
		return err
	}
	temp, err := managedCreateExclusive(tempName, mode)
	if err != nil {
		return err
	}
	defer func() { _ = removeManagedIfExists(tempName) }()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := managedRename(tempName, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func managedMkdirAll(path string, mode os.FileMode) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return errors.New("managed directory path is not absolute")
	}
	current := string(os.PathSeparator)
	for _, part := range strings.FieldsFunc(strings.TrimPrefix(clean, string(os.PathSeparator)), func(r rune) bool { return r == filepath.Separator }) {
		current = filepath.Join(current, part)
		info, err := managedLstat(current)
		if err == nil {
			if !info.IsDir() {
				return errors.New("managed directory path is occupied")
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := managedMkdir(current, mode); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return nil
}

func removeManagedIfExists(path string) error {
	err := managedRemove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	in, err := openManagedRegular(source, maxArtifactSize)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	data, err := io.ReadAll(io.LimitReader(in, maxArtifactSize+1))
	if err != nil || len(data) > maxArtifactSize {
		return errors.New("managed backup exceeds size limit")
	}
	return atomicWriteFile(destination, data, mode)
}

func fileDigest(path string, limit int64) (string, error) {
	file, err := openManagedRegular(path, limit)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if written > limit {
		return "", errors.New("managed file exceeds size limit")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func safeStepName(value string) string {
	return strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(value)
}

func randomTemporaryPath(directory, prefix string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return filepath.Join(directory, prefix+hex.EncodeToString(random)), nil
}
