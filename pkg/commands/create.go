package commands

import (
	"github.com/spf13/cobra"
	"github.com/vhPedroGitHub/tya/pkg/cli_functions"
	"github.com/vhPedroGitHub/tya/pkg/models"
	"go.uber.org/zap"
)

// NewCreateCmd returns the cobra command for `tya create`.
func NewCreateCmd(log *zap.Logger) *cobra.Command {
	opts := &models.CreateOptions{}

	cmd := &cobra.Command{
		Use:   "create [openapi.yaml]",
		Short: "Parse an OpenAPI spec and generate payload fixtures",
		Long: `Parses an OpenAPI YAML spec and generates:
  - JSON model schemas under models/
  - Per-endpoint config.yml and payload JSON files under api/

Example:
  tya create openapi.yaml
  tya create openapi.yaml --config config-create.yml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.SpecFile = args[0]
			return cli_functions.RunCreate(log, opts)
		},
	}

	cmd.Flags().StringVar(&opts.ConfigFile, "config", "config-create.yml", "path to config-create.yml")

	return cmd
}
