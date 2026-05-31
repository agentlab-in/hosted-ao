//go:build windows

package cli

import "os"

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
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
