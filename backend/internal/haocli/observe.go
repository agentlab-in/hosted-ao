package haocli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/daemonmeta"
	"github.com/aoagents/agent-orchestrator/backend/internal/processalive"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

const maxHTTPBody = 1 << 20

// Observer is the complete read-only machine boundary used by status and doctor.
// Tests inject it so no probe depends on the developer's machine or network.
type Observer interface {
	Platform() (string, string)
	Stat(path string) (FileObservation, error)
	Disk(path string) (uint64, error)
	LookPath(name string) (string, error)
	Run(ctx context.Context, name string, args ...string) (string, error)
	ReadRunFile(path string) (*runfile.Info, error)
	ProcessAlive(pid int) bool
	GET(ctx context.Context, url string) ([]byte, error)
	PortAvailable(ctx context.Context, host string, port int) (bool, error)
}

// FileObservation contains only ownership and permission facts needed by doctor.
type FileObservation struct {
	Mode  os.FileMode
	UID   int
	Owner bool
}

type systemObserver struct{}

type boundedBuffer struct {
	bytes.Buffer
	remaining int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	if len(p) > 0 {
		_, _ = b.Buffer.Write(p)
		b.remaining -= len(p)
	}
	return original, nil
}

func (systemObserver) Platform() (string, string)                     { return runtime.GOOS, runtime.GOARCH }
func (systemObserver) Stat(path string) (FileObservation, error)      { return statFile(path) }
func (systemObserver) Disk(path string) (uint64, error)               { return diskAvailable(path) }
func (systemObserver) LookPath(name string) (string, error)           { return exec.LookPath(name) }
func (systemObserver) ReadRunFile(path string) (*runfile.Info, error) { return runfile.Read(path) }
func (systemObserver) ProcessAlive(pid int) bool                      { return processalive.Alive(pid) }

func (systemObserver) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = nil
	out := boundedBuffer{remaining: 4096}
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if len(text) > 256 {
		text = text[:256]
	}
	return text, err
}

func (systemObserver) GET(ctx context.Context, rawURL string) ([]byte, error) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxHTTPBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(body) > maxHTTPBody {
		return nil, errors.New("response exceeds size limit")
	}
	return body, nil
}

func (systemObserver) PortAvailable(ctx context.Context, host string, port int) (bool, error) {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		//nolint:nilerr // A bind failure proves unavailability; OS socket text is not useful evidence.
		return false, nil
	}
	return true, ln.Close()
}

type daemonProbe struct {
	State    string
	PID      int
	Health   string
	Ready    string
	Doctor   *aoDoctorReport
	Evidence string
}

type probePayload struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	PID     int    `json:"pid"`
}

type aoDoctorReport struct {
	OK       bool            `json:"ok"`
	Failures int             `json:"failures"`
	Checks   []aoDoctorCheck `json:"checks"`
}

type aoDoctorCheck struct {
	Level       string `json:"level"`
	Section     string `json:"section,omitempty"`
	Name        string `json:"name"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

func observeDaemon(ctx context.Context, obs Observer, runFile string, timeout time.Duration) daemonProbe {
	probeCtx, cancel := boundedContext(ctx, timeout)
	defer cancel()
	info, err := obs.ReadRunFile(runFile)
	if err != nil {
		return daemonProbe{State: "unknown", Evidence: safeDiagnostic(err)}
	}
	if info == nil {
		return daemonProbe{State: "unavailable", Evidence: "daemon discovery file is absent"}
	}
	result := daemonProbe{State: "unhealthy", PID: info.PID}
	if info.Port < 1 || info.Port > 65535 {
		result.Evidence = "daemon discovery contains an invalid port"
		return result
	}
	if !obs.ProcessAlive(info.PID) {
		result.Evidence = "daemon discovery points to a process that is not running"
		return result
	}
	base := "http://127.0.0.1:" + strconv.Itoa(info.Port)
	read := func(path string, target any) error {
		body, getErr := obs.GET(probeCtx, base+path)
		if getErr != nil {
			if probeCtx.Err() != nil || errors.Is(getErr, context.DeadlineExceeded) {
				return fmt.Errorf("probe timed out: %w", context.DeadlineExceeded)
			}
			return getErr
		}
		if len(body) > maxHTTPBody {
			return errors.New("response exceeds size limit")
		}
		if err := json.Unmarshal(body, target); err != nil {
			return fmt.Errorf("malformed response")
		}
		return nil
	}
	var health probePayload
	if err := read("/healthz", &health); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			result.State = "unknown"
		}
		result.Evidence = "health probe failed: " + safeDiagnostic(err)
		return result
	}
	if health.Service != daemonmeta.ServiceName || health.PID != info.PID {
		result.Evidence = "health response did not match discovered AO daemon"
		return result
	}
	result.Health = health.Status
	if health.Status != "ok" {
		result.Evidence = "daemon health is not ok"
		return result
	}
	var ready probePayload
	if err := read("/readyz", &ready); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			result.State = "unknown"
		} else {
			result.State = "unhealthy"
		}
		result.Evidence = "readiness probe failed: " + safeDiagnostic(err)
		return result
	}
	if ready.Service != daemonmeta.ServiceName || ready.PID != info.PID {
		result.Evidence = "readiness response did not match discovered AO daemon"
		return result
	}
	result.Ready = ready.Status
	if ready.Status == "ready" {
		result.State = "healthy"
	} else {
		result.State = "unhealthy"
		result.Evidence = "daemon is not ready"
	}
	var report aoDoctorReport
	if err := read("/api/v1/doctor", &report); err == nil {
		result.Doctor = &report
	}
	return result
}

func configString(object map[string]any, keys ...string) string {
	var current any = object
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = m[key]
	}
	value, _ := current.(string)
	return value
}

func configBool(object map[string]any, keys ...string) bool {
	var current any = object
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current = m[key]
	}
	value, _ := current.(bool)
	return value
}

func configInt(object map[string]any, keys ...string) int {
	var current any = object
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return 0
		}
		current = m[key]
	}
	switch v := current.(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}
