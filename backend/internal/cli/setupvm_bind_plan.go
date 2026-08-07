package cli

// setupvm_bind_plan.go holds every decision the binding half of `ao setup-vm`
// makes: what one device-flow poll answer means, how far to back off, the exact
// bytes of machine.json, and every line the binding prints. Nothing in here
// touches the network or the disk, so all of it is unit-testable on macOS and
// Windows, where CLI E2E also runs. The thin layer that executes these
// decisions lives in setupvm_bind.go, the same split setupvm.go and
// setupvm_plan.go already use.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

const (
	// devicePollDefaultInterval is used only when the device authorization
	// response carries no interval. The server's own value wins whenever there
	// is one: it is the authority on how fast it is willing to be polled.
	devicePollDefaultInterval = 5 * time.Second
	// deviceSlowDownIncrement is the back-off RFC 8628 section 3.5 prescribes:
	// on slow_down the client raises its polling interval by 5 seconds. It is
	// an instruction, not a warning, so it is applied rather than logged.
	deviceSlowDownIncrement = 5 * time.Second
	// devicePollMaxInterval caps the back-off, so a server answering slow_down
	// forever cannot stretch one wait past the device code's own lifetime.
	devicePollMaxInterval = 60 * time.Second
	// deviceFlowFallbackExpiry bounds the wait when the device authorization
	// response omits expires_in, so an unapproved flow always ends.
	deviceFlowFallbackExpiry = 15 * time.Minute
)

// The RFC 8628 section 3.5 error codes this client understands, plus the one
// OAuth code the control plane returns for a device code it has never seen.
const (
	deviceErrAuthorizationPending = "authorization_pending"
	deviceErrSlowDown             = "slow_down"
	deviceErrAccessDenied         = "access_denied"
	deviceErrExpiredToken         = "expired_token"
	deviceErrInvalidGrant         = "invalid_grant"
)

var (
	// errDeviceCodeExpired is the one failure the flow can recover from, by
	// asking for a fresh code.
	errDeviceCodeExpired = errors.New("the device code expired before anyone approved it")
	// errDeviceAccessDenied is a human saying no in the browser, which is a
	// decision rather than a fault, so it is never retried.
	errDeviceAccessDenied = errors.New("the binding was denied in the browser")
)

// devicePollOutcome is what one answer from the token endpoint means for the
// polling loop.
type devicePollOutcome int

const (
	// devicePollWait: nobody has approved yet. Poll again, unchanged.
	devicePollWait devicePollOutcome = iota
	// devicePollSlowDown: polling too fast. Poll again, after backing off.
	devicePollSlowDown
	// devicePollExpired: the code is unusable and only a new one can help.
	devicePollExpired
	// devicePollDenied: a human refused this machine.
	devicePollDenied
	// devicePollFatal: anything else, which is an outage or a bug rather than
	// a state of the flow, and must not be retried until the code expires.
	devicePollFatal
)

// classifyDevicePollError maps an OAuth error code to what the loop does next.
func classifyDevicePollError(code string) devicePollOutcome {
	switch strings.TrimSpace(code) {
	case deviceErrAuthorizationPending:
		return devicePollWait
	case deviceErrSlowDown:
		return devicePollSlowDown
	case deviceErrExpiredToken, deviceErrInvalidGrant:
		// invalid_grant is an unknown device code, which is what an expired one
		// becomes once the control plane sweeps it. Same remedy either way: the
		// code cannot be approved, so offer a new one.
		return devicePollExpired
	case deviceErrAccessDenied:
		return devicePollDenied
	default:
		return devicePollFatal
	}
}

// nextDevicePollInterval applies the back-off. Only slow_down changes the
// interval; every other outcome keeps polling at the rate the server asked for.
func nextDevicePollInterval(current time.Duration, outcome devicePollOutcome) time.Duration {
	if outcome != devicePollSlowDown {
		return current
	}
	if next := current + deviceSlowDownIncrement; next < devicePollMaxInterval {
		return next
	}
	return devicePollMaxInterval
}

// devicePollStartInterval reads the server-supplied interval, falling back only
// when it is absent or nonsensical.
func devicePollStartInterval(seconds int) time.Duration {
	if seconds <= 0 {
		return devicePollDefaultInterval
	}
	return time.Duration(seconds) * time.Second
}

// deviceFlowExpiry reads the server-supplied expires_in, with the same
// fallback discipline.
func deviceFlowExpiry(seconds int) time.Duration {
	if seconds <= 0 {
		return deviceFlowFallbackExpiry
	}
	return time.Duration(seconds) * time.Second
}

// ---------------------------------------------------------------------------
// machine.json
// ---------------------------------------------------------------------------

// validateMachineFile rejects a binding that would produce a machine.json the
// gateway cannot serve from, before anything is written.
//
// publicUrl has to be a full origin because `ao vm serve` reduces it with
// url.Parse and u.Hostname(). A bare hostname parses as a path, leaves the
// certificate whitelist empty, and fails every TLS handshake while the gateway
// logs a clean start, so it is caught here instead.
func validateMachineFile(mf vmgateway.MachineFile) error {
	for _, field := range [][2]string{
		{"machineId", mf.MachineID},
		{"accountId", mf.AccountID},
		{"publicUrl", mf.PublicURL},
	} {
		if strings.TrimSpace(field[1]) == "" {
			return fmt.Errorf("%s is empty", field[0])
		}
	}
	u, err := url.Parse(mf.PublicURL)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return fmt.Errorf("publicUrl %q is not a full origin like https://vm.example.com", mf.PublicURL)
	}
	return nil
}

// renderMachineFile is the exact bytes of ~/.ao/hosted/machine.json.
//
// It marshals vmgateway.MachineFile, the very type `ao vm serve` unmarshals, so
// the writer and the reader cannot drift: renaming a field there stops this
// compiling. machineId is machines.id and becomes the access token's `aud`,
// compared byte for byte, so it is never the hostname and never the public URL.
// issuedAt goes out as RFC 3339 because a value that does not parse fails the
// whole unmarshal and the gateway then refuses to start over a field it never
// reads.
func renderMachineFile(mf vmgateway.MachineFile, issuedAt time.Time) ([]byte, error) {
	mf.IssuedAt = issuedAt.UTC().Truncate(time.Second)
	if err := validateMachineFile(mf); err != nil {
		return nil, fmt.Errorf("refusing to write an unusable machine file: %w", err)
	}
	data, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render the machine file: %w", err)
	}
	return append(data, '\n'), nil
}

// ---------------------------------------------------------------------------
// What the binding prints
// ---------------------------------------------------------------------------

// renderMachineFields is the one layout every binding readout uses, so
// ao setup-vm and ao whoami describe the same file the same way.
func renderMachineFields(indent string, mf vmgateway.MachineFile) string {
	var b strings.Builder
	for _, field := range [][2]string{
		{"machine id", mf.MachineID},
		{"account id", mf.AccountID},
		{"public url", mf.PublicURL},
		{"bound at", mf.IssuedAt.UTC().Format(time.RFC3339)},
	} {
		fmt.Fprintf(&b, "%s%-12s  %s\n", indent, field[0], field[1])
	}
	return b.String()
}

// renderPreviousBinding is printed before a re-bind. An already-bound machine
// is re-bound rather than refused, so the operator has to see what is about to
// be replaced while there is still time to press Ctrl-C.
func renderPreviousBinding(path string, mf vmgateway.MachineFile) string {
	var b strings.Builder
	b.WriteString("\n==> This machine is already bound. Binding again replaces the entry below.\n\n")
	b.WriteString(renderMachineFields("      ", mf))
	fmt.Fprintf(&b, "      %-12s  %s\n", "machine file", path)
	return b.String()
}

// renderDeviceInstructions is what the operator reads while the flow waits.
//
// The user code is the part meant to be displayed. The device code is a bearer
// secret and appears nowhere: not here, not in a printed URL, not in a log
// line, and not on disk. verification_uri_complete carries only the user code,
// which is why it is safe to print.
func renderDeviceInstructions(auth deviceAuthorization, machineName, publicURL string, expiry time.Duration) string {
	var b strings.Builder
	b.WriteString("\n==> Binding this machine to an AO account.\n\n")
	b.WriteString("      On any device with a browser, open:\n")
	fmt.Fprintf(&b, "        %s\n", auth.VerificationURI)
	b.WriteString("      and enter this code:\n")
	fmt.Fprintf(&b, "        %s\n", auth.UserCode)
	if complete := strings.TrimSpace(auth.VerificationURIComplete); complete != "" {
		b.WriteString("\n      Or open this link, which fills the code in for you:\n")
		fmt.Fprintf(&b, "        %s\n", complete)
	}
	b.WriteString("\n      The approval page will show this machine as:\n")
	fmt.Fprintf(&b, "        %s (%s)\n", machineName, publicURL)
	fmt.Fprintf(&b, "\n    Waiting for approval. This code expires in %s. Press Ctrl-C to stop.\n",
		humanDeviceDuration(expiry))
	return b.String()
}

// renderBindingWritten confirms the write, naming the file and its mode because
// both are part of the contract: the gateway reads that path, and nothing else
// on the box may read that file.
func renderBindingWritten(path string, mf vmgateway.MachineFile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n==> Approved. Wrote %s (mode 0600).\n\n", path)
	b.WriteString(renderMachineFields("      ", mf))
	return b.String()
}

// renderDeviceRestartPrompt asks whether to request a fresh code. An expired
// code is the one device-flow failure that is worth retrying in place: the
// operator is usually still sitting at the terminal, and the alternative is
// re-running the whole of setup-vm to get back here.
func renderDeviceRestartPrompt(expiry time.Duration) string {
	return fmt.Sprintf("\n    Nobody approved that code within %s, so it expired.\n"+
		"    Request a new code? [Y/n]: ", humanDeviceDuration(expiry))
}

// humanDeviceDuration renders a code lifetime the way a person would say it.
func humanDeviceDuration(d time.Duration) string {
	switch {
	case d >= time.Minute && d%time.Minute == 0:
		minutes := int(d / time.Minute)
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	default:
		return d.Round(time.Second).String()
	}
}

// renderWhoami is the whole output of `ao whoami`: the binding `ao vm serve`
// is running with, or a plain statement that there is none.
func renderWhoami(mf *vmgateway.MachineFile, path string) string {
	var b strings.Builder
	if mf == nil {
		b.WriteString("This machine is not bound to an AO account.\n")
		fmt.Fprintf(&b, "There is no machine file at %s.\n", path)
		b.WriteString("\nBind it by running setup on the machine itself:\n")
		b.WriteString("  sudo ao setup-vm --domain <the hostname you own>\n")
		return b.String()
	}

	problem := validateMachineFile(*mf)
	if problem != nil {
		b.WriteString("This machine has a machine file, but it is not usable.\n\n")
	} else {
		b.WriteString("This machine is bound to an AO account.\n\n")
	}
	b.WriteString(renderMachineFields("  ", *mf))
	fmt.Fprintf(&b, "  %-12s  %s\n", "machine file", path)

	if problem != nil {
		fmt.Fprintf(&b, "\nProblem: %s\n", problem)
		fmt.Fprintf(&b, "`ao vm serve` will not start until that is fixed. Bind this machine again:\n")
		b.WriteString("  sudo ao setup-vm --domain <the hostname you own>\n")
		return b.String()
	}

	b.WriteString("\nA request reaches the daemon on this machine only with an AO access token whose\n")
	b.WriteString("audience is that machine id and whose subject is that account id. Everything else\n")
	b.WriteString("gets a 401.\n")
	fmt.Fprintf(&b, "\n`ao vm serve` reads that file once at startup, so after any change to it:\n"+
		"  sudo systemctl restart %s\n", setupVMGatewayUnit)
	return b.String()
}
