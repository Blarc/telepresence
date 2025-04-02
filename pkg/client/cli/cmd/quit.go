package cmd

import (
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
)

func quit() *cobra.Command {
	quitDaemons := false
	cmd := &cobra.Command{
		Use:   "quit",
		Args:  cobra.NoArgs,
		Short: "Tell telepresence daemons to quit",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if quitDaemons {
				connect.InitProgressWriter(cmd)
				defer progress.Stop(cmd.Context())
				connect.Quit(cmd.Context())
			} else {
				cmd.Annotations = map[string]string{ann.UserDaemon: ann.Optional}
				if err := connect.InitCommand(cmd); err != nil {
					return err
				}
				defer progress.Stop(cmd.Context())
				connect.Disconnect(cmd.Context())
			}
			return nil
		},
		ValidArgsFunction: cobra.NoFileCompletions,
	}
	flags := cmd.Flags()
	flags.BoolVarP(&quitDaemons, "stop-daemons", "s", false, "stop all local telepresence daemons")
	return cmd
}
