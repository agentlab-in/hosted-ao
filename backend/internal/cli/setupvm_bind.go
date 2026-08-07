package cli

// setupvm_bind.go is the thin half of the binding step: the HTTP calls of the
// RFC 8628 device flow, the atomic write of machine.json, and the gateway
// restart that makes a fresh binding take effect. Every decision it makes lives
// in setupvm_bind_plan.go, which is pure.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

const (
	// devicePathCode and devicePathToken are the control plane's RFC 8628
	// endpoints (controlplane/internal/device/device.go).
	devicePathCode  = "/device/code"
	devicePathToken = "/device/token"
	// deviceCodeGrantType is the grant type RFC 8628 assigns to the device
	// access token request.
	deviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// deviceMaxTransportFailures bounds consecutive network failures while
	// polling. One dropped request must not throw away a code a human is in
	// the middle of approving, but an endpoint that is simply gone must not be
	// hammered until the code expires either.
	deviceMaxTransportFailures = 5
	// deviceMaxFlowRestarts bounds how many expired codes may be replaced in
	// one run, so an unattended terminal eventually stops asking.
	deviceMaxFlowRestarts = 2
	// deviceResponseLimit caps how much of an answer is read. Both bodies are
	// a few hundred bytes.
	deviceResponseLimit = 1 << 16
)

// deviceAuthorization is the RFC 8628 section 3.2 device authorization
// response.
//
// DeviceCode is a bearer secret. It is used to poll with and for nothing else:
// it is never printed, never put in a URL this command displays, and never
// written to disk.
type deviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// deviceTokenResponse is the approved device access token response, reduced to
// the machine triple machine.json needs. The access token in the same body is
// deliberately not decoded: nothing in setup-vm uses it, and a field that is
// never read cannot be logged or persisted by accident.
type deviceTokenResponse struct {
	MachineID string `json:"machine_id"`
	AccountID string `json:"account_id"`
	PublicURL string `json:"public_url"`
}

// deviceHTTPError is a non-200 answer from either device endpoint, carrying the
// OAuth error code the flow's state machine reads.
type deviceHTTPError struct {
	Status      int
	Code        string
	Description string
}

func (e deviceHTTPError) Error() string {
	switch {
	case e.Code != "" && e.Description != "":
		return e.Code + ": " + e.Description
	case e.Code != "":
		return e.Code
	default:
		return fmt.Sprintf("HTTP %d", e.Status)
	}
}

// bindSetupVM binds this machine to an AO account and writes machine.json, then
// restarts the gateway. `ao vm serve` reads that file once at startup, so a
// fresh binding changes nothing at all until the unit restarts, which is why
// the restart is done here rather than left in the closing summary as advice.
//
// An already-bound machine is re-bound rather than refused, after printing the
// binding that is about to be replaced.
func (c *commandContext) bindSetupVM(ctx context.Context, out io.Writer, plan setupPlan, controlPlaneURL string) error {
	if plan.Bound {
		// A machine file too broken to read is not worth reporting in detail
		// here: it is about to be replaced by a good one.
		if previous, err := vmgateway.ReadMachineFile(plan.MachineFile); err == nil && previous != nil {
			if _, err := io.WriteString(out, renderPreviousBinding(plan.MachineFile, *previous)); err != nil {
				return err
			}
		}
	}

	publicURL := "https://" + plan.Domain
	binding, err := c.runDeviceFlow(ctx, out, controlPlaneURL, setupMachineName(plan.Domain), publicURL)
	if err != nil {
		return err
	}

	content, err := renderMachineFile(binding, c.deps.Now())
	if err != nil {
		return err
	}
	owner, err := setupTargetUser()
	if err != nil {
		return err
	}
	if err := writeMachineFile(plan.MachineFile, content, owner); err != nil {
		return err
	}
	// Read the file back through the gateway's own reader rather than trusting
	// what was just marshalled: this is the one moment where a machine.json the
	// gateway cannot parse is still cheap to notice.
	written, err := vmgateway.ReadMachineFile(plan.MachineFile)
	if err != nil || written == nil {
		return fmt.Errorf("wrote %s but could not read it back: %w", plan.MachineFile, err)
	}
	if _, err := io.WriteString(out, renderBindingWritten(plan.MachineFile, *written)); err != nil {
		return err
	}

	if err := c.runSetupPrivileged(ctx, "systemctl", "restart", setupVMGatewayUnit); err != nil {
		return fmt.Errorf("restart %s so it reads the new binding: %w", setupVMGatewayUnit, err)
	}
	// Whether it came back up is checked by the caller, which owns the summary:
	// the unit is Type=simple, so this restart returning 0 says only that a
	// process was forked.
	_, err = fmt.Fprintf(out, "==> %s restarted so it reads the new binding\n", setupVMGatewayUnit)
	return err
}

// runDeviceFlow requests a device code, prints what the operator has to do, and
// polls until the code is approved. An expired code is the one failure it can
// recover from, by offering a fresh one.
func (c *commandContext) runDeviceFlow(ctx context.Context, out io.Writer, base, machineName, publicURL string) (vmgateway.MachineFile, error) {
	for restart := 0; ; restart++ {
		auth, err := c.requestDeviceCode(ctx, base, machineName, publicURL)
		if err != nil {
			return vmgateway.MachineFile{}, err
		}
		expiry := deviceFlowExpiry(auth.ExpiresIn)
		if _, err := io.WriteString(out, renderDeviceInstructions(auth, machineName, publicURL, expiry)); err != nil {
			return vmgateway.MachineFile{}, err
		}

		binding, err := c.pollForBinding(ctx, base, auth, expiry)
		if err == nil {
			return binding, nil
		}
		if !errors.Is(err, errDeviceCodeExpired) || restart >= deviceMaxFlowRestarts {
			return vmgateway.MachineFile{}, err
		}
		if !c.confirmDeviceFlowRestart(out, expiry) {
			return vmgateway.MachineFile{}, err
		}
	}
}

// pollForBinding polls the token endpoint at the server-supplied interval until
// the code is approved, denied, or expires.
func (c *commandContext) pollForBinding(ctx context.Context, base string, auth deviceAuthorization, expiry time.Duration) (vmgateway.MachineFile, error) {
	interval := devicePollStartInterval(auth.Interval)
	deadline := c.deps.Now().Add(expiry)
	transportFailures := 0

	for {
		// Waiting first is deliberate: a code cannot have been approved in the
		// instant it was issued, and RFC 8628 section 3.4 has the client wait
		// the interval before every request, including the first.
		if err := c.sleepUntilCancelled(ctx, interval); err != nil {
			return vmgateway.MachineFile{}, err
		}
		if c.deps.Now().After(deadline) {
			return vmgateway.MachineFile{}, errDeviceCodeExpired
		}

		binding, err := c.requestDeviceToken(ctx, base, auth.DeviceCode)
		if err == nil {
			if invalid := validateMachineFile(binding); invalid != nil {
				return vmgateway.MachineFile{}, fmt.Errorf("the control plane approved this machine but returned an unusable binding: %w", invalid)
			}
			return binding, nil
		}

		var httpErr deviceHTTPError
		if !errors.As(err, &httpErr) {
			transportFailures++
			if transportFailures >= deviceMaxTransportFailures {
				return vmgateway.MachineFile{}, fmt.Errorf("lost contact with the AO control plane while waiting for approval: %w", err)
			}
			continue
		}
		transportFailures = 0

		switch outcome := classifyDevicePollError(httpErr.Code); outcome {
		case devicePollWait:
		case devicePollSlowDown:
			interval = nextDevicePollInterval(interval, outcome)
		case devicePollExpired:
			return vmgateway.MachineFile{}, errDeviceCodeExpired
		case devicePollDenied:
			return vmgateway.MachineFile{}, errDeviceAccessDenied
		default:
			return vmgateway.MachineFile{}, fmt.Errorf("the AO control plane refused the device code: %w", err)
		}
	}
}

// sleepUntilCancelled waits d, or returns as soon as ctx is cancelled. The poll
// interval is server-supplied and reaches 60 seconds after slow_down backoff, so
// waiting it out before noticing a Ctrl-C is a minute of a terminal that looks
// hung. deps.Sleep is injected (the tests drive a fake clock through it) so it is
// raced rather than replaced by a timer; the goroutine ends when the sleep does.
func (c *commandContext) sleepUntilCancelled(ctx context.Context, d time.Duration) error {
	done := make(chan struct{})
	go func() {
		c.deps.Sleep(d)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return ctx.Err()
	}
}

// confirmDeviceFlowRestart asks whether to request a fresh code. A closed or
// exhausted stdin answers no: with nobody at the terminal there is nobody to
// approve a new code either.
func (c *commandContext) confirmDeviceFlowRestart(out io.Writer, expiry time.Duration) bool {
	if _, err := io.WriteString(out, renderDeviceRestartPrompt(expiry)); err != nil {
		return false
	}
	line, err := bufio.NewReader(c.deps.In).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if err != nil && answer == "" {
		_, _ = io.WriteString(out, "no\n")
		return false
	}
	return answer == "" || answer == "y" || answer == "yes"
}

// requestDeviceCode is the device authorization request. It sends this
// machine's name and public URL because the VM is the only party that knows
// them, and the approval page has to show the operator which box they are about
// to bind.
func (c *commandContext) requestDeviceCode(ctx context.Context, base, machineName, publicURL string) (deviceAuthorization, error) {
	form := url.Values{}
	form.Set("machine_name", machineName)
	form.Set("public_url", publicURL)

	var auth deviceAuthorization
	if err := c.postDeviceForm(ctx, base+devicePathCode, form, &auth); err != nil {
		return deviceAuthorization{}, fmt.Errorf("ask %s for a device code: %w", base, err)
	}
	if strings.TrimSpace(auth.DeviceCode) == "" || strings.TrimSpace(auth.UserCode) == "" {
		return deviceAuthorization{}, fmt.Errorf("%s returned a device authorization with no code", base)
	}
	if strings.TrimSpace(auth.VerificationURI) == "" {
		return deviceAuthorization{}, fmt.Errorf("%s returned a device authorization with no verification URL", base)
	}
	return auth, nil
}

// requestDeviceToken is one device access token request. It returns the machine
// triple on approval, or a deviceHTTPError carrying the OAuth code that says
// what the flow should do next.
func (c *commandContext) requestDeviceToken(ctx context.Context, base, deviceCode string) (vmgateway.MachineFile, error) {
	form := url.Values{}
	form.Set("grant_type", deviceCodeGrantType)
	form.Set("device_code", deviceCode)

	var token deviceTokenResponse
	if err := c.postDeviceForm(ctx, base+devicePathToken, form, &token); err != nil {
		return vmgateway.MachineFile{}, err
	}
	// machineId is machines.id, which is also the access token's audience. It
	// is never the hostname and never the public URL: substituting either makes
	// the gateway 401 every request with nothing on either side to explain why.
	return vmgateway.MachineFile{
		MachineID: token.MachineID,
		AccountID: token.AccountID,
		PublicURL: token.PublicURL,
	}, nil
}

// postDeviceForm posts a form-encoded body, which is what the OAuth specs use,
// and decodes either the success body or the OAuth error body. The device code
// travels in the body rather than the query string so it never reaches an
// access log or a shell history.
func (c *commandContext) postDeviceForm(ctx context.Context, endpoint string, form url.Values, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", setupVMUserAgent)

	resp, err := c.setupHTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, deviceResponseLimit))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return decodeDeviceError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("parse the answer from %s: %w", endpoint, err)
	}
	return nil
}

// decodeDeviceError reads the OAuth error envelope both endpoints return. A
// body that is not that envelope leaves the code empty, which the state machine
// classifies as fatal rather than as a state of the flow.
func decodeDeviceError(status int, body []byte) error {
	var payload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &payload)
	return deviceHTTPError{Status: status, Code: payload.Error, Description: payload.ErrorDescription}
}

// setupMachineName is the name the approval page shows. The hostname is what
// the operator will recognise; the domain is the fallback for a box whose
// hostname says nothing.
func setupMachineName(domain string) string {
	host, err := os.Hostname()
	host = strings.TrimSpace(host)
	if err != nil || host == "" || host == "localhost" {
		return domain
	}
	return host
}

// writeMachineFile writes machine.json mode 0600, atomically.
//
// A partial machine.json is worse than none: the gateway treats a missing file
// as "not bound yet" and starts, but a malformed one fails the unmarshal and
// stops it dead. So the bytes go to a temp file in the same directory and are
// renamed into place, which is atomic within a directory; a temp file in /tmp
// would not be, because rename cannot cross filesystems.
func writeMachineFile(path string, content []byte, owner *user.User) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".machine.json.*")
	if err != nil {
		return fmt.Errorf("create a temporary file next to %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	// Removing an already-renamed file fails harmlessly; an abandoned one is
	// what this cleans up, so no half-written file is ever left behind.
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// os.CreateTemp already creates 0600, but the mode is part of this file's
	// contract rather than an implementation detail of the standard library:
	// machine.json names the only account allowed to reach this box.
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("set the mode of %s: %w", path, err)
	}
	if err := chownMachineFile(tmpPath, owner); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("move the new machine file into place at %s: %w", path, err)
	}
	return nil
}

// chownMachineFile hands the file to the user the gateway unit runs as. Under
// sudo this process is root while ao-gateway.service is not, and a 0600 file
// owned by root is one the gateway cannot read, so it would refuse to start on
// a machine that had just been bound successfully.
func chownMachineFile(path string, owner *user.User) error {
	if owner == nil || os.Getuid() != 0 {
		return nil
	}
	uid, gid := -1, -1
	if parsed, err := strconv.Atoi(owner.Uid); err == nil {
		uid = parsed
	}
	if parsed, err := strconv.Atoi(owner.Gid); err == nil {
		gid = parsed
	}
	if uid < 0 || gid < 0 {
		// Non-numeric ids are a Windows user, and this path only ever runs on
		// the Ubuntu box the platform gate already checked for.
		return nil
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("give %s to %s: %w", path, owner.Username, err)
	}
	return nil
}
