package cli

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/daemonmeta"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

func TestRootHelpDoesNotShowDaemon(t *testing.T) {
	out, _, err := executeCLI(t, Deps{}, "--help")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\n  daemon") {
		t.Fatalf("hidden daemon command leaked into help:\n%s", out)
	}
	for _, want := range []string{"start", "stop", "status", "doctor", "completion", "version"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %q:\n%s", want, out)
		}
	}
}

func TestStatusStoppedJSON(t *testing.T) {
	setConfigEnv(t)

	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return false }}, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"state": "stopped"`) {
		t.Fatalf("status did not report stopped:\n%s", out)
	}
	if strings.Contains(out, "startedAt") {
		t.Fatalf("stopped JSON should omit startedAt:\n%s", out)
	}
}

func TestStartReturnsExistingReadyDaemon(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = fmt.Fprintf(w, `{"status":"ok","service":%q,"pid":%d}`, daemonmeta.ServiceName, os.Getpid())
		case "/readyz":
			_, _ = fmt.Fprintf(w, `{"status":"ready","service":%q,"pid":%d}`, daemonmeta.ServiceName, os.Getpid())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	port := serverPort(t, srv.URL)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: os.Getpid(), Port: port, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	var started bool
	out, _, err := executeCLI(t, Deps{
		ProcessAlive: func(pid int) bool { return pid == os.Getpid() },
		StartProcess: func(processStartConfig) (processHandle, error) {
			started = true
			return processHandle{}, nil
		},
		Now: func() time.Time { return time.Unix(110, 0).UTC() },
	}, "start", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("start should not spawn when daemon is already ready")
	}
	if !strings.Contains(out, `"state": "ready"`) {
		t.Fatalf("start did not report ready:\n%s", out)
	}
}

func TestStopRemovesStaleRunFile(t *testing.T) {
	cfg := setConfigEnv(t)
	if err := runfile.Write(cfg.runFile, runfile.Info{PID: 999999, Port: 3001, StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return false }}, "stop", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"state": "stopped"`) {
		t.Fatalf("stop did not report stopped:\n%s", out)
	}
	info, err := runfile.Read(cfg.runFile)
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Fatalf("stale run-file was not removed: %#v", info)
	}
}

func TestStopDoesNotSignalUnverifiedReusedPID(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/readyz":
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if err := runfile.Write(cfg.runFile, runfile.Info{PID: 4242, Port: serverPort(t, srv.URL), StartedAt: time.Unix(100, 0).UTC()}); err != nil {
		t.Fatal(err)
	}

	var signaled bool
	out, _, err := executeCLI(t, Deps{
		ProcessAlive: func(pid int) bool { return pid == 4242 },
		SignalTerm: func(pid int) error {
			signaled = true
			return nil
		},
	}, "stop", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if signaled {
		t.Fatal("stop signaled a PID whose health probe did not prove AO daemon ownership")
	}
	if !strings.Contains(out, `"state": "stopped"`) {
		t.Fatalf("stop did not report stopped:\n%s", out)
	}
	info, err := runfile.Read(cfg.runFile)
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Fatalf("unverified run-file was not removed: %#v", info)
	}
}

type testConfig struct {
	runFile string
	dataDir string
}

func setConfigEnv(t *testing.T) testConfig {
	t.Helper()
	dir := t.TempDir()
	cfg := testConfig{
		runFile: filepath.Join(dir, "running.json"),
		dataDir: filepath.Join(dir, "data"),
	}
	t.Setenv("AO_RUN_FILE", cfg.runFile)
	t.Setenv("AO_DATA_DIR", cfg.dataDir)
	t.Setenv("AO_PORT", "3001")
	t.Setenv("AO_REQUEST_TIMEOUT", "")
	t.Setenv("AO_SHUTDOWN_TIMEOUT", "")
	return cfg
}

func executeCLI(t *testing.T, deps Deps, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	deps.Out = &out
	deps.Err = &errOut
	if deps.Sleep == nil {
		deps.Sleep = func(time.Duration) {}
	}
	cmd := NewRootCommand(deps)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func serverPort(t *testing.T, raw string) int {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, portRaw, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatal(err)
	}
	return port
}
