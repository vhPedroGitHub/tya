package main

import (
	"fmt"
	"os"

	"tya/pkg/commands"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func main() {
	log, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialise logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	root := &cobra.Command{
		Use:   "tya",
		Short: "TYA — Test Your API",
		Long: `TYA is a CLI tool for testing and load-testing APIs.

Commands:
  init    Initialise a new TYA project
  create  Generate payload fixtures from an OpenAPI spec
  run     Execute flows against a live API
  genk6   Generate k6 load-test scripts from a TYA config
  runk6s  Run generated k6 load-test scripts`,
	}

	root.AddCommand(commands.NewInitCmd(log))
	root.AddCommand(commands.NewCreateCmd(log))
	root.AddCommand(commands.NewRunCmd(log))
	root.AddCommand(commands.NewGenK6Cmd(log))
	root.AddCommand(commands.NewRunK6SCmd(log))

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
