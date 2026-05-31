//go:build !windows

package cli

import (
	"errors"
	"os"
	"syscall"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func signalTerm(pid int) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}
