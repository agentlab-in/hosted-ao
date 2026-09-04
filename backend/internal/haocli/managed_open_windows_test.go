//go:build windows

package haocli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenManagedRegularRejectsWindowsReparsePoint(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "reparse")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("Windows symlink creation unavailable: %v", err)
	}
	if file, err := openManagedRegular(link, 1024); err == nil {
		_ = file.Close()
		t.Fatal("reparse point unexpectedly opened")
	}
}
