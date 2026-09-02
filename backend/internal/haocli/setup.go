package haocli

import (
	"github.com/spf13/cobra"
)

func newSetupCommand(_ Deps, _ *options) *cobra.Command {
	var dryRun bool
	var nonInteractive bool
	var install string
	var yes bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Plan machine preparation",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			if !dryRun {
				return commandError{Code: "feature_deferred", Message: "hao setup mutation is not yet supported", Remediation: "rerun with --dry-run to inspect the setup plan", ExitStatus: 2}
			}
			return commandError{Code: "operation_failed", Message: "setup planning is being implemented", Remediation: "retry with a completed Batch 4 build", ExitStatus: 1}
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "display the setup plan without changing the machine")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "never prompt for input")
	cmd.Flags().StringVar(&install, "install", "", "dependency policy: missing or none")
	cmd.Flags().BoolVar(&yes, "yes", false, "approve the displayed plan for future setup execution")
	return cmd
}
