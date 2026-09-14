package haocli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/haocontract"
)

func TestConfigCreateNonInteractiveLocalAndPair(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  string
		extra []string
	}{
		{name: "local", mode: "local"},
		{name: "pair", mode: "pair", extra: []string{"--workflow", "github", "--pair-port", "8443"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(resolvedTempDir(t), "nested", "config.yaml")
			args := []string{"--json", "--config", path, "config", "create", "--non-interactive", "--machine", "build-box", "--mode", tc.mode, "--ao-version", "0.14.0", "--harness", "claude-code", "--install", "missing", "--service-enabled", "true"}
			args = append(args, tc.extra...)
			out, stderr, code := runCLI(t, Deps{}, args...)
			if code != 0 || stderr != "" || !strings.Contains(out, `"status":"created"`) {
				t.Fatalf("create: code=%d out=%q err=%q", code, out, stderr)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			object, err := haocontract.ParseConfig(data)
			if err != nil || object["mode"] != tc.mode {
				t.Fatalf("written config: object=%v err=%v", object, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("config mode: info=%v err=%v", info, err)
			}
		})
	}
}

func TestConfigCreateInteractiveAndDryRun(t *testing.T) {
	path := filepath.Join(resolvedTempDir(t), "config.yaml")
	input := strings.NewReader("laptop\nlocal\n0.14.0\nclaude-code\nnone\nfalse\ngeneral\n")
	out, prompts, code := runCLI(t, Deps{In: input}, "--config", path, "config", "create", "--dry-run")
	if code != 0 || !strings.Contains(out, "planned:") || !strings.Contains(prompts, "Machine name") {
		t.Fatalf("interactive dry-run: code=%d out=%q prompts=%q", code, out, prompts)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote config: %v", err)
	}
}

func TestConfigCreateMissingNonInteractiveInputUsesStableEnvelope(t *testing.T) {
	path := filepath.Join(resolvedTempDir(t), "config.yaml")
	_, stderr, code := runCLI(t, Deps{}, "--json", "--config", path, "config", "create", "--non-interactive", "--mode", "local")
	assertEnvelope(t, stderr, code, 2, "missing_non_interactive_input", "config create")
}

func TestConfigSetKnownValuesBacksUpAndExistingReadersConsumeResult(t *testing.T) {
	path := filepath.Join(resolvedTempDir(t), "config.yaml")
	copyFixture(t, fixturePath("valid", "local.yaml"), path)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	out, stderr, code := runCLI(t, Deps{}, "--json", "--config", path, "config", "set", "mode", "pair")
	if code != 0 || stderr != "" || !strings.Contains(out, `"status":"updated"`) {
		t.Fatalf("set: code=%d out=%q err=%q", code, out, stderr)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || string(backup) != string(original) {
		t.Fatalf("backup mismatch: err=%v got=%q want=%q", err, backup, original)
	}
	for _, args := range [][]string{{"config", "show"}, {"config", "validate"}} {
		out, stderr, code = runCLI(t, Deps{}, append([]string{"--config", path}, args...)...)
		if code != 0 || stderr != "" {
			t.Fatalf("reader %v: code=%d out=%q err=%q", args, code, out, stderr)
		}
	}
	object := mustReadConfig(t, path)
	if object["mode"] != "pair" || configInt(object, "pair", "listenPort") != 443 {
		t.Fatalf("mode transition did not create valid pair defaults: %v", object)
	}
}

func TestConfigSetRejectsUnknownInvalidAndFutureState(t *testing.T) {
	for _, tc := range []struct {
		name, fixture, key, value, taxonomy string
	}{
		{"unknown", "local.yaml", "pair.passcode", "do-not-store", "invalid_config"},
		{"bad value", "local.yaml", "service.enabled", "sometimes", "invalid_config"},
		{"pair field in local", "local.yaml", "pair.listenPort", "443", "invalid_config"},
		{"future", "../invalid/future-version.yaml", "machine.name", "new-name", "unsupported_config_version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(resolvedTempDir(t), "config.yaml")
			source := filepath.Join(fixturePath("valid"), tc.fixture)
			if strings.HasPrefix(tc.fixture, "../") {
				source = filepath.Clean(filepath.Join(fixturePath("valid"), tc.fixture))
			}
			copyFixture(t, source, path)
			before, _ := os.ReadFile(path)
			_, stderr, code := runCLI(t, Deps{}, "--json", "--config", path, "config", "set", tc.key, tc.value)
			assertEnvelope(t, stderr, code, 2, tc.taxonomy, "config set")
			after, _ := os.ReadFile(path)
			if string(after) != string(before) || strings.Contains(stderr, "do-not-store") {
				t.Fatalf("rejected mutation changed or leaked state: before=%q after=%q err=%q", before, after, stderr)
			}
		})
	}
}

func TestWrittenConfigIsConsumedByStatusDoctorAndSetup(t *testing.T) {
	path := filepath.Join(resolvedTempDir(t), "config.yaml")
	_, stderr, code := runCLI(t, Deps{}, "--config", path, "config", "create", "--non-interactive", "--machine", "reader-box", "--mode", "local", "--ao-version", "0.14.0", "--harness", "claude-code", "--install", "none", "--service-enabled", "false", "--workflow", "general")
	if code != 0 {
		t.Fatalf("create: code=%d err=%q", code, stderr)
	}

	obs := healthyObserver()
	deps := observationDeps(t, "local", obs)
	deps.ReadFile = os.ReadFile
	for _, command := range []string{"status", "doctor"} {
		out, stderr, code := runCLI(t, deps, "--json", "--config", path, command)
		if code != 0 || stderr != "" || !strings.Contains(out, "reader-box") {
			t.Fatalf("%s did not consume written config: code=%d out=%q err=%q", command, code, out, stderr)
		}
	}

	setup, _ := setupDeps(t, "local", healthyObserver())
	setup.ReadFile = os.ReadFile
	out, stderr := "", ""
	out, stderr, code = runCLI(t, setup, "--json", "--config", path, "setup", "--dry-run")
	if code != 0 || stderr != "" || !strings.Contains(out, "reader-box") {
		t.Fatalf("setup did not consume written config: code=%d out=%q err=%q", code, out, stderr)
	}
}

func TestConfigMutationBusyStaleAndUnsafePaths(t *testing.T) {
	t.Run("busy", func(t *testing.T) {
		path := filepath.Join(resolvedTempDir(t), "config.yaml")
		copyFixture(t, fixturePath("valid", "local.yaml"), path)
		lock, err := acquireConfigLock(path + ".lock")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = releaseConfigLock(lock) }()
		_, _, err = (configMutationStore{}).set(path, "machine.name", "other", false)
		if !errors.Is(err, errConfigBusy) {
			t.Fatalf("set err=%v, want busy", err)
		}
	})

	t.Run("stale", func(t *testing.T) {
		path := filepath.Join(resolvedTempDir(t), "config.yaml")
		copyFixture(t, fixturePath("valid", "local.yaml"), path)
		replacement, _ := os.ReadFile(fixturePath("valid", "pair.yaml"))
		store := configMutationStore{beforeCommit: func() {
			if err := os.WriteFile(path, replacement, 0o600); err != nil {
				t.Fatal(err)
			}
		}}
		_, _, err := store.set(path, "machine.name", "other", false)
		if !errors.Is(err, errConfigStale) {
			t.Fatalf("set err=%v, want stale", err)
		}
		if object := mustReadConfig(t, path); object["mode"] != "pair" {
			t.Fatalf("stale writer overwrote replacement: %v", object)
		}
	})

	t.Run("linked ancestor", func(t *testing.T) {
		root := resolvedTempDir(t)
		real := filepath.Join(root, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "linked")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(link, "config.yaml")
		_, stderr, code := runCLI(t, Deps{}, "--json", "--config", path, "config", "create", "--non-interactive", "--machine", "box", "--mode", "local", "--ao-version", "0.14.0", "--harness", "claude-code", "--install", "none", "--service-enabled", "false")
		assertEnvelope(t, stderr, code, 1, "operation_failed", "config create")
		if _, err := os.Stat(filepath.Join(real, "config.yaml")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("write escaped through linked ancestor: %v", err)
		}
	})
}

func TestConfigMutationRecoversBackupAndIgnoresInterruptedTemporary(t *testing.T) {
	path := filepath.Join(resolvedTempDir(t), "config.yaml")
	copyFixture(t, fixturePath("valid", "local.yaml"), path)
	if _, _, err := (configMutationStore{}).set(path, "machine.name", "first", false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("version: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".hao-config-interrupted"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (configMutationStore{}).set(path, "machine.name", "recovered", false); err != nil {
		t.Fatal(err)
	}
	if got := configString(mustReadConfig(t, path), "machine", "name"); got != "recovered" {
		t.Fatalf("machine name=%q, want recovered", got)
	}
	backup := mustReadConfig(t, path+".bak")
	if got := configString(backup, "machine", "name"); got != "laptop" {
		t.Fatalf("post-recovery backup=%q, want original last-known-good", got)
	}
}

func TestConfigBackupPlanningAndRejectedEditsAreReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "dry-run", args: []string{"machine.name", "preview", "--dry-run"}},
		{name: "unknown key", args: []string{"pair.passcode", "do-not-store"}},
		{name: "invalid value", args: []string{"service.enabled", "sometimes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(resolvedTempDir(t), "config.yaml")
			copyFixture(t, fixturePath("valid", "local.yaml"), path+".bak")
			corrupt := []byte("version: [\n")
			if err := os.WriteFile(path, corrupt, 0o600); err != nil {
				t.Fatal(err)
			}
			backupBefore, _ := os.ReadFile(path + ".bak")
			args := append([]string{"--config", path, "config", "set"}, tc.args...)
			_, _, _ = runCLI(t, Deps{}, args...)
			primaryAfter, _ := os.ReadFile(path)
			backupAfter, _ := os.ReadFile(path + ".bak")
			if !bytes.Equal(primaryAfter, corrupt) || !bytes.Equal(backupAfter, backupBefore) {
				t.Fatalf("planning mutated recovery files: primary=%q backup changed=%t", primaryAfter, !bytes.Equal(backupAfter, backupBefore))
			}
		})
	}
}

func copyFixture(t *testing.T, source, target string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustReadConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	object, err := haocontract.ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func resolvedTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
