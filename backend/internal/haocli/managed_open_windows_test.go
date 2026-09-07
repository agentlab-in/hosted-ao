//go:build windows

package haocli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
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
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			t.Fatalf("managed open failed during ancestor swap: data=%q err=%v", data[:n], readErr)
		}
		if string(data[:n]) == "outside" {
			t.Fatalf("managed open escaped during ancestor swap: data=%q err=%v", data[:n], readErr)
		}
	}
}

func TestManagedWindowsMutationsUseStableParentHandles(t *testing.T) {
	t.Run("create file", func(t *testing.T) {
		parent, name, moved, outside := openManagedParentThenSwap(t, "artifact")
		file, err := managedCreateExclusiveAt(parent, name, filepath.Join(moved, name), 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("managed")); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		assertWindowsMutationStayedInMovedParent(t, moved, outside, name)
	})

	t.Run("create directory", func(t *testing.T) {
		parent, name, moved, outside := openManagedParentThenSwap(t, "data")
		if err := managedMkdirAt(parent, name, filepath.Join(moved, name), 0o700); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(moved, name))
		if err != nil || !info.IsDir() {
			t.Fatalf("directory was not created in held parent: info=%v err=%v", info, err)
		}
		if _, err := os.Stat(filepath.Join(outside, name)); !os.IsNotExist(err) {
			t.Fatalf("directory escaped through replacement junction: %v", err)
		}
	})

	t.Run("chmod", func(t *testing.T) {
		parent, name, moved, outside := openManagedParentThenSwap(t, "artifact")
		if err := os.WriteFile(filepath.Join(moved, name), []byte("managed"), 0o600); err != nil {
			t.Fatal(err)
		}
		outsidePath := filepath.Join(outside, name)
		if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := managedChmodAt(parent, name, filepath.Join(moved, name), 0o400); err != nil {
			t.Fatal(err)
		}
		if attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(filepath.Join(moved, name))); err != nil || attributes&windows.FILE_ATTRIBUTE_READONLY == 0 {
			t.Fatalf("held-parent file was not made read-only: attributes=%#x err=%v", attributes, err)
		}
		if attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(outsidePath)); err != nil || attributes&windows.FILE_ATTRIBUTE_READONLY != 0 {
			t.Fatalf("replacement-junction file was modified: attributes=%#x err=%v", attributes, err)
		}
	})

	t.Run("remove", func(t *testing.T) {
		parent, name, moved, outside := openManagedParentThenSwap(t, "artifact")
		if err := os.WriteFile(filepath.Join(moved, name), []byte("managed"), 0o600); err != nil {
			t.Fatal(err)
		}
		outsidePath := filepath.Join(outside, name)
		if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := managedRemoveAt(parent, name); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(moved, name)); !os.IsNotExist(err) {
			t.Fatalf("held-parent file still exists after removal: %v", err)
		}
		if data, err := os.ReadFile(outsidePath); err != nil || string(data) != "outside" {
			t.Fatalf("replacement-junction file was removed or changed: data=%q err=%v", data, err)
		}
	})

	t.Run("rename", func(t *testing.T) {
		root := t.TempDir()
		managed := filepath.Join(root, "managed")
		moved := filepath.Join(root, "moved")
		outside := filepath.Join(root, "outside")
		if err := os.Mkdir(managed, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(managed, "source"), []byte("managed"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "source"), []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		sourceParent, sourceName, err := openManagedParent(filepath.Join(managed, "source"))
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(sourceParent)
		destinationParent, destinationName, err := openManagedParent(filepath.Join(managed, "destination"))
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(destinationParent)
		if err := os.Rename(managed, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, managed); err != nil {
			t.Skipf("Windows symlink creation unavailable: %v", err)
		}
		source, err := openManagedRelative(sourceParent, sourceName, windows.DELETE|windows.SYNCHRONIZE, windows.FILE_OPEN, windows.FILE_SYNCHRONOUS_IO_NONALERT)
		if err != nil {
			t.Fatal(err)
		}
		if err := renameManagedWindowsHandle(source, destinationParent, destinationName); err != nil {
			_ = windows.CloseHandle(source)
			t.Fatal(err)
		}
		if err := windows.CloseHandle(source); err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(filepath.Join(moved, "destination")); err != nil || string(data) != "managed" {
			t.Fatalf("held-parent file was not renamed: data=%q err=%v", data, err)
		}
		if data, err := os.ReadFile(filepath.Join(outside, "source")); err != nil || string(data) != "outside" {
			t.Fatalf("replacement-junction source changed: data=%q err=%v", data, err)
		}
		if _, err := os.Stat(filepath.Join(outside, "destination")); !os.IsNotExist(err) {
			t.Fatalf("rename escaped through replacement junction: %v", err)
		}
	})
}

func openManagedParentThenSwap(t *testing.T, name string) (windows.Handle, string, string, string) {
	t.Helper()
	root := t.TempDir()
	managed := filepath.Join(root, "managed")
	moved := filepath.Join(root, "moved")
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(managed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, component, err := openManagedParent(filepath.Join(managed, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(parent) })
	if err := os.Rename(managed, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, managed); err != nil {
		t.Skipf("Windows symlink creation unavailable: %v", err)
	}
	return parent, component, moved, outside
}

func assertWindowsMutationStayedInMovedParent(t *testing.T, moved, outside, name string) {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(moved, name)); err != nil || string(data) != "managed" {
		t.Fatalf("held-parent file mismatch: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(outside, name)); !os.IsNotExist(err) {
		t.Fatalf("mutation escaped through replacement junction: %v", err)
	}
}
