//go:build !windows

package haocli

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenManagedRegularRejectsConcurrentLinkSwap(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(root, "managed")
	safe := filepath.Join(root, "safe")
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(safe, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(safe, managed); err != nil {
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
			_ = os.Remove(managed)
			_ = os.Symlink(outside, managed)
			_ = os.Remove(managed)
			_ = os.Link(safe, managed)
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()

	for i := 0; i < 2_000; i++ {
		file, openErr := openManagedRegular(managed, 16)
		if openErr != nil {
			continue
		}
		data, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) != "safe" {
			t.Fatalf("managed open escaped during link swap: %q", data)
		}
	}
}
