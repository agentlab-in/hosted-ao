// Package haocli implements the machine-management-only hao command.
package haocli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/haocontract"
)

// Build metadata. Release tooling overrides these with -ldflags.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Deps is the testable side-effect boundary for the read-only CLI.
type Deps struct {
	In       io.Reader
	Out      io.Writer
	Err      io.Writer
	ReadFile func(string) ([]byte, error)
	StateDir func() (string, error)
}

func DefaultDeps() Deps {
	return Deps{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, ReadFile: os.ReadFile, StateDir: stateDir}
}

func (d Deps) withDefaults() Deps {
	defaults := DefaultDeps()
	if d.In == nil {
		d.In = defaults.In
	}
	if d.Out == nil {
		d.Out = defaults.Out
	}
	if d.Err == nil {
		d.Err = defaults.Err
	}
	if d.ReadFile == nil {
		d.ReadFile = defaults.ReadFile
	}
	if d.StateDir == nil {
		d.StateDir = defaults.StateDir
	}
	return d
}

// Execute runs hao using process arguments and stdio.
func Execute() int { return ExecuteArgs(DefaultDeps(), os.Args[1:]) }

// ExecuteArgs runs a testable hao invocation and returns its stable exit code.
func ExecuteArgs(deps Deps, args []string) int {
	deps = deps.withDefaults()
	root := NewRootCommand(deps)
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return 0
	}
	cliErr := classifyError(err, rootJSON(root), operationFromArgs(args))
	if emitErr := emitError(deps.Err, cliErr, rootJSON(root)); emitErr != nil {
		fmt.Fprintln(deps.Err, "hao: could not render error")
	}
	return cliErr.ExitStatus
}

type options struct {
	json       bool
	configPath string
}

// NewRootCommand builds the isolated hao command tree.
func NewRootCommand(deps Deps) *cobra.Command {
	deps = deps.withDefaults()
	opts := &options{}
	root := &cobra.Command{
		Use:           "hao",
		Short:         "Manage Hosted AO machines",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(deps.In)
	root.SetOut(deps.Out)
	root.SetErr(deps.Err)
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().BoolVar(&opts.json, "json", false, "emit stable JSON output")
	root.PersistentFlags().StringVar(&opts.configPath, "config", "", "configuration file path")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return commandError{Code: "invalid_usage", Message: err.Error(), Remediation: "run hao --help", ExitStatus: 2}
	})
	root.AddCommand(newVersionCommand(opts))
	root.AddCommand(newConfigCommand(deps, opts))
	return root
}

func rootJSON(root *cobra.Command) bool {
	flag := root.PersistentFlags().Lookup("json")
	return flag != nil && flag.Value.String() == "true"
}

func versionString() string {
	parts := []string{Version}
	if Commit != "" {
		parts = append(parts, "commit "+Commit)
	}
	if Date != "" {
		parts = append(parts, "built "+Date)
	}
	return strings.Join(parts, " ")
}

func newVersionCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use: "version", Short: "Print version information", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), map[string]string{"version": Version, "commit": Commit, "date": Date})
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), versionString())
			return err
		},
	}
}

func newConfigCommand(deps Deps, opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect hao configuration", Args: noArgs}
	cmd.AddCommand(newConfigPathCommand(deps, opts))
	cmd.AddCommand(newConfigShowCommand(deps, opts))
	cmd.AddCommand(newConfigValidateCommand(deps, opts))
	return cmd
}

func newConfigPathCommand(deps Deps, opts *options) *cobra.Command {
	return &cobra.Command{
		Use: "path", Short: "Print the configuration path", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolveConfigPath(deps, opts.configPath)
			if err != nil {
				return operationalError("resolve configuration path", err)
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), map[string]string{"path": path})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		},
	}
}

func newConfigShowCommand(deps Deps, opts *options) *cobra.Command {
	return &cobra.Command{
		Use: "show", Short: "Show validated configuration", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, object, err := loadConfig(deps, opts.configPath)
			if err != nil {
				return err
			}
			redacted := haocontract.Redact(object)
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"path": path, "config": redacted})
			}
			data, err := yaml.Marshal(redacted)
			if err != nil {
				return operationalError("render configuration", err)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
}

func newConfigValidateCommand(deps Deps, opts *options) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use: "validate", Short: "Validate configuration", Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected := opts.configPath
			if file != "" {
				selected = file
			}
			path, _, err := loadConfig(deps, selected)
			if err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"valid": true, "path": path, "version": 1})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "valid: %s\n", path)
			return err
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "configuration file to validate")
	return cmd
}

func noArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	return commandError{Code: "invalid_usage", Message: "unexpected arguments", Remediation: "run hao --help", ExitStatus: 2}
}

func resolveConfigPath(deps Deps, explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		path, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		return path, nil
	}
	root, err := deps.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "hao", "config.yaml"), nil
}

func stateDir() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", err
	}
	return filepath.Dir(cfg.RunFilePath), nil
}

func loadConfig(deps Deps, explicit string) (string, map[string]any, error) {
	path, err := resolveConfigPath(deps, explicit)
	if err != nil {
		return "", nil, operationalError("resolve configuration path", err)
	}
	data, err := deps.ReadFile(path)
	if err != nil {
		return "", nil, commandError{Code: "operation_failed", Message: "could not read hao configuration", Remediation: "create the file or pass --config with a readable path", Details: map[string]any{"path": path}, ExitStatus: 1, Cause: err}
	}
	object, err := haocontract.ParseConfig(data)
	if err != nil {
		var unsupported haocontract.UnsupportedVersionError
		if errors.As(err, &unsupported) {
			return "", nil, commandError{Code: "unsupported_config_version", Message: "hao configuration version is unsupported", Remediation: "use a version 1 configuration", Details: map[string]any{"path": path}, ExitStatus: 2, Cause: err}
		}
		return "", nil, commandError{Code: "invalid_config", Message: "hao configuration is invalid", Remediation: "fix the file according to contracts/hao/v1/config.schema.json", Details: map[string]any{"path": path, "diagnostic": safeDiagnostic(err)}, ExitStatus: 2, Cause: err}
	}
	return path, object, nil
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
