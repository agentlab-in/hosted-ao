package cli

import (
	"os"
	"os/exec"
)

type processStartConfig struct {
	Path   string
	Args   []string
	Env    []string
	Stdout *os.File
	Stderr *os.File
}

type processHandle struct {
	PID int
}

func startProcess(cfg processStartConfig) (processHandle, error) {
	cmd := exec.Command(cfg.Path, cfg.Args...)
	cmd.Env = cfg.Env
	cmd.Stdout = cfg.Stdout
	cmd.Stderr = cfg.Stderr
	// Detach the daemon into its own session/process group so a Ctrl-C in the
	// terminal where `ao start` is waiting for readiness doesn't also SIGINT the
	// freshly spawned daemon (it would otherwise share the launcher's group).
	cmd.SysProcAttr = detachSysProcAttr()
	if err := cmd.Start(); err != nil {
		return processHandle{}, err
	}
	go func() { _ = cmd.Wait() }()
	return processHandle{PID: cmd.Process.Pid}, nil
}
