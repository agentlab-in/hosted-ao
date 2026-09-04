package haocli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

func setupDeps(t *testing.T, fixture string, obs *fakeObserver) (Deps, string) {
	t.Helper()
	deps := observationDeps(t, fixture, obs)
	root, err := deps.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	obs.files[filepath.Join(root, "bin", "ao")] = FileObservation{Mode: 0o755, Owner: true}
	trusted := ArtifactMetadata{Version: "0.14.0", SHA256: strings.Repeat("a", 64), Source: "https://github.com/agentlab-in/hosted-ao/releases/download/v0.14.0/ao-linux-x64"}
	obs.artifacts[filepath.Join(root, "bin", "ao")] = trusted
	deps.TrustedArtifact = func(_, _, _ string) (ArtifactMetadata, bool) { return trusted, true }
	obs.files["/etc/systemd/system/ao-daemon.service"] = FileObservation{Mode: 0o644, UID: 0}
	obs.files["/etc/systemd/system/ao-gateway.service"] = FileObservation{Mode: 0o644, UID: 0}
	desired := setupDesired{StateRoot: root, PairPort: 443}
	obs.readFiles["/etc/systemd/system/ao-daemon.service"] = []byte(renderSystemdDefinition("service.daemon", desired, obs.user))
	obs.readFiles["/etc/systemd/system/ao-gateway.service"] = []byte(renderSystemdDefinition("service.gateway", desired, obs.user))
	return deps, root
}

func decodeSetupPlan(t *testing.T, out string) SetupPlan {
	t.Helper()
	var plan SetupPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("decode plan: %v\n%s", err, out)
	}
	return plan
}

func stepByID(t *testing.T, plan SetupPlan, id string) SetupStep {
	t.Helper()
	for _, step := range plan.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("missing step %q in %+v", id, plan.Steps)
	return SetupStep{}
}

func TestSetupValidLocalAndPairPlansAreDeterministic(t *testing.T) {
	for _, fixture := range []string{"local", "pair"} {
		t.Run(fixture, func(t *testing.T) {
			obs := healthyObserver()
			deps, _ := setupDeps(t, fixture, obs)
			args := []string{"--json", "--config", fixturePath("valid", fixture+".yaml"), "setup", "--dry-run", "--non-interactive"}
			out1, stderr1, code1 := runCLI(t, deps, args...)
			out2, stderr2, code2 := runCLI(t, deps, args...)
			if code1 != 0 || code2 != 0 || stderr1 != "" || stderr2 != "" || out1 != out2 {
				t.Fatalf("code=%d/%d stderr=%q/%q deterministic=%v\n%s\n%s", code1, code2, stderr1, stderr2, out1 == out2, out1, out2)
			}
			plan := decodeSetupPlan(t, out1)
			if plan.SchemaVersion != 1 || !plan.DryRun || !plan.Summary.Ready || plan.Summary.Blocked != 0 {
				t.Fatalf("plan=%+v", plan)
			}
			for _, step := range plan.Steps {
				if step.Disposition != "no-op" {
					t.Fatalf("satisfied step is not no-op: %+v", step)
				}
			}
			joined := strings.ToLower(out1)
			if strings.Contains(joined, "tmux") || strings.Contains(joined, "runtimebackend") {
				t.Fatalf("AO-internal runtime policy leaked into plan: %s", out1)
			}
			_, hasGH := func() (SetupStep, bool) {
				for _, step := range plan.Steps {
					if step.ID == "prerequisite.gh" {
						return step, true
					}
				}
				return SetupStep{}, false
			}()
			if hasGH != (fixture == "pair") {
				t.Fatalf("profile-conditional gh presence=%v fixture=%s", hasGH, fixture)
			}
		})
	}
}

func TestSetupInstallPoliciesAndStructuredPrivilege(t *testing.T) {
	t.Run("none blocks missing prerequisites without actions", func(t *testing.T) {
		obs := healthyObserver()
		delete(obs.paths, "git")
		delete(obs.paths, "claude")
		deps, root := setupDeps(t, "local", obs)
		deps.TrustedArtifact = nil
		obs.statErr[filepath.Join(root, "bin", "ao")] = os.ErrNotExist
		out, stderr, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "local.yaml"), "setup", "--dry-run", "--install", "none")
		if code != 1 || stderr != "" {
			t.Fatalf("code=%d stderr=%q out=%s", code, stderr, out)
		}
		plan := decodeSetupPlan(t, out)
		for _, id := range []string{"prerequisite.git", "prerequisite.harness"} {
			step := stepByID(t, plan, id)
			if step.Disposition != "blocked" || step.Operation != "manual-install" || step.Action != nil {
				t.Fatalf("%s=%+v", id, step)
			}
		}
		artifact := stepByID(t, plan, "artifact.ao")
		if artifact.Disposition != "blocked" || artifact.Action != nil {
			t.Fatalf("audit-only artifact=%+v", artifact)
		}
	})

	t.Run("missing blocks package and vendor work without immutable metadata", func(t *testing.T) {
		obs := healthyObserver()
		for _, name := range []string{"git", "gh", "claude"} {
			delete(obs.paths, name)
		}
		deps, root := setupDeps(t, "pair", obs)
		obs.statErr[filepath.Join(root, "bin", "ao")] = os.ErrNotExist
		obs.statErr["/etc/systemd/system/ao-daemon.service"] = os.ErrNotExist
		obs.statErr["/etc/systemd/system/ao-gateway.service"] = os.ErrNotExist
		out, stderr, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "pair.yaml"), "setup", "--dry-run", "--install", "missing")
		if code != 1 || stderr != "" {
			t.Fatalf("code=%d stderr=%q out=%s", code, stderr, out)
		}
		plan := decodeSetupPlan(t, out)
		for _, id := range []string{"prerequisite.git", "prerequisite.gh"} {
			step := stepByID(t, plan, id)
			if step.Disposition != "blocked" || step.Privilege.Required || step.Action != nil || !strings.Contains(step.Reason, "immutable package metadata") {
				t.Fatalf("%s=%+v", id, step)
			}
		}
		harness := stepByID(t, plan, "prerequisite.harness")
		if harness.Disposition != "blocked" || harness.Privilege.Required || harness.Action != nil {
			t.Fatalf("harness=%+v", harness)
		}
		if artifact := stepByID(t, plan, "artifact.ao"); artifact.Disposition != "blocked" || artifact.Action != nil {
			t.Fatalf("artifact=%+v", artifact)
		}
	})
}

func TestSetupUnknownConflictAndBlockedPropagation(t *testing.T) {
	obs := healthyObserver()
	deps, root := setupDeps(t, "pair", obs)
	artifact := filepath.Join(root, "bin", "ao")
	obs.artifactErr[artifact] = context.DeadlineExceeded
	obs.files[root] = FileObservation{Mode: 0o700, Owner: false}
	obs.runErr["/usr/bin/apt-get --version"] = errors.New("probe failure")
	out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "pair.yaml"), "setup", "--dry-run")
	if code != 1 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	plan := decodeSetupPlan(t, out)
	if stepByID(t, plan, "directory.state").Disposition != "blocked" || stepByID(t, plan, "artifact.ao").Disposition != "blocked" {
		t.Fatalf("plan=%+v", plan)
	}
	if stepByID(t, plan, "service.daemon").Disposition != "blocked" || stepByID(t, plan, "service.gateway").Disposition != "blocked" {
		t.Fatalf("blocked dependencies did not propagate: %+v", plan.Steps)
	}
}

func TestSetupPlatformsAndServiceManagers(t *testing.T) {
	tests := []struct {
		name, fixture, goos, distro string
		serviceEnabled              bool
		wantCode                    int
		wantPlatform, wantService   string
	}{
		{"ubuntu systemd", "pair", "linux", "ubuntu", true, 0, "no-op", "no-op"},
		{"mac local desktop", "local", "darwin", "darwin", false, 0, "no-op", "no-op"},
		{"mac pair unsupported lifecycle", "pair", "darwin", "darwin", true, 1, "no-op", "blocked"},
		{"windows pair unsupported", "pair", "windows", "windows", true, 1, "blocked", "blocked"},
		{"other linux unsupported", "pair", "linux", "fedora", true, 1, "blocked", "blocked"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := healthyObserver()
			obs.platform, obs.distribution = tc.goos, tc.distro
			deps, _ := setupDeps(t, tc.fixture, obs)
			out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", tc.fixture+".yaml"), "setup", "--dry-run")
			if code != tc.wantCode {
				t.Fatalf("code=%d want=%d out=%s", code, tc.wantCode, out)
			}
			plan := decodeSetupPlan(t, out)
			if stepByID(t, plan, "host.platform").Disposition != tc.wantPlatform || stepByID(t, plan, "service.daemon").Disposition != tc.wantService {
				t.Fatalf("plan=%+v", plan)
			}
		})
	}

	for _, state := range []string{"absent", "unknown"} {
		t.Run("systemd "+state, func(t *testing.T) {
			obs := healthyObserver()
			if state == "absent" {
				delete(obs.paths, "systemctl")
			} else {
				obs.pathErr["systemctl"] = context.DeadlineExceeded
			}
			deps, _ := setupDeps(t, "pair", obs)
			out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "pair.yaml"), "setup", "--dry-run")
			if code != 1 || stepByID(t, decodeSetupPlan(t, out), "service.daemon").Disposition != "blocked" {
				t.Fatalf("code=%d out=%s", code, out)
			}
		})
	}
}

func TestSetupWrongVersionsPermissionsAndManagerUnknown(t *testing.T) {
	obs := healthyObserver()
	deps, root := setupDeps(t, "pair", obs)
	artifact := filepath.Join(root, "bin", "ao")
	obs.artifacts[artifact] = ArtifactMetadata{Version: "0.13.9", SHA256: strings.Repeat("b", 64), Source: "old-release"}
	obs.files[filepath.Join(root, "data")] = FileObservation{Mode: 0o755, Owner: true, IsDir: true}
	out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "pair.yaml"), "setup", "--dry-run")
	if code != 1 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	plan := decodeSetupPlan(t, out)
	if stepByID(t, plan, "artifact.ao").Disposition != "blocked" || stepByID(t, plan, "directory.data").Disposition != "update" {
		t.Fatalf("plan=%+v", plan)
	}

	obs = healthyObserver()
	delete(obs.paths, "git")
	obs.pathErr["apt-get"] = context.DeadlineExceeded
	deps, _ = setupDeps(t, "pair", obs)
	out, _, code = runCLI(t, deps, "--json", "--config", fixturePath("valid", "pair.yaml"), "setup", "--dry-run")
	if code != 1 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	plan = decodeSetupPlan(t, out)
	if stepByID(t, plan, "host.package-manager").Disposition != "blocked" || stepByID(t, plan, "prerequisite.git").Disposition != "blocked" {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestSetupUntrustedArtifactProvenanceNeverBecomesExecutableOrNoOp(t *testing.T) {
	t.Run("self-attested existing artifact is blocked", func(t *testing.T) {
		obs := healthyObserver()
		deps, root := setupDeps(t, "local", obs)
		deps.TrustedArtifact = nil
		artifact := filepath.Join(root, "bin", "ao")
		obs.artifacts[artifact] = ArtifactMetadata{Version: "0.14.0", SHA256: strings.Repeat("a", 64), Source: "https://github.com/agentlab-in/hosted-ao/releases/download/v0.14.0/ao-linux-x64"}
		out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "local.yaml"), "setup", "--dry-run")
		if code != 1 {
			t.Fatalf("code=%d out=%s", code, out)
		}
		step := stepByID(t, decodeSetupPlan(t, out), "artifact.ao")
		if step.Disposition != "blocked" || step.Action != nil || !strings.Contains(step.Reason, "immutable release metadata") {
			t.Fatalf("artifact step=%+v", step)
		}
	})

	t.Run("absent artifact and harness are manual without immutable metadata", func(t *testing.T) {
		obs := healthyObserver()
		delete(obs.paths, "claude")
		deps, root := setupDeps(t, "local", obs)
		obs.statErr[filepath.Join(root, "bin", "ao")] = os.ErrNotExist
		out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "local.yaml"), "setup", "--dry-run", "--install", "missing")
		if code != 1 {
			t.Fatalf("code=%d out=%s", code, out)
		}
		plan := decodeSetupPlan(t, out)
		for _, id := range []string{"artifact.ao", "prerequisite.harness"} {
			step := stepByID(t, plan, id)
			if step.Disposition != "blocked" || step.Action != nil || !strings.Contains(step.Reason, "immutable") {
				t.Fatalf("%s=%+v", id, step)
			}
		}
	})
}

func TestSetupLocalNeverPlansGatewayAndUnsafeArtifactIsNotExecuted(t *testing.T) {
	obs := healthyObserver()
	deps, root := setupDeps(t, "local", obs)
	artifact := filepath.Join(root, "bin", "ao")
	obs.files[artifact] = FileObservation{Mode: 0o777, Owner: true}
	out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "local.yaml"), "setup", "--dry-run")
	if code != 1 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	plan := decodeSetupPlan(t, out)
	if stepByID(t, plan, "artifact.ao").Disposition != "blocked" {
		t.Fatalf("plan=%+v", plan)
	}
	if obs.runCalls != 0 {
		t.Fatalf("setup executed %d subprocess probes", obs.runCalls)
	}
	for _, step := range plan.Steps {
		if strings.Contains(step.ID, "gateway") || strings.HasPrefix(step.ID, "pair.") {
			t.Fatalf("local plan contains gateway step: %+v", step)
		}
	}
}

func TestSetupHarnessDoesNotDependOnPackageManager(t *testing.T) {
	obs := healthyObserver()
	delete(obs.paths, "claude")
	delete(obs.paths, "apt-get")
	deps, _ := setupDeps(t, "pair", obs)
	out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "pair.yaml"), "setup", "--dry-run")
	if code != 1 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	step := stepByID(t, decodeSetupPlan(t, out), "prerequisite.harness")
	if step.Disposition != "blocked" || step.Action != nil || len(step.Dependencies) != 1 {
		t.Fatalf("step=%+v", step)
	}
}

func TestSetupServiceDefinitionDriftCarriesCanonicalContent(t *testing.T) {
	obs := healthyObserver()
	deps, _ := setupDeps(t, "pair", obs)
	obs.readFiles["/etc/systemd/system/ao-daemon.service"] = []byte("[Service]\nExecStart=/wrong\n")
	out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "pair.yaml"), "setup", "--dry-run")
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	step := stepByID(t, decodeSetupPlan(t, out), "service.daemon")
	if step.Disposition != "update" || step.Action == nil || step.Action.Service == nil || !strings.Contains(step.Action.Service.Content, "ExecStart=") || !strings.Contains(step.Action.Service.Content, "AO_DATA_DIR") {
		t.Fatalf("step=%+v", step)
	}
}

func TestSystemObserverBlocksManagedSymlinkAndVerifiesArtifactManifest(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	item := observeFile(systemObserver{}, link)
	if !item.Link || planDirectory("directory.state", item).Disposition != "blocked" {
		t.Fatalf("item=%+v", item)
	}

	artifact := filepath.Join(root, "ao")
	payload := []byte("non-executable-test-artifact")
	if err := os.WriteFile(artifact, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	metadata := ArtifactMetadata{Version: "0.14.0", SHA256: fmt.Sprintf("%x", sum[:]), Source: "test-release"}
	manifest, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact+".hao-manifest.json", manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := (systemObserver{}).InspectArtifact(context.Background(), artifact)
	if err != nil || !reflect.DeepEqual(got, metadata) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestTrustedReleaseMetadataMatchesInspectedArtifactExactly(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(root, "ao")
	payload := []byte("release-artifact")
	if err := os.WriteFile(artifact, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	trusted := ArtifactMetadata{Version: "0.14.0", Source: "https://github.com/agentlab-in/hosted-ao/releases/download/v0.14.0/ao-linux-x64", SHA256: fmt.Sprintf("%x", sum[:])}
	manifest, err := json.Marshal(trusted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact+".hao-manifest.json", manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	observed, err := (systemObserver{}).InspectArtifact(context.Background(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !matchesTrustedArtifact(observed, trusted) {
		t.Fatalf("exact immutable provenance did not match: observed=%+v trusted=%+v", observed, trusted)
	}
	mutated := trusted
	mutated.SHA256 = strings.Repeat("b", 64)
	if matchesTrustedArtifact(observed, mutated) {
		t.Fatal("mismatched trusted digest was accepted")
	}
}

func TestSystemObserverRejectsLinkedAndOversizedManagedReads(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := (systemObserver{}).ReadFile(linked); err == nil {
		t.Fatal("linked managed read unexpectedly succeeded")
	}
	oversized := filepath.Join(root, "oversized")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxArtifactSize + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (systemObserver{}).InspectArtifact(context.Background(), oversized); err == nil {
		t.Fatal("oversized artifact inspection unexpectedly succeeded")
	}
}

func TestSystemObserverRejectsArtifactThroughLinkedAncestor(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("artifact")
	artifact := filepath.Join(target, "ao")
	if err := os.WriteFile(artifact, payload, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	metadata := ArtifactMetadata{Version: "0.14.0", Source: "release", SHA256: fmt.Sprintf("%x", sum[:])}
	manifest, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact+".hao-manifest.json", manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(root, "linked-parent")
	if err := os.Symlink(target, ancestor); err != nil {
		t.Fatal(err)
	}
	if _, err := (systemObserver{}).InspectArtifact(context.Background(), filepath.Join(ancestor, "ao")); err == nil {
		t.Fatal("artifact inspection followed a linked ancestor")
	}
}

func TestSystemObserverArtifactHashHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inspectArtifact(ctx, filepath.Join(t.TempDir(), "ao")); !errors.Is(err, context.Canceled) {
		t.Fatalf("inspect canceled artifact error=%v, want context.Canceled", err)
	}
}

func TestManagedReadNeverReturnsLinkSwapTarget(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(root, "managed")
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		_ = os.Remove(managed)
		if err := os.Symlink(outside, managed); err != nil {
			t.Fatal(err)
		}
		if data, readErr := (systemObserver{}).ReadFile(managed); readErr == nil && string(data) != "safe" {
			t.Fatalf("managed read escaped through swap: %q", data)
		}
		_ = os.Remove(managed)
		if err := os.WriteFile(managed, []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSetupBlocksArtifactInspectionThroughManagedAncestorLink(t *testing.T) {
	obs := healthyObserver()
	deps, root := setupDeps(t, "local", obs)
	obs.files[root] = FileObservation{Mode: os.ModeSymlink | 0o777, Owner: true, Link: true}
	out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "local.yaml"), "setup", "--dry-run")
	if code != 1 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if stepByID(t, decodeSetupPlan(t, out), "artifact.ao").Disposition != "blocked" {
		t.Fatalf("artifact ancestry did not block: %s", out)
	}
}

func TestSetupActionValidatorAndPairServiceConsumerContract(t *testing.T) {
	obs := healthyObserver()
	deps, _ := setupDeps(t, "pair", obs)
	obs.statErr["/etc/systemd/system/ao-gateway.service"] = os.ErrNotExist
	out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "pair.yaml"), "setup", "--dry-run")
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	plan := decodeSetupPlan(t, out)
	if err := validateSetupPlan(plan); err != nil {
		t.Fatalf("validate action contract: %v", err)
	}
	action := stepByID(t, plan, "service.gateway").Action
	if action == nil || action.SchemaVersion != 1 || action.Service == nil || action.Service.Version != "1" || action.Service.SHA256 == "" {
		t.Fatalf("gateway action=%+v", action)
	}
	for _, fragment := range []string{"AO_VM_HTTPS_ADDR=:443", "PATH=/home/ubuntu/.local/bin", "AmbientCapabilities=CAP_NET_BIND_SERVICE", "CapabilityBoundingSet=CAP_NET_BIND_SERVICE"} {
		if !strings.Contains(action.Service.Content, fragment) {
			t.Fatalf("gateway content missing %q: %s", fragment, action.Service.Content)
		}
	}
	tampered := plan
	for i := range tampered.Steps {
		if tampered.Steps[i].Action != nil && tampered.Steps[i].Action.Kind == "service-definition-v1" {
			copyAction := *tampered.Steps[i].Action
			copyService := *copyAction.Service
			copyService.Content += "# tampered\n"
			copyAction.Service = &copyService
			tampered.Steps[i].Action = &copyAction
			break
		}
	}
	if err := validateSetupPlan(tampered); err == nil {
		t.Fatal("tampered action contract unexpectedly validated")
	}
}

func TestSetupActionValidatorRejectsMutableOrIncompleteProvenance(t *testing.T) {
	for _, action := range []*SetupAction{
		{SchemaVersion: 1, Kind: "verified-release-artifact", Artifact: &SetupArtifactAction{Path: "/managed/ao", Version: "0.14.0", Source: "https://example.invalid/ao"}},
		{SchemaVersion: 1, Kind: "verified-release-artifact", Artifact: &SetupArtifactAction{Path: "/managed/ao", Version: "0.14.0", Source: "https://example.invalid/latest/ao", SHA256: strings.Repeat("a", 64)}},
		{SchemaVersion: 1, Kind: "vendor-package", Vendor: &SetupVendorAction{Executable: "/usr/bin/npm", Argv: []string{"install", "pkg@latest"}, Version: "latest", Source: "https://registry.invalid/pkg"}},
		{SchemaVersion: 1, Kind: "vendor-package", Vendor: &SetupVendorAction{Executable: "/usr/bin/npm", Argv: []string{"install", "pkg@latest"}, Version: "1.2.3", Source: "https://registry.invalid/pkg-1.2.3.tgz", SHA256: strings.Repeat("a", 64)}},
		{SchemaVersion: 1, Kind: "package-manager", Package: &SetupPackageAction{Executable: "/usr/bin/apt-get", Argv: []string{"install", "git"}, Version: "latest", Source: "https://packages.invalid/git", SHA256: strings.Repeat("a", 64)}},
		{SchemaVersion: 1, Kind: "package-manager", Package: &SetupPackageAction{Executable: "/usr/bin/apt-get", Argv: []string{"install", "git"}, Version: "1.2.3", Source: "https://packages.invalid/git-1.2.3.deb", SHA256: strings.Repeat("a", 64)}},
	} {
		plan := SetupPlan{SchemaVersion: 1, DryRun: true, Steps: []SetupStep{{ID: "test", Disposition: "create", Action: action}}}
		if err := validateSetupPlan(plan); err == nil {
			t.Fatalf("mutable/incomplete action unexpectedly validated: %+v", action)
		}
	}
}

func TestImmutableActionJSONRoundTripsThroughNonMutatingConsumer(t *testing.T) {
	digest := strings.Repeat("a", 64)
	plan := SetupPlan{SchemaVersion: 1, DryRun: true, Steps: []SetupStep{{
		ID: "artifact.ao", Disposition: "create", Action: &SetupAction{
			SchemaVersion: 1, Kind: "verified-release-artifact", Artifact: &SetupArtifactAction{Path: "/managed/ao",
				Version: "0.14.0", Source: "https://releases.example.invalid/v0.14.0/ao-linux-x64", SHA256: digest},
		},
	}, {
		ID: "prerequisite.git", Disposition: "create", Action: &SetupAction{
			SchemaVersion: 1, Kind: "package-manager", Package: &SetupPackageAction{
				Executable: "/usr/bin/apt-get", Argv: []string{"install", "git=1.2.3"}, Version: "1.2.3",
				Source: "https://packages.example.invalid/git-1.2.3.deb", SHA256: digest,
			},
		},
	}, {
		ID: "prerequisite.harness", Disposition: "create", Action: &SetupAction{
			SchemaVersion: 1, Kind: "vendor-package", Vendor: &SetupVendorAction{
				Executable: "/usr/bin/npm", Argv: []string{"install", "--global", "pkg@1.2.3"}, Version: "1.2.3",
				Source: "https://registry.example.invalid/pkg/-/pkg-1.2.3.tgz", SHA256: digest,
			},
		},
	}}}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := consumeSetupPlanJSON(data)
	if err != nil {
		t.Fatalf("consume serialized plan: %v", err)
	}
	want := []string{
		"artifact source=https://releases.example.invalid/v0.14.0/ao-linux-x64 version=0.14.0 sha256=" + digest + " path=/managed/ao",
		"package executable=/usr/bin/apt-get source=https://packages.example.invalid/git-1.2.3.deb version=1.2.3 sha256=" + digest + ` argv=["install" "git=1.2.3"]`,
		"vendor source=https://registry.example.invalid/pkg/-/pkg-1.2.3.tgz version=1.2.3 sha256=" + digest,
	}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("operations=%q want=%q", operations, want)
	}
}

func TestPairSystemdConsumerCarriesPortCapabilitiesAndHarnessPath(t *testing.T) {
	desired := setupDesired{StateRoot: "/home/ubuntu/.ao/hosted", PairPort: 8443}
	user := UserObservation{Name: "ubuntu", UID: 1000, Home: "/home/ubuntu"}
	parsed, err := consumeSystemdDefinition(renderSystemdDefinition("service.gateway", desired, user))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := vmgateway.Resolve(vmgateway.Options{
		Pair: true, HTTPSAddr: parsed.Environment["AO_VM_HTTPS_ADDR"],
		CertDir: parsed.Environment["AO_VM_CERT_DIR"], PasscodeDir: parsed.Environment["AO_VM_PASSCODE_DIR"],
		MachineFile: filepath.Join(t.TempDir(), "absent-machine.json"),
	}, "/home/ubuntu/.ao/hosted/data")
	if err != nil {
		t.Fatalf("resolve gateway from parsed unit: %v", err)
	}
	if cfg.HTTPSAddr != ":8443" || parsed.HasNetBindCapability {
		t.Fatalf("https=%q capability=%v", cfg.HTTPSAddr, parsed.HasNetBindCapability)
	}
	if parsed.Environment["PATH"] != "/home/ubuntu/.local/bin:/home/ubuntu/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" {
		t.Fatalf("service PATH=%q", parsed.Environment["PATH"])
	}

	desired.PairPort = 443
	parsed, err = consumeSystemdDefinition(renderSystemdDefinition("service.gateway", desired, user))
	if err != nil || !parsed.HasNetBindCapability {
		t.Fatalf("privileged port consumer capability=%v err=%v", parsed.HasNetBindCapability, err)
	}
}

func TestControlledServicePathFindsUserHarness(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(bin, "claude")
	if err := os.WriteFile(harness, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	parsed, err := consumeSystemdDefinition(renderSystemdDefinition("service.daemon", setupDesired{StateRoot: filepath.Join(home, ".ao", "hosted")}, UserObservation{Name: "agent", UID: 1000, Home: home}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := lookPathInService("claude", parsed.Environment["PATH"])
	if err != nil || got != harness {
		t.Fatalf("harness path=%q err=%v want=%q", got, err, harness)
	}
}

func TestSetupPairOrderingAndSetupInitSeparation(t *testing.T) {
	obs := healthyObserver()
	deps, _ := setupDeps(t, "pair", obs)
	out, _, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "pair.yaml"), "setup", "--dry-run", "--yes")
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	plan := decodeSetupPlan(t, out)
	positions := map[string]int{}
	for i, step := range plan.Steps {
		positions[step.ID] = i
	}
	if positions["directory.data"] > positions["artifact.ao"] || positions["artifact.ao"] > positions["service.daemon"] || positions["service.daemon"] > positions["service.gateway"] {
		t.Fatalf("ordering=%v", positions)
	}
	if !reflect.DeepEqual(stepByID(t, plan, "service.gateway").Dependencies, []string{"service.daemon", "directory.gateway", "artifact.ao"}) {
		t.Fatalf("gateway deps=%v", stepByID(t, plan, "service.gateway").Dependencies)
	}
	for _, forbidden := range []string{"certificate", "passcode", "pair string", "ao-pair://", "listener", "authenticate", "start-service", "enable-service"} {
		if strings.Contains(strings.ToLower(out), forbidden) {
			t.Fatalf("setup planned init concern %q: %s", forbidden, out)
		}
	}
}

func TestSetupWithoutDryRunFailsBeforeEveryBoundary(t *testing.T) {
	panicRead := func(string) ([]byte, error) { panic("config read invoked") }
	panicPath := func() (string, error) { panic("path resolution invoked") }
	out, stderr, code := runCLI(t, Deps{ReadFile: panicRead, StateDir: panicPath, RunFile: panicPath, Observer: panicObserver{}}, "--json", "setup", "--non-interactive", "--install", "missing", "--yes")
	if code != 4 || out != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	assertEnvelope(t, stderr, code, 4, "feature_deferred", "setup")
}

func TestSetupWithoutDryRunProcessExitFourHumanAndJSON(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "hao")
	build := exec.Command("go", "build", "-o", binary, "./cmd/hao")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build hao: %v\n%s", err, output)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "human", args: []string{"setup"}, want: "hao setup mutation is not yet supported"},
		{name: "json", args: []string{"--json", "setup"}, want: `"code":"feature_deferred"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(binary, tc.args...)
			output, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 4 || !strings.Contains(string(output), tc.want) {
				t.Fatalf("exit=%v output=%s", err, output)
			}
		})
	}
}

type panicObserver struct{}

func (panicObserver) Platform() (string, string)            { panic("platform probe") }
func (panicObserver) Distribution() (string, error)         { panic("distribution probe") }
func (panicObserver) CurrentUser() (UserObservation, error) { panic("user probe") }
func (panicObserver) Stat(string) (FileObservation, error)  { panic("filesystem probe") }
func (panicObserver) ReadFile(string) ([]byte, error)       { panic("file read") }
func (panicObserver) InspectArtifact(context.Context, string) (ArtifactMetadata, error) {
	panic("artifact probe")
}
func (panicObserver) Disk(string) (uint64, error)     { panic("disk probe") }
func (panicObserver) LookPath(string) (string, error) { panic("path probe") }
func (panicObserver) Run(context.Context, string, ...string) (string, error) {
	panic("subprocess probe")
}
func (panicObserver) ReadRunFile(string) (*runfile.Info, error)   { panic("runfile probe") }
func (panicObserver) ProcessAlive(int) bool                       { panic("process probe") }
func (panicObserver) GET(context.Context, string) ([]byte, error) { panic("network probe") }
func (panicObserver) PortAvailable(context.Context, string, int) (bool, error) {
	panic("listener probe")
}

func TestSetupHumanJSONRedactionAndYesSemantics(t *testing.T) {
	obs := healthyObserver()
	deps, _ := setupDeps(t, "local", obs)
	obs.paths["git"] = `/tmp/token=setup-secret/git`
	for _, args := range [][]string{{"--config", fixturePath("valid", "local.yaml"), "setup", "--dry-run", "--yes"}, {"--json", "--config", fixturePath("valid", "local.yaml"), "setup", "--dry-run", "--yes"}} {
		out, stderr, code := runCLI(t, deps, args...)
		if code != 0 || stderr != "" || strings.Contains(out, "setup-secret") || !strings.Contains(out, "[REDACTED]") {
			t.Fatalf("args=%v code=%d out=%s err=%s", args, code, out, stderr)
		}
		if args[0] != "--json" && !strings.Contains(out, "--yes has no mutation effect") {
			t.Fatalf("missing --yes note: %s", out)
		}
	}
}
