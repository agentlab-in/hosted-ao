//go:build windows

package supervisor

import (
	"net"
	"path/filepath"
	"regexp"

	"github.com/Microsoft/go-winio"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

var unsafePipeChars = regexp.MustCompile(`[^a-zA-Z0-9\-]`)

// basePipeName is the default-root pipe name, and the prefix every other
// instance's name is built from. Windows named pipes are a single global
// namespace shared by every process on the machine, unlike a run-file path
// under ~/.ao/hosted, which moves with the state root. The "-hosted" suffix
// is what keeps this build's supervisor pipe from colliding with the
// upstream agent-orchestrator desktop app's \\.\pipe\ao-supervise: that
// collision is exactly what moving the default state root to ~/.ao/hosted
// was meant to eliminate, and it would resurface here if this name did not
// move too.
const basePipeName = `\\.\pipe\ao-supervise-hosted`

// pipeNameFromRunFile derives a per-instance named-pipe path from the
// run-file's parent directory, mirroring the Unix supervise.sock placement.
// ~/.ao/hosted/running.json     → \\.\pipe\ao-supervise-hosted       (default)
// ~/.ao/hosted/dev/running.json → \\.\pipe\ao-supervise-hosted-dev   (dev isolation)
func pipeNameFromRunFile(runFilePath string) string {
	if runFilePath == "" {
		return basePipeName
	}
	dir := filepath.Base(filepath.Dir(runFilePath))
	if dir == config.StateRootSubdir || dir == "." || dir == "" {
		return basePipeName
	}
	return basePipeName + "-" + unsafePipeChars.ReplaceAllString(dir, "-")
}

// Listen creates a Windows named pipe listener for the supervisor watchdog.
// The pipe name is derived from runFilePath so dev and installed-app instances
// use separate pipes and cannot collide.
func Listen(runFilePath string) (net.Listener, string, error) {
	name := pipeNameFromRunFile(runFilePath)
	ln, err := winio.ListenPipe(name, nil)
	if err != nil {
		return nil, "", err
	}
	return ln, name, nil
}
