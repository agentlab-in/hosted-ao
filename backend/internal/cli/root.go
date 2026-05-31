// Package cli implements the user-facing ao command. It stays thin: commands
// discover the local daemon, call its loopback HTTP API, and format output.
package cli

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/daemon"
)

// Execute runs the ao CLI with process stdio.
func Execute() error {
	return NewRootCommand(DefaultDeps()).Execute()
}

// Deps holds the small set of side effects the CLI needs. Tests replace these
// functions without reaching into process-global state.
type Deps struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer

	HTTPClient   *http.Client
	Executable   func() (string, error)
	StartProcess func(processStartConfig) (processHandle, error)
	SignalTerm   func(pid int) error
	ProcessAlive func(pid int) bool
	LookPath     func(file string) (string, error)
	Now          func() time.Time
	Sleep        func(time.Duration)
}

// DefaultDeps returns production dependencies.
func DefaultDeps() Deps {
	return Deps{
		In:           os.Stdin,
		Out:          os.Stdout,
		Err:          os.Stderr,
		HTTPClient:   &http.Client{Timeout: 2 * time.Second},
		Executable:   os.Executable,
		StartProcess: startProcess,
		SignalTerm:   signalTerm,
		ProcessAlive: processAlive,
		LookPath:     exec.LookPath,
		Now:          time.Now,
		Sleep:        time.Sleep,
	}
}

func (d Deps) withDefaults() Deps {
	def := DefaultDeps()
	if d.In == nil {
		d.In = def.In
	}
	if d.Out == nil {
		d.Out = def.Out
	}
	if d.Err == nil {
		d.Err = def.Err
	}
	if d.HTTPClient == nil {
		d.HTTPClient = def.HTTPClient
	}
	if d.Executable == nil {
		d.Executable = def.Executable
	}
	if d.StartProcess == nil {
		d.StartProcess = def.StartProcess
	}
	if d.SignalTerm == nil {
		d.SignalTerm = def.SignalTerm
	}
	if d.ProcessAlive == nil {
		d.ProcessAlive = def.ProcessAlive
	}
	if d.LookPath == nil {
		d.LookPath = def.LookPath
	}
	if d.Now == nil {
		d.Now = def.Now
	}
	if d.Sleep == nil {
		d.Sleep = def.Sleep
	}
	return d
}

// NewRootCommand builds a testable root command.
func NewRootCommand(deps Deps) *cobra.Command {
	deps = deps.withDefaults()
	ctx := &commandContext{deps: deps}

	root := &cobra.Command{
		Use:           "ao",
		Short:         "Agent Orchestrator",
		Long:          "Agent Orchestrator manages the local daemon that supervises parallel coding-agent sessions.",
		Version:       VersionString(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(deps.In)
	root.SetOut(deps.Out)
	root.SetErr(deps.Err)
	root.CompletionOptions.DisableDefaultCmd = true

	root.AddCommand(newDaemonCommand())
	root.AddCommand(newStartCommand(ctx))
	root.AddCommand(newStopCommand(ctx))
	root.AddCommand(newStatusCommand(ctx))
	root.AddCommand(newDoctorCommand(ctx))
	root.AddCommand(newCompletionCommand())
	root.AddCommand(newVersionCommand())

	return root
}

type commandContext struct {
	deps Deps
}

func newDaemonCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Short:  "Run the AO backend daemon",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.Run()
		},
	}
}
