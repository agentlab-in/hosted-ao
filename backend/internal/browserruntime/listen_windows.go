//go:build windows

package browserruntime

import (
	"net"
	"path/filepath"
	"regexp"

	"github.com/Microsoft/go-winio"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

var unsafePipeChars = regexp.MustCompile(`[^a-zA-Z0-9\-]`)

// basePipeName carries the same "-hosted" collision guard as
// supervisor.basePipeName: Windows named pipes are a single global namespace,
// so this build's browser-bridge pipe must not collide with the upstream
// agent-orchestrator desktop app's \\.\pipe\ao-browser.
const basePipeName = `\\.\pipe\ao-browser-hosted`

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

// Listen creates the local daemon-to-Electron browser bridge listener.
func Listen(runFilePath string) (net.Listener, string, error) {
	name := pipeNameFromRunFile(runFilePath)
	ln, err := winio.ListenPipe(name, &winio.PipeConfig{
		// Protected DACL: the creating owner and LocalSystem only. This prevents
		// another local account from connecting to or squatting the runtime pipe.
		SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;OW)",
	})
	if err != nil {
		return nil, "", err
	}
	return ln, name, nil
}
