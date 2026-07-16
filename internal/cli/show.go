package cli

import (
	"github.com/spf13/cobra"

	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

func init() { registerCommand(newShowCmd) }

// newShowCmd builds the show subcommand: show-only summoning (never
// hides a visible bar), for bindings that should be idempotent.
func newShowCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the search bar (starts the app if it is not running)",
		Long: "Show asks the running instance to show the search bar; unlike\n" +
			"toggle it never hides a visible bar. Show\n" +
			"starts the app if it is not running, with the bar shown once it\n" +
			"is ready.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return summon(e, ipc.CmdShow)
		},
	}
}
