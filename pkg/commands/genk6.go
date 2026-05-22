package commands

import (
	"fmt"
	"path/filepath"
	"strings"

	"tya/pkg/cli_functions"
	"tya/pkg/configyml"
	"tya/pkg/k6gen"
	"tya/pkg/models"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// NewGenK6Cmd returns the cobra command for `tya genk6`.
func NewGenK6Cmd(log *zap.Logger) *cobra.Command {
	opts := &models.GenK6Options{}

	cmd := &cobra.Command{
		Use:   "genk6 [config-run.yml]",
		Short: "Generate k6 load-test scripts from a TYA config",
		Long: `Parses a TYA config-run.yml and generates k6 JavaScript scripts for each
defined flow. Scripts are stored in a directory named after the config file
with '-k6' appended.

Examples:
  tya genk6 config-run.yml
  tya genk6 config-run.yml --output my-k6-scripts/
  tya genk6 config-run.yml --api-dir ./api`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ConfigFile = args[0]
			return runGenK6(log, opts)
		},
	}

	cmd.Flags().StringVarP(&opts.OutputDir, "output", "o", "", "output directory (default: <config-name>-k6/)")
	cmd.Flags().StringVar(&opts.APIDir, "api-dir", "api", "path to api/ directory with payload fixtures")

	return cmd
}

func runGenK6(log *zap.Logger, opts *models.GenK6Options) error {
	cfg, err := configyml.LoadRunConfig(opts.ConfigFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if len(cfg.Flows) == 0 {
		return fmt.Errorf("no flows defined in %s", opts.ConfigFile)
	}

	// Validate dependency graph
	if err := cli_functions.ValidateDependencyGraph(cfg.Flows); err != nil {
		return fmt.Errorf("dependency graph error: %w", err)
	}

	// Determine output directory
	outputDir := opts.OutputDir
	if outputDir == "" {
		base := strings.TrimSuffix(filepath.Base(opts.ConfigFile), filepath.Ext(opts.ConfigFile))
		outputDir = base + "-k6"
	}

	log.Info("generating k6 scripts",
		zap.String("config", opts.ConfigFile),
		zap.String("output", outputDir),
		zap.Int("flows", len(cfg.Flows)),
	)

	scripts, err := k6gen.GenerateAll(log, cfg, opts.APIDir)
	if err != nil {
		return fmt.Errorf("generate scripts: %w", err)
	}

	if err := k6gen.WriteScripts(scripts, outputDir); err != nil {
		return fmt.Errorf("write scripts: %w", err)
	}

	if err := k6gen.WriteConfigJSON(cfg, outputDir); err != nil {
		return fmt.Errorf("write config.json: %w", err)
	}

	log.Info("k6 scripts generated",
		zap.String("directory", outputDir),
		zap.Int("scripts", len(scripts)),
	)
	fmt.Printf("Generated %d k6 scripts in %s/\n", len(scripts), outputDir)
	for _, s := range scripts {
		fmt.Printf("  - %s\n", s.FileName)
	}

	return nil
}
