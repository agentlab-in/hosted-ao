package haocli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type setupCommandCall struct {
	Privileged bool
	Executable string
	Argv       []string
	Input      string
}

type fakeSetupExecutionSystem struct {
	payload        []byte
	downloadErr    error
	reader         io.ReadCloser
	privilegeErr   error
	runErr         error
	calls          []setupCommandCall
	downloadCalls  int
	downloadStart  chan struct{}
	downloadResume chan struct{}
	mu             sync.Mutex
}

func (f *fakeSetupExecutionSystem) Download(context.Context, string) (io.ReadCloser, error) {
	f.mu.Lock()
	f.downloadCalls++
	f.mu.Unlock()
	if f.downloadStart != nil {
		select {
		case f.downloadStart <- struct{}{}:
		default:
		}
		<-f.downloadResume
	}
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	if f.reader != nil {
		return f.reader, nil
	}
	return io.NopCloser(bytes.NewReader(f.payload)), nil
}

func (f *fakeSetupExecutionSystem) CheckPrivilege(context.Context, bool, io.Reader) error {
	return f.privilegeErr
}

func (f *fakeSetupExecutionSystem) Run(_ context.Context, privileged, _ bool, input io.Reader, executable string, argv ...string) error {
	var inputData []byte
	if input != nil && executable == "/usr/bin/tee" {
		inputData, _ = io.ReadAll(input)
	}
	f.mu.Lock()
	f.calls = append(f.calls, setupCommandCall{Privileged: privileged, Executable: executable, Argv: append([]string(nil), argv...), Input: string(inputData)})
	f.mu.Unlock()
	return f.runErr
}

func executablePlan(steps ...SetupStep) SetupPlan {
	return SetupPlan{SchemaVersion: setupPlanSchemaVersion, DryRun: false, Machine: "test", Mode: "local", Platform: "linux/amd64", InstallPolicy: "missing", Steps: steps}
}

func artifactStep(root string, payload []byte, disposition string) SetupStep {
	digest := sha256.Sum256(payload)
	version := "0.14.0"
	return SetupStep{ID: "artifact.ao", Disposition: disposition, Action: &SetupAction{SchemaVersion: 1, Kind: "verified-release-artifact", Artifact: &SetupArtifactAction{
		Path: filepath.Join(root, "bin", "ao"), Version: version,
		Source: "https://github.com/agentlab-in/hosted-ao/releases/download/v" + version + "/ao-linux-x64",
		SHA256: fmt.Sprintf("%x", digest[:]),
	}}}
}

func preparedExecutorRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "hao"), filepath.Join(root, "bin")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func commandErrorCode(t *testing.T, err error) string {
	t.Helper()
	var typed commandError
	if !errors.As(err, &typed) {
		t.Fatalf("error type=%T value=%v", err, err)
	}
	return typed.Code
}

func TestSetupExecutorVerifiesArtifactAndReplayIsNoOp(t *testing.T) {
	root := preparedExecutorRoot(t)
	payload := []byte("verified ao artifact")
	step := artifactStep(root, payload, "create")
	system := &fakeSetupExecutionSystem{payload: payload}
	result, err := executeSetupPlanWithSystem(context.Background(), executablePlan(step), root, SetupExecutionOptions{NonInteractive: true}, system)
	if err != nil || result.Status != "completed" || !reflect.DeepEqual(result.Completed, []string{"artifact.ao"}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	binary, err := os.ReadFile(step.Action.Artifact.Path)
	if err != nil || !bytes.Equal(binary, payload) {
		t.Fatalf("installed artifact=%q err=%v", binary, err)
	}
	metadata, err := inspectArtifact(context.Background(), step.Action.Artifact.Path)
	if err != nil || metadata.Version != "0.14.0" || metadata.SHA256 != step.Action.Artifact.SHA256 {
		t.Fatalf("metadata=%+v err=%v", metadata, err)
	}
	if _, err := executeSetupPlanWithSystem(context.Background(), executablePlan(step), root, SetupExecutionOptions{NonInteractive: true}, system); err != nil {
		t.Fatalf("replay failed: %v", err)
	}
	if system.downloadCalls != 1 {
		t.Fatalf("replay downloaded artifact %d times", system.downloadCalls)
	}
}

func TestSetupExecutorRejectsDigestMismatchAndInterruptedDownloadBeforeReplacement(t *testing.T) {
	tests := []struct {
		name   string
		system *fakeSetupExecutionSystem
	}{
		{name: "digest mismatch", system: &fakeSetupExecutionSystem{payload: []byte("wrong")}},
		{name: "interrupted", system: &fakeSetupExecutionSystem{reader: io.NopCloser(errorAfterReader{data: []byte("partial"), err: io.ErrUnexpectedEOF})}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := preparedExecutorRoot(t)
			step := artifactStep(root, []byte("expected"), "create")
			result, err := executeSetupPlanWithSystem(context.Background(), executablePlan(step), root, SetupExecutionOptions{}, tc.system)
			if err == nil || commandErrorCode(t, err) != "setup_failed" || result.Status != "failed" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if _, statErr := os.Stat(step.Action.Artifact.Path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed download replaced target: %v", statErr)
			}
		})
	}
}

type errorAfterReader struct {
	data []byte
	err  error
}

func (r errorAfterReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		return copy(p, r.data), r.err
	}
	return 0, r.err
}

func TestSetupExecutorPrivilegeRefusalIsExitThreeBeforeMutation(t *testing.T) {
	root := preparedExecutorRoot(t)
	action := &SetupPackageAction{Executable: "/usr/bin/apt-get", Argv: []string{"install", "--yes", "git=1.2.3"}, Version: "1.2.3", Source: "https://packages.example.invalid/git-1.2.3.deb", SHA256: strings.Repeat("a", 64)}
	plan := executablePlan(SetupStep{ID: "prerequisite.git", Disposition: "create", Privilege: SetupPrivilege{Required: true, Scope: "system-package"}, Action: &SetupAction{SchemaVersion: 1, Kind: "package-manager", Package: action}})
	system := &fakeSetupExecutionSystem{privilegeErr: errPrivilegeRefused}
	result, err := executeSetupPlanWithSystem(context.Background(), plan, root, SetupExecutionOptions{NonInteractive: true}, system)
	var typed commandError
	if !errors.As(err, &typed) || typed.ExitStatus != 3 || typed.Code != "privilege_required" || len(result.Completed) != 0 || len(system.calls) != 0 {
		t.Fatalf("result=%+v error=%+v calls=%+v", result, typed, system.calls)
	}
}

func TestSetupExecutorRunsOnlyAllowlistedStructuredPackageAndVendorArgv(t *testing.T) {
	root := preparedExecutorRoot(t)
	digest := strings.Repeat("a", 64)
	packageAction := &SetupPackageAction{Executable: "/usr/bin/apt-get", Argv: []string{"install", "--yes", "git=1.2.3"}, Version: "1.2.3", Source: "https://packages.example.invalid/git-1.2.3.deb", SHA256: digest}
	vendorAction := &SetupVendorAction{Executable: "/usr/bin/npm", Argv: []string{"install", "--global", "@anthropic-ai/claude-code@2.3.4"}, Version: "2.3.4", Source: "https://registry.example.invalid/claude-code-2.3.4.tgz", SHA256: digest}
	plan := executablePlan(
		SetupStep{ID: "prerequisite.git", Disposition: "create", Privilege: SetupPrivilege{Required: true, Scope: "system-package"}, Action: &SetupAction{SchemaVersion: 1, Kind: "package-manager", Package: packageAction}},
		SetupStep{ID: "prerequisite.harness", Disposition: "create", Action: &SetupAction{SchemaVersion: 1, Kind: "vendor-package", Vendor: vendorAction}},
	)
	system := &fakeSetupExecutionSystem{}
	if _, err := executeSetupPlanWithSystem(context.Background(), plan, root, SetupExecutionOptions{NonInteractive: true}, system); err != nil {
		t.Fatal(err)
	}
	want := []setupCommandCall{
		{Privileged: true, Executable: "/usr/bin/apt-get", Argv: []string{"install", "--yes", "git=1.2.3"}},
		{Privileged: false, Executable: "/usr/bin/npm", Argv: []string{"install", "--global", "@anthropic-ai/claude-code@2.3.4"}},
	}
	if !reflect.DeepEqual(system.calls, want) {
		t.Fatalf("calls=%+v want=%+v", system.calls, want)
	}

	hostile := executablePlan(SetupStep{ID: "bad", Disposition: "create", Action: &SetupAction{SchemaVersion: 1, Kind: "vendor-package", Vendor: &SetupVendorAction{Executable: "/bin/sh", Argv: []string{"-c", "id"}, Version: "1", Source: "https://example.invalid/tool-1", SHA256: digest}}})
	if _, err := executeSetupPlanWithSystem(context.Background(), hostile, root, SetupExecutionOptions{}, system); err == nil || commandErrorCode(t, err) != "invalid_setup_plan" {
		t.Fatalf("hostile command accepted: %v", err)
	}
}

func TestSetupExecutorUsesAtomicStructuredServiceDefinitionCommands(t *testing.T) {
	root := preparedExecutorRoot(t)
	desired := setupDesired{StateRoot: root, PairPort: 443}
	content := renderSystemdDefinition("service.gateway", desired, UserObservation{Name: "agent", UID: 1000, Home: "/home/agent"})
	digest := sha256.Sum256([]byte(content))
	action := &SetupServiceAction{Path: "/etc/systemd/system/ao-gateway.service", Mode: "0644", Version: "1", Content: content, SHA256: fmt.Sprintf("%x", digest[:])}
	step := SetupStep{ID: "service.gateway", Disposition: "create", Privilege: SetupPrivilege{Required: true, Scope: "system-service-definition"}, Action: &SetupAction{SchemaVersion: 1, Kind: "service-definition-v1", Service: action}}
	system := &fakeSetupExecutionSystem{}
	result, err := executeSetupPlanWithSystem(context.Background(), executablePlan(step), root, SetupExecutionOptions{NonInteractive: true}, system)
	if err != nil || result.Status != "completed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(system.calls) != 4 || system.calls[0].Executable != "/usr/bin/tee" || system.calls[0].Input != content || system.calls[1].Executable != "/usr/bin/chmod" || system.calls[2].Executable != "/usr/bin/chown" || system.calls[3].Executable != "/usr/bin/mv" {
		t.Fatalf("service commands=%+v", system.calls)
	}
	for _, call := range system.calls {
		if !call.Privileged {
			t.Fatalf("service command was not privileged: %+v", call)
		}
	}
	for _, required := range []string{"AmbientCapabilities=CAP_NET_BIND_SERVICE", "CapabilityBoundingSet=CAP_NET_BIND_SERVICE"} {
		if !strings.Contains(content, required) {
			t.Fatalf("service capability scope missing %q", required)
		}
	}
}

func TestSetupExecutorDirectoryAndModeActionsAreIdempotent(t *testing.T) {
	root := preparedExecutorRoot(t)
	target := filepath.Join(root, "data")
	create := SetupStep{ID: "directory.data", Disposition: "create", Action: &SetupAction{SchemaVersion: 1, Kind: "directory", File: &SetupFileAction{Path: target, Mode: "0700"}}}
	system := &fakeSetupExecutionSystem{}
	if _, err := executeSetupPlanWithSystem(context.Background(), executablePlan(create), root, SetupExecutionOptions{}, system); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	mode := SetupStep{ID: "directory.data", Disposition: "update", Action: &SetupAction{SchemaVersion: 1, Kind: "file-mode", File: &SetupFileAction{Path: target, Mode: "0700"}}}
	for i := 0; i < 2; i++ {
		if _, err := executeSetupPlanWithSystem(context.Background(), executablePlan(mode), root, SetupExecutionOptions{}, system); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestSetupExecutorCreatesCleanStateRootHierarchy(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, ".ao", "hosted")
	directory := func(id, path string, dependencies ...string) SetupStep {
		return SetupStep{ID: id, Disposition: "create", Dependencies: dependencies, Action: &SetupAction{SchemaVersion: 1, Kind: "directory", File: &SetupFileAction{Path: path, Mode: "0700"}}}
	}
	plan := executablePlan(
		directory("directory.parent", filepath.Dir(root)),
		directory("directory.state", root, "directory.parent"),
		directory("directory.hao", filepath.Join(root, "hao"), "directory.state"),
		directory("directory.bin", filepath.Join(root, "bin"), "directory.state"),
		directory("directory.data", filepath.Join(root, "data"), "directory.state"),
	)
	result, err := executeSetupPlanWithSystem(context.Background(), plan, root, SetupExecutionOptions{}, &fakeSetupExecutionSystem{})
	if err != nil || len(result.Completed) != 5 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, path := range []string{filepath.Dir(root), root, filepath.Join(root, "hao"), filepath.Join(root, "bin"), filepath.Join(root, "data")} {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("directory %s error=%v", path, statErr)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s mode=%v", path, info.Mode().Perm())
		}
	}
}

func TestSetupExecutorRollsBackArtifactAndRedactsFailure(t *testing.T) {
	root := preparedExecutorRoot(t)
	path := filepath.Join(root, "bin", "ao")
	old := []byte("old artifact")
	if err := os.WriteFile(path, old, 0o700); err != nil {
		t.Fatal(err)
	}
	oldDigest := sha256.Sum256(old)
	oldMetadata := ArtifactMetadata{Version: "0.13.0", Source: "https://example.invalid/v0.13.0/ao", SHA256: fmt.Sprintf("%x", oldDigest[:])}
	metadataBytes, _ := json.Marshal(oldMetadata)
	if err := os.WriteFile(path+".hao-manifest.json", metadataBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	newPayload := []byte("new artifact")
	vendor := &SetupVendorAction{Executable: "/usr/bin/npm", Argv: []string{"install", "--global", "@anthropic-ai/claude-code@2.3.4"}, Version: "2.3.4", Source: "https://registry.example.invalid/claude-code-2.3.4.tgz", SHA256: strings.Repeat("a", 64)}
	plan := executablePlan(artifactStep(root, newPayload, "update"), SetupStep{ID: "prerequisite.harness", Disposition: "create", Action: &SetupAction{SchemaVersion: 1, Kind: "vendor-package", Vendor: vendor}})
	system := &fakeSetupExecutionSystem{payload: newPayload, runErr: errors.New("token=super-secret installer failed")}
	result, err := executeSetupPlanWithSystem(context.Background(), plan, root, SetupExecutionOptions{}, system)
	if err == nil || result.Status != "failed" || len(result.Rollback) == 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	restored, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(restored, old) {
		t.Fatalf("artifact was not restored: %q err=%v", restored, readErr)
	}
	var typed commandError
	if !errors.As(err, &typed) {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(typed.Details)
	if strings.Contains(err.Error()+string(encoded), "super-secret") || !strings.Contains(err.Error()+string(encoded), "[REDACTED]") {
		t.Fatalf("failure was not redacted: %v details=%s", err, encoded)
	}
}

func TestSetupExecutorRecoversInterruptedJournalBeforeRetry(t *testing.T) {
	root := preparedExecutorRoot(t)
	target := filepath.Join(root, "bin", "ao")
	backupDir := filepath.Join(root, "hao", "backups", "crash")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(backupDir, "ao")
	if err := os.WriteFile(target, []byte("partial-new"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("old-safe"), 0o700); err != nil {
		t.Fatal(err)
	}
	journal := setupJournal{SchemaVersion: 1, PlanSHA256: strings.Repeat("a", 64), State: "in_progress", Records: []journalRecord{{StepID: "artifact.ao", Kind: "verified-release-artifact", Target: target, Backup: backup, ManifestTarget: target + ".hao-manifest.json", Temporary: filepath.Join(root, "bin", ".hao-artifact-"+strings.Repeat("a", 32)), Reversible: true}}}
	journalBytes, _ := json.Marshal(journal)
	if err := os.WriteFile(filepath.Join(root, ".hao-setup-transaction.json"), journalBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := executeSetupPlanWithSystem(context.Background(), executablePlan(), root, SetupExecutionOptions{}, &fakeSetupExecutionSystem{})
	if err != nil || len(result.Recovery) != 1 || len(result.Rollback) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	restored, _ := os.ReadFile(target)
	if string(restored) != "old-safe" {
		t.Fatalf("recovery left %q", restored)
	}
}

func TestSetupExecutorRejectsConcurrentInvocation(t *testing.T) {
	root := preparedExecutorRoot(t)
	payload := []byte("artifact")
	system := &fakeSetupExecutionSystem{payload: payload, downloadStart: make(chan struct{}, 1), downloadResume: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := executeSetupPlanWithSystem(context.Background(), executablePlan(artifactStep(root, payload, "create")), root, SetupExecutionOptions{}, system)
		done <- err
	}()
	<-system.downloadStart
	_, err := executeSetupPlanWithSystem(context.Background(), executablePlan(), root, SetupExecutionOptions{}, &fakeSetupExecutionSystem{})
	if err == nil || commandErrorCode(t, err) != "setup_busy" {
		t.Fatalf("concurrent invocation error=%v", err)
	}
	close(system.downloadResume)
	if err := <-done; err != nil {
		t.Fatalf("first invocation failed: %v", err)
	}
}

func TestSetupExecutorRejectsHostileManagedPaths(t *testing.T) {
	root := preparedExecutorRoot(t)
	hostile := executablePlan(SetupStep{ID: "directory.escape", Disposition: "create", Action: &SetupAction{SchemaVersion: 1, Kind: "directory", File: &SetupFileAction{Path: filepath.Join(root, "..", "escape"), Mode: "0700"}}})
	if _, err := executeSetupPlanWithSystem(context.Background(), hostile, root, SetupExecutionOptions{}, &fakeSetupExecutionSystem{}); err == nil || commandErrorCode(t, err) != "invalid_setup_plan" {
		t.Fatalf("escape accepted: %v", err)
	}
	outside := t.TempDir()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	linkedPlan := executablePlan(SetupStep{ID: "directory.linked", Disposition: "create", Action: &SetupAction{SchemaVersion: 1, Kind: "directory", File: &SetupFileAction{Path: filepath.Join(link, "child"), Mode: "0700"}}})
	if _, err := executeSetupPlanWithSystem(context.Background(), linkedPlan, root, SetupExecutionOptions{}, &fakeSetupExecutionSystem{}); err == nil || commandErrorCode(t, err) != "invalid_setup_plan" {
		t.Fatalf("linked ancestor accepted: %v", err)
	}
}

func TestSetupExecutorRejectsStaleArtifactCreateTarget(t *testing.T) {
	root := preparedExecutorRoot(t)
	artifact := artifactStep(root, []byte("expected"), "create")
	if err := os.WriteFile(artifact.Action.Artifact.Path, []byte("appeared"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := executeSetupPlanWithSystem(context.Background(), executablePlan(artifact), root, SetupExecutionOptions{}, &fakeSetupExecutionSystem{}); err == nil || commandErrorCode(t, err) != "setup_failed" {
		t.Fatalf("stale artifact plan accepted: %v", err)
	}
}

func TestSetupExecutorRejectsHostileRecoveryJournalBeforePrivilege(t *testing.T) {
	root := preparedExecutorRoot(t)
	journal := setupJournal{SchemaVersion: 1, PlanSHA256: strings.Repeat("a", 64), State: "in_progress", Records: []journalRecord{{
		StepID: "service.daemon", Kind: "service-definition-v1", Target: "/etc/passwd", Backup: "/tmp/hostile", Temporary: "/tmp/hostile-temp", Reversible: true,
	}}}
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hao-setup-transaction.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	system := &fakeSetupExecutionSystem{}
	if _, err := executeSetupPlanWithSystem(context.Background(), executablePlan(), root, SetupExecutionOptions{}, system); err == nil || commandErrorCode(t, err) != "setup_failed" {
		t.Fatalf("hostile journal accepted: %v", err)
	}
	if len(system.calls) != 0 {
		t.Fatalf("hostile journal ran commands: %+v", system.calls)
	}
}

func TestSetupDryRunAndExecutionUseIdenticalActionSchema(t *testing.T) {
	obs := healthyObserver()
	deps, root := setupDeps(t, "local", obs)
	obs.statErr[filepath.Join(root, "data")] = os.ErrNotExist
	dryOut, _, dryCode := runCLI(t, deps, "--json", "--config", fixturePath("valid", "local.yaml"), "setup", "--dry-run")
	if dryCode != 0 {
		t.Fatalf("dry-run code=%d out=%s", dryCode, dryOut)
	}
	var executed SetupPlan
	deps.ExecuteSetup = func(_ context.Context, plan SetupPlan, _ string, _ SetupExecutionOptions) (SetupExecutionResult, error) {
		executed = plan
		return SetupExecutionResult{Status: "completed"}, nil
	}
	execOut, _, execCode := runCLI(t, deps, "--json", "--config", fixturePath("valid", "local.yaml"), "setup", "--non-interactive", "--yes")
	if execCode != 0 {
		t.Fatalf("execution code=%d out=%s", execCode, execOut)
	}
	var dry SetupPlan
	if err := json.Unmarshal([]byte(strings.Split(strings.TrimSpace(dryOut), "\n")[0]), &dry); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dry.Steps, executed.Steps) || dry.SchemaVersion != executed.SchemaVersion || !dry.DryRun || executed.DryRun {
		t.Fatalf("dry-run and execution drifted\ndry=%+v\nexecution=%+v", dry, executed)
	}
}
