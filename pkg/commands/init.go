package commands

import (
	"fmt"
	"path/filepath"

	"tya/pkg/cli_functions"
	"tya/pkg/configyml"
	"tya/pkg/models"

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
			return runInit(log, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Name, "name", "n", "", "project name (creates a sub-directory when set)")

	return cmd
}

func runInit(log *zap.Logger, opts *models.InitOptions) error {
	// --- prerequisite checks ---
	if err := cli_functions.CheckDocker(log); err != nil {
		return fmt.Errorf("prerequisite failed: %w", err)
	}
	if err := cli_functions.CheckJava(log); err != nil {
		return fmt.Errorf("prerequisite failed: %w", err)
	}

	// --- determine project root ---
	root := "."
	if opts.Name != "" {
		root = opts.Name
	}

	log.Info("initialising TYA project", zap.String("root", root))

	// --- create directory structure ---
	dirs := []string{
		filepath.Join(root, "models"),
		filepath.Join(root, "api"),
	}
	for _, d := range dirs {
		if err := cli_functions.EnsureDir(d); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
		log.Info("created directory", zap.String("path", d))
	}

	// --- write config-create.yml ---
	createCfgPath := filepath.Join(root, "config-create.yml")
	if !cli_functions.FileExists(createCfgPath) {
		if err := configyml.WriteCreateConfig(createCfgPath, configyml.DefaultCreateConfig()); err != nil {
			return fmt.Errorf("write %s: %w", createCfgPath, err)
		}
		log.Info("created config file", zap.String("path", createCfgPath))
	} else {
		log.Info("config file already exists, skipping", zap.String("path", createCfgPath))
	}

	// --- write config-run.yml ---
	runCfgPath := filepath.Join(root, "config-run.yml")
	if !cli_functions.FileExists(runCfgPath) {
		if err := configyml.WriteRunConfig(runCfgPath, configyml.DefaultRunConfig()); err != nil {
			return fmt.Errorf("write %s: %w", runCfgPath, err)
		}
		log.Info("created config file", zap.String("path", runCfgPath))
	} else {
		log.Info("config file already exists, skipping", zap.String("path", runCfgPath))
	}

	log.Info("TYA project initialised successfully", zap.String("root", root))
	return nil
}
