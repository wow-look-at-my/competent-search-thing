package cli

import (
	"io"
	"log"
	"os"

	"github.com/spf13/cobra"

	"github.com/wow-look-at-my/competent-search-thing/internal/ffext"
)

func init() { registerCommand(newFirefoxHostCmd) }

// newFirefoxHostCmd builds the firefox-host subcommand: the
// native-messaging relay process Firefox spawns (via the generated
// wrapper script) when the companion extension connects. It never
// boots the GUI and never touches the single-instance IPC socket; it
// only bridges native-messaging frames on its own stdio to the running
// app's ffext socket, reconnecting with backoff while the app is down,
// and exits when Firefox closes its stdin.
//
// STDOUT DISCIPLINE: this process's stdout is the native-messaging
// frame channel Firefox parses -- one stray printed byte corrupts the
// stream and kills the extension's port. Every diagnostic goes through
// the standard logger (stderr); nothing may ever write to
// cmd.OutOrStdout() here.
func newFirefoxHostCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "firefox-host [manifest-path] [extension-id]",
		Short: "Run the Firefox native-messaging relay (spawned by Firefox, not for manual use)",
		Long: "firefox-host is the native-messaging host process behind the\n" +
			"companion Firefox extension: Firefox spawns it (through the\n" +
			"wrapper script the app installs) and speaks length-prefixed JSON\n" +
			"frames on its stdio, which it relays to the running app's\n" +
			"tab-bridge unix socket. With no app running it stays alive and\n" +
			"retries the socket with backoff -- the link comes up when the app\n" +
			"starts. Firefox passes the manifest path and the extension id as\n" +
			"arguments; both are accepted and ignored.",
		// Firefox's argv: [manifest path, extension id] -- tolerate
		// anything so a future Firefox adding arguments cannot break
		// the host.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Errors are logged to stderr below; cobra must not print
			// its own "Error:" line onto... stderr is fine, but the
			// usage dump is noise for a Firefox-spawned process.
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			var in io.Reader = os.Stdin
			var out io.Writer = os.Stdout
			if e.hostIn != nil {
				in = e.hostIn
			}
			if e.hostOut != nil {
				out = e.hostOut
			}
			err := ffext.RunHost(ffext.HostOptions{
				In:         in,
				Out:        out,
				SocketPath: ffext.SocketPath(os.Getenv),
				Logf:       log.Printf,
			})
			if err != nil {
				log.Printf("firefox-host: %v", err)
			}
			return err
		},
	}
}
