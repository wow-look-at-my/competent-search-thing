# internal/cli

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/cli` -- the cobra command line, the real process entry
point (main.go calls cli.Execute(app.Version, runGUI)). SELF-HEAL
CONTRACT (the death-race incident fix): every summon-shaped path
classifies refused / reset / EOF / timeout / non-JSON garbage
UNIFORMLY as "no healthy instance" and BECOMES the instance --
e.listen (env's seam; production = ipc.ListenWith with Logf:
log.Printf + Build: e.buildID(), the memoized ipc.OwnBuild) runs
the probe+takeover engine, so dead files, wedged or mid-death
holders, pre-JSON daemons and version-skewed daemons are all
replaced automatically; a parsed "not ready" reply is a RESPONSIVE
booting daemon and never triggers takeover. Bare
invocation = the GUI path: e.listen; ErrAlreadyRunning (now
guaranteed a healthy same-build instance) = Send "show" and report
HONESTLY (showRunningInstance): Reply.OK -> "already running;
showing it" + exit 0, Reply.NotReady() -> "already running (still
starting up)" + exit 0; a show that gets NO parsed reply (the
instance died in the probe-to-show gap) = loud log + ONE bounded
re-listen whose probe takes the corpse over -> run the GUI (hidden,
like any cold start; a healthy winner appearing in the gap instead
gets one final Send show + honest report, no loop); a parsed-but-
weird reply stays the honest "already running but did not respond"
exit 1 (never kill a responsive daemon); any other listen error
= log + run the GUI with a
NIL server (degraded, no IPC -- the app must still work). toggle /
show first classify via ONE version exchange (classifyInstance):
healthy same-build -> send the command exactly as before (NotReady
= success + the one-line "still starting up; it may take a moment
to respond" notice); version/build mismatch -> "new instance wins"
(log + becomeInstance: e.listen's probe quits/terminates the old
daemon) -> start the GUI with ShowOnStartup=true, so brew-upgrade-
then-summon converges to the new binary; absent/unhealthy ->
becomeInstance the same way (an ErrAlreadyRunning race-loss
delivers Send "show" to the winner once, mapped by summonReply --
no loop). hide is the one exception, contract unchanged: never
starts the app, never takes over, never signals anything; not
running = plain notice on
stderr + exit 1 (cobra error/usage output suppressed), transport
errors = honest exit 1. config
(config.go) opens the SETTINGS WINDOW and deliberately never
touches the searchbar's socket: it e.listens on
ipc.ConfigSocketPath (the settings window's OWN single-instance
socket) and runs the GUI with RunOptions{ConfigWindow: true};
ErrAlreadyRunning means a settings window is up, so it Sends
"show" and reports honestly (raiseConfigWindow), and any other
listen error degrades to a settings window with a NIL server
(the bare-invocation precedent). It therefore neither needs nor
starts the searchbar. firefox-host (firefoxhost.go) is the
native-messaging relay Firefox spawns through the generated
wrapper: it never boots the GUI and never touches the
single-instance socket -- it only runs ffext.RunHost over its
stdio and ffext.SocketPath(os.Getenv), tolerates and ignores
Firefox's positional args (manifest path, extension id), exits
cleanly on stdin EOF, and keeps STDOUT strictly for protocol
frames (cobra out is os.Stdout, so diagnostics go to the stderr
logger, NEVER cmd.OutOrStdout(); env.hostIn/hostOut inject the
stdio in tests, which drive a full relay round-trip against an
in-process ffext.Server). setup-watch (setupwatch.go) is the manual
retry for the automatic fanotify-capability setup (internal/watchsetup):
it never boots the GUI and never touches the single-instance socket --
it calls watchsetup.New(config).Attempt(ctx, out) (forced: ignores the
decline marker + watcher.setupEnabled, prompts via pkexec, runs setcap,
reports; NEVER re-execs -- caps apply at the next launch), ActionAttemptFailed
= exit 1 (SilenceErrors, the reason already printed by Attempt); the
env.setupWatch seam injects a fake in tests so no real pkexec/probe
runs. The CLI
branches ONLY on ipc.Reply fields (checkReply/summonReply/
configReply/classifyInstance over Parsed/OK/NotReady/
UnknownCommand/Version/Build); ALL
wire parsing lives in ipc.Send -- a non-JSON reply (a still-running
pre-JSON daemon's raw line) arrives in-band via Raw with Parsed
false and classifies as no-healthy-instance (only hide still
surfaces it as the "unexpected reply" error). Convention:
ONE self-registering subcommand per file (init -> registerCommand);
newRoot() consumes the builder registry so Execute -- and every
test -- gets a fresh command tree (executeEnv is the test entry
over a caller-built env). RunOptions{Server,
ShowOnStartup, ConfigWindow} is the runGUI contract (main.go
branches on ConfigWindow into runConfigWindow); the App takes
ownership of
the server (Shutdown closes it). Unit-tested headlessly: fake
runGUI, real ipc servers on temp sockets, COMPETENT_SEARCH_SOCKET
(t.Setenv) isolation; takeover_test.go drives the self-heal matrix
over scripted fake daemons with a RECORDED ipc.ListenOptions.Kill
(in-process fakes report the test's own pid -- a real kill would
signal the test) and a pinned env build stamp (the test binary's
own vcs stamp must never decide a skew scenario).
