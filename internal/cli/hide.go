package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

func init() { registerCommand(newHideCmd) }

// newHideCmd builds the hide subcommand. Unlike toggle/show it never
// starts the app: hiding nothing is a reportable failure (exit 1).
func newHideCmd(_ *env) *cobra.Command {
	return &cobra.Command{
		Use:   "hide",
		Short: "Hide the search bar of the running instance",
		Long: "Hide asks the running instance to hide the search bar. Unlike\n" +
			"toggle and show it never starts the app: when no instance is\n" +
			"running it prints a notice and exits nonzero.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := ipc.Send(ipc.SocketPath(os.Getenv), ipc.CmdHide, sendTimeout)
			if err != nil {
				if ipc.IsNotRunning(err) {
					// Print the plain notice ourselves and keep cobra
					// from adding its "Error:" line on top.
					cmd.SilenceErrors = true
					fmt.Fprintln(cmd.ErrOrStderr(), "competent-search-thing is not running")
				}
				return err
			}
			return checkReply(resp)
		},
	}
}
