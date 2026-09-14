//go:build windows

package haocli

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func acquireConfigLock(path string) (*os.File, error) {
	info, err := managedLstat(path)
	if errors.Is(err, os.ErrNotExist) {
		file, createErr := managedCreateExclusive(path, 0o600)
		if createErr != nil && !errors.Is(createErr, os.ErrExist) {
			return nil, createErr
		}
		if createErr == nil {
			_ = file.Close()
		}
		info, err = managedLstat(path)
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("configuration lock is not a safe regular file")
	}
	file, err := openManagedRegular(path, 1)
	if err != nil {
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped); err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errConfigBusy
		}
		return nil, err
	}
	return file, nil
}

func releaseConfigLock(file *os.File) error {
	overlapped := new(windows.Overlapped)
	unlockErr := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
