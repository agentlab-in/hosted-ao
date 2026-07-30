package cli

// Every decision `ao setup-vm` makes is tested here, with no VM, no apt, and no
// systemd: the platform gate, the preflight verdicts, the path plan, both unit
// files, and the closing summary. This file has to pass on macOS and Windows,
// where CLI E2E also runs, which is exactly why the decisions live in pure
// functions and not inside the privileged shell-out layer.

import (
	"errors"
	"fmt"
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
		{
			name: "debian is refused",
			platform: setupPlatform{
				GOOS: "linux", HasSystemctl: true, HasAptGet: true,
				OSRelease: parseOSRelease("ID=debian\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\n"),
			},
			wantErr: true,
		},
		{name: "macOS is refused", platform: setupPlatform{GOOS: "darwin"}, wantErr: true},
		{name: "windows is refused", platform: setupPlatform{GOOS: "windows"}, wantErr: true},
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
		if !strings.HasPrefix(got, "/home/ubuntu/.ao") {
			t.Errorf("%s = %q, want it under /home/ubuntu/.ao: all AO state lives there only", name, got)
		}
	}
	if plan.DataDir != "/home/ubuntu/.ao/data" {
		t.Errorf("DataDir = %q", plan.DataDir)
	}
	if plan.RunFile != "/home/ubuntu/.ao/running.json" {
		t.Errorf("RunFile = %q", plan.RunFile)
	}
	if plan.CertDir != "/home/ubuntu/.ao/data/vm-gateway/certs" {
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
		DataDir: "/srv/ao/data", RunFile: "/srv/ao/running.json", MachineFile: "/srv/ao/machine.json",
	})
	if err != nil {
		t.Fatalf("buildSetupPlan err = %v", err)
	}
	if plan.DataDir != "/srv/ao/data" || plan.RunFile != "/srv/ao/running.json" || plan.MachineFile != "/srv/ao/machine.json" {
		t.Fatalf("absolute overrides were not honored: %+v", plan)
	}
	if plan.CertDir != "/srv/ao/data/vm-gateway/certs" {
		t.Errorf("CertDir = %q, want it to follow the overridden data dir", plan.CertDir)
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
	for _, want := range []string{"/home/ubuntu/.ao", "/home/ubuntu/.ao/data", "/home/ubuntu/.ao/data/vm-gateway/certs"} {
		if !seen[want] {
			t.Errorf("setupDirs is missing %q: %v", want, dirs)
		}
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
		`WorkingDirectory="/home/ubuntu/.ao/data"`,
		`Environment="AO_DATA_DIR=/home/ubuntu/.ao/data"`,
		`Environment="AO_RUN_FILE=/home/ubuntu/.ao/running.json"`,
		`Environment="HOME=/home/ubuntu"`,
		"ExecStart=/usr/local/bin/ao daemon",
		"Restart=on-failure",
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
		`WorkingDirectory="/home/ubuntu/.ao/data"`,
		`Environment="AO_DATA_DIR=/home/ubuntu/.ao/data"`,
		`Environment="AO_MACHINE_FILE=/home/ubuntu/.ao/machine.json"`,
		`Environment="AO_VM_DOMAIN=vm.example.com"`,
		`Environment="AO_VM_CERT_DIR=/home/ubuntu/.ao/data/vm-gateway/certs"`,
		"ExecStart=/usr/local/bin/ao vm serve",
		"AmbientCapabilities=CAP_NET_BIND_SERVICE",
		"CapabilityBoundingSet=CAP_NET_BIND_SERVICE",
		// The gateway fronts the daemon, so it starts after it.
		"After=network-online.target ao-daemon.service",
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
				if !strings.HasPrefix(value, `"/`) {
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
	summary := renderSetupSummary(plan, false, []string{"80 and 443 were not verified from outside"})
	for _, want := range []string{
		"installed, but not yet ready",
		"not bound to an AO account",
		"sudo systemctl start ao-gateway.service",
		"ao vm setup-harness claude",
		"gh auth login",
		"ao doctor",
		"/home/ubuntu/.ao/machine.json",
		"80 and 443 were not verified from outside",
		"enabled, not started",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary is missing %q:\n%s", want, summary)
		}
	}
	assertNoDashes(t, summary)
}

func TestRenderSetupSummary_Bound(t *testing.T) {
	plan := testSetupPlan(t)
	plan.Bound = true
	summary := renderSetupSummary(plan, true, nil)
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
