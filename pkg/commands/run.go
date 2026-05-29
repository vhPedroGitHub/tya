package commands

import (
	"github.com/vhPedroGitHub/tya/pkg/cli_functions"
	"github.com/vhPedroGitHub/tya/pkg/models"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// NewRunCmd returns the cobra command for `tya run`.
func NewRunCmd(log *zap.Logger) *cobra.Command {
	opts := &models.RunOptions{}

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute flows against a live API",
		Long: `Reads config-run.yml and executes the defined flows.

Flows are started in dependency order: a flow listed in another flow's
depends_on will not start until all of its dependencies have completed.

Examples:
  tya run                    # Execute all flows in dependency order
  tya run -t                 # Test mode: single pass, ignores RPS
  tya run --flow login-flow  # Execute a specific named flow`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli_functions.RunFlows(log, opts)
		},
	}

	cmd.Flags().StringVar(&opts.ConfigFile, "config", "config-run.yml", "path to config-run.yml")
	cmd.Flags().BoolVarP(&opts.TestMode, "test", "t", false, "test mode: single pass, ignores RPS")
	cmd.Flags().StringVar(&opts.Flow, "flow", "", "run only this named flow")

	return cmd
}
