package cmd

import (
	"os"
	"slices"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker/compose"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
)

func composeCmd() *cobra.Command {
	var request *daemon.CobraRequest
	config := compose.Config{}

	cmd := &cobra.Command{
		Use:   "compose [flags] [services]",
		Args:  cobra.ArbitraryArgs,
		Short: `Perform a "docker compose up" after establishing engagements described in x-<engagement> annotations`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := request.CommitFlags(cmd); err != nil {
				return err
			}
			return config.Run(cmd, args)
		},
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			dir := cobra.ShellCompDirectiveNoFileComp
			if slices.Contains(os.Args, "--") {
				dir = cobra.ShellCompDirectiveDefault
			}
			return nil, dir
		},
	}
	uf := cmd.UsageFunc()
	cmd.SetUsageFunc(func(*cobra.Command) error {
		cmd.SetContext(flags.WithFlagSets(cmd.Context(), config.ComposeFlags, config.ComposeUpFlags))
		return uf(cmd)
	})
	request = daemon.InitRequest(cmd)
	config.AddFlags(cmd)
	return cmd
}
