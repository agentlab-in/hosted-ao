package cli

// Tests for the binding half of `ao setup-vm`. The highest-value one here is
// TestMachineFileRoundTripsThroughTheGatewayReader: it writes the file with the
// real writer and reads it back with vmgateway's real reader, which is what
// stops the two from ever disagreeing about machine.json.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

// testMachineID and testAccountID are lowercase hyphenated UUIDs, the shape
// machines.id and accounts.id actually have. machineId in particular becomes
// the token audience, so a test that used a hostname here would pass while the
// real thing 401s.
const (
	testMachineID = "6f1b6b0a-6a7f-4f2a-9a2f-1f3d2c4b5a60"
	testAccountID = "b2c3d4e5-1111-4222-8333-444455556666"
	testPublicURL = "https://vm.example.com"
)

// fakeDeviceClock makes the polling loop deterministic: sleeping advances the
// clock, so a code's expiry is reached by polling rather than by waiting.
type fakeDeviceClock struct {
	mu    sync.Mutex
	now   time.Time
	slept []time.Duration
}

func newFakeDeviceClock() *fakeDeviceClock {
	return &fakeDeviceClock{now: time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeDeviceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeDeviceClock) Sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
}

func (c *fakeDeviceClock) sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.slept...)
}

// deviceFlowServer is a stand-in control plane. tokenAnswers is walked one
// entry per poll; the last entry repeats once the list runs out.
type deviceFlowServer struct {
	mu           sync.Mutex
	deviceCode   string
	codeRequests int
	tokenPolls   int
	machineName  string
	publicURL    string
	grantType    string
	presented    []string
	tokenAnswers []func(w http.ResponseWriter)
	expiresIn    int
	interval     int
}

func (s *deviceFlowServer) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /device/code", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("device code request body: %v", err)
		}
		s.mu.Lock()
		s.codeRequests++
		s.machineName = r.PostForm.Get("machine_name")
		s.publicURL = r.PostForm.Get("public_url")
		code := s.deviceCode
		s.mu.Unlock()

		writeTestJSON(w, http.StatusOK, map[string]any{
			"device_code":               code,
			"user_code":                 "BCDF-GHJK",
			"verification_uri":          "https://ao.example.com/device",
			"verification_uri_complete": "https://ao.example.com/device?user_code=BCDF-GHJK",
			"expires_in":                s.expiresIn,
			"interval":                  s.interval,
		})
	})
	mux.HandleFunc("POST /device/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("device token request body: %v", err)
		}
		s.mu.Lock()
		s.grantType = r.PostForm.Get("grant_type")
		s.presented = append(s.presented, r.PostForm.Get("device_code"))
		answer := s.tokenAnswers[min(s.tokenPolls, len(s.tokenAnswers)-1)]
		s.tokenPolls++
		s.mu.Unlock()
		answer(w)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

func writeTestJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func oauthError(status int, code string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		writeTestJSON(w, status, map[string]string{"error": code, "error_description": "test"})
	}
}

func approved(w http.ResponseWriter) {
	writeTestJSON(w, http.StatusOK, map[string]any{
		"access_token": "an-access-token-the-cli-must-ignore",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"machine_id":   testMachineID,
		"account_id":   testAccountID,
		"public_url":   testPublicURL,
	})
}

// ---------------------------------------------------------------------------
// machine.json: the writer against the gateway's real reader
// ---------------------------------------------------------------------------

// TestMachineFileRoundTripsThroughTheGatewayReader is the test this whole task
// hangs on. `ao vm serve` refuses to start on a machine.json it cannot parse,
// and that only ever shows up on a real VM, so the writer is pinned to the
// reader here instead.
func TestMachineFileRoundTripsThroughTheGatewayReader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".ao", "machine.json")
	issuedAt := time.Date(2026, 7, 30, 9, 15, 30, 500_000_000, time.UTC)

	content, err := renderMachineFile(vmgateway.MachineFile{
		MachineID: testMachineID,
		AccountID: testAccountID,
		PublicURL: testPublicURL,
	}, issuedAt)
	if err != nil {
		t.Fatalf("renderMachineFile: %v", err)
	}
	if err := writeMachineFile(path, content, nil); err != nil {
		t.Fatalf("writeMachineFile: %v", err)
	}

	got, err := vmgateway.ReadMachineFile(path)
	if err != nil {
		t.Fatalf("the gateway's own reader rejected the file this command wrote: %v\n%s", err, content)
	}
	if got == nil {
		t.Fatal("ReadMachineFile returned no file for one that was just written")
	}
	if got.MachineID != testMachineID {
		t.Errorf("machineId = %q, want %q: it is the token audience, never the hostname", got.MachineID, testMachineID)
	}
	if got.AccountID != testAccountID {
		t.Errorf("accountId = %q, want %q", got.AccountID, testAccountID)
	}
	if got.PublicURL != testPublicURL {
		t.Errorf("publicUrl = %q, want the full origin %q", got.PublicURL, testPublicURL)
	}
	if !got.IssuedAt.Equal(issuedAt.Truncate(time.Second)) {
		t.Errorf("issuedAt = %s, want %s", got.IssuedAt, issuedAt.Truncate(time.Second))
	}

	// issuedAt is only ever read by encoding/json, so assert the wire form
	// directly: a value that is not RFC 3339 fails the whole unmarshal and
	// stops the gateway over a field nobody consumes.
	var raw struct {
		IssuedAt string `json:"issuedAt"`
	}
	if err := json.Unmarshal(content, &raw); err != nil {
		t.Fatalf("unmarshal the rendered file: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, raw.IssuedAt); err != nil {
		t.Errorf("issuedAt %q is not RFC 3339: %v", raw.IssuedAt, err)
	}

	// The whole point of the file: the gateway resolves its configuration from
	// it and gets a bare hostname for the certificate whitelist.
	for _, key := range []string{"AO_VM_DOMAIN", "AO_VM_MACHINE_ID", "AO_VM_ACCOUNT_ID", "AO_MACHINE_FILE"} {
		t.Setenv(key, "")
	}
	cfg, err := vmgateway.Resolve(vmgateway.Options{MachineFile: path}, t.TempDir())
	if err != nil {
		t.Fatalf("the gateway could not resolve its config from the written file: %v", err)
	}
	if cfg.Domain != "vm.example.com" {
		t.Errorf("resolved domain = %q, want the bare hostname vm.example.com", cfg.Domain)
	}
	if cfg.MachineID != testMachineID {
		t.Errorf("resolved machine id = %q, want %q", cfg.MachineID, testMachineID)
	}
}

func TestWriteMachineFileIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "machine.json")

	first, err := renderMachineFile(vmgateway.MachineFile{
		MachineID: testMachineID, AccountID: testAccountID, PublicURL: testPublicURL,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMachineFile(path, first, nil); err != nil {
		t.Fatal(err)
	}

	// Re-binding replaces the file rather than appending to it or leaving the
	// old one in place.
	replacement := "11111111-2222-4333-8444-555566667777"
	second, err := renderMachineFile(vmgateway.MachineFile{
		MachineID: replacement, AccountID: testAccountID, PublicURL: testPublicURL,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMachineFile(path, second, nil); err != nil {
		t.Fatal(err)
	}
	got, err := vmgateway.ReadMachineFile(path)
	if err != nil || got == nil {
		t.Fatalf("read back after re-bind: %v", err)
	}
	if got.MachineID != replacement {
		t.Errorf("machineId = %q after re-bind, want %q", got.MachineID, replacement)
	}

	// No temp file may survive either write: the reader globs nothing, but a
	// stray half-written sibling is exactly what the rename dance exists to
	// avoid leaving around.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "machine.json" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("directory holds %v, want only machine.json", names)
	}

	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("machine.json mode = %04o, want 0600", perm)
	}
}

func TestRenderMachineFileRefusesAnUnusableBinding(t *testing.T) {
	for name, mf := range map[string]vmgateway.MachineFile{
		"no machine id": {AccountID: testAccountID, PublicURL: testPublicURL},
		"no account id": {MachineID: testMachineID, PublicURL: testPublicURL},
		"no public url": {MachineID: testMachineID, AccountID: testAccountID},
		// A bare hostname parses as a path, so the gateway's certificate
		// whitelist ends up empty and every handshake fails.
		"bare hostname public url": {MachineID: testMachineID, AccountID: testAccountID, PublicURL: "vm.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := renderMachineFile(mf, time.Now()); err == nil {
				t.Fatalf("expected %s to be refused before anything is written", name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The device flow state machine
// ---------------------------------------------------------------------------

func TestClassifyDevicePollError(t *testing.T) {
	for code, want := range map[string]devicePollOutcome{
		"authorization_pending": devicePollWait,
		"slow_down":             devicePollSlowDown,
		"expired_token":         devicePollExpired,
		"invalid_grant":         devicePollExpired,
		"access_denied":         devicePollDenied,
		"invalid_request":       devicePollFatal,
		"server_error":          devicePollFatal,
		"":                      devicePollFatal,
	} {
		if got := classifyDevicePollError(code); got != want {
			t.Errorf("classifyDevicePollError(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestNextDevicePollIntervalBacksOffOnlyOnSlowDown(t *testing.T) {
	if got := nextDevicePollInterval(5*time.Second, devicePollWait); got != 5*time.Second {
		t.Errorf("authorization_pending changed the interval to %s, want it unchanged", got)
	}
	if got := nextDevicePollInterval(5*time.Second, devicePollSlowDown); got != 10*time.Second {
		t.Errorf("slow_down interval = %s, want 10s (RFC 8628 adds 5 seconds)", got)
	}
	if got := nextDevicePollInterval(devicePollMaxInterval, devicePollSlowDown); got != devicePollMaxInterval {
		t.Errorf("slow_down past the cap = %s, want it capped at %s", got, devicePollMaxInterval)
	}
}

func TestDevicePollStartIntervalPrefersTheServerValue(t *testing.T) {
	if got := devicePollStartInterval(7); got != 7*time.Second {
		t.Errorf("interval = %s, want the server's 7s", got)
	}
	if got := devicePollStartInterval(0); got != devicePollDefaultInterval {
		t.Errorf("interval with no server value = %s, want %s", got, devicePollDefaultInterval)
	}
}

// ---------------------------------------------------------------------------
// The device flow against a stand-in control plane
// ---------------------------------------------------------------------------

func deviceFlowContext(t *testing.T, clock *fakeDeviceClock, stdin string) (*commandContext, *strings.Builder) {
	t.Helper()
	out := &strings.Builder{}
	deps := Deps{
		In:    strings.NewReader(stdin),
		Now:   clock.Now,
		Sleep: clock.Sleep,
	}
	return &commandContext{deps: deps.withDefaults()}, out
}

func TestDeviceFlowPollsAtTheServerIntervalAndBacksOff(t *testing.T) {
	server := &deviceFlowServer{
		deviceCode: "device-code-that-must-never-be-printed",
		expiresIn:  900,
		interval:   7,
		tokenAnswers: []func(http.ResponseWriter){
			oauthError(http.StatusBadRequest, "authorization_pending"),
			oauthError(http.StatusBadRequest, "authorization_pending"),
			oauthError(http.StatusBadRequest, "slow_down"),
			approved,
		},
	}
	base := server.start(t)
	clock := newFakeDeviceClock()
	ctx, out := deviceFlowContext(t, clock, "")

	binding, err := ctx.runDeviceFlow(context.Background(), out, base, "vm-01", testPublicURL)
	if err != nil {
		t.Fatalf("runDeviceFlow: %v", err)
	}
	if binding.MachineID != testMachineID || binding.AccountID != testAccountID || binding.PublicURL != testPublicURL {
		t.Fatalf("binding = %+v, want the machine triple the token endpoint returned", binding)
	}

	// Sleep before every poll, at the server's interval, plus 5 seconds after
	// slow_down. Ignoring slow_down is how a client gets rate limited off the
	// endpoint entirely, so it has to show up here.
	want := []time.Duration{7 * time.Second, 7 * time.Second, 7 * time.Second, 12 * time.Second}
	got := clock.sleeps()
	if len(got) != len(want) {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sleep %d = %s, want %s (full sequence %v)", i, got[i], want[i], got)
		}
	}

	if server.grantType != deviceCodeGrantType {
		t.Errorf("grant_type = %q, want %q", server.grantType, deviceCodeGrantType)
	}
	if server.machineName != "vm-01" || server.publicURL != testPublicURL {
		t.Errorf("device authorization sent name %q and public url %q, want vm-01 and %q",
			server.machineName, server.publicURL, testPublicURL)
	}
	for i, presented := range server.presented {
		if presented != server.deviceCode {
			t.Errorf("poll %d presented %q, want the device code from the authorization response", i, presented)
		}
	}
}

// TestDeviceFlowNeverPrintsTheDeviceCode pins the one security property of the
// printed output: the user code is meant to be shown, the device code is a
// bearer secret and must not reach the terminal, a URL, or a log.
func TestDeviceFlowNeverPrintsTheDeviceCode(t *testing.T) {
	secret := "sup3rsecret-device-code-value"
	server := &deviceFlowServer{
		deviceCode:   secret,
		expiresIn:    900,
		interval:     1,
		tokenAnswers: []func(http.ResponseWriter){approved},
	}
	base := server.start(t)
	clock := newFakeDeviceClock()
	ctx, out := deviceFlowContext(t, clock, "")

	if _, err := ctx.runDeviceFlow(context.Background(), out, base, "vm-01", testPublicURL); err != nil {
		t.Fatalf("runDeviceFlow: %v", err)
	}
	printed := out.String()
	if strings.Contains(printed, secret) {
		t.Fatalf("the device code was printed:\n%s", printed)
	}
	for _, want := range []string{"BCDF-GHJK", "https://ao.example.com/device", "vm-01", testPublicURL} {
		if !strings.Contains(printed, want) {
			t.Errorf("output is missing %q:\n%s", want, printed)
		}
	}
	assertNoDashes(t, printed)
}

func TestDeviceFlowOffersToRestartAnExpiredCode(t *testing.T) {
	newServer := func() *deviceFlowServer {
		return &deviceFlowServer{
			deviceCode:   "device-code",
			expiresIn:    900,
			interval:     1,
			tokenAnswers: []func(http.ResponseWriter){oauthError(http.StatusBadRequest, "expired_token")},
		}
	}

	t.Run("accepted", func(t *testing.T) {
		server := newServer()
		base := server.start(t)
		ctx, out := deviceFlowContext(t, newFakeDeviceClock(), "y\n")
		_, err := ctx.runDeviceFlow(context.Background(), out, base, "vm-01", testPublicURL)
		if !errors.Is(err, errDeviceCodeExpired) {
			t.Fatalf("err = %v, want errDeviceCodeExpired", err)
		}
		if server.codeRequests < 2 {
			t.Errorf("device code requests = %d, want a fresh code after the operator said yes", server.codeRequests)
		}
		if !strings.Contains(out.String(), "Request a new code?") {
			t.Errorf("an expired code must offer to restart the flow:\n%s", out.String())
		}
	})

	t.Run("declined", func(t *testing.T) {
		server := newServer()
		base := server.start(t)
		ctx, out := deviceFlowContext(t, newFakeDeviceClock(), "n\n")
		_, err := ctx.runDeviceFlow(context.Background(), out, base, "vm-01", testPublicURL)
		if !errors.Is(err, errDeviceCodeExpired) {
			t.Fatalf("err = %v, want errDeviceCodeExpired", err)
		}
		if server.codeRequests != 1 {
			t.Errorf("device code requests = %d, want 1: a declined restart asks for nothing more", server.codeRequests)
		}
	})
}

func TestDeviceFlowStopsWhenTheApprovalIsDenied(t *testing.T) {
	server := &deviceFlowServer{
		deviceCode:   "device-code",
		expiresIn:    900,
		interval:     1,
		tokenAnswers: []func(http.ResponseWriter){oauthError(http.StatusForbidden, "access_denied")},
	}
	base := server.start(t)
	ctx, out := deviceFlowContext(t, newFakeDeviceClock(), "y\n")

	_, err := ctx.runDeviceFlow(context.Background(), out, base, "vm-01", testPublicURL)
	if !errors.Is(err, errDeviceAccessDenied) {
		t.Fatalf("err = %v, want errDeviceAccessDenied", err)
	}
	if server.codeRequests != 1 {
		t.Errorf("device code requests = %d, want 1: a refusal is a decision, not something to retry", server.codeRequests)
	}
}

// TestDeviceFlowGivesUpWhenTheCodeExpiresUnapproved covers the deadline rather
// than the server's expired_token: a control plane that simply stops answering
// must not leave setup-vm polling forever.
func TestDeviceFlowGivesUpWhenTheCodeExpiresUnapproved(t *testing.T) {
	server := &deviceFlowServer{
		deviceCode:   "device-code",
		expiresIn:    30,
		interval:     10,
		tokenAnswers: []func(http.ResponseWriter){oauthError(http.StatusBadRequest, "authorization_pending")},
	}
	base := server.start(t)
	ctx, out := deviceFlowContext(t, newFakeDeviceClock(), "")

	_, err := ctx.runDeviceFlow(context.Background(), out, base, "vm-01", testPublicURL)
	if !errors.Is(err, errDeviceCodeExpired) {
		t.Fatalf("err = %v, want errDeviceCodeExpired", err)
	}
}

// TestDeviceFlowRejectsABindingItCannotWrite guards the confusion PR #18 was
// opened for: a control plane that answered with the hostname where machines.id
// belongs must fail loudly here, not silently produce a machine that 401s
// every request.
func TestDeviceFlowRejectsABindingItCannotWrite(t *testing.T) {
	server := &deviceFlowServer{
		deviceCode: "device-code",
		expiresIn:  900,
		interval:   1,
		tokenAnswers: []func(http.ResponseWriter){func(w http.ResponseWriter) {
			writeTestJSON(w, http.StatusOK, map[string]any{
				"machine_id": "",
				"account_id": testAccountID,
				"public_url": testPublicURL,
			})
		}},
	}
	base := server.start(t)
	ctx, out := deviceFlowContext(t, newFakeDeviceClock(), "")

	if _, err := ctx.runDeviceFlow(context.Background(), out, base, "vm-01", testPublicURL); err == nil {
		t.Fatal("expected an approval with no machine id to be refused")
	}
}

// ---------------------------------------------------------------------------
// bindSetupVM end to end
// ---------------------------------------------------------------------------

func TestBindSetupVMWritesTheFileAndRestartsTheGateway(t *testing.T) {
	server := &deviceFlowServer{
		deviceCode:   "device-code",
		expiresIn:    900,
		interval:     1,
		tokenAnswers: []func(http.ResponseWriter){approved},
	}
	base := server.start(t)

	dir := t.TempDir()
	plan := setupPlan{
		Domain:      "vm.example.com",
		MachineFile: filepath.Join(dir, "machine.json"),
	}

	var mu sync.Mutex
	var calls []string
	clock := newFakeDeviceClock()
	deps := Deps{
		Now:   clock.Now,
		Sleep: clock.Sleep,
		CommandOutput: func(_ context.Context, name string, args ...string) ([]byte, error) {
			mu.Lock()
			calls = append(calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
			mu.Unlock()
			return nil, nil
		},
	}
	ctx := &commandContext{deps: deps.withDefaults()}
	out := &strings.Builder{}

	if err := ctx.bindSetupVM(context.Background(), out, plan, base); err != nil {
		t.Fatalf("bindSetupVM: %v", err)
	}

	mf, err := vmgateway.ReadMachineFile(plan.MachineFile)
	if err != nil || mf == nil {
		t.Fatalf("read the file bindSetupVM wrote: %v", err)
	}
	if mf.MachineID != testMachineID {
		t.Errorf("machineId = %q, want %q", mf.MachineID, testMachineID)
	}
	if !mf.IssuedAt.Equal(clock.Now().Truncate(time.Second)) {
		t.Errorf("issuedAt = %s, want the time of the binding", mf.IssuedAt)
	}

	// `ao vm serve` reads machine.json once at startup, so a binding that does
	// not restart the unit is a binding the gateway never notices.
	restarted := false
	mu.Lock()
	for _, call := range calls {
		if strings.Contains(call, "systemctl restart "+setupVMGatewayUnit) {
			restarted = true
		}
	}
	mu.Unlock()
	if !restarted {
		t.Errorf("%s was never restarted after binding, so the gateway is still unbound. Calls: %v",
			setupVMGatewayUnit, calls)
	}
	assertNoDashes(t, out.String())
}

// TestBindSetupVMPrintsThePreviousBindingBeforeReplacingIt is the re-bind
// contract: an already-bound machine is never refused, and the binding about to
// be replaced is shown while there is still time to stop.
func TestBindSetupVMPrintsThePreviousBindingBeforeReplacingIt(t *testing.T) {
	server := &deviceFlowServer{
		deviceCode:   "device-code",
		expiresIn:    900,
		interval:     1,
		tokenAnswers: []func(http.ResponseWriter){approved},
	}
	base := server.start(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "machine.json")
	previousID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	previous, err := renderMachineFile(vmgateway.MachineFile{
		MachineID: previousID, AccountID: testAccountID, PublicURL: "https://old.example.com",
	}, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMachineFile(path, previous, nil); err != nil {
		t.Fatal(err)
	}

	clock := newFakeDeviceClock()
	deps := Deps{
		Now:           clock.Now,
		Sleep:         clock.Sleep,
		CommandOutput: func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	}
	ctx := &commandContext{deps: deps.withDefaults()}
	out := &strings.Builder{}

	plan := setupPlan{Domain: "vm.example.com", MachineFile: path, Bound: true}
	if err := ctx.bindSetupVM(context.Background(), out, plan, base); err != nil {
		t.Fatalf("bindSetupVM on an already-bound machine: %v", err)
	}

	printed := out.String()
	if !strings.Contains(printed, "already bound") || !strings.Contains(printed, previousID) {
		t.Errorf("a re-bind must print the binding it replaces:\n%s", printed)
	}
	mf, err := vmgateway.ReadMachineFile(path)
	if err != nil || mf == nil {
		t.Fatalf("read back after re-bind: %v", err)
	}
	if mf.MachineID != testMachineID {
		t.Errorf("machineId = %q after re-bind, want the new %q: the old file must not survive", mf.MachineID, testMachineID)
	}
	assertNoDashes(t, printed)
}
