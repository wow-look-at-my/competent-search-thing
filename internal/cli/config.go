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

// newConfigCmd builds the config subcommand: open the in-app config
// editor, starting the app when none is running (the toggle/show
// pattern, aimed at the editor instead of the bare bar).
func newConfigCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Open the config editor (starts the app if it is not running)",
		Long: "Config asks the running instance to open its in-app config\n" +
			"editor, where every setting applies live -- no restart. It\n" +
			"starts the app if it is not running, opening the editor once it\n" +
			"is ready. The editor can also open config.json itself for hand\n" +
			"edits; those hot-apply too.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return openConfig(e, cmd)
		},
	}
}

// openConfig delivers the config command to the running instance;
// when no instance is running it starts the GUI in this process with
// the editor opened once the frontend is ready (summon's pattern with
// the OpenConfig flag). A running instance too old to know the
// command earns a clear version-skew message instead of a generic
// failure.
func openConfig(e *env, cmd *cobra.Command) error {
	path := ipc.SocketPath(os.Getenv)
	rep, err := ipc.Send(path, ipc.CmdConfig, sendTimeout)
	if err == nil {
		return configReply(e, cmd, rep)
	}
	if !ipc.IsNotRunning(err) {
		return err
	}
	// No instance: become it, editor-first.
	srv, lerr := ipc.Listen(path, e.version)
	if lerr != nil {
		if errors.Is(lerr, ipc.ErrAlreadyRunning) {
			// Lost a startup race; the winner opens the editor instead.
			rep, serr := ipc.Send(path, ipc.CmdConfig, sendTimeout)
			if serr != nil {
				return serr
			}
			return configReply(e, cmd, rep)
		}
		log.Printf("ipc: %v (running without single-instance IPC)", lerr)
		srv = nil
	}
	return e.runGUI(RunOptions{Server: srv, ShowOnStartup: true, OpenConfig: true})
}

// configReply maps a config exchange's parsed reply to its result:
// confirmed acceptance and the still-booting notice both succeed (the
// summonReply pattern), while an unknown-command reply -- a running
// daemon from before the config command existed -- gets its own
// actionable message and a nonzero exit.
func configReply(e *env, cmd *cobra.Command, rep ipc.Reply) error {
	switch {
	case rep.OK:
		fmt.Fprintln(e.stdout(), "opening the config editor in the running instance")
		return nil
	case rep.NotReady():
		fmt.Fprintln(e.stdout(), "competent-search-thing is still starting up; it may take a moment to respond")
		return nil
	case rep.UnknownCommand():
		// Print the notice ourselves and keep cobra from adding its
		// "Error:" line on top (the hide subcommand's pattern).
		cmd.SilenceErrors = true
		err := errors.New("the running instance is an older version without the config command; restart it to use 'config'")
		fmt.Fprintln(cmd.ErrOrStderr(), err)
		return err
	default:
		return fmt.Errorf("unexpected reply from the running instance: %q", rep.Raw)
	}
}
