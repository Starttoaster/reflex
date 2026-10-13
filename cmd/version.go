package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Set at build time with -ldflags "-X github.com/starttoaster/reflex/cmd.version=...".
var (
	version = "dev"
	commit  = "none"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "reflex %s (commit %s, %s)\n", version, commit, runtime.Version())
		},
	}
}
