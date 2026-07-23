// Package cli implements the competent-search-thing command line:
// running the binary bare boots the GUI as a single instance (a second
// launch just shows the already-running bar), and the toggle/show/hide
// subcommands drive a running instance over the internal/ipc unix
// socket, so any keybinding mechanism -- Wayland compositors included
// -- can summon the bar.
package cli

import (
	"context"
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
	// OpenConfig asks the app to open the bar straight into its
	// config editor once the frontend is ready (set when the config
	// subcommand had to start the app itself).
	OpenConfig bool
}

// env is the state every command builder closes over: the app version,
// the blocking GUI entry point, and the process stdout for the
// user-facing notices summon prints (injectable in tests). hostIn and
// hostOut override the firefox-host relay's stdio in tests; nil means
// the real os.Stdin/os.Stdout (which Firefox owns in production).
type env struct {
	version string
	// build is the version-skew discriminator (the binary's vcs
	// stamp); empty derives it lazily via ipc.OwnBuild. Tests set it
	// to pin skew scenarios deterministically.
	build   string
	runGUI  func(RunOptions) error
	out     io.Writer
	hostIn  io.Reader
	hostOut io.Writer
	// listenFn overrides the production socket acquisition in tests
	// (which MUST script ipc.ListenOptions.Kill: in-process fake
	// daemons report the test's own pid as the socket owner).
	listenFn func(path string) (*ipc.Server, error)
	// setupWatch overrides the `setup-watch` command's action in tests;
	// nil uses the production internal/watchsetup path (which prompts
	// for privileges and runs setcap).
	setupWatch func(context.Context, io.Writer) error
}

// stdout returns the notice writer, defaulting to os.Stdout when none
// was injected (the cobra OutOrStdout convention).
func (e *env) stdout() io.Writer {
	if e.out != nil {
		return e.out
	}
	return os.Stdout
}

// buildID resolves this binary's build identity once.
func (e *env) buildID() string {
	if e.build == "" {
		e.build = ipc.OwnBuild()
	}
	return e.build
}

// listen acquires the single-instance socket. ipc.ListenWith's probe
// classifies whatever holds it and self-heals every unhealthy case
// (dead file, wedged or mid-death holder, pre-JSON legacy daemon,
// version skew); the one non-Server outcome left is ErrAlreadyRunning
// -- a healthy instance of this same version+build.
func (e *env) listen(path string) (*ipc.Server, error) {
	if e.listenFn != nil {
		return e.listenFn(path)
	}
	return ipc.ListenWith(path, e.version, ipc.ListenOptions{Logf: log.Printf, Build: e.buildID()})
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
// socket, then run the GUI. e.listen's probe self-heals every
// unhealthy socket holder, so ErrAlreadyRunning here means a healthy
// same-version instance: ask it to show its bar and report what
// actually happened. When the socket cannot be created at all, run
// the GUI without IPC rather than not at all.
func runRoot(cmd *cobra.Command, e *env) error {
	path := ipc.SocketPath(os.Getenv)
	srv, err := e.listen(path)
	if err != nil {
		if errors.Is(err, ipc.ErrAlreadyRunning) {
			return showRunningInstance(cmd, e, path)
		}
		log.Printf("ipc: %v (running without single-instance IPC)", err)
		srv = nil
	}
	return e.runGUI(RunOptions{Server: srv})
}

// showRunningInstance is the second-launch path: deliver "show" to the
// healthy instance the listen probe just confirmed and report honestly
// -- "showing it" only on a confirmed acknowledgement, a still-starting
// notice on a not-ready reply. When even this exchange gets no JSON
// answer (the instance died in the gap -- the probe-to-show death
// race), ONE bounded re-listen self-heals: its probe classifies the
// corpse, takes the socket over, and this process becomes the
// instance instead of asking the user to clean anything up.
func showRunningInstance(cmd *cobra.Command, e *env, path string) error {
	rep, err := ipc.Send(path, ipc.CmdShow, sendTimeout)
	if err == nil && rep.Parsed {
		return showReply(cmd, rep)
	}
	if err != nil {
		log.Printf("ipc: the running instance stopped answering mid-show (%v); re-acquiring the socket", err)
	} else {
		log.Printf("ipc: the running instance answered show with the non-JSON line %q; re-acquiring the socket", rep.Raw)
	}
	srv, lerr := e.listen(path)
	if lerr != nil {
		if errors.Is(lerr, ipc.ErrAlreadyRunning) {
			// A healthy instance (re)appeared in the gap (e.g. another
			// launcher won the takeover): one final delivery, honest
			// reporting, no loop.
			rep, serr := ipc.Send(path, ipc.CmdShow, sendTimeout)
			if serr == nil && rep.Parsed {
				return showReply(cmd, rep)
			}
			if serr == nil {
				serr = fmt.Errorf("unexpected reply %q", rep.Raw)
			}
			return reportNoResponse(cmd, serr)
		}
		log.Printf("ipc: %v (running without single-instance IPC)", lerr)
		srv = nil
	}
	return e.runGUI(RunOptions{Server: srv})
}

// showReply maps the second-launch show exchange's parsed reply.
func showReply(cmd *cobra.Command, rep ipc.Reply) error {
	switch {
	case rep.OK:
		fmt.Fprintln(cmd.OutOrStdout(), "competent-search-thing is already running; showing it")
		return nil
	case rep.NotReady():
		fmt.Fprintln(cmd.OutOrStdout(), "competent-search-thing is already running (still starting up)")
		return nil
	default:
		// A parsed reply from a responsive instance that still refused
		// the show: report it honestly, never kill a responsive daemon.
		return reportNoResponse(cmd, fmt.Errorf("unexpected reply %q", rep.Raw))
	}
}

// reportNoResponse prints the second-launch failure ourselves and
// keeps cobra from adding its "Error:" line on top (the hide
// subcommand's pattern).
func reportNoResponse(cmd *cobra.Command, err error) error {
	cmd.SilenceErrors = true
	err = fmt.Errorf("competent-search-thing is already running but did not respond: %v", err)
	fmt.Fprintln(cmd.ErrOrStderr(), err)
	return err
}

// instanceState buckets what one version exchange proved about the
// socket's holder.
type instanceState int

const (
	// instanceHealthy: a live JSON daemon of this same version+build.
	instanceHealthy instanceState = iota
	// instanceReplace: a live JSON daemon of another vintage -- new
	// instance wins.
	instanceReplace
	// instanceAbsent: nothing healthy answered (not running, refused,
	// reset, EOF, timeout, or a pre-JSON raw line).
	instanceAbsent
)

// classifyInstance probes the running instance with one version
// exchange. Refused, reset, EOF, timeout and non-JSON garbage all
// classify UNIFORMLY as instanceAbsent -- "no healthy instance" -- per
// the self-heal contract; only a parsed JSON reply proves a
// responsive daemon (a "not ready" answer is a RESPONSIVE booting
// daemon and lands in instanceHealthy's proceed path, never in a
// takeover).
func classifyInstance(e *env, path string) instanceState {
	rep, err := ipc.Send(path, ipc.CmdVersion, sendTimeout)
	switch {
	case err != nil && ipc.IsNotRunning(err):
		// The ordinary cold start: nothing to log loudly about.
		return instanceAbsent
	case err != nil:
		log.Printf("ipc: the instance behind %s did not answer a version probe (%v); treating it as gone", path, err)
		return instanceAbsent
	case !rep.Parsed:
		log.Printf("ipc: the instance behind %s answered the version probe with the non-JSON line %q (pre-JSON protocol); new instance wins", path, rep.Raw)
		return instanceAbsent
	case rep.OK && (rep.Version != e.version || rep.Build != e.buildID()):
		log.Printf("ipc: the running instance is version %s build %q, this binary is %s %q; replacing it",
			rep.Version, rep.Build, e.version, e.buildID())
		return instanceReplace
	default:
		return instanceHealthy
	}
}

// logNoAnswer records why a command exchange against a
// just-classified-healthy instance still failed (the death race
// between the version probe and the command).
func logNoAnswer(path, cmdName string, rep ipc.Reply, err error) {
	if err != nil {
		log.Printf("ipc: the instance behind %s stopped answering mid-%s (%v); taking over", path, cmdName, err)
		return
	}
	log.Printf("ipc: the instance behind %s answered %s with the non-JSON line %q; taking over", path, cmdName, rep.Raw)
}

// becomeInstance starts the GUI in this process after acquiring the
// single-instance socket (e.listen's probe replaces dead, wedged,
// legacy and skewed holders on the way). The one losing outcome is
// ErrAlreadyRunning -- a healthy same-version winner of a startup
// race -- where raceCmd is delivered to the winner instead (once,
// no loop) and its reply mapped by raceReply.
func becomeInstance(e *env, raceCmd string, opts RunOptions, raceReply func(*env, ipc.Reply) error) error {
	path := ipc.SocketPath(os.Getenv)
	srv, err := e.listen(path)
	if err != nil {
		if errors.Is(err, ipc.ErrAlreadyRunning) {
			rep, serr := ipc.Send(path, raceCmd, sendTimeout)
			if serr != nil {
				return serr
			}
			return raceReply(e, rep)
		}
		log.Printf("ipc: %v (running without single-instance IPC)", err)
		srv = nil
	}
	opts.Server = srv
	return e.runGUI(opts)
}

// summon delivers cmdName (toggle or show) to the running instance
// when a healthy same-build one answers a version probe; in every
// other state -- not running, unresponsive, reset mid-exchange,
// pre-JSON, or version-skewed -- it becomes the instance itself
// (listen's probe handling any replacement) and starts the GUI with
// the bar shown once the frontend is ready.
func summon(e *env, cmdName string) error {
	path := ipc.SocketPath(os.Getenv)
	if classifyInstance(e, path) == instanceHealthy {
		rep, err := ipc.Send(path, cmdName, sendTimeout)
		if err == nil && rep.Parsed {
			return summonReply(e, rep)
		}
		logNoAnswer(path, cmdName, rep, err)
	}
	return becomeInstance(e, ipc.CmdShow, RunOptions{ShowOnStartup: true}, summonReply)
}

// summonReply maps a summon exchange's parsed reply to its result. A
// not-ready reply still counts as success (checkReply), but earns a
// one-line heads-up: the instance is booting and cannot act on the
// summon just yet.
func summonReply(e *env, rep ipc.Reply) error {
	if rep.NotReady() {
		fmt.Fprintln(e.stdout(), "competent-search-thing is still starting up; it may take a moment to respond")
	}
	return checkReply(rep)
}

// checkReply maps a parsed IPC reply to a subcommand result. A
// not-ready reply counts as success: the instance is booting and
// shows the bar itself once the frontend is ready. The reply parsing
// itself lives entirely in ipc.Send; this layer only maps outcomes
// to exit behavior. On the summon paths a non-JSON reply never gets
// here (it classifies as no-healthy-instance and self-heals); hide --
// which by contract never starts, takes over, or kills anything --
// still lands such a reply in the unexpected-reply error below.
func checkReply(rep ipc.Reply) error {
	if rep.OK || rep.NotReady() {
		return nil
	}
	return fmt.Errorf("unexpected reply from the running instance: %q", rep.Raw)
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
	return executeEnv(&env{version: version, runGUI: runGUI, out: out}, args, out, errOut)
}

// executeEnv is execute over a caller-built env (tests that script the
// listen seam or pin the build stamp construct their own).
func executeEnv(e *env, args []string, out, errOut io.Writer) int {
	root := newRoot(e)
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errOut)
	if err := root.Execute(); err != nil {
		return 1
	}
	return 0
}
