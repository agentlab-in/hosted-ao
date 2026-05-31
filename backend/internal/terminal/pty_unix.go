//go:build !windows

package terminal

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// defaultSpawn starts argv on a real PTY via creack/pty. ctx cancellation kills
// the process. Windows uses a stub (see pty_windows.go) until a ConPTY path is
// added.
func defaultSpawn(ctx context.Context, argv []string) (ptyProcess, error) {
	if len(argv) == 0 {
		return nil, errors.New("terminal: empty attach command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	f, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	return &creackPTY{f: f, cmd: cmd}, nil
}

type creackPTY struct {
	f   *os.File
	cmd *exec.Cmd
}

func (p *creackPTY) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *creackPTY) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *creackPTY) Resize(rows, cols uint16) error {
	return pty.Setsize(p.f, &pty.Winsize{Rows: rows, Cols: cols})
}

func (p *creackPTY) Wait() error { return p.cmd.Wait() }

// Close stops the attach process and releases the PTY. tmux attach exits cleanly
// when the master closes, but kill the process to be sure it does not linger.
func (p *creackPTY) Close() error {
	closeErr := p.f.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.cmd.Wait()
	return closeErr
}
