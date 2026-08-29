package haocli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

type fakeObserver struct {
	platform, arch string
	files          map[string]FileObservation
	statErr        map[string]error
	paths          map[string]string
	runs           map[string]string
	runErr         map[string]error
	runFile        *runfile.Info
	runFileErr     error
	alive          bool
	responses      map[string][]byte
	getErr         map[string]error
	urls           []string
	disk           uint64
	diskErr        error
	portAvailable  bool
	portErr        error
	runCalls       int
}

func (f *fakeObserver) Platform() (string, string) { return f.platform, f.arch }
func (f *fakeObserver) Stat(path string) (FileObservation, error) {
	if err := f.statErr[path]; err != nil {
		return FileObservation{}, err
	}
	if v, ok := f.files[path]; ok {
		return v, nil
	}
	return FileObservation{Mode: 0o700, Owner: true}, nil
}
func (f *fakeObserver) Disk(string) (uint64, error) { return f.disk, f.diskErr }
func (f *fakeObserver) LookPath(name string) (string, error) {
	if p, ok := f.paths[name]; ok {
		return p, nil
	}
	return "", os.ErrNotExist
}
func (f *fakeObserver) Run(ctx context.Context, name string, args ...string) (string, error) {
	f.runCalls++
	key := name + " " + strings.Join(args, " ")
	if err := f.runErr[key]; err != nil {
		return "", err
	}
	if v, ok := f.runs[key]; ok {
		return v, nil
	}
	return "version 1.0.0", nil
}
func (f *fakeObserver) ReadRunFile(string) (*runfile.Info, error) { return f.runFile, f.runFileErr }
func (f *fakeObserver) ProcessAlive(int) bool                     { return f.alive }
func (f *fakeObserver) GET(_ context.Context, url string) ([]byte, error) {
	f.urls = append(f.urls, url)
	if err := f.getErr[url]; err != nil {
		return nil, err
	}
	return f.responses[url], nil
}
func (f *fakeObserver) PortAvailable(context.Context, string, int) (bool, error) {
	return f.portAvailable, f.portErr
}

func healthyObserver() *fakeObserver {
	return &fakeObserver{platform: "linux", arch: "amd64", files: map[string]FileObservation{}, statErr: map[string]error{}, paths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "claude": "/usr/bin/claude", "apt-get": "/usr/bin/apt-get", "systemctl": "/usr/bin/systemctl"}, runs: map[string]string{}, runErr: map[string]error{}, runFile: &runfile.Info{PID: 42, Port: 4321}, alive: true, disk: 2 << 30, portAvailable: true, responses: map[string][]byte{
		"http://127.0.0.1:4321/healthz":       []byte(`{"status":"ok","service":"agent-orchestrator-daemon","pid":42}`),
		"http://127.0.0.1:4321/readyz":        []byte(`{"status":"ready","service":"agent-orchestrator-daemon","pid":42}`),
		"http://127.0.0.1:4321/api/v1/doctor": []byte(`{"ok":true,"failures":0,"checks":[{"level":"PASS","section":"Core","name":"runtime","message":"available"}]}`),
	}, getErr: map[string]error{}}
}

func observationDeps(t *testing.T, fixture string, obs *fakeObserver) Deps {
	t.Helper()
	root := t.TempDir()
	path := fixturePath("valid", fixture+".yaml")
	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return Deps{StateDir: func() (string, error) { return root, nil }, ReadFile: func(got string) ([]byte, error) {
		if got != absPath {
			t.Fatalf("config path=%q want %q", got, absPath)
		}
		return os.ReadFile(got)
	}, Observer: obs, Timeout: 25 * time.Millisecond}
}

func TestStatusLocalPairJSONAndStrict(t *testing.T) {
	for _, fixture := range []string{"local", "pair"} {
		t.Run(fixture, func(t *testing.T) {
			obs := healthyObserver()
			deps := observationDeps(t, fixture, obs)
			out, stderr, code := runCLI(t, deps, "--config", fixturePath("valid", fixture+".yaml"), "--json", "status", "--strict")
			if code != 0 || stderr != "" {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			var report StatusReport
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatal(err)
			}
			if report.SchemaVersion != 1 || report.Mode != fixture || report.Machine == "" {
				t.Fatalf("report=%+v", report)
			}
			for _, url := range obs.urls {
				if !strings.HasPrefix(url, "http://127.0.0.1:4321/") {
					t.Fatalf("off-loopback URL %q", url)
				}
			}
		})
	}

	obs := healthyObserver()
	obs.runFile = nil
	_, stderr, code := runCLI(t, observationDeps(t, "pair", obs), "--config", fixturePath("valid", "pair.yaml"), "--json", "status", "--strict")
	if code != 1 || stderr != "" {
		t.Fatalf("strict code=%d stderr=%q", code, stderr)
	}
}

func TestStatusUnknownAndConditionalTools(t *testing.T) {
	obs := healthyObserver()
	delete(obs.paths, "gh")
	delete(obs.paths, "claude")
	obs.getErr["http://127.0.0.1:4321/readyz"] = errors.New("timeout token=secret")
	out, _, code := runCLI(t, observationDeps(t, "local", obs), "--config", fixturePath("valid", "local.yaml"), "--json", "status")
	if code != 0 || strings.Contains(out, "secret") || !strings.Contains(out, "unknown") || !strings.Contains(out, `"id":"tool.gh","status":"disabled"`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestDoctorPartialFailuresStableIDsAndZeroMutation(t *testing.T) {
	obs := healthyObserver()
	obs.statErr = map[string]error{}
	delete(obs.paths, "gh")
	obs.diskErr = errors.New("disk probe failed")
	obs.responses["http://127.0.0.1:4321/api/v1/doctor"] = []byte(`{"ok":false,"failures":1,"checks":[{"level":"FAIL","name":"terminal capability","message":"missing","remediation":"repair AO"}]}`)
	out, stderr, code := runCLI(t, observationDeps(t, "pair", obs), "--config", fixturePath("valid", "pair.yaml"), "--json", "doctor")
	if code != 1 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, `"id":"ao.doctor.terminal-capability"`) || !strings.Contains(out, `"id":"host.disk"`) || !strings.Contains(out, `"status":"error"`) || !strings.Contains(out, `"id":"tool.gh"`) {
		t.Fatalf("out=%s", out)
	}
	if obs.runCalls != 3 {
		t.Fatalf("run calls=%d; wanted only available version and exit-only auth probes", obs.runCalls)
	}
}

func TestMalformedAndMissingConfigRemainConfigurationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		code       int
	}{{"missing", filepath.Join(t.TempDir(), "missing.yaml"), 1}, {"malformed", fixturePath("invalid", "future-version.yaml"), 2}} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, code := runCLI(t, Deps{}, "--config", tc.path, "status")
			if code != tc.code {
				t.Fatalf("code=%d want=%d", code, tc.code)
			}
		})
	}
}

func TestHTTPObserverRejectsRedirectAndOversizedBody(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		targetHit := false
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHit = true }))
		defer target.Close()
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
		defer source.Close()
		_, err := (systemObserver{}).GET(context.Background(), source.URL)
		if err == nil || targetHit {
			t.Fatalf("err=%v targetHit=%v", err, targetHit)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", maxHTTPBody+1)))
		}))
		defer server.Close()
		_, err := (systemObserver{}).GET(context.Background(), server.URL)
		if err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestProbeTimeoutIsUnknown(t *testing.T) {
	obs := healthyObserver()
	obs.Run(context.Background(), "noop")
	obs.runErr["/usr/bin/git --version"] = context.DeadlineExceeded
	o := toolObservation(context.Background(), Deps{Observer: obs, Timeout: time.Nanosecond}.withDefaults(), "tool.git", "git", true, "--version")
	if o.Status != "unknown" {
		t.Fatalf("observation=%+v", o)
	}
}
