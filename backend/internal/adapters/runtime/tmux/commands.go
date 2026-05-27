package tmux

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const runtimeName = "tmux"

func newSessionArgs(id, workspacePath, shellPath, script string) []string {
	return []string{"new-session", "-d", "-s", id, "-c", workspacePath, shellPath, "-lc", script}
}

func setStatusOffArgs(id string) []string {
	return []string{"set-option", "-t", id, "status", "off"}
}

func hasSessionArgs(id string) []string {
	return []string{"has-session", "-t", id}
}

func killSessionArgs(id string) []string {
	return []string{"kill-session", "-t", id}
}

func capturePaneArgs(id string, lines int) []string {
	return []string{"capture-pane", "-p", "-t", id, "-S", fmt.Sprintf("-%d", lines)}
}

func sendLiteralArgs(id, message string) []string {
	return []string{"send-keys", "-t", id, "-l", message}
}

func sendEnterArgs(id string) []string {
	return []string{"send-keys", "-t", id, "C-m"}
}

func loadBufferArgs(bufferName, path string) []string {
	return []string{"load-buffer", "-b", bufferName, path}
}

func pasteBufferArgs(id, bufferName string) []string {
	return []string{"paste-buffer", "-d", "-t", id, "-b", bufferName}
}

func wrapLaunchCommand(cfg ports.RuntimeConfig, shellPath string) string {
	path := cfg.Env["PATH"]
	if path == "" {
		path = getenv("PATH")
	}

	var b strings.Builder
	for _, key := range sortedKeys(cfg.Env) {
		if key == "PATH" {
			continue
		}
		b.WriteString("export ")
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(shellQuote(cfg.Env[key]))
		b.WriteString("; ")
	}
	if path != "" {
		b.WriteString("export PATH=")
		b.WriteString(shellQuote(path))
		b.WriteString("; ")
	}
	b.WriteString(cfg.LaunchCommand)
	b.WriteString("; exec ")
	b.WriteString(shellQuote(shellPath))
	b.WriteString(" -i")
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
