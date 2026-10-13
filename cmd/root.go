// Package cmd implements the reflex command line.
package cmd

import (
	"github.com/spf13/cobra"
)

// NewRootCommand returns the reflex command tree.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "reflex",
		Short: "Route Alertmanager notifications to urgent or informational Discord channels with Jev",
		Long: `reflex receives Alertmanager webhooks and asks TypeSafe's Jev model whether each
alert group should page someone now or is informational, then posts it to the
matching Discord webhook. If Jev is slow, unavailable or unsure, it falls back
to label routing: severity=critical is urgent, everything else is informational.

Flags can also be set with environment variables (see each command's help).
Secrets are read from the environment only: JEV_API_KEY, DISCORD_URGENT_WEBHOOK
and DISCORD_INFO_WEBHOOK.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return applyEnv(cmd.Flags())
		},
	}
	root.AddCommand(newServeCommand(), newClassifyCommand(), newVersionCommand())
	return root
}
