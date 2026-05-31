//go:build !windows

package cli

import (
	"errors"
	"syscall"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// detachSysProcAttr puts the daemon in a new session (Setsid) so it is no
// longer in the launcher's foreground process group and won't receive the
// terminal's SIGINT/SIGHUP.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
