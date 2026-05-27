package zellij

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	runtimeName       = "zellij"
	agentPaneName     = "agent"
	defaultChunkBytes = 16 * 1024
)

func versionArgs() []string {
	return []string{"--version"}
}

func createSessionArgs(id, layoutPath string) []string {
	return []string{
		"attach", "--create-background", id,
		"options",
		"--default-layout", layoutPath,
		"--pane-frames", "false",
		"--session-serialization", "false",
		"--show-startup-tips", "false",
		"--show-release-notes", "false",
	}
}

func listPanesArgs(id string) []string {
	return []string{"--session", id, "action", "list-panes", "--all", "--json"}
}

func pasteArgs(id, paneID, chunk string) []string {
	return []string{"--session", id, "action", "paste", "--pane-id", paneID, chunk}
}

func sendEnterArgs(id, paneID string) []string {
	return []string{"--session", id, "action", "send-keys", "--pane-id", paneID, "Enter"}
}

func dumpScreenArgs(id, paneID string) []string {
	return []string{"--session", id, "action", "dump-screen", "--pane-id", paneID, "--full"}
}

func listSessionsArgs() []string {
	return []string{"list-sessions", "--no-formatting"}
}

func killSessionArgs(id string) []string {
	return []string{"kill-session", id}
}

func attachArgs(id string) []string {
	return []string{"attach", id}
}

func handleIDValue(sessionID, paneID string) string {
	return sessionID + "/" + paneID
}

func terminalPaneID(id int) string {
	return fmt.Sprintf("terminal_%d", id)
}

func buildLayout(cfg ports.RuntimeConfig, shellPath string) string {
	return "layout {\n" +
		"  cwd " + kdlQuote(cfg.WorkspacePath) + "\n" +
		"  pane command=" + kdlQuote(shellPath) + " name=" + kdlQuote(agentPaneName) + " {\n" +
		"    args " + kdlQuote("-lc") + " " + kdlQuote(wrapLaunchCommand(cfg, shellPath)) + "\n" +
		"  }\n" +
		"}\n"
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

func kdlQuote(s string) string {
	return strconv.Quote(s)
}
