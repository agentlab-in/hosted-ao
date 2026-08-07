package cli

// This file holds every decision `ao setup-vm` makes: the platform gate, the
// preflight verdicts, the path plan, the systemd unit text, and the closing
// summary. Nothing in here shells out, reads the network, or touches the disk,
// so all of it is unit-testable on macOS and Windows, where `CLI E2E` also
// runs. The thin privileged layer that executes these decisions lives in
// setupvm.go.

import (
	"encoding/json"
	"fmt"
	"net"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

const (
	// setupVMBinaryPath is where the ao binary is installed so both systemd
	// units can name it by absolute path. A unit must never depend on the
	// invoking shell's PATH.
	setupVMBinaryPath = "/usr/local/bin/ao"
	// setupVMUnitDir is the system-wide systemd unit directory. Units go here
	// rather than under /lib so a distro package upgrade never overwrites them.
	setupVMUnitDir = "/etc/systemd/system"
	// setupVMDaemonUnit runs the loopback daemon; setupVMGatewayUnit runs the
	// public TLS gateway. Two units, two processes, per ADR 0002. They are
	// never collapsed into one.
	setupVMDaemonUnit  = "ao-daemon.service"
	setupVMGatewayUnit = "ao-gateway.service"
	// setupVMKeyringPath and setupVMSourceListPath are GitHub's documented
	// apt locations for the gh package, used only when gh is missing.
	setupVMKeyringPath    = "/etc/apt/keyrings/githubcli-archive-keyring.gpg"
	setupVMSourceListPath = "/etc/apt/sources.list.d/github-cli.list"
	setupVMKeyringURL     = "https://cli.github.com/packages/githubcli-archive-keyring.gpg"
)

// setupVMPackages are the apt packages the VM needs. Agent harnesses are
// deliberately absent: they have interactive logins and are installed later by
// `ao vm setup-harness`.
var setupVMPackages = []string{"tmux", "git", "gh"}

// setupVMPorts are the ports the gateway must be able to bind and be reached
// on: 80 for the ACME HTTP-01 challenge and the https redirect, 443 for the
// public TLS listener.
var setupVMPorts = []int{80, 443}

// ---------------------------------------------------------------------------
// Platform gate
// ---------------------------------------------------------------------------

// setupPlatform is what a probe learned about this box's operating system.
type setupPlatform struct {
	GOOS         string
	OSRelease    map[string]string
	HasSystemctl bool
	HasAptGet    bool
}

// errUnsupportedPlatform reports that this box is not an Ubuntu box with
// systemd and apt. The command prints the manual path and exits without
// touching anything.
type errUnsupportedPlatform struct{ reason string }

func (e errUnsupportedPlatform) Error() string {
	return "ao setup-vm supports Ubuntu LTS with systemd and apt only: " + e.reason
}

// parseOSRelease parses the shell-assignment format of /etc/os-release. Values
// may be single- or double-quoted; anything unparseable is skipped rather than
// failing the whole file, because the gate only needs ID and VERSION.
func parseOSRelease(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if key != "" {
			out[key] = value
		}
	}
	return out
}

// platformName renders the best human name available for this box, for the
// refusal message.
func (p setupPlatform) platformName() string {
	if name := p.OSRelease["PRETTY_NAME"]; name != "" {
		return name
	}
	if id := p.OSRelease["ID"]; id != "" {
		return strings.TrimSpace(id + " " + p.OSRelease["VERSION_ID"])
	}
	return p.GOOS
}

// checkSetupPlatform is the platform gate. It returns an
// errUnsupportedPlatform for anything that is not Ubuntu with systemd and apt,
// so the caller can print the manual path instead of half-installing. A
// non-LTS Ubuntu is a warning rather than a refusal: apt and systemd are
// there, so every step still works, the release just goes end-of-life sooner.
func checkSetupPlatform(p setupPlatform) (warnings []string, err error) {
	if p.GOOS != "linux" {
		return nil, errUnsupportedPlatform{reason: "this is " + p.GOOS + ", not Linux"}
	}
	if id := strings.ToLower(p.OSRelease["ID"]); id != "ubuntu" {
		return nil, errUnsupportedPlatform{reason: "this box reports " + p.platformName()}
	}
	if !p.HasSystemctl {
		return nil, errUnsupportedPlatform{reason: "systemctl was not found on PATH, so systemd units cannot be installed"}
	}
	if !p.HasAptGet {
		return nil, errUnsupportedPlatform{reason: "apt-get was not found on PATH, so packages cannot be installed"}
	}
	if !strings.Contains(strings.ToUpper(p.OSRelease["VERSION"]), "LTS") {
		warnings = append(warnings, fmt.Sprintf(
			"%s is not an LTS release. Setup will work, but the release goes end-of-life sooner than an LTS one.",
			p.platformName()))
	}
	return warnings, nil
}

// renderManualPath is what an unsupported box gets instead of a half-install:
// the same work, spelled out, for a machine ao setup-vm will not touch.
func renderManualPath(p setupPlatform, domain string) string {
	var b strings.Builder
	b.WriteString("ao setup-vm automates Ubuntu LTS only, because it installs apt packages and\n")
	fmt.Fprintf(&b, "systemd units. This machine reports %s, so nothing was changed.\n", p.platformName())
	b.WriteString("\nSet the machine up by hand instead:\n")
	b.WriteString("\n  1. Install tmux, git, and the GitHub CLI (gh) with this system's package manager.\n")
	fmt.Fprintf(&b, "  2. Put the ao binary on PATH at an absolute location, for example %s.\n", setupVMBinaryPath)
	b.WriteString("  3. Run the daemon as your own (non-root) user, with the state directory set\n")
	b.WriteString("     explicitly to an absolute path:\n")
	b.WriteString("       AO_DATA_DIR=\"$HOME/.ao/hosted/data\" ao daemon\n")
	b.WriteString("     It listens on 127.0.0.1 only and has no authentication, which is why it must\n")
	b.WriteString("     never be exposed directly.\n")
	b.WriteString("  4. Run the gateway as a second, separate process, never the same one:\n")
	fmt.Fprintf(&b, "       AO_DATA_DIR=\"$HOME/.ao/hosted/data\" ao vm serve --domain %s\n", domain)
	b.WriteString("     It binds :80 and :443, so it needs both ports free, the privilege to bind\n")
	b.WriteString("     them, and both reachable from the internet for the Let's Encrypt HTTP-01\n")
	b.WriteString("     challenge.\n")
	fmt.Fprintf(&b, "  5. Point %s at this machine's public IP with a DNS A record.\n", domain)
	b.WriteString("  6. Supervise both processes with whatever this system uses, restarting them on\n")
	b.WriteString("     failure and on boot.\n")
	b.WriteString("\nThen finish the same way ao setup-vm would:\n")
	b.WriteString("  ao vm setup-harness claude   (log in to an agent harness in the foreground)\n")
	b.WriteString("  gh auth login                (git credentials for private repositories)\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Preflight
// ---------------------------------------------------------------------------

// setupProblem is one failed preflight check together with the exact steps
// that fix it. Preflight collects every problem before reporting, so one run
// tells the user everything they have to do.
type setupProblem struct {
	Check       string
	Detail      string
	Remediation []string
}

// setupPortProbe is the result of trying to bind one port on this box, on the
// loopback interface. Loopback is enough: privilege for ports under 1024 and a
// conflicting wildcard listener both show up there, and preflight has no
// business opening a public port of its own.
type setupPortProbe struct {
	Port int
	// Err is the bind error, nil when the port could be bound.
	Err error
	// HeldByGateway is true when the port is already held by this machine's
	// own gateway unit, which is a pass rather than a conflict: it is what a
	// second run of ao setup-vm on a working machine looks like.
	HeldByGateway bool
}

// setupReachability is the outside-in verdict for the public ports.
type setupReachability struct {
	// Ran is false when the probe could not be made: no prober answered, or
	// nothing is listening on this box yet so a closed answer would be
	// meaningless. Neither is evidence that the ports are closed, so it
	// downgrades to a warning rather than a failure, with the firewall
	// remediation printed anyway.
	Ran bool
	// Open maps port to whether an off-box prober could connect to it.
	Open map[int]bool
	// Err explains why Ran is false.
	Err error
}

// setupPreflight is everything observed about the box before anything is
// mutated. Every field is filled by a probe in setupvm.go; the verdict is
// computed by evaluatePreflight, which is pure.
type setupPreflight struct {
	Domain string

	// UID is this process's user id, 0 when it is already root.
	UID int
	// SudoPath is where sudo was found, empty when it is not installed.
	SudoPath string
	// SudoPasswordless is true when `sudo -n true` succeeded.
	SudoPasswordless bool

	// PublicIP is this box's address as seen from the internet.
	PublicIP    string
	PublicIPErr error
	// ResolvedIPs is what Domain resolves to right now.
	ResolvedIPs []string
	ResolveErr  error

	// TargetUser is who both units would run as: SUDO_USER when the command was
	// run through sudo, otherwise the login user. Root is refused, because the
	// daemon runs agent sessions, git, and gh.
	TargetUser string

	Ports []setupPortProbe
	Reach setupReachability
	// GatewayActive is whether this machine's own gateway unit is already
	// running, which is what makes an outside-in probe meaningful.
	GatewayActive bool

	// Cloud is the provider key detected from DMI, empty when unknown. It only
	// selects which firewall click-path to print.
	Cloud string
}

// evaluatePreflight turns observations into a verdict. Problems mean the run
// stops before mutating anything; warnings are printed and the run continues.
func evaluatePreflight(pf setupPreflight) (problems []setupProblem, warnings []string) {
	if p := checkSetupPrivilege(pf); p != nil {
		problems = append(problems, *p)
	}
	if p := checkSetupDNS(pf); p != nil {
		problems = append(problems, *p)
	}
	portProblems, portWarnings := checkSetupPorts(pf)
	problems = append(problems, portProblems...)
	warnings = append(warnings, portWarnings...)

	blocked := blockedSetupPorts(pf.Reach)
	switch {
	case len(blocked) > 0:
		problems = append(problems, setupProblem{
			Check:       "public reachability",
			Detail:      fmt.Sprintf("an off-box prober could not connect to %s on %s", portList(blocked), pf.Domain),
			Remediation: firewallRemediation(pf.Cloud, pf.Domain, blocked),
		})
	case !pf.Reach.Ran:
		warnings = append(warnings, unverifiedReachabilityDetail(pf)+
			"\n  This is the one thing setup cannot check for you and cannot fix. If the ports are"+
			"\n  closed, the gateway will never get a certificate."+
			// The local probe is not a substitute either: it binds loopback, so a
			// listener bound to this machine's public address alone never shows up
			// in it and `ao vm serve` would still lose the wildcard bind later.
			"\n  Preflight only bound "+portList(setupVMPorts)+" on 127.0.0.1, which is enough to catch a"+
			"\n  missing privilege or a wildcard listener, but not one bound to this machine's public"+
			"\n  address alone."+
			"\n"+strings.Join(prefixLines(firewallRemediation(pf.Cloud, pf.Domain, setupVMPorts), "  "), "\n"))
	}
	return problems, warnings
}

// unverifiedReachabilityDetail explains why the outside-in check did not
// produce a verdict. "Not verified" and "closed" are different answers and must
// never be printed as if they were the same one.
func unverifiedReachabilityDetail(pf setupPreflight) string {
	switch {
	case pf.Reach.Err != nil:
		return "could not verify " + portList(setupVMPorts) + " from outside: " + pf.Reach.Err.Error()
	case !pf.GatewayActive:
		return "nothing is listening on " + portList(setupVMPorts) +
			" on this machine yet, so an off-box probe cannot tell a blocked firewall from a stopped gateway"
	default:
		return "no off-box prober answered, so " + portList(setupVMPorts) + " were not verified from outside"
	}
}

func checkSetupPrivilege(pf setupPreflight) *setupProblem {
	// A root target user is a privilege problem, not a path problem, so it is
	// reported here: preflight stops before the first mutation, which keeps the
	// "nothing on this machine was changed" guarantee. buildSetupPlan rejects it
	// again for anything that reaches it another way.
	if isRootSetupIdentity(pf.TargetUser) {
		problem := rootSetupUserProblem(pf.Domain)
		return &problem
	}
	if pf.UID == 0 {
		return nil
	}
	rerun := fmt.Sprintf("sudo ao setup-vm --domain %s", pf.Domain)
	if pf.SudoPath == "" {
		return &setupProblem{
			Check:  "sudo",
			Detail: "sudo is not installed and this command is not running as root",
			Remediation: []string{
				"Installing packages and systemd units needs root. Either log in as root and run:",
				"  ao setup-vm --domain " + pf.Domain,
				"or install sudo first:",
				"  apt-get install -y sudo",
			},
		}
	}
	if !pf.SudoPasswordless {
		return &setupProblem{
			Check:  "sudo",
			Detail: "sudo needs a password and this command cannot prompt for one mid-install",
			Remediation: []string{
				"Run the whole command through sudo instead, so the password is asked once, up front:",
				"  " + rerun,
			},
		}
	}
	return nil
}

// isRootSetupIdentity reports whether a user or group name is the superuser.
// Only the name is available at this point, which is enough: this runs on the
// Ubuntu box the platform gate already checked for, where uid 0 is root.
func isRootSetupIdentity(name string) bool {
	return strings.TrimSpace(name) == "root"
}

// rootSetupUserProblem is the single wording for a root target user, shared by
// the preflight check and buildSetupPlan so the two can never disagree about
// why it is refused or how to fix it.
func rootSetupUserProblem(domain string) setupProblem {
	rerun := "ao setup-vm --domain " + domain
	if strings.TrimSpace(domain) == "" {
		rerun = "ao setup-vm --domain <your domain>"
	}
	return setupProblem{
		Check: "target user",
		Detail: "both systemd units would run as root, and the daemon runs your agent sessions, git, " +
			"and gh, so it must not be root",
		Remediation: []string{
			"This machine's login user is root, which is the default on DigitalOcean and Hetzner,",
			"and it is also what sudo -i and sudo su - leave behind. There is no unprivileged user",
			"to hand the agents to, so create one and run setup again as that user:",
			"  adduser --disabled-password --gecos \"\" ao",
			"  usermod -aG sudo ao",
			"  echo 'ao ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/90-ao && chmod 0440 /etc/sudoers.d/90-ao",
			"  sudo -u ao sudo " + rerun,
			"If this machine already has a human user, log in as them and run this instead:",
			"  sudo " + rerun,
			"Either way SUDO_USER then names the human who owns the machine, and that is who both",
			"units run as. The gateway still reaches :80 and :443, through CAP_NET_BIND_SERVICE",
			"rather than through privilege.",
		},
	}
}

func checkSetupDNS(pf setupPreflight) *setupProblem {
	if pf.PublicIPErr != nil {
		return &setupProblem{
			Check:  "public IP",
			Detail: "could not determine this machine's public IP: " + pf.PublicIPErr.Error(),
			Remediation: []string{
				"This machine needs outbound HTTPS to look up its own public address, and it needs it",
				"anyway to reach Let's Encrypt and the AO control plane. Check outbound connectivity:",
				"  curl -sS https://api.ipify.org",
				"If this machine has no outbound access on purpose, pass the address explicitly:",
				"  ao setup-vm --domain " + pf.Domain + " --public-ip <this machine's public IP>",
			},
		}
	}
	if pf.ResolveErr != nil {
		return &setupProblem{
			Check:       "DNS",
			Detail:      fmt.Sprintf("%s does not resolve: %s", pf.Domain, pf.ResolveErr.Error()),
			Remediation: dnsRemediation(pf.Domain, pf.PublicIP, nil),
		}
	}
	for _, ip := range pf.ResolvedIPs {
		if sameSetupIP(ip, pf.PublicIP) {
			return nil
		}
	}
	return &setupProblem{
		Check: "DNS",
		Detail: fmt.Sprintf("%s resolves to %s, but this machine's public IP is %s",
			pf.Domain, strings.Join(pf.ResolvedIPs, ", "), pf.PublicIP),
		Remediation: dnsRemediation(pf.Domain, pf.PublicIP, pf.ResolvedIPs),
	}
}

// sameSetupIP compares two addresses as addresses rather than as strings, so a
// non-canonical answer such as 2001:0DB8::1 matches the 2001:db8::1 a resolver
// hands back and preflight does not report a DNS mismatch that does not exist.
func sameSetupIP(a, b string) bool {
	ipA, ipB := net.ParseIP(strings.TrimSpace(a)), net.ParseIP(strings.TrimSpace(b))
	if ipA == nil || ipB == nil {
		return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
	}
	return ipA.Equal(ipB)
}

func dnsRemediation(domain, publicIP string, resolved []string) []string {
	record := "A"
	if ip := net.ParseIP(publicIP); ip != nil && ip.To4() == nil {
		record = "AAAA"
	}
	lines := []string{}
	if len(resolved) > 0 {
		lines = append(lines, fmt.Sprintf("Change the %s record for %s from %s to %s at your DNS provider:",
			record, domain, strings.Join(resolved, ", "), publicIP))
	} else {
		lines = append(lines, fmt.Sprintf("Add this record at your DNS provider, the one that hosts %s:", domain))
	}
	lines = append(lines,
		fmt.Sprintf("  %-24s %-6s %s", domain+".", record, publicIP),
		"Use a short TTL (60 seconds) while setting up. Then wait for it to propagate and check:",
		"  dig +short "+domain,
		"The gateway gets its certificate over ACME, which only succeeds once the domain resolves",
		"to this machine, so this has to be right before setup can finish.",
	)
	if len(resolved) > 0 {
		// A dual-stack box can be reported as a mismatch against a record that is
		// perfectly correct for the other family: whichever address this machine
		// was seen at is the one preflight compares. Naming the right one by hand
		// is the escape hatch, and it is the same flag the undiscoverable case uses.
		lines = append(lines,
			"If this machine has more than one public address and the record above is already right,",
			"name the address that record points at and run setup again:",
			"  ao setup-vm --domain "+domain+" --public-ip "+resolved[0],
		)
	}
	return lines
}

func checkSetupPorts(pf setupPreflight) (problems []setupProblem, warnings []string) {
	for _, probe := range pf.Ports {
		if probe.HeldByGateway {
			warnings = append(warnings, fmt.Sprintf(
				"port %d is already held by %s, which is this machine's own gateway. Leaving it running.",
				probe.Port, setupVMGatewayUnit))
			continue
		}
		if probe.Err == nil {
			continue
		}
		problems = append(problems, setupProblem{
			Check:       fmt.Sprintf("port %d", probe.Port),
			Detail:      fmt.Sprintf("cannot bind :%d on this machine: %s", probe.Port, probe.Err.Error()),
			Remediation: portRemediation(probe),
		})
	}
	return problems, warnings
}

// portRemediation classifies a bind failure by its message rather than by
// errno, because this function has to compile and be testable on every OS the
// CLI E2E matrix runs.
func portRemediation(probe setupPortProbe) []string {
	msg := strings.ToLower(probe.Err.Error())
	switch {
	case strings.Contains(msg, "permission denied"), strings.Contains(msg, "access"):
		return []string{
			"Ports below 1024 need privilege. Run the whole command through sudo:",
			"  sudo ao setup-vm --domain <your domain>",
			"The gateway unit itself does not run as root: it gets CAP_NET_BIND_SERVICE instead.",
		}
	case strings.Contains(msg, "in use"), strings.Contains(msg, "address already"):
		return []string{
			fmt.Sprintf("Something else is already listening on :%d. Find it:", probe.Port),
			fmt.Sprintf("  sudo ss -ltnp 'sport = :%d'", probe.Port),
			"A default web server is the usual answer. Stop and disable it, then run setup again:",
			"  sudo systemctl disable --now nginx    # or apache2, caddy, whatever ss reported",
			"The gateway terminates TLS itself, so this machine needs no other reverse proxy.",
		}
	default:
		return []string{
			fmt.Sprintf("Make :%d bindable on this machine, then run setup again.", probe.Port),
		}
	}
}

func blockedSetupPorts(reach setupReachability) []int {
	if !reach.Ran {
		return nil
	}
	blocked := make([]int, 0, len(setupVMPorts))
	for _, port := range setupVMPorts {
		if open, ok := reach.Open[port]; ok && !open {
			blocked = append(blocked, port)
		}
	}
	return blocked
}

// firewallRemediation prints exactly what the user has to click. The cloud
// firewall is the one blocker ao setup-vm cannot fix for them, so when the
// provider is known this narrows to that provider's path instead of a menu.
func firewallRemediation(cloud, domain string, ports []int) []string {
	return slices.Concat(
		[]string{
			fmt.Sprintf("Open inbound TCP %s to this machine. Two separate layers can block them:", portList(ports)),
			"",
			"  1. The host firewall on this box, which you can fix here:",
			"       sudo ufw status",
			"       sudo ufw allow 80/tcp && sudo ufw allow 443/tcp",
			"",
			"  2. Your cloud provider's firewall, which ao setup-vm cannot change for you:",
		},
		cloudFirewallSteps(cloud),
		[]string{
			"",
			"Then verify from a machine that is not this one, for example your laptop:",
			"  nc -vz " + domain + " 80",
			"  nc -vz " + domain + " 443",
		},
	)
}

func cloudFirewallSteps(cloud string) []string {
	switch cloud {
	case "aws":
		return []string{
			"       AWS (this looks like EC2): EC2 console > Instances > this instance > Security >",
			"       click its security group > Edit inbound rules > Add rule (Type HTTP, Source",
			"       0.0.0.0/0) > Add rule (Type HTTPS, Source 0.0.0.0/0) > Save rules.",
		}
	case "gcp":
		return []string{
			"       Google Cloud (this looks like Compute Engine): VPC network > Firewall >",
			"       Create firewall rule, Direction Ingress, Source 0.0.0.0/0, Protocols and ports",
			"       tcp:80,tcp:443 > Create. Or from a machine with gcloud:",
			"         gcloud compute firewall-rules create ao-gateway --direction=INGRESS \\",
			"           --action=allow --rules=tcp:80,tcp:443 --source-ranges=0.0.0.0/0",
		}
	case "azure":
		return []string{
			"       Azure (this looks like an Azure VM): Portal > this VM > Networking >",
			"       Network settings > Add inbound port rule, Destination port ranges 80,443,",
			"       Protocol TCP, Action Allow > Add. Or from a machine with az:",
			"         az network nsg rule create -g <resource-group> --nsg-name <nsg> \\",
			"           -n ao-gateway --priority 300 --destination-port-ranges 80 443 \\",
			"           --access Allow --protocol Tcp",
		}
	case "digitalocean":
		return []string{
			"       DigitalOcean (this looks like a Droplet): Networking > Firewalls > the firewall",
			"       attached to this Droplet > Inbound Rules > Add rule HTTP (80), then HTTPS (443),",
			"       sources All IPv4 and All IPv6 > Save. A Droplet with no firewall attached needs",
			"       no change here.",
		}
	case "hetzner":
		return []string{
			"       Hetzner Cloud (this looks like a Hetzner server): Console > this server >",
			"       Firewalls > the attached firewall > Rules > Add rule, Inbound, TCP, port 80,",
			"       any source; repeat for 443 > Save.",
		}
	default:
		return []string{
			"       AWS: the instance's security group > Edit inbound rules > add HTTP and HTTPS.",
			"       Google Cloud: VPC network > Firewall > allow ingress tcp:80,tcp:443.",
			"       Azure: the VM > Networking > Add inbound port rule for ports 80 and 443.",
			"       DigitalOcean: Networking > Firewalls > add HTTP and HTTPS inbound rules.",
			"       Hetzner Cloud: the server > Firewalls > add inbound TCP 80 and 443.",
			"       Anything else: look for a security group, network ACL, or cloud firewall and",
			"       allow inbound TCP 80 and 443 from anywhere.",
		}
	}
}

// setupCloudFromVendor maps the DMI system vendor string to a provider key. It
// is only ever used to pick which firewall instructions to print, so an
// unrecognised vendor is harmless.
func setupCloudFromVendor(vendor string) string {
	v := strings.ToLower(strings.TrimSpace(vendor))
	switch {
	case v == "":
		return ""
	case strings.Contains(v, "amazon"):
		return "aws"
	case strings.Contains(v, "google"):
		return "gcp"
	case strings.Contains(v, "microsoft"):
		return "azure"
	case strings.Contains(v, "digitalocean"):
		return "digitalocean"
	case strings.Contains(v, "hetzner"):
		return "hetzner"
	default:
		return ""
	}
}

// renderPreflightFailure is the whole output of a failed preflight. It leads
// with the fact that nothing was changed, because that is the guarantee the
// user most needs to trust when a setup script stops halfway.
func renderPreflightFailure(problems []setupProblem) string {
	var b strings.Builder
	b.WriteString("Preflight failed. Nothing on this machine was changed.\n")
	for _, problem := range problems {
		fmt.Fprintf(&b, "\nFAIL %s: %s\n", problem.Check, problem.Detail)
		for _, line := range problem.Remediation {
			if line == "" {
				b.WriteString("\n")
				continue
			}
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	b.WriteString("\nFix the items above and run the same command again. Preflight is re-runnable.\n")
	return b.String()
}

// parseSetupReachability reads the prober's answer. The contract is a JSON
// object of port to open, for example {"ports":{"80":true,"443":false}}; a
// port the prober did not report is left unknown rather than assumed closed.
func parseSetupReachability(body []byte, ports []int) (map[int]bool, error) {
	var payload struct {
		Ports map[string]bool `json:"ports"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse reachability response: %w", err)
	}
	if len(payload.Ports) == 0 {
		return nil, fmt.Errorf("reachability response reported no ports")
	}
	open := map[int]bool{}
	for _, port := range ports {
		if value, ok := payload.Ports[strconv.Itoa(port)]; ok {
			open[port] = value
		}
	}
	if len(open) == 0 {
		return nil, fmt.Errorf("reachability response reported no ports")
	}
	return open, nil
}

// ---------------------------------------------------------------------------
// The install plan
// ---------------------------------------------------------------------------

// setupPlan is every absolute path and identity the install needs. It is
// computed once, up front, so the systemd units and the summary can never
// disagree about where state lives.
type setupPlan struct {
	Domain string
	// User and Group own every path below and are who both units run as. The
	// daemon runs agent sessions, git, and gh, so it must not be root.
	User  string
	Group string
	Home  string
	// AODir is ~/.ao/hosted, the only place AO state may live.
	AODir string
	// DataDir, RunFile, MachineFile, and CertDir are all absolute. PR #18 made
	// the control plane require an absolute DATA_DIR because under systemd a
	// working-directory-relative path is how keys and state silently relocate.
	// Same discipline here: the units set every path explicitly.
	DataDir     string
	RunFile     string
	MachineFile string
	CertDir     string
	BinaryPath  string
	Packages    []string
	// Bound is whether machine.json already exists. `ao vm serve` reads it
	// once at startup, so an unbound machine gets an enabled-but-stopped
	// gateway and a summary line telling the user to restart it after binding.
	Bound bool
}

// setupPlanInput is the raw material for buildSetupPlan: the target user, and
// whatever AO_* overrides the environment carries.
type setupPlanInput struct {
	Domain      string
	User        string
	Group       string
	Home        string
	DataDir     string
	RunFile     string
	MachineFile string
	Bound       bool
}

// buildSetupPlan resolves every path to an absolute one. A relative AO_DATA_DIR
// or AO_RUN_FILE is rejected rather than absolutized: under systemd it would be
// resolved against the unit's working directory, which is exactly the silent
// state relocation this command has to prevent.
func buildSetupPlan(in setupPlanInput) (setupPlan, error) {
	if strings.TrimSpace(in.User) == "" {
		return setupPlan{}, fmt.Errorf("no target user for the systemd units")
	}
	// The invariant systemdUnit documents: User and Group are never root. The
	// preflight check refuses this earlier and with the full remediation; this is
	// the pure guard that makes the invariant true for every caller.
	if isRootSetupIdentity(in.User) || isRootSetupIdentity(in.Group) {
		return setupPlan{}, fmt.Errorf(
			"refusing to install units that run as root: %s\n%s",
			rootSetupUserProblem(in.Domain).Detail,
			strings.Join(prefixLines(rootSetupUserProblem(in.Domain).Remediation, "  "), "\n"))
	}
	if !isLinuxAbs(in.Home) {
		return setupPlan{}, fmt.Errorf("home directory %q for user %s is not an absolute path", in.Home, in.User)
	}
	group := in.Group
	if group == "" {
		group = in.User
	}
	// aoDir is the state root, derived from config.StateRootSegments() so the
	// rendered systemd units land in the same place the daemon itself defaults
	// to (~/.ao/hosted), not the upstream agent-orchestrator's ~/.ao.
	aoDir := slashPath(append([]string{in.Home}, config.StateRootSegments()...)...)
	plan := setupPlan{
		Domain:      in.Domain,
		User:        in.User,
		Group:       group,
		Home:        in.Home,
		AODir:       aoDir,
		DataDir:     slashPath(aoDir, "data"),
		RunFile:     slashPath(aoDir, "running.json"),
		MachineFile: slashPath(aoDir, "machine.json"),
		BinaryPath:  setupVMBinaryPath,
		Packages:    setupVMPackages,
		Bound:       in.Bound,
	}
	for _, override := range []struct {
		name  string
		value string
		dest  *string
	}{
		{"AO_DATA_DIR", in.DataDir, &plan.DataDir},
		{"AO_RUN_FILE", in.RunFile, &plan.RunFile},
		{"AO_MACHINE_FILE", in.MachineFile, &plan.MachineFile},
	} {
		value := strings.TrimSpace(override.value)
		if value == "" {
			continue
		}
		if !isLinuxAbs(value) {
			return setupPlan{}, fmt.Errorf(
				"%s=%q is a relative path. A systemd unit resolves it against the unit's working directory, "+
					"which silently moves state; set it to an absolute path or unset it", override.name, value)
		}
		*override.dest = value
	}
	// machine.json is the one file the gateway must be able to read as an
	// unprivileged user, and the install only creates and chowns directories
	// inside ~/.ao/hosted. An AO_MACHINE_FILE pointing outside it would be
	// written into a parent this run created as root, mode 0700, which the
	// gateway user cannot traverse: it would then refuse to start on a machine
	// that had just been bound successfully.
	if !insideSetupDir(plan.AODir, plan.MachineFile) {
		return setupPlan{}, fmt.Errorf(
			"AO_MACHINE_FILE=%q is outside %s. All AO state lives under %s, and the gateway runs as "+
				"%s, so a machine file anywhere else is one it cannot read; unset it or point it inside %s",
			plan.MachineFile, plan.AODir, plan.AODir, plan.User, plan.AODir)
	}
	// Matches vmgateway's own default so the unit is explicit about a path the
	// gateway would otherwise derive on its own.
	plan.CertDir = slashPath(plan.DataDir, "vm-gateway", "certs")
	return plan, nil
}

// insideSetupDir reports whether p is dir or below it, comparing whole path
// components so /home/ubuntu/.aoelsewhere is not read as being inside
// /home/ubuntu/.ao. Both are Linux paths for the target box, so path is right
// here and filepath is not.
func insideSetupDir(dir, p string) bool {
	dir, p = path.Clean(dir), path.Clean(p)
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// slashPath joins Linux paths regardless of the OS this command was compiled
// for, which is why it uses path and not filepath: the units it writes always
// run on the Ubuntu box, never on the machine a unit test happens to run on.
func slashPath(parts ...string) string {
	return path.Join(parts...)
}

// isLinuxAbs answers the question filepath.IsAbs cannot: these are always Linux
// paths, and on Windows filepath.IsAbs("/home/ubuntu") is false because it wants
// a drive letter, which would make the plan reject a perfectly good path when
// the unit tests run on the Windows leg of the CLI E2E matrix.
func isLinuxAbs(p string) bool {
	return strings.HasPrefix(p, "/")
}

// setupDirs are the directories the install creates, in order, all owned by
// the target user and none of them outside ~/.ao/hosted.
func (p setupPlan) setupDirs() []string {
	dirs := []string{p.AODir}
	for _, dir := range []string{p.DataDir, p.CertDir, path.Dir(p.RunFile)} {
		if !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// ---------------------------------------------------------------------------
// systemd units
// ---------------------------------------------------------------------------

// systemdUnit is the small subset of unit syntax these two services need.
type systemdUnit struct {
	Description string
	After       []string
	Wants       []string
	// User and Group are never root: the daemon runs agent sessions, git, and
	// gh as the human who owns the machine, and the gateway reaches :80 and
	// :443 through a capability instead of through privilege.
	User       string
	Group      string
	WorkingDir string
	Env        [][2]string
	ExecStart  string
	// AmbientCaps lets a non-root service bind a privileged port without
	// running as root.
	AmbientCaps []string
	// Restart is the systemd restart policy, defaulting to on-failure. The
	// gateway holds no session state and overrides it to always, so a clean
	// exit still comes back; the daemon supervises live agent sessions, so its
	// deliberate exits are left alone.
	Restart string
	Comment []string
}

// renderSystemdUnit writes a unit file.
//
// Environment= values are quoted, and that is deliberate: it is parsed as a
// list of words, so systemd splits it on whitespace and does unquote it.
// WorkingDirectory= is the opposite case and must never be quoted. It is a
// single value that systemd hands straight to
// path_simplify_and_warn(PATH_CHECK_ABSOLUTE|PATH_CHECK_FATAL) with no
// unquoting, so a leading double quote is not an absolute path, the parse
// handler returns -ENOEXEC, and the whole unit refuses to load rather than
// ignoring one setting. A space needs no escaping there for the same reason the
// quoting was wrong: the setting takes the rest of the line as one path.
func renderSystemdUnit(u systemdUnit) string {
	var b strings.Builder
	for _, line := range u.Comment {
		fmt.Fprintf(&b, "# %s\n", line)
	}
	b.WriteString("# Written by `ao setup-vm`. Re-running that command rewrites this file.\n")
	b.WriteString("\n[Unit]\n")
	fmt.Fprintf(&b, "Description=%s\n", u.Description)
	b.WriteString("Documentation=https://github.com/agentlab-in/hosted-ao\n")
	if len(u.Wants) > 0 {
		fmt.Fprintf(&b, "Wants=%s\n", strings.Join(u.Wants, " "))
	}
	if len(u.After) > 0 {
		fmt.Fprintf(&b, "After=%s\n", strings.Join(u.After, " "))
	}
	// systemd's default start rate limit is 5 starts in 10 seconds, and with
	// RestartSec=5 a unit that fails at boot burns that budget in 30 seconds and
	// enters failed, where nothing retries it until a human runs
	// systemctl reset-failed. A gateway that cannot bind :443 for the first half
	// minute after boot, or that hits one transient ACME error, would stop for
	// good on an unattended VM. Turning the limit off keeps systemd retrying.
	b.WriteString("StartLimitIntervalSec=0\n")

	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "User=%s\n", u.User)
	fmt.Fprintf(&b, "Group=%s\n", u.Group)
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", u.WorkingDir)
	for _, kv := range u.Env {
		fmt.Fprintf(&b, "Environment=%q\n", kv[0]+"="+kv[1])
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", u.ExecStart)
	if len(u.AmbientCaps) > 0 {
		fmt.Fprintf(&b, "AmbientCapabilities=%s\n", strings.Join(u.AmbientCaps, " "))
		fmt.Fprintf(&b, "CapabilityBoundingSet=%s\n", strings.Join(u.AmbientCaps, " "))
		b.WriteString("NoNewPrivileges=yes\n")
	}
	restart := u.Restart
	if restart == "" {
		restart = "on-failure"
	}
	fmt.Fprintf(&b, "Restart=%s\n", restart)
	b.WriteString("RestartSec=5\n")
	b.WriteString("KillSignal=SIGTERM\n")
	b.WriteString("TimeoutStopSec=30\n")

	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// unitEnvironment is the absolute state paths both units share. Nothing here
// is relative and nothing is left to be derived from the process working
// directory: that is how keys and state silently relocate under systemd, which
// is the hazard PR #18 fixed in the control plane.
func (p setupPlan) unitEnvironment() [][2]string {
	return [][2]string{
		{"HOME", p.Home},
		{"AO_DATA_DIR", p.DataDir},
		{"AO_RUN_FILE", p.RunFile},
	}
}

// daemonPath puts the user's own bin directories ahead of systemd's default
// PATH. The daemon spawns tmux, git, gh, and the agent harnesses, and harnesses
// install into ~/.local/bin or ~/bin; a unit that cannot see them reports a
// harness as missing when it is installed and working from a login shell.
func (p setupPlan) daemonPath() string {
	return strings.Join([]string{
		slashPath(p.Home, ".local", "bin"),
		slashPath(p.Home, "bin"),
		"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin",
	}, ":")
}

// renderDaemonUnit renders the loopback daemon's unit. The daemon stays bound
// to 127.0.0.1 and unauthenticated, per the hard rule in AGENTS.md, so it gets
// no capabilities and no public port.
func renderDaemonUnit(p setupPlan) string {
	return renderSystemdUnit(systemdUnit{
		Comment: []string{
			"The AO daemon: loopback only (127.0.0.1) and unauthenticated by design.",
			"Nothing off this box can reach it. Public traffic arrives through",
			"ao-gateway.service, a separate process, which authenticates every request",
			"before proxying it here. See docs/adr/0002-hosted-public-gateway.md.",
		},
		Description: "Agent Orchestrator daemon (loopback API)",
		Wants:       []string{"network-online.target"},
		After:       []string{"network-online.target"},
		User:        p.User,
		Group:       p.Group,
		WorkingDir:  p.DataDir,
		Env:         append(p.unitEnvironment(), [2]string{"PATH", p.daemonPath()}),
		ExecStart:   p.BinaryPath + " daemon",
	})
}

// renderGatewayUnit renders the public TLS gateway's unit. Every path is
// absolute and set here rather than derived from the process working
// directory, which is how ACME keys and state silently relocate under systemd.
func renderGatewayUnit(p setupPlan) string {
	env := append(p.unitEnvironment(),
		[2]string{"AO_MACHINE_FILE", p.MachineFile},
		[2]string{"AO_VM_DOMAIN", p.Domain},
		[2]string{"AO_VM_CERT_DIR", p.CertDir},
	)
	return renderSystemdUnit(systemdUnit{
		Comment: []string{
			"The AO VM gateway: binds :80 and :443, obtains and renews a Let's Encrypt",
			"certificate over ACME, verifies the AO access token on every request, and",
			"reverse-proxies authenticated requests to the loopback daemon.",
			"It is a separate process from the daemon and must stay one (ADR 0002).",
			"It reads " + p.MachineFile + " once at startup, so it needs a restart",
			"after this machine is bound to an AO account.",
		},
		Description: "Agent Orchestrator VM gateway (public TLS on :80 and :443)",
		Wants:       []string{"network-online.target"},
		After:       []string{"network-online.target", setupVMDaemonUnit},
		User:        p.User,
		Group:       p.Group,
		WorkingDir:  p.DataDir,
		Env:         env,
		ExecStart:   p.BinaryPath + " vm serve",
		AmbientCaps: []string{"CAP_NET_BIND_SERVICE"},
		// The gateway holds no session state, and a stopped gateway is a machine
		// that shows Offline in the desktop for no visible reason, so every exit
		// earns a restart.
		Restart: "always",
	})
}

// ---------------------------------------------------------------------------
// The closing summary
// ---------------------------------------------------------------------------

// setupUnitStates is what `systemctl is-active` said about each unit after it
// was started. Both units are Type=simple, so a successful `systemctl start`
// proves only that a process was forked; the summary must not report a unit as
// running on that alone.
type setupUnitStates struct {
	DaemonRunning  bool
	GatewayRunning bool
}

// renderSetupSummary is the last thing ao setup-vm prints. It reports the
// machine as installed but not ready, and names every remaining step with the
// exact command, rather than implying the machine is done.
func renderSetupSummary(p setupPlan, units setupUnitStates, warnings []string) string {
	var b strings.Builder
	b.WriteString("\nao setup-vm finished. This machine is installed, but not yet ready to use.\n")

	b.WriteString("\nInstalled:\n")
	fmt.Fprintf(&b, "  ao binary        %s\n", p.BinaryPath)
	fmt.Fprintf(&b, "  packages         %s\n", strings.Join(p.Packages, ", "))
	fmt.Fprintf(&b, "  state directory  %s (owned by %s)\n", p.AODir, p.User)
	daemonState := "enabled, running, loopback only"
	if !units.DaemonRunning {
		daemonState = "enabled, but not running: see the warnings below"
	}
	fmt.Fprintf(&b, "  daemon unit      %s (%s)\n", slashPath(setupVMUnitDir, setupVMDaemonUnit), daemonState)
	gatewayState := "enabled, not started: this machine is not bound yet"
	switch {
	case units.GatewayRunning:
		gatewayState = "enabled, running"
	case p.Bound:
		gatewayState = "enabled, but not running: see the warnings below"
	}
	fmt.Fprintf(&b, "  gateway unit     %s (%s)\n", slashPath(setupVMUnitDir, setupVMGatewayUnit), gatewayState)

	if len(warnings) > 0 {
		b.WriteString("\nWarnings from this run:\n")
		for _, warning := range warnings {
			fmt.Fprintf(&b, "  %s\n", warning)
		}
	}

	b.WriteString("\nStill missing. Nothing below is done for you:\n")
	step := 0
	next := func() int { step++; return step }

	if !p.Bound {
		fmt.Fprintf(&b, "\n  %d. This machine is not bound to an AO account, so the gateway is installed and\n", next())
		b.WriteString("     enabled but not serving. Run setup again to retry the binding, which is the\n")
		b.WriteString("     only step still outstanding; everything above it is already done:\n")
		fmt.Fprintf(&b, "       sudo ao setup-vm --domain %s\n", p.Domain)
		fmt.Fprintf(&b, "     Once that writes %s, the gateway needs a restart to\n", p.MachineFile)
		b.WriteString("     read it, because `ao vm serve` reads that file once at startup:\n")
		fmt.Fprintf(&b, "       sudo systemctl start %s\n", setupVMGatewayUnit)
	} else {
		fmt.Fprintf(&b, "\n  %d. This machine is already bound (%s), and ao setup-vm restarted\n", next(), p.MachineFile)
		fmt.Fprintf(&b, "     %s so it reads that binding. Check it at any time:\n", setupVMGatewayUnit)
		b.WriteString("       ao whoami\n")
		b.WriteString("     If you ever change that file by hand, restart the gateway yourself, because\n")
		b.WriteString("     `ao vm serve` reads it once at startup:\n")
		fmt.Fprintf(&b, "       sudo systemctl restart %s\n", setupVMGatewayUnit)
	}

	fmt.Fprintf(&b, "\n  %d. No agent harness is configured. ao setup-vm deliberately does not install one,\n", next())
	b.WriteString("     because the login is interactive. Run it in the foreground and finish the\n")
	b.WriteString("     harness's own login:\n")
	b.WriteString("       ao vm setup-harness claude\n")

	fmt.Fprintf(&b, "\n  %d. No git credentials on this machine, so private repositories cannot be cloned.\n", next())
	b.WriteString("     gh is installed; authenticate it once:\n")
	b.WriteString("       gh auth login\n")

	// Nothing in the run proves 80 and 443 are reachable from the internet: a
	// cloud firewall is invisible from inside the box, and the certificate is
	// only ordered on the first TLS handshake, so a blocked port shows up much
	// later as a machine that is Offline for no stated reason. This is a step the
	// operator has to run, not a warning, so it is numbered like the rest.
	fmt.Fprintf(&b, "\n  %d. Nobody has confirmed %s are reachable from the internet. setup-vm cannot\n",
		next(), portList(setupVMPorts))
	b.WriteString("     check that from this machine, because a cloud firewall is invisible from inside\n")
	b.WriteString("     the box. Run these two from any machine that is not this one, your laptop for\n")
	b.WriteString("     example:\n")
	fmt.Fprintf(&b, "       nc -vz %s 80\n", p.Domain)
	fmt.Fprintf(&b, "       nc -vz %s 443\n", p.Domain)
	b.WriteString("     If either one refuses or times out, open the port in your cloud provider's\n")
	b.WriteString("     firewall and in ufw. Until both answer, the gateway never gets a certificate,\n")
	b.WriteString("     because Let's Encrypt validates over :80.\n")

	b.WriteString("\nCheck this machine at any time:\n")
	b.WriteString("  ao doctor\n")
	fmt.Fprintf(&b, "  systemctl status %s %s\n", setupVMDaemonUnit, setupVMGatewayUnit)
	fmt.Fprintf(&b, "  journalctl -u %s -f\n", setupVMGatewayUnit)
	return b.String()
}

// renderSetupDryRun shows the whole plan, including both unit files verbatim,
// without touching the machine. Preflight has already run at this point, so a
// dry run is also the way to check DNS, ports, and sudo on a box you are not
// ready to modify.
func renderSetupDryRun(p setupPlan, warnings []string) string {
	var b strings.Builder
	b.WriteString("Dry run. Nothing on this machine was changed.\n")
	if len(warnings) > 0 {
		b.WriteString("\nWarnings:\n")
		for _, warning := range warnings {
			fmt.Fprintf(&b, "  %s\n", warning)
		}
	}
	b.WriteString("\nPlan:\n")
	fmt.Fprintf(&b, "  domain           %s\n", p.Domain)
	fmt.Fprintf(&b, "  run as           %s:%s\n", p.User, p.Group)
	fmt.Fprintf(&b, "  state directory  %s\n", p.AODir)
	fmt.Fprintf(&b, "  data dir         %s\n", p.DataDir)
	fmt.Fprintf(&b, "  run file         %s\n", p.RunFile)
	fmt.Fprintf(&b, "  machine file     %s (bound: %t)\n", p.MachineFile, p.Bound)
	fmt.Fprintf(&b, "  certificate dir  %s\n", p.CertDir)
	fmt.Fprintf(&b, "  ao binary        %s\n", p.BinaryPath)
	fmt.Fprintf(&b, "  apt packages     %s\n", strings.Join(p.Packages, ", "))

	for _, unit := range []struct {
		name    string
		content string
	}{
		{setupVMDaemonUnit, renderDaemonUnit(p)},
		{setupVMGatewayUnit, renderGatewayUnit(p)},
	} {
		fmt.Fprintf(&b, "\n--- %s ---\n", slashPath(setupVMUnitDir, unit.name))
		b.WriteString(unit.content)
	}
	b.WriteString("\nA real run also binds this machine to an AO account over a device code, which\n")
	b.WriteString("needs a browser and a human to approve it, and then writes the machine file\n")
	b.WriteString("above and restarts the gateway so it reads it.\n")
	b.WriteString("\nRun the same command without --dry-run to apply this.\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// small shared helpers
// ---------------------------------------------------------------------------

// normalizeSetupDomain reduces whatever the user passed to the bare hostname a
// certificate can be issued for. It mirrors vmgateway.Resolve's own
// normalization so `ao setup-vm --domain https://vm.example.com/` and the
// gateway agree on what the domain is.
func normalizeSetupDomain(raw string) (string, error) {
	domain := strings.TrimSpace(raw)
	if scheme, rest, ok := strings.Cut(domain, "://"); ok && scheme != "" {
		domain = rest
	}
	domain = strings.TrimSuffix(domain, "/")
	if domain == "" {
		return "", fmt.Errorf("--domain is required, for example --domain vm.example.com")
	}
	if strings.ContainsAny(domain, ":/ ") || !strings.Contains(domain, ".") {
		return "", fmt.Errorf("invalid --domain %q: expected a bare hostname you own, like vm.example.com", raw)
	}
	if net.ParseIP(domain) != nil {
		return "", fmt.Errorf("invalid --domain %q: a certificate cannot be issued for a bare IP, so hosted AO needs a hostname", raw)
	}
	return domain, nil
}

// dpkgInstalled reads `dpkg-query -W -f=${Status}` output. Anything other than
// the fully-installed status means the package still has to be installed.
func dpkgInstalled(out string) bool {
	return strings.Contains(out, "install ok installed")
}

// githubCLISourceList is the apt source GitHub documents for gh, used only
// when the distro has no gh package. arch comes from `dpkg --print-architecture`.
func githubCLISourceList(arch string) string {
	return fmt.Sprintf("deb [arch=%s signed-by=%s] https://cli.github.com/packages stable main\n",
		arch, setupVMKeyringPath)
}

func portList(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	switch len(parts) {
	case 0:
		return "no ports"
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

func prefixLines(lines []string, prefix string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, prefix+line)
	}
	return out
}
