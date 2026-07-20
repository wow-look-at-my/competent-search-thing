package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/wow-look-at-my/competent-search-thing/internal/service"
)

func init() { registerCommand(newServiceCmd) }

// newServiceManager builds the production service manager; a seam so
// service_test.go can inject a scripted runner + temp home.
var newServiceManager = service.NewManager

// newServiceCmd builds the service command group: install the app as
// a per-user login service (launchd LaunchAgent on macOS, systemd
// user unit on Linux) so it starts with the desktop session, stays
// running and logs somewhere inspectable.
func newServiceCmd(_ *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage the login service (start at login, restart on crash)",
		Long: "Service installs the app as a per-user login service -- a launchd\n" +
			"LaunchAgent on macOS, a systemd user unit on Linux -- so the\n" +
			"searchbar starts with your desktop session, is restarted if it\n" +
			"crashes, and logs somewhere inspectable. A clean exit (for\n" +
			"example handing off to an already-running instance) is never\n" +
			"respawned. Windows is not supported yet.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(
		newServiceInstallCmd(),
		newServiceUninstallCmd(),
		newServiceStatusCmd(),
		newServiceRestartCmd(),
	)
	return cmd
}

// newServiceInstallCmd builds `service install`: write the service
// file, enable it and start it, converging on repeat runs.
func newServiceInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install, enable and start the login service",
		Long: "Install writes the service file for this binary, enables it for\n" +
			"login startup and starts it. Running it again converges: files\n" +
			"are only rewritten when their content changed. If an instance\n" +
			"was already running manually, the service copy just shows its\n" +
			"bar once and exits; quit the manual instance and run 'service\n" +
			"restart' to hand over to the service.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newServiceManager()
			if err != nil {
				return err
			}
			res, err := m.Install(cmd.Context())
			printNotes(cmd.OutOrStdout(), res.Notes)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if res.Changed {
				fmt.Fprintln(out, "wrote "+res.ServicePath)
			} else {
				fmt.Fprintln(out, "service file already up to date: "+res.ServicePath)
			}
			if res.Started {
				fmt.Fprintln(out, "service started (starts at login, restarts on crash)")
			} else if len(res.Notes) == 0 {
				fmt.Fprintln(out, "service already loaded")
			}
			fmt.Fprintln(out, "logs: "+res.LogHint)
			return nil
		},
	}
}

// newServiceUninstallCmd builds `service uninstall`: stop the service
// and remove its file; repeat runs no-op gracefully.
func newServiceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the login service and remove its service file",
		Long: "Uninstall stops the service, removes the service file and\n" +
			"disables login startup. Log files are left in place. Running it\n" +
			"when nothing is installed is a harmless no-op.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newServiceManager()
			if err != nil {
				return err
			}
			res, err := m.Uninstall(cmd.Context())
			printNotes(cmd.OutOrStdout(), res.Notes)
			if err != nil {
				return err
			}
			if res.Removed {
				fmt.Fprintln(cmd.OutOrStdout(), "service uninstalled (removed "+res.ServicePath+")")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "service was not installed; nothing to do")
			}
			return nil
		},
	}
}

// newServiceStatusCmd builds `service status`: report the real
// observed state (file, load state, running PID, log location).
func newServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report the login service's real state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newServiceManager()
			if err != nil {
				return err
			}
			st, err := m.Status(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			installed := "not installed"
			if st.Installed {
				installed = "installed"
			}
			fmt.Fprintln(out, "service file: "+st.ServicePath+" ("+installed+")")
			fmt.Fprintln(out, "loaded: "+yesNo(st.Loaded))
			if st.Running && st.PID > 0 {
				fmt.Fprintf(out, "running: yes (pid %d)\n", st.PID)
			} else {
				fmt.Fprintln(out, "running: "+yesNo(st.Running))
			}
			for _, line := range st.Extra {
				fmt.Fprintln(out, line)
			}
			fmt.Fprintln(out, "logs: "+st.LogHint)
			return nil
		},
	}
}

// newServiceRestartCmd builds `service restart`: kill and relaunch
// the service instance.
func newServiceRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the login service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newServiceManager()
			if err != nil {
				return err
			}
			if err := m.Restart(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "service restarted")
			return nil
		},
	}
}

// printNotes prints each honest observation on its own note line.
func printNotes(w io.Writer, notes []string) {
	for _, n := range notes {
		fmt.Fprintln(w, "note: "+n)
	}
}

// yesNo renders a boolean for status lines.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
