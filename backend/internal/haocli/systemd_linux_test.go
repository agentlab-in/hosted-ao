//go:build linux

package haocli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderedPrivilegedGatewayPassesSystemdVerify(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze is unavailable")
	}
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state")
	for _, dir := range []string{filepath.Join(stateRoot, "bin"), filepath.Join(stateRoot, "data")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "bin", "ao"), []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	desired := setupDesired{StateRoot: stateRoot, PairPort: 443}
	user := UserObservation{Name: "hao-test", UID: 65534, Home: root}
	daemonPath := filepath.Join(root, "ao-daemon.service")
	gatewayPath := filepath.Join(root, "ao-gateway.service")
	if err := os.WriteFile(daemonPath, []byte(renderSystemdDefinition("service.daemon", desired, user)), 0o600); err != nil {
		t.Fatal(err)
	}
	gateway := renderSystemdDefinition("service.gateway", desired, user)
	if err := os.WriteFile(gatewayPath, []byte(gateway), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("systemd-analyze", "verify", daemonPath, gatewayPath).CombinedOutput()
	if err != nil {
		t.Fatalf("systemd rejected rendered units: %v\n%s", err, output)
	}
	for _, directive := range []string{"AmbientCapabilities=CAP_NET_BIND_SERVICE", "CapabilityBoundingSet=CAP_NET_BIND_SERVICE", "User=hao-test", "AO_VM_HTTPS_ADDR=:443"} {
		if !strings.Contains(gateway, directive) {
			t.Fatalf("verified gateway lacks effective privileged-port directive %q", directive)
		}
	}
}
