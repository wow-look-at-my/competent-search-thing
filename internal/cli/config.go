package cli

import (
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/spf13/cobra"

	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

func init() { registerCommand(newConfigCmd) }

// newConfigCmd builds the config subcommand: open the settings
// window.
func newConfigCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Open the settings window",
		Long: "Config opens the settings window: an ordinary, resizable\n" +
			"window of its own, separate from the searchbar. Settings apply\n" +
			"live to a running app -- no restart -- and the window works on\n" +
			"its own when the app is not running. It can also open\n" +
			"config.json itself for hand edits; those hot-apply too.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return openConfig(e, cmd)
		},
	}
}

// openConfig runs the settings window in THIS process, single-instanced
// on its own socket: when one is already open, it is raised (an IPC
// show) and this process exits. Nothing here talks to the searchbar
// instance -- the settings window is standalone by design, and the
// running app picks up saved settings through its own config-file
// watcher.
//
// The searchbar's own window used to host the editor, and hiding is
// suppressed while it is up, so alt-tabbing away could bury it behind
// other windows with no way back. A separate top-level window is
// reachable from the taskbar and the window switcher like any other.
func openConfig(e *env, _ *cobra.Command) error {
	path := ipc.ConfigSocketPath(os.Getenv)
	srv, err := e.listen(path)
	switch {
	case err == nil:
		return e.runGUI(RunOptions{Server: srv, ConfigWindow: true})
	case errors.Is(err, ipc.ErrAlreadyRunning):
		return raiseConfigWindow(e, path)
	default:
		// No IPC (a socket we cannot bind): still open the window,
		// degraded -- a second invocation then opens a second one.
		log.Printf("ipc: %v (the settings window runs without single-instance IPC)", err)
		return e.runGUI(RunOptions{ConfigWindow: true})
	}
}

// raiseConfigWindow asks the settings window that already holds the
// socket to come to the front and reports honestly.
func raiseConfigWindow(e *env, path string) error {
	rep, err := ipc.Send(path, ipc.CmdShow, sendTimeout)
	switch {
	case err == nil && rep.Parsed && (rep.OK || rep.NotReady()):
		fmt.Fprintln(e.stdout(), "the settings window is already open; showing it")
		return nil
	default:
		logNoAnswer(path, ipc.CmdShow, rep, err)
		return errors.New("a settings window is already open but did not respond")
	}
}
