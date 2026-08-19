package cli

// Every decision `ao setup-vm` makes is tested here, with no VM, no apt, and no
// systemd: the platform gate, the preflight verdicts, the path plan, both unit
// files, and the closing summary. This file has to pass on macOS and Windows,
// where CLI E2E also runs, which is exactly why the decisions live in pure
// functions and not inside the privileged shell-out layer.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

const ubuntuNobleOSRelease = `PRETTY_NAME="Ubuntu 24.04.2 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.2 LTS (Noble Numbat)"
VERSION_CODENAME=noble
ID=ubuntu
ID_LIKE=debian
HOME_URL="https://www.ubuntu.com/"
`

func ubuntuPlatform() setupPlatform {
	return setupPlatform{
		GOOS:         "linux",
		OSRelease:    parseOSRelease(ubuntuNobleOSRelease),
		HasSystemctl: true,
		HasAptGet:    true,
	}
}

func TestParseOSRelease(t *testing.T) {
	got := parseOSRelease(ubuntuNobleOSRelease + "\n# a comment\nBROKEN\nSINGLE='quoted'\n")
	for key, want := range map[string]string{
		"ID":          "ubuntu",
		"VERSION_ID":  "24.04",
		"VERSION":     "24.04.2 LTS (Noble Numbat)",
		"PRETTY_NAME": "Ubuntu 24.04.2 LTS",
		"SINGLE":      "quoted",
	} {
		if got[key] != want {
			t.Errorf("parseOSRelease()[%q] = %q, want %q", key, got[key], want)
		}
	}
	if _, ok := got["BROKEN"]; ok {
		t.Error("a line with no '=' should be skipped, not stored")
	}
}

func TestCheckSetupPlatform(t *testing.T) {
	tests := []struct {
		name        string
		platform    setupPlatform
		wantErr     bool
		wantWarning string
	}{
		{name: "ubuntu LTS", platform: ubuntuPlatform()},
		{
			name: "ubuntu non-LTS warns but proceeds",
			platform: setupPlatform{
				GOOS: "linux", HasSystemctl: true, HasAptGet: true,
				OSRelease: parseOSRelease("ID=ubuntu\nVERSION=\"25.04 (Plucky Puffin)\"\nPRETTY_NAME=\"Ubuntu 25.04\"\n"),
			},
			wantWarning: "not an LTS release",
		},
		// The distro gate widened to the whole Debian family
		// (docs/plans/2026-08-16-pair-by-ip-headless-boxes.md, task 7), so
		// Raspberry Pi OS can pass it: Debian itself and Raspberry Pi OS both
		// now pass, and neither gets the Ubuntu-specific "not an LTS release"
		// warning, since neither uses that term.
		{
			name: "debian passes and gets no LTS warning",
			platform: setupPlatform{
				GOOS: "linux", HasSystemctl: true, HasAptGet: true,
				OSRelease: parseOSRelease("ID=debian\nVERSION=\"12 (bookworm)\"\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n"),
			},
		},
		{
			name: "raspberry pi os (id=debian) passes",
			platform: setupPlatform{
				GOOS: "linux", HasSystemctl: true, HasAptGet: true,
				OSRelease: parseOSRelease("ID=debian\nVERSION=\"12 (bookworm)\"\nPRETTY_NAME=\"Raspberry Pi OS (64-bit)\"\n"),
			},
		},
		{
			name: "raspberry pi os (older id=raspbian, id_like=debian) passes",
			platform: setupPlatform{
				GOOS: "linux", HasSystemctl: true, HasAptGet: true,
				OSRelease: parseOSRelease("ID=raspbian\nID_LIKE=debian\nVERSION=\"11 (bullseye)\"\nPRETTY_NAME=\"Raspbian GNU/Linux 11 (bullseye)\"\n"),
			},
		},
		{name: "macOS is refused", platform: setupPlatform{GOOS: "darwin"}, wantErr: true},
		{name: "windows is refused", platform: setupPlatform{GOOS: "windows"}, wantErr: true},
		{
			name: "a genuinely unsupported distro is still refused",
			platform: setupPlatform{
				GOOS: "linux", HasSystemctl: true, HasAptGet: true,
				OSRelease: parseOSRelease("ID=fedora\nPRETTY_NAME=\"Fedora Linux 40\"\n"),
			},
			wantErr: true,
		},
		{
			name: "ubuntu without systemd is refused",
			platform: setupPlatform{
				GOOS: "linux", HasAptGet: true, OSRelease: parseOSRelease(ubuntuNobleOSRelease),
			},
			wantErr: true,
		},
		{
			name: "ubuntu without apt is refused",
			platform: setupPlatform{
				GOOS: "linux", HasSystemctl: true, OSRelease: parseOSRelease(ubuntuNobleOSRelease),
			},
			wantErr: true,
		},
		{
			name: "debian without systemd is still refused: the widened gate does not drop the real requirements",
			platform: setupPlatform{
				GOOS: "linux", HasAptGet: true,
				OSRelease: parseOSRelease("ID=debian\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n"),
			},
			wantErr: true,
		},
		{
			name: "debian without apt-get is still refused",
			platform: setupPlatform{
				GOOS: "linux", HasSystemctl: true,
				OSRelease: parseOSRelease("ID=debian\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n"),
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			warnings, err := checkSetupPlatform(tc.platform)
			if tc.wantErr {
				var unsupported errUnsupportedPlatform
				if !errors.As(err, &unsupported) {
					t.Fatalf("checkSetupPlatform err = %v, want errUnsupportedPlatform", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkSetupPlatform err = %v, want nil", err)
			}
			if tc.wantWarning == "" {
				if len(warnings) != 0 {
					t.Fatalf("warnings = %v, want none", warnings)
				}
				return
			}
			if !strings.Contains(strings.Join(warnings, "\n"), tc.wantWarning) {
				t.Fatalf("warnings = %v, want one containing %q", warnings, tc.wantWarning)
			}
		})
	}
}

func TestRenderManualPathNamesEveryStep(t *testing.T) {
	text := renderManualPath(setupPlatform{GOOS: "darwin"}, "vm.example.com")
	for _, want := range []string{
		"nothing was changed",
		"ao daemon",
		"ao vm serve --domain vm.example.com",
		"ao vm setup-harness claude",
		"gh auth login",
		"AO_DATA_DIR",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("manual path is missing %q:\n%s", want, text)
		}
	}
	assertNoDashes(t, text)
}

func TestNormalizeSetupDomain(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "vm.example.com", want: "vm.example.com"},
		{in: "  vm.example.com  ", want: "vm.example.com"},
		{in: "https://vm.example.com", want: "vm.example.com"},
		{in: "https://vm.example.com/", want: "vm.example.com"},
		{in: "", wantErr: true},
		{in: "vm.example.com:443", wantErr: true},
		{in: "localhost", wantErr: true},
		{in: "203.0.113.10", wantErr: true},
		{in: "https://203.0.113.10", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := normalizeSetupDomain(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeSetupDomain(%q) = %q, want an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeSetupDomain(%q) err = %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeSetupDomain(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// passingPreflight is a box where every check succeeds: root, DNS pointing
// here, both ports free, and the gateway not yet running.
func passingPreflight() setupPreflight {
	return setupPreflight{
		Domain:      "vm.example.com",
		UID:         0,
		TargetUser:  "ubuntu",
		PublicIP:    "203.0.113.10",
		ResolvedIPs: []string{"203.0.113.10"},
		Ports:       []setupPortProbe{{Port: 80}, {Port: 443}},
	}
}

func TestEvaluatePreflight_Passing(t *testing.T) {
	problems, warnings := evaluatePreflight(passingPreflight())
	if len(problems) != 0 {
		t.Fatalf("problems = %+v, want none", problems)
	}
	// Nothing was listening yet, so the outside-in check cannot run and has to
	// say so loudly rather than silently passing.
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "cannot tell a blocked firewall from a stopped gateway") {
		t.Fatalf("warnings = %v, want the unverified-reachability warning", warnings)
	}
	if !strings.Contains(joined, "sudo ufw allow 80/tcp") || !strings.Contains(joined, "nc -vz vm.example.com 443") {
		t.Fatalf("the unverified warning must still print the firewall fix and the off-box check:\n%s", joined)
	}
	// The local probe binds loopback only, so it cannot see a listener bound to
	// the public address alone. Saying so is the difference between a passed check
	// and a proven one.
	if !strings.Contains(joined, "127.0.0.1") {
		t.Fatalf("the warning must say the local probe only bound loopback:\n%s", joined)
	}
}

func TestEvaluatePreflight_SudoProblems(t *testing.T) {
	t.Run("no sudo and not root", func(t *testing.T) {
		pf := passingPreflight()
		pf.UID = 1000
		problems, _ := evaluatePreflight(pf)
		assertProblem(t, problems, "sudo", "apt-get install -y sudo")
	})
	t.Run("sudo needs a password", func(t *testing.T) {
		pf := passingPreflight()
		pf.UID = 1000
		pf.SudoPath = "/usr/bin/sudo"
		problems, _ := evaluatePreflight(pf)
		assertProblem(t, problems, "sudo", "sudo ao setup-vm --domain vm.example.com")
	})
	t.Run("passwordless sudo passes", func(t *testing.T) {
		pf := passingPreflight()
		pf.UID = 1000
		pf.SudoPath = "/usr/bin/sudo"
		pf.SudoPasswordless = true
		if problems, _ := evaluatePreflight(pf); len(problems) != 0 {
			t.Fatalf("problems = %+v, want none", problems)
		}
	})
}

// TestEvaluatePreflight_RefusesARootTargetUser is the preflight half of the
// root guard. A DigitalOcean or Hetzner box, or anyone who ran sudo -i, is root
// with SUDO_USER unset, so nothing else in preflight objects: UID 0 is exactly
// what the privilege check wants. Both units would then run as root, so ao daemon
// would run every agent session, every git, and every gh as root. It has to
// surface here, before anything on the box is touched, and not later as an
// install-time error.
func TestEvaluatePreflight_RefusesARootTargetUser(t *testing.T) {
	pf := passingPreflight()
	pf.TargetUser = "root"
	problems, _ := evaluatePreflight(pf)
	problem := assertProblem(t, problems, "target user", "adduser")
	if !strings.Contains(problem.Detail, "root") {
		t.Errorf("the detail must say the units would run as root: %q", problem.Detail)
	}
	if !strings.Contains(strings.Join(problem.Remediation, "\n"), "sudo ao setup-vm --domain vm.example.com") {
		t.Errorf("remediation must name how to re-run as a human user: %v", problem.Remediation)
	}
	// The privilege check still has to be the one that reports it, so the
	// no-mutation guarantee holds.
	if !strings.HasPrefix(renderPreflightFailure(problems), "Preflight failed. Nothing on this machine was changed.") {
		t.Error("a root target user must be a preflight failure, which changes nothing")
	}
	assertNoDashes(t, renderPreflightFailure(problems))
}

func TestEvaluatePreflight_DNSProblems(t *testing.T) {
	t.Run("domain points somewhere else", func(t *testing.T) {
		pf := passingPreflight()
		pf.ResolvedIPs = []string{"198.51.100.7"}
		problems, _ := evaluatePreflight(pf)
		problem := assertProblem(t, problems, "DNS", "203.0.113.10")
		if !strings.Contains(problem.Detail, "198.51.100.7") {
			t.Errorf("the detail must name what the domain resolves to now: %q", problem.Detail)
		}
		if !strings.Contains(strings.Join(problem.Remediation, "\n"), "dig +short vm.example.com") {
			t.Errorf("remediation must include the verification command: %v", problem.Remediation)
		}
	})
	t.Run("domain does not resolve at all", func(t *testing.T) {
		pf := passingPreflight()
		pf.ResolvedIPs = nil
		pf.ResolveErr = errors.New("no such host")
		problems, _ := evaluatePreflight(pf)
		problem := assertProblem(t, problems, "DNS", "Add this record")
		if !strings.Contains(strings.Join(problem.Remediation, "\n"), "A      203.0.113.10") {
			t.Errorf("remediation must spell out the exact A record: %v", problem.Remediation)
		}
	})
	t.Run("an IPv6 public address asks for AAAA", func(t *testing.T) {
		pf := passingPreflight()
		pf.PublicIP = "2001:db8::1"
		pf.ResolvedIPs = []string{"203.0.113.10"}
		problems, _ := evaluatePreflight(pf)
		problem := assertProblem(t, problems, "DNS", "AAAA")
		if strings.Contains(strings.Join(problem.Remediation, "\n"), " A ") {
			t.Errorf("an IPv6 address must not be described as an A record: %v", problem.Remediation)
		}
	})
	t.Run("a non-canonical IPv6 answer is not a mismatch", func(t *testing.T) {
		pf := passingPreflight()
		pf.PublicIP = "2001:0DB8::1"
		pf.ResolvedIPs = []string{"2001:db8::1"}
		if problems, _ := evaluatePreflight(pf); len(problems) != 0 {
			t.Fatalf("problems = %+v, want none: these two spellings are the same address", problems)
		}
	})
	t.Run("a real mismatch offers the explicit address", func(t *testing.T) {
		pf := passingPreflight()
		pf.ResolvedIPs = []string{"198.51.100.7"}
		problems, _ := evaluatePreflight(pf)
		problem := assertProblem(t, problems, "DNS", "--public-ip 198.51.100.7")
		if !strings.Contains(strings.Join(problem.Remediation, "\n"), "more than one public address") {
			t.Errorf("a dual-stack box needs the escape hatch explained: %v", problem.Remediation)
		}
	})
	t.Run("public IP undiscoverable", func(t *testing.T) {
		pf := passingPreflight()
		pf.PublicIP = ""
		pf.PublicIPErr = errors.New("dial tcp: i/o timeout")
		problems, _ := evaluatePreflight(pf)
		assertProblem(t, problems, "public IP", "--public-ip")
	})
}

func TestEvaluatePreflight_PortProblems(t *testing.T) {
	t.Run("another web server holds 80", func(t *testing.T) {
		pf := passingPreflight()
		pf.Ports = []setupPortProbe{
			{Port: 80, Err: errors.New("listen tcp 127.0.0.1:80: bind: address already in use")},
			{Port: 443},
		}
		problems, _ := evaluatePreflight(pf)
		problem := assertProblem(t, problems, "port 80", "ss -ltnp 'sport = :80'")
		if !strings.Contains(strings.Join(problem.Remediation, "\n"), "systemctl disable --now nginx") {
			t.Errorf("remediation must name how to stop the conflicting server: %v", problem.Remediation)
		}
	})
	t.Run("no privilege for a low port", func(t *testing.T) {
		pf := passingPreflight()
		pf.Ports = []setupPortProbe{
			{Port: 80},
			{Port: 443, Err: errors.New("listen tcp 127.0.0.1:443: bind: permission denied")},
		}
		problems, _ := evaluatePreflight(pf)
		assertProblem(t, problems, "port 443", "sudo ao setup-vm")
	})
	t.Run("our own gateway holding the ports is a warning, not a failure", func(t *testing.T) {
		pf := passingPreflight()
		pf.GatewayActive = true
		pf.Ports = []setupPortProbe{
			{Port: 80, Err: errors.New("bind: address already in use"), HeldByGateway: true},
			{Port: 443, Err: errors.New("bind: address already in use"), HeldByGateway: true},
		}
		problems, warnings := evaluatePreflight(pf)
		if len(problems) != 0 {
			t.Fatalf("problems = %+v, want none: a re-run on a working machine must not fail", problems)
		}
		if !strings.Contains(strings.Join(warnings, "\n"), "this machine's own gateway") {
			t.Fatalf("warnings = %v, want one naming the gateway", warnings)
		}
	})
}

// TestEvaluatePreflight_PairDoesNotDemandPort80 is the pair-mode port
// requirement directly: pair mode binds only the HTTPS port, never :80 (no
// ACME challenge to answer), so preflight must never probe or complain about
// :80 in pair mode, and setupVMPortsPair itself must be HTTPS only.
func TestEvaluatePreflight_PairDoesNotDemandPort80(t *testing.T) {
	if len(setupVMPortsPair) != 1 || setupVMPortsPair[0] != 443 {
		t.Fatalf("setupVMPortsPair = %v, want only 443: pair mode never binds :80", setupVMPortsPair)
	}
	pf := setupPreflight{
		Pair:       true,
		TargetUser: "ubuntu",
		Ports:      []setupPortProbe{{Port: 443}},
	}
	problems, warnings := evaluatePreflight(pf)
	if len(problems) != 0 {
		t.Fatalf("problems = %+v, want none", problems)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none: pair mode has no domain and no off-box reachability check", warnings)
	}
}

// TestEvaluatePreflight_PairSkipsDNSAndReachability confirms pair mode never
// reports a DNS or reachability problem, even when the (unused) fields that
// would normally cause one are populated: a pair preflight simply never
// looks at them, because pair mode has no domain and never contacts the
// control plane (docs/adr/0003-pair-mode-gateway.md).
func TestEvaluatePreflight_PairSkipsDNSAndReachability(t *testing.T) {
	pf := setupPreflight{
		Pair:        true,
		TargetUser:  "ubuntu",
		Domain:      "",
		ResolveErr:  errors.New("no such host"),
		PublicIPErr: errors.New("dial tcp: i/o timeout"),
		Reach:       setupReachability{Ran: true, Open: map[int]bool{80: false, 443: false}},
		Ports:       []setupPortProbe{{Port: 443}},
	}
	problems, warnings := evaluatePreflight(pf)
	if len(problems) != 0 {
		t.Fatalf("problems = %+v, want none: pair mode must not evaluate DNS or reachability at all", problems)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
}

// TestEvaluatePreflight_PairPortFailureUsesPairRerun pins the pair-specific
// remediation wording: a low-port bind failure in pair mode must point at
// `ao setup-vm --pair`, never at the hosted `--domain` form, which pair mode
// does not take at all.
func TestEvaluatePreflight_PairPortFailureUsesPairRerun(t *testing.T) {
	pf := setupPreflight{
		Pair:       true,
		TargetUser: "ubuntu",
		Ports: []setupPortProbe{
			{Port: 443, Err: errors.New("listen tcp 127.0.0.1:443: bind: permission denied")},
		},
	}
	problems, _ := evaluatePreflight(pf)
	problem := assertProblem(t, problems, "port 443", "sudo ao setup-vm --pair")
	if strings.Contains(strings.Join(problem.Remediation, "\n"), "--domain") {
		t.Errorf("pair-mode remediation must not mention --domain: %v", problem.Remediation)
	}
}

func TestEvaluatePreflight_BlockedFirewallIsDetectedAndNamed(t *testing.T) {
	pf := passingPreflight()
	pf.Cloud = "aws"
	pf.GatewayActive = true
	pf.Reach = setupReachability{Ran: true, Open: map[int]bool{80: true, 443: false}}

	problems, _ := evaluatePreflight(pf)
	problem := assertProblem(t, problems, "public reachability", "Edit inbound rules")
	remediation := strings.Join(problem.Remediation, "\n")
	if !strings.Contains(problem.Detail, "443") || strings.Contains(problem.Detail, "80 and 443") {
		t.Errorf("only the blocked port should be reported: %q", problem.Detail)
	}
	if strings.Contains(remediation, "Hetzner") {
		t.Errorf("a known cloud must get its own instructions, not the whole menu:\n%s", remediation)
	}
	if !strings.Contains(remediation, "nc -vz vm.example.com 443") {
		t.Errorf("remediation must include the off-box verification: %s", remediation)
	}
	assertNoDashes(t, renderPreflightFailure(problems))
}

func TestFirewallRemediationPerCloud(t *testing.T) {
	for cloud, want := range map[string]string{
		"aws":          "security group",
		"gcp":          "gcloud compute firewall-rules create",
		"azure":        "az network nsg rule create",
		"digitalocean": "Droplet",
		"hetzner":      "Hetzner Cloud",
		"":             "Anything else",
	} {
		lines := strings.Join(firewallRemediation(cloud, "vm.example.com", setupVMPorts), "\n")
		if !strings.Contains(lines, want) {
			t.Errorf("firewallRemediation(%q) is missing %q:\n%s", cloud, want, lines)
		}
		if !strings.Contains(lines, "ufw allow 443/tcp") {
			t.Errorf("firewallRemediation(%q) must also cover the host firewall", cloud)
		}
	}
}

func TestSetupCloudFromVendor(t *testing.T) {
	for vendor, want := range map[string]string{
		"Amazon EC2":            "aws",
		"Google":                "gcp",
		"Microsoft Corporation": "azure",
		"DigitalOcean":          "digitalocean",
		"Hetzner":               "hetzner",
		"QEMU":                  "",
		"":                      "",
		"  Amazon EC2\n":        "aws",
	} {
		if got := setupCloudFromVendor(vendor); got != want {
			t.Errorf("setupCloudFromVendor(%q) = %q, want %q", vendor, got, want)
		}
	}
}

func TestRenderPreflightFailureLeadsWithChangedNothing(t *testing.T) {
	pf := passingPreflight()
	pf.UID = 1000
	pf.ResolvedIPs = []string{"198.51.100.7"}
	problems, _ := evaluatePreflight(pf)
	if len(problems) < 2 {
		t.Fatalf("expected every problem to be collected in one pass, got %+v", problems)
	}
	text := renderPreflightFailure(problems)
	if !strings.HasPrefix(text, "Preflight failed. Nothing on this machine was changed.") {
		t.Fatalf("the first line must be the no-mutation guarantee:\n%s", text)
	}
	if !strings.Contains(text, "run the same command again") {
		t.Errorf("the failure text must say it is re-runnable:\n%s", text)
	}
}

func TestParseSetupReachability(t *testing.T) {
	open, err := parseSetupReachability([]byte(`{"ports":{"80":true,"443":false}}`), setupVMPorts)
	if err != nil {
		t.Fatalf("parseSetupReachability err = %v", err)
	}
	if !open[80] || open[443] {
		t.Fatalf("open = %v, want 80 open and 443 closed", open)
	}
	if _, err := parseSetupReachability([]byte(`not json`), setupVMPorts); err == nil {
		t.Error("malformed JSON must be an error, never a silent pass")
	}
	if _, err := parseSetupReachability([]byte(`{"ports":{}}`), setupVMPorts); err == nil {
		t.Error("an empty ports object must be an error, never a silent pass")
	}
	// A prober that answers about other ports tells us nothing about ours.
	if _, err := parseSetupReachability([]byte(`{"ports":{"22":true}}`), setupVMPorts); err == nil {
		t.Error("a response about unrelated ports must be an error")
	}
}

// ---------------------------------------------------------------------------
// the plan and the units
// ---------------------------------------------------------------------------

func testSetupPlan(t *testing.T) setupPlan {
	t.Helper()
	plan, err := buildSetupPlan(setupPlanInput{
		Domain: "vm.example.com",
		User:   "ubuntu",
		Group:  "ubuntu",
		Home:   "/home/ubuntu",
	})
	if err != nil {
		t.Fatalf("buildSetupPlan err = %v", err)
	}
	return plan
}

func TestBuildSetupPlan_DefaultsUnderAODir(t *testing.T) {
	plan := testSetupPlan(t)
	for name, got := range map[string]string{
		"AODir":       plan.AODir,
		"DataDir":     plan.DataDir,
		"RunFile":     plan.RunFile,
		"MachineFile": plan.MachineFile,
		"CertDir":     plan.CertDir,
	} {
		if !strings.HasPrefix(got, "/home/ubuntu/.ao/hosted") {
			t.Errorf("%s = %q, want it under /home/ubuntu/.ao/hosted: all AO state lives there only", name, got)
		}
	}
	if plan.DataDir != "/home/ubuntu/.ao/hosted/data" {
		t.Errorf("DataDir = %q", plan.DataDir)
	}
	if plan.RunFile != "/home/ubuntu/.ao/hosted/running.json" {
		t.Errorf("RunFile = %q", plan.RunFile)
	}
	if plan.CertDir != "/home/ubuntu/.ao/hosted/data/vm-gateway/certs" {
		t.Errorf("CertDir = %q, want vmgateway's own default so the unit is explicit about it", plan.CertDir)
	}
	if plan.BinaryPath != "/usr/local/bin/ao" {
		t.Errorf("BinaryPath = %q, want an absolute path a systemd unit can name", plan.BinaryPath)
	}
	if plan.Bound {
		t.Error("a fresh plan must not claim the machine is bound")
	}
}

func TestBuildSetupPlan_RejectsRelativeOverrides(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   setupPlanInput
	}{
		{name: "AO_DATA_DIR", in: setupPlanInput{User: "ubuntu", Home: "/home/ubuntu", DataDir: "data"}},
		{name: "AO_RUN_FILE", in: setupPlanInput{User: "ubuntu", Home: "/home/ubuntu", RunFile: "running.json"}},
		{name: "AO_MACHINE_FILE", in: setupPlanInput{User: "ubuntu", Home: "/home/ubuntu", MachineFile: "machine.json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildSetupPlan(tc.in)
			if err == nil {
				t.Fatal("a relative override must be rejected, not absolutized: under systemd it silently relocates state")
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("the error must name the variable at fault: %v", err)
			}
		})
	}
}

func TestBuildSetupPlan_HonorsAbsoluteOverrides(t *testing.T) {
	plan, err := buildSetupPlan(setupPlanInput{
		Domain: "vm.example.com", User: "ubuntu", Home: "/home/ubuntu",
		DataDir: "/srv/ao/data", RunFile: "/srv/ao/running.json",
		// The machine file has to stay inside ~/.ao/hosted, which the next test covers.
		MachineFile: "/home/ubuntu/.ao/hosted/machines/this-one.json",
	})
	if err != nil {
		t.Fatalf("buildSetupPlan err = %v", err)
	}
	if plan.DataDir != "/srv/ao/data" || plan.RunFile != "/srv/ao/running.json" {
		t.Fatalf("absolute overrides were not honored: %+v", plan)
	}
	if plan.MachineFile != "/home/ubuntu/.ao/hosted/machines/this-one.json" {
		t.Fatalf("MachineFile = %q, want the override inside ~/.ao/hosted honored", plan.MachineFile)
	}
	if plan.CertDir != "/srv/ao/data/vm-gateway/certs" {
		t.Errorf("CertDir = %q, want it to follow the overridden data dir", plan.CertDir)
	}
}

// TestBuildSetupPlan_RejectsAMachineFileOutsideAODir keeps the one file the
// gateway must be able to read inside the only tree this install creates and
// chowns. A machine.json under a parent this run created as root, mode 0700,
// is one the gateway user cannot traverse, so `ao vm serve` would refuse to
// start on a machine that had just been bound successfully.
func TestBuildSetupPlan_RejectsAMachineFileOutsideAODir(t *testing.T) {
	for _, machineFile := range []string{
		"/srv/ao/machine.json",
		"/etc/ao/machine.json",
		// A prefix match on the string alone would let this one through.
		"/home/ubuntu/.ao/hostedelsewhere/machine.json",
		"/home/ubuntu/.ao/hosted/../machine.json",
	} {
		t.Run(machineFile, func(t *testing.T) {
			_, err := buildSetupPlan(setupPlanInput{
				Domain: "vm.example.com", User: "ubuntu", Home: "/home/ubuntu", MachineFile: machineFile,
			})
			if err == nil {
				t.Fatalf("AO_MACHINE_FILE=%q must be refused: the gateway could not read it", machineFile)
			}
			if !strings.Contains(err.Error(), "AO_MACHINE_FILE") {
				t.Errorf("the error must name the variable at fault: %v", err)
			}
		})
	}
	// The directory itself is fine, and so is a subdirectory of it.
	for _, machineFile := range []string{"/home/ubuntu/.ao/hosted/machine.json", "/home/ubuntu/.ao/hosted/sub/machine.json"} {
		if _, err := buildSetupPlan(setupPlanInput{
			Domain: "vm.example.com", User: "ubuntu", Home: "/home/ubuntu", MachineFile: machineFile,
		}); err != nil {
			t.Errorf("AO_MACHINE_FILE=%q is inside ~/.ao/hosted and must be honored: %v", machineFile, err)
		}
	}
}

// TestBuildSetupPlan_RejectsRoot is the pure half of the root guard, and it is
// what makes systemdUnit's documented invariant ("User and Group are never
// root") true rather than aspirational.
func TestBuildSetupPlan_RejectsRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   setupPlanInput
	}{
		{name: "user", in: setupPlanInput{Domain: "vm.example.com", User: "root", Home: "/root"}},
		{name: "group", in: setupPlanInput{Domain: "vm.example.com", User: "ubuntu", Group: "root", Home: "/home/ubuntu"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildSetupPlan(tc.in)
			if err == nil {
				t.Fatal("a root identity must be refused: the daemon runs agent sessions, git, and gh")
			}
			if !strings.Contains(err.Error(), "root") {
				t.Errorf("the error must say what is wrong: %v", err)
			}
			// The same remediation the preflight problem carries, so an operator
			// who somehow reaches this path is not told less.
			if !strings.Contains(err.Error(), "adduser") {
				t.Errorf("the error must name the fix: %v", err)
			}
		})
	}
}

func TestBuildSetupPlan_RejectsUnusableIdentity(t *testing.T) {
	if _, err := buildSetupPlan(setupPlanInput{Home: "/home/ubuntu"}); err == nil {
		t.Error("a plan with no target user must fail: the units would otherwise run as root")
	}
	if _, err := buildSetupPlan(setupPlanInput{User: "ubuntu", Home: "relative/home"}); err == nil {
		t.Error("a relative home directory must fail")
	}
	plan, err := buildSetupPlan(setupPlanInput{User: "ubuntu", Home: "/home/ubuntu"})
	if err != nil {
		t.Fatalf("buildSetupPlan err = %v", err)
	}
	if plan.Group != "ubuntu" {
		t.Errorf("Group = %q, want it to default to the user's own name", plan.Group)
	}
}

func TestSetupDirsAreDeduplicated(t *testing.T) {
	dirs := testSetupPlan(t).setupDirs()
	seen := map[string]bool{}
	for _, dir := range dirs {
		if seen[dir] {
			t.Fatalf("setupDirs returned %q twice: %v", dir, dirs)
		}
		seen[dir] = true
	}
	for _, want := range []string{"/home/ubuntu/.ao/hosted", "/home/ubuntu/.ao/hosted/data", "/home/ubuntu/.ao/hosted/data/vm-gateway/certs"} {
		if !seen[want] {
			t.Errorf("setupDirs is missing %q: %v", want, dirs)
		}
	}
}

// testPairSetupPlan mirrors testSetupPlan for a pair-mode plan: no domain.
func testPairSetupPlan(t *testing.T) setupPlan {
	t.Helper()
	plan, err := buildSetupPlan(setupPlanInput{
		Pair:  true,
		User:  "ubuntu",
		Group: "ubuntu",
		Home:  "/home/ubuntu",
	})
	if err != nil {
		t.Fatalf("buildSetupPlan err = %v", err)
	}
	return plan
}

func TestBuildSetupPlan_PairDefaultsUnderAODir(t *testing.T) {
	plan := testPairSetupPlan(t)
	if !plan.Pair {
		t.Fatal("Pair must be true")
	}
	if plan.Domain != "" {
		t.Errorf("Domain = %q, want empty: pair mode has no domain", plan.Domain)
	}
	if plan.PairCertDir != "/home/ubuntu/.ao/hosted/vm-gateway/pair-cert" {
		t.Errorf("PairCertDir = %q, want it under the state root, matching vmgateway.resolvePair's own default", plan.PairCertDir)
	}
	if plan.PasscodeDir != "/home/ubuntu/.ao/hosted/vm-gateway/pair-passcode" {
		t.Errorf("PasscodeDir = %q, want it under the state root, matching vmgateway.resolvePair's own default", plan.PasscodeDir)
	}
	if plan.Bound {
		t.Error("a pair plan must never claim Bound: pair mode has no account to bind")
	}
}

// TestSetupDirsPairModeCreatesPairDirsNotTheACMEOne is the pair-mode half of
// TestSetupDirsAreDeduplicated: a pair plan must create its own certificate
// and passcode directories, and must not create the hosted ACME cache
// directory it will never use.
func TestSetupDirsPairModeCreatesPairDirsNotTheACMEOne(t *testing.T) {
	plan := testPairSetupPlan(t)
	dirs := plan.setupDirs()
	seen := map[string]bool{}
	for _, dir := range dirs {
		seen[dir] = true
	}
	for _, want := range []string{plan.AODir, plan.DataDir, plan.PairCertDir, plan.PasscodeDir} {
		if !seen[want] {
			t.Errorf("setupDirs is missing %q: %v", want, dirs)
		}
	}
	if seen[plan.CertDir] {
		t.Errorf("setupDirs must not create the unused hosted ACME cert dir %q in pair mode: %v", plan.CertDir, dirs)
	}
}

func TestRenderDaemonUnit(t *testing.T) {
	unit := renderDaemonUnit(testSetupPlan(t))
	for _, want := range []string{
		"[Unit]",
		"[Service]",
		"[Install]",
		"User=ubuntu",
		"Group=ubuntu",
		// Unquoted, deliberately: see TestSetupUnitsQuoteOnlyTheListSettings.
		"WorkingDirectory=/home/ubuntu/.ao/hosted/data",
		`Environment="AO_DATA_DIR=/home/ubuntu/.ao/hosted/data"`,
		`Environment="AO_RUN_FILE=/home/ubuntu/.ao/hosted/running.json"`,
		`Environment="HOME=/home/ubuntu"`,
		"ExecStart=/usr/local/bin/ao daemon",
		// The daemon supervises live agent sessions, so a deliberate exit is left
		// alone; only a failure earns a restart.
		"Restart=on-failure",
		// A real start-limit policy, not the old retry-forever one: see
		// setupVMStartLimitIntervalSec's comment for the numbers' reasoning.
		"StartLimitIntervalSec=300",
		"StartLimitBurst=4",
		"RestartSteps=4",
		"RestartMaxDelaySec=60",
		// Generous enough that a crash loop never has journald drop the lines
		// the summary tells the operator to go read.
		"LogRateLimitIntervalSec=60",
		"LogRateLimitBurst=10000",
		// The daemon spawns arbitrary agent subprocesses, so it gets real
		// headroom rather than the gateway's small ceiling.
		"MemoryMax=85%",
		"CPUQuota=400%",
		"TasksMax=4096",
		"WantedBy=multi-user.target",
		// The daemon spawns the harnesses, which install under the user's home.
		`Environment="PATH=/home/ubuntu/.local/bin:/home/ubuntu/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"`,
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("daemon unit is missing %q:\n%s", want, unit)
		}
	}
	// The daemon is loopback-only and unauthenticated: it must never be handed
	// the right to bind a public port.
	for _, unwanted := range []string{"AmbientCapabilities", "CAP_NET_BIND_SERVICE", "AO_VM_DOMAIN", "vm serve"} {
		if strings.Contains(unit, unwanted) {
			t.Errorf("daemon unit must not contain %q:\n%s", unwanted, unit)
		}
	}
	assertNoDashes(t, unit)
}

func TestRenderGatewayUnit(t *testing.T) {
	unit := renderGatewayUnit(testSetupPlan(t))
	for _, want := range []string{
		"User=ubuntu",
		"Group=ubuntu",
		"WorkingDirectory=/home/ubuntu/.ao/hosted/data",
		`Environment="AO_DATA_DIR=/home/ubuntu/.ao/hosted/data"`,
		`Environment="AO_MACHINE_FILE=/home/ubuntu/.ao/hosted/machine.json"`,
		`Environment="AO_VM_DOMAIN=vm.example.com"`,
		`Environment="AO_VM_CERT_DIR=/home/ubuntu/.ao/hosted/data/vm-gateway/certs"`,
		"ExecStart=/usr/local/bin/ao vm serve",
		"AmbientCapabilities=CAP_NET_BIND_SERVICE",
		"CapabilityBoundingSet=CAP_NET_BIND_SERVICE",
		// The gateway fronts the daemon, so it starts after it.
		"After=network-online.target ao-daemon.service",
		// The gateway holds no session state, so every exit earns a restart, and
		// the start-limit policy below is what stops a persistently broken
		// gateway from retrying an ACME order into Let's Encrypt's rate limit.
		"Restart=always",
		"StartLimitIntervalSec=300",
		"StartLimitBurst=4",
		"RestartSteps=4",
		"RestartMaxDelaySec=60",
		"LogRateLimitIntervalSec=60",
		"LogRateLimitBurst=10000",
		// The gateway is one small Go binary with no subprocess spawning of its
		// own, so its ceiling only has to cover a leak or a bug.
		"MemoryMax=256M",
		"CPUQuota=100%",
		"TasksMax=128",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("gateway unit is missing %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "ao daemon") {
		t.Error("the gateway unit must not start the daemon: they are two separate processes (ADR 0002)")
	}
	if !strings.Contains(unit, "needs a restart") {
		t.Error("the gateway unit should record that machine.json is read once at startup")
	}
	assertNoDashes(t, unit)
}

// TestRenderGatewayUnit_Pair pins pair mode's gateway unit env: AO_VM_PAIR=on
// selects pair mode, AO_VM_CERT_DIR and AO_VM_PASSCODE_DIR point at the pair
// certificate and passcode directories, and none of the hosted-only
// variables are set. vmgateway.Resolve's resolvePair rejects any of them
// alongside AO_VM_PAIR (internal/vmgateway/config.go), so setting one here
// would make the rendered unit fail to start.
func TestRenderGatewayUnit_Pair(t *testing.T) {
	plan := testPairSetupPlan(t)
	unit := renderGatewayUnit(plan)
	for _, want := range []string{
		`Environment="AO_VM_PAIR=on"`,
		`Environment="AO_VM_CERT_DIR=/home/ubuntu/.ao/hosted/vm-gateway/pair-cert"`,
		`Environment="AO_VM_PASSCODE_DIR=/home/ubuntu/.ao/hosted/vm-gateway/pair-passcode"`,
		"ExecStart=/usr/local/bin/ao vm serve",
		"AmbientCapabilities=CAP_NET_BIND_SERVICE",
		"Restart=always",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("pair gateway unit is missing %q:\n%s", want, unit)
		}
	}
	for _, unwanted := range []string{"AO_VM_DOMAIN", "AO_MACHINE_FILE", "AO_VM_ISSUER", "AO_VM_ACCOUNT_ID", "AO_VM_JWKS_URL", "AO_VM_HTTP_ADDR"} {
		if strings.Contains(unit, unwanted) {
			t.Errorf("pair gateway unit must not set %q: vmgateway.resolvePair rejects hosted-only fields alongside AO_VM_PAIR:\n%s", unwanted, unit)
		}
	}
	assertNoDashes(t, unit)
}

// TestGatewayResourceCeilingIsSmallerThanTheDaemons is the defect-2 contract
// directly: the daemon spawns arbitrary agent subprocesses and needs real
// headroom, but the gateway is one small Go binary, so a bug in it must not be
// able to claim as much of the box as a legitimate multi-session daemon can.
func TestGatewayResourceCeilingIsSmallerThanTheDaemons(t *testing.T) {
	daemon := renderDaemonUnit(testSetupPlan(t))
	gateway := renderGatewayUnit(testSetupPlan(t))

	daemonMemory, ok := unitSetting(daemon, "MemoryMax")
	if !ok || !strings.HasSuffix(daemonMemory, "%") {
		t.Fatalf("daemon MemoryMax = %q, want a percentage of system RAM", daemonMemory)
	}
	gatewayMemory, ok := unitSetting(gateway, "MemoryMax")
	if !ok || strings.HasSuffix(gatewayMemory, "%") {
		t.Fatalf("gateway MemoryMax = %q, want a fixed byte ceiling, not a percentage of system RAM", gatewayMemory)
	}

	daemonCPU, ok := unitSetting(daemon, "CPUQuota")
	if !ok {
		t.Fatal("daemon has no CPUQuota")
	}
	gatewayCPU, ok := unitSetting(gateway, "CPUQuota")
	if !ok {
		t.Fatal("gateway has no CPUQuota")
	}
	daemonPercent, err := strconv.Atoi(strings.TrimSuffix(daemonCPU, "%"))
	if err != nil {
		t.Fatalf("daemon CPUQuota = %q: %v", daemonCPU, err)
	}
	gatewayPercent, err := strconv.Atoi(strings.TrimSuffix(gatewayCPU, "%"))
	if err != nil {
		t.Fatalf("gateway CPUQuota = %q: %v", gatewayCPU, err)
	}
	if gatewayPercent >= daemonPercent {
		t.Errorf("gateway CPUQuota %s must be smaller than the daemon's %s", gatewayCPU, daemonCPU)
	}

	daemonTasks, ok := unitSetting(daemon, "TasksMax")
	if !ok {
		t.Fatal("daemon has no TasksMax")
	}
	gatewayTasks, ok := unitSetting(gateway, "TasksMax")
	if !ok {
		t.Fatal("gateway has no TasksMax")
	}
	daemonTasksN, err := strconv.Atoi(daemonTasks)
	if err != nil {
		t.Fatalf("daemon TasksMax = %q: %v", daemonTasks, err)
	}
	gatewayTasksN, err := strconv.Atoi(gatewayTasks)
	if err != nil {
		t.Fatalf("gateway TasksMax = %q: %v", gatewayTasks, err)
	}
	if gatewayTasksN >= daemonTasksN {
		t.Errorf("gateway TasksMax %s must be smaller than the daemon's %s", gatewayTasks, daemonTasks)
	}
}

// TestSetupUnitsQuoteOnlyTheListSettings is the regression test for the defect
// that would have made the very first real run fail after apt had already
// installed packages and both units had been written.
//
// systemd only unquotes settings it parses as a list of words. Environment= is
// one of those, so quoting it is right and stays. WorkingDirectory= is not: the
// raw value goes to path_simplify_and_warn with PATH_CHECK_ABSOLUTE and
// PATH_CHECK_FATAL, so a value starting with a double quote is not an absolute
// path, the parse handler returns -ENOEXEC, and the unit refuses to load at all
// rather than ignoring one setting. The golden assertions in this file used to
// bake in the quoted form, which is why CI stayed green while the box would not
// have booted the service.
func TestSetupUnitsQuoteOnlyTheListSettings(t *testing.T) {
	plan := testSetupPlan(t)
	for name, unit := range map[string]string{
		setupVMDaemonUnit:  renderDaemonUnit(plan),
		setupVMGatewayUnit: renderGatewayUnit(plan),
	} {
		value, ok := unitSetting(unit, "WorkingDirectory")
		if !ok {
			t.Fatalf("%s has no WorkingDirectory:\n%s", name, unit)
		}
		if strings.ContainsAny(value, `"'`) {
			t.Errorf("%s: WorkingDirectory=%s is quoted, which systemd treats as a fatal error and "+
				"refuses to load the unit for", name, value)
		}
		if !strings.HasPrefix(value, "/") {
			t.Errorf("%s: WorkingDirectory=%s must be a plain absolute path", name, value)
		}
		if value != plan.DataDir {
			t.Errorf("%s: WorkingDirectory=%s, want the plan's data dir %s", name, value, plan.DataDir)
		}
		// Environment= is the opposite case and must keep its quotes: systemd
		// splits that one on whitespace.
		if !strings.Contains(unit, `Environment="AO_DATA_DIR=`+plan.DataDir+`"`) {
			t.Errorf("%s: Environment= values must stay quoted, they are parsed as a list of words:\n%s", name, unit)
		}
	}
}

// unitSetting returns the raw value of the first occurrence of a unit setting,
// exactly as systemd's parser would see it: no unquoting, no trimming.
func unitSetting(unit, directive string) (string, bool) {
	for _, line := range strings.Split(unit, "\n") {
		if key, value, ok := strings.Cut(line, "="); ok && key == directive {
			return value, true
		}
	}
	return "", false
}

// TestSetupUnitsSetEveryPathAbsolutely is the regression test for the hazard
// PR #18 fixed in the control plane: a working-directory-relative path under
// systemd silently relocates keys and state.
func TestSetupUnitsSetEveryPathAbsolutely(t *testing.T) {
	plan := testSetupPlan(t)
	for name, unit := range map[string]string{
		setupVMDaemonUnit:  renderDaemonUnit(plan),
		setupVMGatewayUnit: renderGatewayUnit(plan),
	} {
		for _, line := range strings.Split(unit, "\n") {
			directive, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch directive {
			case "WorkingDirectory":
				if !strings.HasPrefix(value, "/") {
					t.Errorf("%s: %s must be absolute, got %q", name, directive, value)
				}
			case "ExecStart":
				if !strings.HasPrefix(value, "/") {
					t.Errorf("%s: %s must be absolute, got %q", name, directive, value)
				}
			case "Environment":
				key, path, found := strings.Cut(strings.Trim(value, `"`), "=")
				if !found || !strings.HasPrefix(key, "AO_") || !strings.HasSuffix(key, "DIR") && !strings.HasSuffix(key, "FILE") {
					continue
				}
				if !strings.HasPrefix(path, "/") {
					t.Errorf("%s: %s=%s must be absolute", name, key, path)
				}
			}
		}
	}
}

func TestRenderSetupSummary_Unbound(t *testing.T) {
	plan := testSetupPlan(t)
	summary := renderSetupSummary(plan, setupUnitStates{DaemonRunning: true},
		[]string{"80 and 443 were not verified from outside"})
	for _, want := range []string{
		"installed, but not yet ready",
		"not bound to an AO account",
		"sudo systemctl start ao-gateway.service",
		"ao vm setup-harness claude",
		"gh auth login",
		"ao doctor",
		"/home/ubuntu/.ao/hosted/machine.json",
		"80 and 443 were not verified from outside",
		"enabled, not started",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary is missing %q:\n%s", want, summary)
		}
	}
	assertNoDashes(t, summary)
}

// TestRenderSetupSummary_NamesTheOffBoxPortCheck pins the one preflight
// requirement setup-vm cannot meet from inside the box. The control plane does
// not implement the reachability probe, and a cloud firewall is invisible from
// here, so the pair of nc commands is a numbered step the operator has to run,
// not remediation text buried inside a warning.
func TestRenderSetupSummary_NamesTheOffBoxPortCheck(t *testing.T) {
	plan := testSetupPlan(t)
	summary := renderSetupSummary(plan, setupUnitStates{DaemonRunning: true}, nil)
	for _, want := range []string{
		"nc -vz vm.example.com 80",
		"nc -vz vm.example.com 443",
		"cannot",
		"from any machine that is not this one",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary is missing %q:\n%s", want, summary)
		}
	}
	// It is a numbered still-missing step, in the same list as the harness and
	// the git credentials.
	stillMissing := summary[strings.Index(summary, "Still missing"):]
	if !strings.Contains(stillMissing, "nc -vz") {
		t.Errorf("the off-box check must be one of the numbered steps:\n%s", stillMissing)
	}
	assertNoDashes(t, summary)
}

// TestRenderSetupSummary_DoesNotClaimAUnitItDidNotSeeRunning is M7 seen from the
// summary: both units are Type=simple, so a successful `systemctl start` proves
// only that a process was forked. A crash loop must not read as a green run.
func TestRenderSetupSummary_DoesNotClaimAUnitItDidNotSeeRunning(t *testing.T) {
	plan := testSetupPlan(t)
	plan.Bound = true
	summary := renderSetupSummary(plan, setupUnitStates{}, []string{"ao-daemon.service was started but is not active"})
	if strings.Contains(summary, "enabled, running") {
		t.Errorf("a unit that is not active must not be reported as running:\n%s", summary)
	}
	if strings.Count(summary, "not running") != 2 {
		t.Errorf("both units were started and neither is active, so both lines must say so:\n%s", summary)
	}
	assertNoDashes(t, summary)
}

func TestRenderSetupSummary_Bound(t *testing.T) {
	plan := testSetupPlan(t)
	plan.Bound = true
	summary := renderSetupSummary(plan, setupUnitStates{DaemonRunning: true, GatewayRunning: true}, nil)
	if !strings.Contains(summary, "already bound") {
		t.Errorf("a bound machine's summary must say so:\n%s", summary)
	}
	if !strings.Contains(summary, "sudo systemctl restart ao-gateway.service") {
		t.Errorf("a bound machine still needs the restart command after re-binding:\n%s", summary)
	}
	if !strings.Contains(summary, "enabled, running") {
		t.Errorf("a started gateway must be reported as running:\n%s", summary)
	}
	// The two remaining manual steps are never done for the user.
	for _, want := range []string{"ao vm setup-harness claude", "gh auth login"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary is missing %q:\n%s", want, summary)
		}
	}
}

func TestRenderSetupDryRunShowsBothUnitsAndChangesNothing(t *testing.T) {
	plan := testSetupPlan(t)
	text := renderSetupDryRun(plan, nil)
	if !strings.HasPrefix(text, "Dry run. Nothing on this machine was changed.") {
		t.Fatalf("dry run must lead with the no-mutation guarantee:\n%s", text)
	}
	for _, want := range []string{
		"/etc/systemd/system/ao-daemon.service",
		"/etc/systemd/system/ao-gateway.service",
		"ExecStart=/usr/local/bin/ao daemon",
		"ExecStart=/usr/local/bin/ao vm serve",
		"tmux, git, gh",
		"without --dry-run",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("dry run output is missing %q:\n%s", want, text)
		}
	}
}

func TestRenderSetupDryRunPairShowsBothUnitsAndChangesNothing(t *testing.T) {
	plan := testPairSetupPlan(t)
	text := renderSetupDryRunPair(plan, nil)
	if !strings.HasPrefix(text, "Dry run. Nothing on this machine was changed.") {
		t.Fatalf("dry run must lead with the no-mutation guarantee:\n%s", text)
	}
	for _, want := range []string{
		"no domain, no AO account, no control-plane contact",
		"/etc/systemd/system/ao-daemon.service",
		"/etc/systemd/system/ao-gateway.service",
		"ExecStart=/usr/local/bin/ao daemon",
		"ExecStart=/usr/local/bin/ao vm serve",
		"AO_VM_PAIR=on",
		"pair cert dir",
		"passcode dir",
		"Re-running never rotates either",
		"ao vm rotate-passcode",
		"without --dry-run",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("pair dry run output is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "AO_VM_DOMAIN") || strings.Contains(text, "device code") {
		t.Errorf("pair dry run must not mention the hosted domain/device-flow path:\n%s", text)
	}
	assertNoDashes(t, text)
}

// TestRenderSetupSummaryPair_FirstRunShowsThePasscodeOnce is the printed-output
// half of the single most important pair-mode contract: the passcode and
// fingerprint appear together, once, on the run that generated them.
func TestRenderSetupSummaryPair_FirstRunShowsThePasscodeOnce(t *testing.T) {
	plan := testPairSetupPlan(t)
	summary := renderSetupSummaryPair(plan, setupUnitStates{DaemonRunning: true, GatewayRunning: true}, nil,
		"AB12CD34", true, "ao-pair://v1/192.168.1.20:443#"+strings.Repeat("0", 64)+":AB12CD34",
		"07:CA:9F:3E:B2:11:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99", nil)
	for _, want := range []string{
		"No domain, no AO account, no control-plane contact",
		"AB12CD34",
		"07:CA:9F:3E:B2:11",
		"HTTPS only, no :80",
		"ao vm setup-harness claude",
		"gh auth login",
		"ao doctor",
		"mismatch means refuse and re-pair",
		"Paste this in Hosted AO: ao-pair://v1/192.168.1.20:443#",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("pair summary is missing %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "AO account") == false {
		t.Errorf("pair summary must state no AO account is involved:\n%s", summary)
	}
	assertNoDashes(t, summary)
}

// TestRenderSetupSummaryPair_ReRunNeverShowsThePasscodeAgain is the printed
// side of the non-rotation guarantee: a run that did not generate a fresh
// passcode must never print one, plaintext or otherwise, and must point at
// the deliberate rotate command instead.
func TestRenderSetupSummaryPair_ReRunNeverShowsThePasscodeAgain(t *testing.T) {
	plan := testPairSetupPlan(t)
	summary := renderSetupSummaryPair(plan, setupUnitStates{DaemonRunning: true, GatewayRunning: true}, nil,
		"", false, "", "07:CA:9F:3E:B2:11:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99", nil)
	if strings.Contains(summary, "AB12CD34") {
		t.Error("a re-run must never print a passcode")
	}
	if strings.Contains(summary, "Paste this in Hosted AO") {
		t.Error("a re-run must never print a pairing string: the passcode is only known in plaintext on the run that generates it")
	}
	if !strings.Contains(summary, "already set from an earlier run") {
		t.Errorf("a re-run must say the passcode is unchanged:\n%s", summary)
	}
	if !strings.Contains(summary, "ao vm rotate-passcode") {
		t.Errorf("a re-run must point at the deliberate rotate command:\n%s", summary)
	}
	// The fingerprint is not a secret and must still be printed every run.
	if !strings.Contains(summary, "07:CA:9F:3E:B2:11") {
		t.Errorf("the fingerprint must still be printed on a re-run:\n%s", summary)
	}
	assertNoDashes(t, summary)
}

func TestRenderPairCredentials_FallsBackWhenNoAddressWasFound(t *testing.T) {
	text := renderPairCredentials("AB12CD34", true, "", "07:CA", nil, ":443")
	if !strings.Contains(text, "443") {
		t.Errorf("must still print the port when no address was found: %q", text)
	}
	if !strings.Contains(text, "LAN IP") {
		t.Errorf("must fall back to telling the operator to find their own address: %q", text)
	}
}

// TestRenderPairCredentials_ExplainsWhenNoAddressWasFound is the "no
// usable address" fallback the review found missing: a run that generated
// a fresh passcode but could not build a pairing string (no address found)
// must say so explicitly, not silently omit the "Paste this in Hosted AO:"
// line with no explanation at all.
func TestRenderPairCredentials_ExplainsWhenNoAddressWasFound(t *testing.T) {
	text := renderPairCredentials("AB12CD34", true, "", "07:CA", nil, ":443")
	if strings.Contains(text, "Paste this in Hosted AO") {
		t.Errorf("must not claim to have a pairing string when none was built:\n%s", text)
	}
	if !strings.Contains(text, "No pairing string could be built") {
		t.Errorf("must explicitly say no pairing string could be built, not omit it silently:\n%s", text)
	}
	assertNoDashes(t, text)
}

func TestRenderPairCredentials_ListsEveryDiscoveredAddress(t *testing.T) {
	text := renderPairCredentials("AB12CD34", true, "", "07:CA", []string{"192.168.1.20", "10.0.0.5"}, ":443")
	for _, want := range []string{"192.168.1.20:443", "10.0.0.5:443"} {
		if !strings.Contains(text, want) {
			t.Errorf("credentials block is missing %q:\n%s", want, text)
		}
	}
}

// TestRenderPairCredentials_PrintsThePairingStringOnceWhenGenerated is the
// credential-rule regression: the full ao-pair:// string must appear
// exactly once, on its own line, prefixed "Paste this in Hosted AO:", on the
// run that generated the passcode it was built from.
func TestRenderPairCredentials_PrintsThePairingStringOnceWhenGenerated(t *testing.T) {
	pairingString := "ao-pair://v1/192.168.1.20:443#" + strings.Repeat("0", 64) + ":AB12CD34"
	text := renderPairCredentials("AB12CD34", true, pairingString, "07:CA", []string{"192.168.1.20"}, ":443")
	want := "Paste this in Hosted AO: " + pairingString
	if n := strings.Count(text, want); n != 1 {
		t.Errorf("pairing-string line count = %d, want exactly 1:\n%s", n, text)
	}
	assertNoDashes(t, text)
}

// TestRenderPairCredentials_NoPairingStringWhenNotGenerated confirms a
// re-run (nothing generated, so pairingString is empty) never prints the
// "Paste this in Hosted AO:" line at all: there is no plaintext passcode to
// build one from.
func TestRenderPairCredentials_NoPairingStringWhenNotGenerated(t *testing.T) {
	text := renderPairCredentials("", false, "", "07:CA", []string{"192.168.1.20"}, ":443")
	if strings.Contains(text, "Paste this in Hosted AO") {
		t.Errorf("must not print a pairing string on a re-run:\n%s", text)
	}
}

func TestRenderManualPathPair_NamesEveryStep(t *testing.T) {
	text := renderManualPathPair(setupPlatform{GOOS: "darwin"})
	for _, want := range []string{
		"nothing was changed",
		"ao daemon",
		"AO_VM_PAIR=on",
		"ao vm serve",
		"ao vm setup-harness claude",
		"gh auth login",
		"AO_DATA_DIR",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("pair manual path is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "--domain") {
		t.Errorf("pair manual path must not mention --domain:\n%s", text)
	}
	assertNoDashes(t, text)
}

func TestRenderPasscodeRotated(t *testing.T) {
	pairingString := "ao-pair://v1/192.168.1.20:443#" + strings.Repeat("0", 64) + ":XY98ZW76"
	text := renderPasscodeRotated("XY98ZW76", pairingString)
	for _, want := range []string{"XY98ZW76", "Every device connected with the old passcode has been dropped", "fingerprint to re-check", "Paste this in Hosted AO: " + pairingString} {
		if !strings.Contains(text, want) {
			t.Errorf("rotation output is missing %q:\n%s", want, text)
		}
	}
	assertNoDashes(t, text)
}

// TestRenderPasscodeRotated_ExplainsWhenNoPairingStringCouldBeBuilt is
// rotate-passcode's half of the same "no usable address" fallback: an
// empty pairingString must produce an explicit line, not a silent gap in
// the output.
func TestRenderPasscodeRotated_ExplainsWhenNoPairingStringCouldBeBuilt(t *testing.T) {
	text := renderPasscodeRotated("XY98ZW76", "")
	if strings.Contains(text, "Paste this in Hosted AO") {
		t.Errorf("must not claim to have a pairing string when none was built:\n%s", text)
	}
	if !strings.Contains(text, "No pairing string could be built") {
		t.Errorf("must explicitly say no pairing string could be built, not omit it silently:\n%s", text)
	}
	assertNoDashes(t, text)
}

func TestSetupPackagesExcludeHarnesses(t *testing.T) {
	for _, unwanted := range []string{"claude", "codex", "caddy", "nginx"} {
		for _, pkg := range setupVMPackages {
			if pkg == unwanted {
				t.Errorf("setup-vm must not install %q: harnesses have interactive logins and Caddy stays on the control plane", unwanted)
			}
		}
	}
	for _, want := range []string{"tmux", "git", "gh"} {
		found := false
		for _, pkg := range setupVMPackages {
			if pkg == want {
				found = true
			}
		}
		if !found {
			t.Errorf("setupVMPackages is missing %q", want)
		}
	}
}

func TestDpkgInstalled(t *testing.T) {
	if !dpkgInstalled("install ok installed") {
		t.Error("a fully installed package must be recognized")
	}
	for _, status := range []string{"deinstall ok config-files", "unknown ok not-installed", ""} {
		if dpkgInstalled(status) {
			t.Errorf("dpkgInstalled(%q) = true, want false", status)
		}
	}
}

// TestSetupNeedsGitHubCLIRefresh is the defect-6 regression: once gh is
// installed, "gh is missing" alone never asks this question again, which is
// what let a rotated GitHub CLI signing key stay stale forever. A source list
// already on disk has to force the refresh even when nothing is missing.
func TestSetupNeedsGitHubCLIRefresh(t *testing.T) {
	for _, tc := range []struct {
		name             string
		missing          []string
		sourceListExists bool
		want             bool
	}{
		{name: "gh missing, no prior source list", missing: []string{"gh"}, sourceListExists: false, want: true},
		{name: "gh missing, source list already there", missing: []string{"gh"}, sourceListExists: true, want: true},
		{name: "gh already installed, source list from an earlier run", missing: []string{"tmux"}, sourceListExists: true, want: true},
		{name: "gh already installed, nothing else missing, no source list", missing: nil, sourceListExists: false, want: false},
		{name: "only unrelated packages missing, no source list", missing: []string{"tmux", "git"}, sourceListExists: false, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := setupNeedsGitHubCLIRefresh(tc.missing, tc.sourceListExists); got != tc.want {
				t.Errorf("setupNeedsGitHubCLIRefresh(%v, %t) = %t, want %t", tc.missing, tc.sourceListExists, got, tc.want)
			}
		})
	}
}

func TestGitHubCLISourceList(t *testing.T) {
	line := githubCLISourceList("arm64")
	want := "deb [arch=arm64 signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main\n"
	if line != want {
		t.Fatalf("githubCLISourceList(arm64) = %q, want %q", line, want)
	}
}

func TestPortList(t *testing.T) {
	for _, tc := range []struct {
		in   []int
		want string
	}{
		{in: nil, want: "no ports"},
		{in: []int{80}, want: "80"},
		{in: []int{80, 443}, want: "80 and 443"},
		{in: []int{80, 443, 8080}, want: "80, 443 and 8080"},
	} {
		if got := portList(tc.in); got != tc.want {
			t.Errorf("portList(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func assertProblem(t *testing.T, problems []setupProblem, check, wantText string) setupProblem {
	t.Helper()
	for _, problem := range problems {
		if problem.Check != check {
			continue
		}
		haystack := problem.Detail + "\n" + strings.Join(problem.Remediation, "\n")
		if !strings.Contains(haystack, wantText) {
			t.Fatalf("problem %q does not mention %q:\n%s", check, wantText, haystack)
		}
		if len(problem.Remediation) == 0 {
			t.Fatalf("problem %q has no remediation: every failure must say exactly what to do", check)
		}
		return problem
	}
	t.Fatalf("no %q problem in %+v", check, problems)
	return setupProblem{}
}

// assertNoDashes enforces the repo's writing rule on user-facing text, where it
// is easiest to break by accident.
func assertNoDashes(t *testing.T, text string) {
	t.Helper()
	for _, dash := range []string{"\u2014", "\u2013"} {
		if strings.Contains(text, dash) {
			t.Errorf("user-facing text contains %s:\n%s", fmt.Sprintf("%q", dash), text)
		}
	}
}
