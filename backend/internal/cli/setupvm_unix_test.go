//go:build unix

package cli

// Root-only regression test for the live provisioning incident: it needs a
// real chown syscall and syscall.Stat_t to read the result back, neither of
// which exists on the Windows leg of the CLI E2E matrix, so it lives in its
// own unix-only file rather than setupvm_test.go.

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// TestEnsureSetupPasscode_ChownsTheIntermediateDirItCreates is the
// end-to-end regression test for the live incident: on a fresh state root,
// ensureSetupPasscode must chown not only the leaf passcode directory
// (already covered by chownSetupTree, and by
// TestEnsureSetupPasscode_ReRunDoesNotRotate) but also the intermediate
// vm-gateway directory that vmgateway.GeneratePasscode's own os.MkdirAll
// mints along the way, which is exactly what was left root-owned on the
// live box this fixes. Root-only: chown is a no-op for anything else, per
// TestChownSetupDirs_NoOpWhenNotRoot in setupvm_test.go, so this only ever
// exercises the real syscall in an environment (a root-run test container,
// for example) where it can succeed.
func TestEnsureSetupPasscode_ChownsTheIntermediateDirItCreates(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("this test asserts the real chown syscalls, which only run as root")
	}
	// A uid/gid guaranteed to exist and to differ from root's: "nobody" is
	// present on every Linux CI image, which is the only place this test
	// ever actually runs (root tests do not run on macOS/Windows CI legs).
	nobody, err := user.Lookup("nobody")
	if err != nil {
		t.Skipf("no nobody user on this box: %v", err)
	}

	c := &commandContext{deps: DefaultDeps()}
	stateRoot := t.TempDir()
	dir := filepath.Join(stateRoot, "vm-gateway", "pair-passcode")

	if _, _, err := c.ensureSetupPasscode(dir, nobody); err != nil {
		t.Fatalf("ensureSetupPasscode: %v", err)
	}

	vmGateway := filepath.Join(stateRoot, "vm-gateway")
	info, err := os.Stat(vmGateway)
	if err != nil {
		t.Fatalf("Stat(%s): %v", vmGateway, err)
	}
	uid := info.Sys().(*syscall.Stat_t).Uid
	wantUID, _ := strconv.Atoi(nobody.Uid)
	if int(uid) != wantUID {
		t.Fatalf("intermediate directory %s is owned by uid %d, want %d (%s): this is the bug where a "+
			"sudo'd provisioning run leaves a parent directory root-owned even though the leaf under it "+
			"is correctly chowned", vmGateway, uid, wantUID, nobody.Username)
	}
}
