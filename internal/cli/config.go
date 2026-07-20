package cli

import (
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

// openConfig delivers the config command to the running instance when
// a healthy same-build one answers a version probe; in every other
// state -- not running, unresponsive, pre-JSON, version-skewed, or a
// daemon so old it answers the config command itself with
// unknown-command (skew by definition) -- it becomes the instance
// (summon's self-heal pattern) and starts the GUI with the editor
// opened once the frontend is ready. The old "restart it to use
// 'config'" dead end is gone: convergence is automatic.
func openConfig(e *env, _ *cobra.Command) error {
	path := ipc.SocketPath(os.Getenv)
	if classifyInstance(e, path) == instanceHealthy {
		rep, err := ipc.Send(path, ipc.CmdConfig, sendTimeout)
		switch {
		case err == nil && rep.Parsed && rep.UnknownCommand():
			// Healthy enough to answer version, too old to know the
			// config command: replace it instead of telling the user
			// to restart anything.
			log.Printf("ipc: the running instance predates the config command; replacing it")
		case err == nil && rep.Parsed:
			return configReply(e, rep)
		default:
			logNoAnswer(path, ipc.CmdConfig, rep, err)
		}
	}
	return becomeInstance(e, ipc.CmdConfig, RunOptions{ShowOnStartup: true, OpenConfig: true}, configReply)
}

// configReply maps a config exchange's parsed reply to its result:
// confirmed acceptance and the still-booting notice both succeed (the
// summonReply pattern); anything else is reported honestly (only
// reachable off the bounded lost-a-startup-race delivery, where the
// winner is a fresh same-version instance).
func configReply(e *env, rep ipc.Reply) error {
	switch {
	case rep.OK:
		fmt.Fprintln(e.stdout(), "opening the config editor in the running instance")
		return nil
	case rep.NotReady():
		fmt.Fprintln(e.stdout(), "competent-search-thing is still starting up; it may take a moment to respond")
		return nil
	default:
		return fmt.Errorf("unexpected reply from the running instance: %q", rep.Raw)
	}
}
