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

func TestOpenManagedRegularRejectsWindowsReparseAncestor(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "artifact"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(root, "reparse-parent")
	if err := os.Symlink(target, ancestor); err != nil {
		t.Skipf("Windows symlink creation unavailable: %v", err)
	}
	if file, err := openManagedRegular(filepath.Join(ancestor, "artifact"), 1024); err == nil {
		_ = file.Close()
		t.Fatal("artifact through reparse ancestor unexpectedly opened")
	}
}

func TestOpenManagedRegularRejectsWindowsAncestorSwap(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "artifact"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(root, "managed-parent")
	if err := os.Symlink(outside, ancestor); err != nil {
		t.Skipf("Windows symlink creation unavailable: %v", err)
	}
	if err := os.Remove(ancestor); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.Remove(ancestor)
			_ = os.Symlink(outside, ancestor)
			_ = os.Remove(ancestor)
			if os.Mkdir(ancestor, 0o700) == nil {
				_ = os.WriteFile(filepath.Join(ancestor, "artifact"), []byte("safe"), 0o600)
				_ = os.Remove(filepath.Join(ancestor, "artifact"))
				_ = os.Remove(ancestor)
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()

	for i := 0; i < 2_000; i++ {
		file, err := openManagedRegular(filepath.Join(ancestor, "artifact"), 16)
		if err != nil {
			continue
		}
		data := make([]byte, 16)
		n, readErr := file.Read(data)
		_ = file.Close()
		if readErr != nil || string(data[:n]) != "safe" {
			t.Fatalf("managed open escaped during ancestor swap: data=%q err=%v", data[:n], readErr)
		}
	}
}
