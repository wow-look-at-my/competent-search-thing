package cli

import (
	"github.com/spf13/cobra"

	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

func init() { registerCommand(newToggleCmd) }

// newToggleCmd builds the toggle subcommand: what the global hotkey
// does, but reachable from any external keybinding mechanism.
func newToggleCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "toggle",
		Short: "Toggle the search bar (starts the app if it is not running)",
		Long: "Toggle asks the running instance to toggle the search bar --\n" +
			"exactly what the global hotkey does -- and starts the app if it\n" +
			"is not running, showing the bar once it is ready. Bind this to a\n" +
			"key in desktop environments where the app cannot grab a global\n" +
			"hotkey itself (Wayland).",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return summon(e, ipc.CmdToggle)
		},
	}
}
