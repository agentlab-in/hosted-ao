//go:build !windows

package haocli

import (
	"errors"
	"os"
	"syscall"
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
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errConfigBusy
		}
		return nil, err
	}
	return file, nil
}

func releaseConfigLock(file *os.File) error {
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
