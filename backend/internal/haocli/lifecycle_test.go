package haocli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// lifecycleConfig renders a mutated copy of a valid fixture as a real
// config file on disk, so lifecycle commands run their full
// load-config -> discover-manager -> operate path.
func lifecycleConfig(t *testing.T, fixtureName string, mutate func(map[string]any)) string {
	t.Helper()
	data, err := os.ReadFile(fixturePath("valid", fixtureName+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := yaml.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(object)
	}
	out, err := yaml.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func lifecycleDeps(t *testing.T, obs *fakeObserver) Deps {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Deps{
		StateDir: func() (string, error) { return root, nil },
		DataDir:  func() (string, error) { return filepath.Join(root, "data"), nil },
		RunFile:  func() (string, error) { return filepath.Join(root, "running.json"), nil },
		In:       strings.NewReader(""),
		ReadFile: os.ReadFile,
		Observer: obs,
		Timeout:  25 * time.Millisecond,
	}
}

func decodeLifecycleReport(t *testing.T, out string) lifecycleReport {
	t.Helper()
	var report lifecycleReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode lifecycle report: %v\n%s", err, out)
	}
	return report
}

func TestLifecycleStartStopRestartOrderDependencies(t *testing.T) {
	obs := healthyObserver()
	obs.runFile = nil
	activeUnits(obs)
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps := lifecycleDeps(t, obs)
	deps.ServiceCommands = commands
	config := fixturePathReal(t, "pair")

	out, stderr, code := runCLI(t, deps, "--json", "--config", config, "start")
	if code != 0 || stderr != "" {
		t.Fatalf("start: code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodeLifecycleReport(t, out)
	if report.Operation != "start" || !report.Supported || report.Manager != "systemd" {
		t.Fatalf("report=%+v", report)
	}
	if want := []string{"start " + haoDaemonUnit, "start " + haoGatewayUnit}; !reflect.DeepEqual(commandArgv(commands.calls), want) {
		t.Fatalf("start calls=%v want=%v", commandArgv(commands.calls), want)
	}
	if len(report.States) != 2 || !report.States[0].Active || !report.States[1].Active {
		t.Fatalf("states=%+v", report.States)
	}

	commands.calls = nil
	_, stderr, code = runCLI(t, deps, "--json", "--config", config, "stop")
	if code != 0 || stderr != "" {
		t.Fatalf("stop: code=%d err=%q", code, stderr)
	}
	if want := []string{"stop " + haoGatewayUnit, "stop " + haoDaemonUnit}; !reflect.DeepEqual(commandArgv(commands.calls), want) {
		t.Fatalf("stop calls=%v want=%v", commandArgv(commands.calls), want)
	}

	commands.calls = nil
	_, stderr, code = runCLI(t, deps, "--json", "--config", config, "restart")
	if code != 0 || stderr != "" {
		t.Fatalf("restart: code=%d err=%q", code, stderr)
	}
	want := []string{"stop " + haoGatewayUnit, "stop " + haoDaemonUnit, "start " + haoDaemonUnit, "start " + haoGatewayUnit}
	if got := commandArgv(commands.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("restart calls=%v want=%v", got, want)
	}
	for _, call := range commands.calls {
		if !call.privileged || call.executable != "/usr/bin/systemctl" {
			t.Fatalf("unsafe service mutation=%+v", call)
		}
	}
}

func fixturePathReal(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(fixturePath("valid", name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLifecycleComponentSelection(t *testing.T) {
	obs := healthyObserver()
	obs.runFile = nil
	activeUnits(obs)
	t.Run("explicit component", func(t *testing.T) {
		commands := &fakeServiceCommands{fail: map[string]error{}}
		deps := lifecycleDeps(t, obs)
		deps.ServiceCommands = commands
		_, stderr, code := runCLI(t, deps, "--config", fixturePathReal(t, "pair"), "start", "gateway")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d err=%q", code, stderr)
		}
		if got := commandArgv(commands.calls); !reflect.DeepEqual(got, []string{"start " + haoGatewayUnit}) {
			t.Fatalf("calls=%v", got)
		}
	})
	t.Run("local mode defaults to daemon only", func(t *testing.T) {
		commands := &fakeServiceCommands{fail: map[string]error{}}
		deps := lifecycleDeps(t, obs)
		deps.ServiceCommands = commands
		// The local.yaml fixture disables service management on purpose; a
		// lifecycle operator must have enabled it, so force it here.
		config := lifecycleConfig(t, "local", func(m map[string]any) {
			m["service"] = map[string]any{"enabled": true}
		})
		_, stderr, code := runCLI(t, deps, "--config", config, "stop")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d err=%q", code, stderr)
		}
		if got := commandArgv(commands.calls); !reflect.DeepEqual(got, []string{"stop " + haoDaemonUnit}) {
			t.Fatalf("calls=%v", got)
		}
	})
	t.Run("unknown component is usage error", func(t *testing.T) {
		deps := lifecycleDeps(t, obs)
		_, stderr, code := runCLI(t, deps, "--json", "--config", fixturePathReal(t, "pair"), "start", "bogus")
		if code != 2 || !strings.Contains(stderr, "invalid_usage") {
			t.Fatalf("code=%d err=%q", code, stderr)
		}
	})
}

func TestLifecycleServiceDisabledRefusesMutationButAllowsLogs(t *testing.T) {
	obs := healthyObserver()
	obs.runFile = nil
	obs.paths["journalctl"] = "/usr/bin/journalctl"
	commands := &fakeServiceCommands{fail: map[string]error{}, output: map[string]string{}}
	commands.output["/usr/bin/journalctl --no-pager --lines 200 --output short-iso-precise --unit "+haoDaemonUnit] = "daemon booted"
	deps := lifecycleDeps(t, obs)
	deps.ServiceCommands = commands
	config := lifecycleConfig(t, "pair", func(m map[string]any) {
		m["service"] = map[string]any{"enabled": false}
	})

	for _, operation := range []string{"start", "stop", "restart"} {
		_, stderr, code := runCLI(t, deps, "--json", "--config", config, operation)
		if code != 2 || !strings.Contains(stderr, "service_disabled") {
			t.Fatalf("%s: code=%d err=%q", operation, code, stderr)
		}
	}
	if len(commands.calls) != 0 {
		t.Fatalf("disabled service was mutated: %+v", commands.calls)
	}

	out, stderr, code := runCLI(t, deps, "--json", "--config", config, "logs")
	if code != 0 || stderr != "" {
		t.Fatalf("logs: code=%d err=%q out=%s", code, stderr, out)
	}
	var journal journalReport
	if err := json.Unmarshal([]byte(out), &journal); err != nil {
		t.Fatal(err)
	}
	if journal.Component != "daemon" || journal.Unit != haoDaemonUnit || journal.Output != "daemon booted" {
		t.Fatalf("journal=%+v", journal)
	}
}

func TestLifecycleReportsManualCommandsWithoutManager(t *testing.T) {
	obs := healthyObserver()
	obs.platform = "darwin"
	deps := lifecycleDeps(t, obs)
	out, stderr, code := runCLI(t, deps, "--json", "--config", fixturePathReal(t, "pair"), "start")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
	}
	report := decodeLifecycleReport(t, out)
	if report.Supported || len(report.Manual) != 3 {
		t.Fatalf("report=%+v", report)
	}
	joined := strings.Join(report.Manual, "\n")
	if !strings.Contains(joined, "systemctl start "+haoDaemonUnit+" "+haoGatewayUnit) {
		t.Fatalf("manual=%v", report.Manual)
	}
}

func TestLifecycleRefusesDesktopSupervisedDaemon(t *testing.T) {
	obs := healthyObserver() // runFile alive (PID 42), daemon unit inactive
	obs.runs[serviceStatusKey("daemon")] = "LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead"
	obs.runs[serviceStatusKey("gateway")] = "LoadState=loaded\nUnitFileState=disabled\nActiveState=inactive\nSubState=dead"
	commands := &fakeServiceCommands{fail: map[string]error{}}
	deps := lifecycleDeps(t, obs)
	deps.ServiceCommands = commands

	_, stderr, code := runCLI(t, deps, "--json", "--config", fixturePathReal(t, "pair"), "start")
	if code != 1 || !strings.Contains(stderr, "desktop_supervised_daemon") {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
	if len(commands.calls) != 0 {
		t.Fatalf("conflicting daemon was started: %+v", commands.calls)
	}
}

func TestLifecycleLogsValidationAndScoping(t *testing.T) {
	obs := healthyObserver()
	obs.runFile = nil
	obs.paths["journalctl"] = "/usr/bin/journalctl"
	t.Run("default daemon component", func(t *testing.T) {
		commands := &fakeServiceCommands{fail: map[string]error{}, output: map[string]string{}}
		commands.output["/usr/bin/journalctl --no-pager --lines 5 --output short-iso-precise --unit "+haoDaemonUnit] = "line1\nline2"
		deps := lifecycleDeps(t, obs)
		deps.ServiceCommands = commands
		out, stderr, code := runCLI(t, deps, "--json", "--config", fixturePathReal(t, "local"), "logs", "--lines", "5")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
		}
		var journal journalReport
		if err := json.Unmarshal([]byte(out), &journal); err != nil {
			t.Fatal(err)
		}
		if journal.Component != "daemon" || journal.Lines != 5 || journal.Output != "line1\nline2" {
			t.Fatalf("journal=%+v", journal)
		}
	})
	t.Run("explicit gateway component", func(t *testing.T) {
		commands := &fakeServiceCommands{fail: map[string]error{}, output: map[string]string{}}
		commands.output["/usr/bin/journalctl --no-pager --lines 200 --output short-iso-precise --unit "+haoGatewayUnit] = "gateway log"
		deps := lifecycleDeps(t, obs)
		deps.ServiceCommands = commands
		out, stderr, code := runCLI(t, deps, "--json", "--config", fixturePathReal(t, "pair"), "logs", "gateway")
		if code != 0 || stderr != "" {
			t.Fatalf("code=%d err=%q out=%s", code, stderr, out)
		}
		var journal journalReport
		if err := json.Unmarshal([]byte(out), &journal); err != nil {
			t.Fatal(err)
		}
		if journal.Component != "gateway" || journal.Unit != haoGatewayUnit {
			t.Fatalf("journal=%+v", journal)
		}
	})
	t.Run("lines outside range is usage error", func(t *testing.T) {
		deps := lifecycleDeps(t, obs)
		_, stderr, code := runCLI(t, deps, "--json", "--config", fixturePathReal(t, "local"), "logs", "--lines", "500")
		if code != 2 || !strings.Contains(stderr, "invalid_usage") {
			t.Fatalf("code=%d err=%q", code, stderr)
		}
	})
	t.Run("no journal on unsupported host", func(t *testing.T) {
		obs.platform = "darwin"
		deps := lifecycleDeps(t, obs)
		_, stderr, code := runCLI(t, deps, "--json", "--config", fixturePathReal(t, "local"), "logs")
		if code != 1 || !strings.Contains(stderr, "service_unsupported") {
			t.Fatalf("code=%d err=%q", code, stderr)
		}
	})
}
