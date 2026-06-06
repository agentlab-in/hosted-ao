// Package hookutil holds small filesystem helpers shared by the agent hook
// installers (claude-code, codex, opencode). It centralizes the atomic-write
// primitive so every adapter writes hook config the same crash-safe way.
package hookutil

import (
	"os"
	"path/filepath"
)

// AtomicWriteFile writes data to path via a temp file in the same directory
// followed by a rename, so a crash or signal mid-write can't leave a truncated
// or empty file that the agent then fails to parse (silently disabling hooks).
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ao-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
