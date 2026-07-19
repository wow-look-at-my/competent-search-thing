// Package cli implements the competent-search-thing command line:
// running the binary bare boots the GUI as a single instance (a second
// launch just shows the already-running bar), and the toggle/show/hide
// subcommands drive a running instance over the internal/ipc unix
// socket, so any keybinding mechanism -- Wayland compositors included
// -- can summon the bar.
package cli

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

// sendTimeout bounds each IPC exchange a subcommand makes with the
// running instance.
const sendTimeout = 2 * time.Second

// RunOptions carries what the CLI layer decided into the GUI entry
// point (main.go's runGUI).
type RunOptions struct {
	// Server is this process's single-instance IPC server, already
	// listening; nil when IPC could not be brought up (the GUI still
	// runs, degraded). The app takes ownership: App.Shutdown closes
	// it.
	Server *ipc.Server
	// ShowOnStartup asks the app to show the bar as soon as the
	// frontend is ready (set when a toggle/show subcommand had to
	// start the app itself).
	ShowOnStartup bool
}

// env is the state every command builder closes over: the app version,
// the blocking GUI entry point, and the process stdout for the
// user-facing notices summon prints (injectable in tests).
type env struct {
	version string
	runGUI  func(RunOptions) error
	out     io.Writer
}

// stdout returns the notice writer, defaulting to os.Stdout when none
// was injected (the cobra OutOrStdout convention).
func (e *env) stdout() io.Writer {
	if e.out != nil {
		return e.out
	}
	return os.Stdout
}

// commandBuilders collects the subcommand constructors. Each
// subcommand file registers its own builder via init (one command per
// file, self-registering); newRoot consumes the registry, so every
// Execute -- and every test -- gets a fresh, fully wired command tree.
var commandBuilders []func(*env) *cobra.Command

// registerCommand is called from the init func of each subcommand
// file.
func registerCommand(b func(*env) *cobra.Command) {
	commandBuilders = append(commandBuilders, b)
}

// newRoot builds the root command; running it bare is the GUI path.
func newRoot(e *env) *cobra.Command {
	root := &cobra.Command{
		Use:   "competent-search-thing",
		Short: "Spotlight-style desktop searchbar with instant filename search",
		Long: "competent-search-thing is a Spotlight-style desktop searchbar.\n" +
			"Run it without arguments to start the app; a second launch just\n" +
			"shows the already-running instance's bar. The toggle, show and\n" +
			"hide subcommands drive a running instance over its unix socket,\n" +
			"so any keybinding mechanism -- Wayland compositors included --\n" +
			"can summon the bar.",
		Version:      e.version,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRoot(cmd, e)
		},
	}
	for _, b := range commandBuilders {
		root.AddCommand(b(e))
	}
	return root
}

// runRoot is the bare-invocation GUI path: acquire the single-instance
// socket, then run the GUI. When another instance already holds the
// socket, ask it to show its bar and report what actually happened;
// when the socket cannot be created at all, run the GUI without IPC
// rather than not at all.
func runRoot(cmd *cobra.Command, e *env) error {
	path := ipc.SocketPath(os.Getenv)
	srv, err := ipc.Listen(path, e.version)
	if err != nil {
		if errors.Is(err, ipc.ErrAlreadyRunning) {
			return showRunningInstance(cmd, path)
		}
		log.Printf("ipc: %v (running without single-instance IPC)", err)
		srv = nil
	}
	return e.runGUI(RunOptions{Server: srv})
}

// showRunningInstance is the second-launch path: deliver "show" to the
// instance holding the socket and report honestly -- "showing it" only
// on a confirmed acknowledgement, a still-starting notice on a
// not-ready reply, and a nonzero exit when the instance did not answer
// at all.
func showRunningInstance(cmd *cobra.Command, path string) error {
	resp, err := ipc.Send(path, ipc.CmdShow, sendTimeout)
	if err == nil {
		switch resp {
		case ipc.ReplyOK:
			fmt.Fprintln(cmd.OutOrStdout(), "competent-search-thing is already running; showing it")
			return nil
		case ipc.ReplyNotReady:
			fmt.Fprintln(cmd.OutOrStdout(), "competent-search-thing is already running (still starting up)")
			return nil
		default:
			err = fmt.Errorf("unexpected reply %q", resp)
		}
	}
	// Print the failure ourselves and keep cobra from adding its
	// "Error:" line on top (the hide subcommand's pattern).
	cmd.SilenceErrors = true
	err = fmt.Errorf("competent-search-thing is already running but did not respond: %v", err)
	fmt.Fprintln(cmd.ErrOrStderr(), err)
	return err
}

// summon delivers cmdName (toggle or show) to the running instance;
// when no instance is running it starts the GUI in this process with
// the bar shown once the frontend is ready.
func summon(e *env, cmdName string) error {
	path := ipc.SocketPath(os.Getenv)
	resp, err := ipc.Send(path, cmdName, sendTimeout)
	if err == nil {
		return summonReply(e, resp)
	}
	if !ipc.IsNotRunning(err) {
		return err
	}
	// No instance: become it.
	srv, lerr := ipc.Listen(path, e.version)
	if lerr != nil {
		if errors.Is(lerr, ipc.ErrAlreadyRunning) {
			// Lost a startup race; the winner shows the bar instead.
			resp, serr := ipc.Send(path, ipc.CmdShow, sendTimeout)
			if serr != nil {
				return serr
			}
			return summonReply(e, resp)
		}
		log.Printf("ipc: %v (running without single-instance IPC)", lerr)
		srv = nil
	}
	return e.runGUI(RunOptions{Server: srv, ShowOnStartup: true})
}

// summonReply maps a summon exchange's reply to its result. A
// not-ready reply still counts as success (checkReply), but earns a
// one-line heads-up: the instance is booting and cannot act on the
// summon just yet.
func summonReply(e *env, resp string) error {
	if resp == ipc.ReplyNotReady {
		fmt.Fprintln(e.stdout(), "competent-search-thing is still starting up; it may take a moment to respond")
	}
	return checkReply(resp)
}

// checkReply maps an IPC response line to a subcommand result.
// ReplyNotReady counts as success: the instance is booting and shows
// the bar itself once the frontend is ready.
func checkReply(resp string) error {
	if resp == ipc.ReplyOK || resp == ipc.ReplyNotReady {
		return nil
	}
	return fmt.Errorf("unexpected reply from the running instance: %q", resp)
}

// Execute runs the CLI and returns the process exit code. version is
// the app version (cobra's --version flag and the IPC version reply)
// and runGUI is the blocking GUI entry point; both are injected so
// main.go stays glue-only.
func Execute(version string, runGUI func(RunOptions) error) int {
	return execute(version, runGUI, os.Args[1:], os.Stdout, os.Stderr)
}

// execute is Execute with the process bits injectable for tests.
func execute(version string, runGUI func(RunOptions) error, args []string, out, errOut io.Writer) int {
	root := newRoot(&env{version: version, runGUI: runGUI, out: out})
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errOut)
	if err := root.Execute(); err != nil {
		return 1
	}
	return 0
}
