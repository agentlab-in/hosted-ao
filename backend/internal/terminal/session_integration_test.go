package terminal

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/tmux"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestSessionStreamsRealTmuxPane attaches a real PTY to a real tmux session and
// asserts output streams back, then that killing the pane stops the session
// without a re-attach storm. Skipped when tmux is unavailable.
func TestSessionStreamsRealTmuxPane(t *testing.T) {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}

	name := "ao-term-it-" + strings.ReplaceAll(t.Name(), "/", "-")
	mustRun(t, tmuxBin, "new-session", "-d", "-s", name, "/bin/sh")
	t.Cleanup(func() { _ = exec.Command(tmuxBin, "kill-session", "-t", "="+name).Run() })

	rt := tmux.New(tmux.Options{Binary: tmuxBin})
	handle := ports.RuntimeHandle{ID: name}

	s := newSession(name, handle, rt, defaultSpawn, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.run(ctx)

	var got safeBytes
	s.subscribe(got.add, nil)

	// Type a unique marker and expect it echoed back through the PTY.
	eventually(t, 3*time.Second, func() bool { return s.write([]byte("echo AO_MARKER_42\n")) == nil })
	eventually(t, 5*time.Second, func() bool { return strings.Contains(got.string(), "AO_MARKER_42") })

	// Kill the pane: the session must observe it as gone and not re-attach.
	mustRun(t, tmuxBin, "kill-session", "-t", "="+name)
	eventually(t, 5*time.Second, func() bool { return s.isExited() })
}

func mustRun(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}
