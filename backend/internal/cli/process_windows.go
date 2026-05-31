//go:build windows

package cli

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return true
		}
		return false
	}
	defer windows.CloseHandle(handle)

	status, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return false
	}
	return status == uint32(windows.WAIT_TIMEOUT)
}

func signalTerm(pid int) error {
	if pid <= 0 {
		return os.ErrProcessDone
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer p.Release()
	return p.Kill()
}
