package cli

// whoami.go reports the binding this machine currently has: the machine id an
// access token's audience must match, the one account allowed to reach it, and
// where that came from. It only ever reads the file. `ao setup-vm` owns writing
// it, on the machine itself.

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/vmgateway"
)

func newWhoamiCommand(ctx *commandContext) *cobra.Command {
	var machineFile string
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show which AO account this machine is bound to",
		Long: "ao whoami reads ~/.ao/hosted/machine.json and reports the binding `ao vm serve` is\n" +
			"running with: the machine id an access token's audience has to match, the one\n" +
			"account allowed to reach this machine, and the public URL it was registered\n" +
			"under.\n\n" +
			"It reads that file and nothing else, so it answers the same on a machine whose\n" +
			"gateway is stopped. Binding is done by `ao setup-vm`, on the machine itself.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runWhoami(cmd, machineFile)
		},
	}
	cmd.Flags().StringVar(&machineFile, "machine-file", "", "machine.json path (default: AO_MACHINE_FILE, else ~/.ao/hosted/machine.json)")
	return cmd
}

func (c *commandContext) runWhoami(cmd *cobra.Command, machineFile string) error {
	path, err := resolveMachineFilePath(machineFile)
	if err != nil {
		return err
	}
	// The gateway's own reader, so a file this command calls readable is one
	// the gateway can also start from.
	mf, err := vmgateway.ReadMachineFile(path)
	if err != nil {
		return err
	}
	return writeSetupText(cmd.OutOrStdout(), renderWhoami(mf, path))
}

// resolveMachineFilePath mirrors vmgateway.Resolve exactly: the flag wins, then
// AO_MACHINE_FILE, then the gateway's own default. The default comes from
// vmgateway.DefaultMachineFilePath rather than being spelled out a second time,
// because the two have to name the same file or this command would confidently
// report a binding the gateway never sees. That function documents why the
// default is the AO home directory and not the AO_DATA_DIR-resolved data dir.
func resolveMachineFilePath(override string) (string, error) {
	for _, candidate := range []string{override, os.Getenv("AO_MACHINE_FILE")} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate, nil
		}
	}
	path, err := vmgateway.DefaultMachineFilePath()
	if err != nil {
		return "", fmt.Errorf("resolve the machine file path: %w", err)
	}
	return path, nil
}
