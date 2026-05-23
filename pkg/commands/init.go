package commands

import (
	"github.com/vhPedroGitHub/tya/pkg/cli_functions"
	"github.com/vhPedroGitHub/tya/pkg/models"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// NewInitCmd returns the cobra command for `tya init`.
func NewInitCmd(log *zap.Logger) *cobra.Command {
	opts := &models.InitOptions{}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialise a new TYA project",
		Long: `Scaffolds all folders and files required by TYA.

Prerequisite checks:
  - Docker must be installed and reachable.
  - Java must be available (required for openapi-generator-cli).

By default the project is created in the current directory.
Use --name / -n to specify a custom project name (creates a sub-directory).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli_functions.RunInit(log, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Name, "name", "n", "", "project name (creates a sub-directory when set)")

	return cmd
}
