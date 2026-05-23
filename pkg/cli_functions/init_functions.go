package cli_functions

import (
	"fmt"
	"path/filepath"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
	"github.com/vhPedroGitHub/tya/pkg/models"
	"go.uber.org/zap"
)

func RunInit(log *zap.Logger, opts *models.InitOptions) error {
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
		if err := EnsureDir(d); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
		log.Info("created directory", zap.String("path", d))
	}

	// --- write config-create.yml ---
	createCfgPath := filepath.Join(root, "config-create.yml")
	if !FileExists(createCfgPath) {
		if err := configyml.WriteCreateConfig(createCfgPath, configyml.DefaultCreateConfig()); err != nil {
			return fmt.Errorf("write %s: %w", createCfgPath, err)
		}
		log.Info("created config file", zap.String("path", createCfgPath))
	} else {
		log.Info("config file already exists, skipping", zap.String("path", createCfgPath))
	}

	// --- write config-run.yml ---
	runCfgPath := filepath.Join(root, "config-run.yml")
	if !FileExists(runCfgPath) {
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
