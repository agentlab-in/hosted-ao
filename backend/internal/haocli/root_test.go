package haocli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func runCLI(t *testing.T, deps Deps, args ...string) (string, string, int) {
	t.Helper()
	var out, stderr bytes.Buffer
	deps.Out = &out
	deps.Err = &stderr
	deps.In = strings.NewReader("")
	returnOutput := ExecuteArgs(deps, args)
	return out.String(), stderr.String(), returnOutput
}

func fixturePath(parts ...string) string {
	base := []string{"..", "..", "..", "contracts", "hao", "v1", "examples"}
	return filepath.Join(append(base, parts...)...)
}

func TestVersionHumanAndJSON(t *testing.T) {
	restoreVersion, restoreCommit, restoreDate := Version, Commit, Date
	Version, Commit, Date = "1.2.3", "abc123", "2026-08-29T00:00:00Z"
	t.Cleanup(func() { Version, Commit, Date = restoreVersion, restoreCommit, restoreDate })

	out, stderr, code := runCLI(t, Deps{}, "version")
	if code != 0 || stderr != "" || out != "1.2.3 commit abc123 built 2026-08-29T00:00:00Z\n" {
		t.Fatalf("human version: code=%d out=%q err=%q", code, out, stderr)
	}
	out, stderr, code = runCLI(t, Deps{}, "--json", "version")
	if code != 0 || stderr != "" {
		t.Fatalf("JSON version: code=%d err=%q", code, stderr)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"version": "1.2.3", "commit": "abc123", "date": "2026-08-29T00:00:00Z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("version = %#v, want %#v", got, want)
	}
}

func TestConfigPathDefaultExplicitAndRunFileOverride(t *testing.T) {
	root := t.TempDir()
	out, _, code := runCLI(t, Deps{StateDir: func() (string, error) { return root, nil }}, "config", "path")
	if code != 0 || strings.TrimSpace(out) != filepath.Join(root, "hao", "config.yaml") {
		t.Fatalf("default path: code=%d out=%q", code, out)
	}
	explicit := filepath.Join(t.TempDir(), "custom.yaml")
	out, _, code = runCLI(t, Deps{}, "--config", explicit, "--json", "config", "path")
	var got map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &got) != nil || got["path"] != explicit {
		t.Fatalf("explicit path: code=%d out=%q", code, out)
	}

	t.Setenv("AO_RUN_FILE", filepath.Join(root, "overridden", "running.json"))
	out, _, code = runCLI(t, Deps{}, "config", "path")
	if code != 0 || strings.TrimSpace(out) != filepath.Join(root, "overridden", "hao", "config.yaml") {
		t.Fatalf("run-file-derived path: code=%d out=%q", code, out)
	}
}

func TestConfigPathHonorsDataDirWithoutLoadingDaemonSettings(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AO_RUN_FILE", "")
	t.Setenv("AO_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("AO_PORT", "not-a-port")
	t.Setenv("AO_GITLAB_HOST_TOKENS", "example.invalid=example-value=padding")
	out, stderr, code := runCLI(t, Deps{}, "config", "path")
	if code != 0 || stderr != "" || strings.TrimSpace(out) != filepath.Join(root, "hao", "config.yaml") {
		t.Fatalf("data-dir-derived path: code=%d out=%q err=%q", code, out, stderr)
	}
	if strings.Contains(out+stderr, "example-value") {
		t.Fatalf("unrelated daemon setting leaked: out=%q err=%q", out, stderr)
	}
}

func TestConfigValidateExamplesAndExplicitFilePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		code int
		want string
	}{
		{"local", fixturePath("valid", "local.yaml"), 0, `"valid":true`},
		{"pair", fixturePath("valid", "pair.yaml"), 0, `"valid":true`},
		{"future", fixturePath("invalid", "future-version.yaml"), 2, "unsupported_config_version"},
		{"secret", fixturePath("invalid", "secret.yaml"), 2, "invalid_config"},
		{"runtime", fixturePath("invalid", "runtime-backend.yaml"), 2, "invalid_config"},
		{"pair fields", fixturePath("invalid", "pair-missing-port.yaml"), 2, "invalid_config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, code := runCLI(t, Deps{}, "--json", "config", "validate", "--file", tc.path)
			if code != tc.code {
				t.Fatalf("code=%d want=%d out=%q err=%q", code, tc.code, out, stderr)
			}
			combined := out + stderr
			if !strings.Contains(combined, tc.want) {
				t.Fatalf("output %q missing %q", combined, tc.want)
			}
			if strings.Contains(combined, "XK4M2P7Q") {
				t.Fatalf("secret leaked: %s", combined)
			}
		})
	}

	missingDefault := filepath.Join(t.TempDir(), "missing.yaml")
	out, stderr, code := runCLI(t, Deps{}, "--config", missingDefault, "config", "validate", "--file", fixturePath("valid", "local.yaml"))
	if code != 0 || stderr != "" || !strings.Contains(out, "local.yaml") {
		t.Fatalf("--file did not override --config: code=%d out=%q err=%q", code, out, stderr)
	}
}

func TestConfigFailuresUseStableExitAndEnvelope(t *testing.T) {
	t.Run("missing file is operational", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.yaml")
		_, stderr, code := runCLI(t, Deps{}, "--json", "--config", path, "config", "show")
		assertEnvelope(t, stderr, code, 1, "operation_failed", "config show")
	})
	t.Run("malformed YAML is configuration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.yaml")
		if err := os.WriteFile(path, []byte("version: [\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, stderr, code := runCLI(t, Deps{}, "--json", "config", "validate", "--file", path)
		assertEnvelope(t, stderr, code, 2, "invalid_config", "config validate")
	})
	t.Run("bad flag is usage", func(t *testing.T) {
		for _, args := range [][]string{
			{"--json", "config", "show", "--wat"},
			{"config", "show", "--wat", "--json"},
			{"unknown", "--json"},
			{"--json", "--config"},
		} {
			_, stderr, code := runCLI(t, Deps{}, args...)
			if code != 2 {
				t.Fatalf("args=%v code=%d err=%q", args, code, stderr)
			}
			var envelope errorEnvelope
			if err := json.Unmarshal([]byte(stderr), &envelope); err != nil {
				t.Fatalf("args=%v did not preserve JSON framing: %q: %v", args, stderr, err)
			}
			if envelope.Code != "invalid_usage" {
				t.Fatalf("args=%v envelope=%+v", args, envelope)
			}
		}
	})
	t.Run("error details redact secret-looking path fragments", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "token=example-value", "missing.yaml")
		_, stderr, code := runCLI(t, Deps{}, "--json", "--config", path, "config", "show")
		assertEnvelope(t, stderr, code, 1, "operation_failed", "config show")
		if strings.Contains(stderr, "example-value") || !strings.Contains(stderr, "[REDACTED]") {
			t.Fatalf("error envelope was not defensively redacted: %s", stderr)
		}
	})
	t.Run("missing version is invalid config", func(t *testing.T) {
		data, err := os.ReadFile(fixturePath("valid", "local.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "versionless.yaml")
		if err := os.WriteFile(path, []byte(strings.TrimPrefix(string(data), "version: 1\n")), 0o600); err != nil {
			t.Fatal(err)
		}
		_, stderr, code := runCLI(t, Deps{}, "--json", "config", "validate", "--file", path)
		assertEnvelope(t, stderr, code, 2, "invalid_config", "config validate")
	})
	t.Run("state root failure is operational", func(t *testing.T) {
		_, stderr, code := runCLI(t, Deps{StateDir: func() (string, error) { return "", errors.New("no home") }}, "--json", "config", "path")
		assertEnvelope(t, stderr, code, 1, "operation_failed", "config path")
	})
}

func assertEnvelope(t *testing.T, raw string, gotCode, wantCode int, wantTaxonomy, wantOperation string) {
	t.Helper()
	if gotCode != wantCode {
		t.Fatalf("exit=%d want=%d body=%q", gotCode, wantCode, raw)
	}
	var envelope errorEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if envelope.Code != wantTaxonomy || envelope.Component != "hao" || envelope.Operation != wantOperation || envelope.Message == "" || envelope.Remediation == "" || envelope.Details == nil {
		t.Fatalf("incomplete envelope: %+v", envelope)
	}
}

func TestConfigShowHumanAndJSON(t *testing.T) {
	path := fixturePath("valid", "pair.yaml")
	out, stderr, code := runCLI(t, Deps{}, "--config", path, "config", "show")
	if code != 0 || stderr != "" || !strings.Contains(out, "mode: pair") || strings.Contains(out, "\"config\"") {
		t.Fatalf("human show: code=%d out=%q err=%q", code, out, stderr)
	}
	out, stderr, code = runCLI(t, Deps{}, "--json", "--config", path, "config", "show")
	var body struct {
		Path   string         `json:"path"`
		Config map[string]any `json:"config"`
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr != "" || json.Unmarshal([]byte(out), &body) != nil || body.Path != absPath || body.Config["mode"] != "pair" {
		t.Fatalf("JSON show: code=%d out=%q err=%q body=%+v", code, out, stderr, body)
	}
}

func TestCommandSurfaceContainsNoAOOrchestration(t *testing.T) {
	root := NewRootCommand(Deps{})
	got := make([]string, 0, len(root.Commands()))
	for _, cmd := range root.Commands() {
		got = append(got, cmd.Name())
	}
	if want := []string{"config", "doctor", "status", "version"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("hao commands = %v, want %v", got, want)
	}
	for _, forbidden := range []string{"spawn", "session", "send", "project", "review", "terminal", "daemon", "vm"} {
		_, _, code := runCLI(t, Deps{}, forbidden)
		if code != 2 {
			t.Errorf("hao %s exit=%d, want usage exit 2", forbidden, code)
		}
	}
}
