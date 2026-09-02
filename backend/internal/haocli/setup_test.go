package haocli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

func setupDeps(t *testing.T, fixture string, obs *fakeObserver) (Deps, string) {
	t.Helper()
	deps := observationDeps(t, fixture, obs)
	root, err := deps.StateDir()
	if err != nil {
		t.Fatal(err)
	}
	obs.files[filepath.Join(root, "bin", "ao")] = FileObservation{Mode: 0o755, Owner: true}
	obs.artifacts[filepath.Join(root, "bin", "ao")] = ArtifactMetadata{Version: "0.14.0", SHA256: strings.Repeat("a", 64), Source: "test-release"}
	obs.files["/etc/systemd/system/ao-daemon.service"] = FileObservation{Mode: 0o644, UID: 0}
	obs.files["/etc/systemd/system/ao-gateway.service"] = FileObservation{Mode: 0o644, UID: 0}
	desired := setupDesired{StateRoot: root}
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

	t.Run("missing plans package and allowlisted vendor actions", func(t *testing.T) {
		obs := healthyObserver()
		for _, name := range []string{"git", "gh", "claude"} {
			delete(obs.paths, name)
		}
		deps, root := setupDeps(t, "pair", obs)
		obs.statErr[filepath.Join(root, "bin", "ao")] = os.ErrNotExist
		obs.statErr["/etc/systemd/system/ao-daemon.service"] = os.ErrNotExist
		obs.statErr["/etc/systemd/system/ao-gateway.service"] = os.ErrNotExist
		out, stderr, code := runCLI(t, deps, "--json", "--config", fixturePath("valid", "pair.yaml"), "setup", "--dry-run", "--install", "missing")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d stderr=%q out=%s", code, stderr, out)
		}
		plan := decodeSetupPlan(t, out)
		for _, id := range []string{"prerequisite.git", "prerequisite.gh"} {
			step := stepByID(t, plan, id)
			if step.Disposition != "create" || !step.Privilege.Required || step.Privilege.Scope != "package-install" || step.Action == nil || len(step.Action.Argv) != 2 {
				t.Fatalf("%s=%+v", id, step)
			}
			if strings.Contains(strings.Join(step.Action.Argv, " "), "sudo") {
				t.Fatalf("sudo embedded in argv: %+v", step.Action)
			}
		}
		harness := stepByID(t, plan, "prerequisite.harness")
		if harness.Disposition != "create" || harness.Privilege.Required || harness.Action == nil {
			t.Fatalf("harness=%+v", harness)
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
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	plan := decodeSetupPlan(t, out)
	if stepByID(t, plan, "artifact.ao").Disposition != "update" || stepByID(t, plan, "directory.data").Disposition != "update" {
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
	if code != 0 {
		t.Fatalf("code=%d out=%s", code, out)
	}
	step := stepByID(t, decodeSetupPlan(t, out), "prerequisite.harness")
	if step.Disposition != "create" || step.Action == nil || step.Action.Kind != "vendor-package" || len(step.Dependencies) != 1 {
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
	if step.Disposition != "update" || step.Action == nil || !strings.Contains(step.Action.Content, "ExecStart=") || !strings.Contains(step.Action.Content, "AO_DATA_DIR") {
		t.Fatalf("step=%+v", step)
	}
}

func TestSystemObserverBlocksManagedSymlinkAndVerifiesArtifactManifest(t *testing.T) {
	root := t.TempDir()
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
	got, err := (systemObserver{}).InspectArtifact(artifact)
	if err != nil || !reflect.DeepEqual(got, metadata) {
		t.Fatalf("got=%+v err=%v", got, err)
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
	if code != 2 || out != "" {
		t.Fatalf("code=%d out=%q stderr=%q", code, out, stderr)
	}
	assertEnvelope(t, stderr, code, 2, "feature_deferred", "setup")
}

type panicObserver struct{}

func (panicObserver) Platform() (string, string)                       { panic("platform probe") }
func (panicObserver) Distribution() (string, error)                    { panic("distribution probe") }
func (panicObserver) CurrentUser() (UserObservation, error)            { panic("user probe") }
func (panicObserver) Stat(string) (FileObservation, error)             { panic("filesystem probe") }
func (panicObserver) ReadFile(string) ([]byte, error)                  { panic("file read") }
func (panicObserver) InspectArtifact(string) (ArtifactMetadata, error) { panic("artifact probe") }
func (panicObserver) Disk(string) (uint64, error)                      { panic("disk probe") }
func (panicObserver) LookPath(string) (string, error)                  { panic("path probe") }
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
