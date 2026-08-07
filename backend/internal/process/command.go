package process

import (
	"context"
	"os/exec"
	"time"
)

// WaitDelay bounds how long a command's Wait may keep blocking on the output
// pipes after its context has already killed the direct child.
//
// It matters for every short probe run through CombinedOutput: exec is handed
// a non-*os.File writer, so it creates an os.Pipe plus a copy goroutine, and
// Wait does not return until every holder of the write end has closed it. On
// context expiry exec kills only the direct child, so a grandchild that
// inherited the pipe (a Node CLI's worker, git's transport helper) keeps Wait
// blocked for as long as it likes, and the context deadline the caller set
// buys nothing. See the same reasoning at cloneWaitDelay in
// internal/service/project/clone.go, which is where this bit AO first.
const WaitDelay = 2 * time.Second

// Command creates a non-interactive child process. On Windows it suppresses
// transient console windows for CLI tools launched by the desktop daemon.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	configureHidden(cmd)
	return cmd
}

// CommandContext is Command with cancellation support. A caller that reads the
// child's output through a pipe (CombinedOutput, Output, StdoutPipe) should set
// cmd.WaitDelay = WaitDelay, or use CombinedOutput below, so a surviving
// grandchild cannot outlast ctx.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	configureHidden(cmd)
	return cmd
}

// CombinedOutput runs name under ctx and returns its combined stdout and
// stderr, bounded by WaitDelay so the call cannot outlive ctx by more than
// that even when a grandchild is still holding the output pipe open. It is
// the form every short-lived probe should use.
func CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := CommandContext(ctx, name, args...)
	cmd.WaitDelay = WaitDelay
	return cmd.CombinedOutput()
}
