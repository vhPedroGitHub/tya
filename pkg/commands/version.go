package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// Version holds the build-time version string. It can be overridden at build
// time with -ldflags "-X 'github.com/vhPedroGitHub/tya/pkg/commands.Version=1.2.3'".
var Version = "dev"

// NewVersionCmd returns the cobra command for `tya version`.
func NewVersionCmd(log *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Show TYA version",
		Long:  "Print the current TYA build version.",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(Version)
		},
	}
	return cmd
}
