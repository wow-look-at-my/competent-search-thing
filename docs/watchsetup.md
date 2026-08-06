# internal/watchsetup

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/watchsetup` -- the automatic optimal-watch setup that runs
BEFORE the GUI (main.go's runGUI calls `New(Config{Backend, Enabled,
ConfigDir}).Ensure()` before wails.Run), so a fresh Linux install
comes up on the whole-filesystem fanotify backend (full coverage,
negligible memory) instead of the per-directory fallback -- the goal
being "put itself in its best state," not log a setcap hint and run
degraded. Pure decision matrix over seam fields (New fills production;
tests inject fakes), exhaustively headless-tested (watchsetup_test.go,
85%+). `Ensure()` gate order: not-linux / EnvDisable
(COMPETENT_SEARCH_NO_WATCH_SETUP, both CI scripts set it) /
!setupEnabled / backend inotify|fsevents (respect the user's choice;
auto+fanotify proceed) / headless (no DISPLAY and no WAYLAND_DISPLAY)
-> skip; then the `probe` seam (fanoprobe_linux.go: one
unix.FanotifyInit with internal/watch's exact FAN_REPORT_DFID_NAME
flags, immediately closed -- err==nil = StateReady (caps already
present -> ActionOptimal, tidy any stale marker), EPERM =
StateNeedsCaps (grantable), else = StateUnsupported (old
kernel/container, caps cannot help -> one honest log line);
fanoprobe_other.go returns StateUnsupported). On StateNeedsCaps:
`resolve` (prodResolve: os.Executable -> platform.ResolvedExecutable
-- the REAL file, setcap refuses symlinks -- plus a path|mtime|size
identity) then the loop guard (envAttempted
COMPETENT_SEARCH_WATCH_SETUP_ATTEMPTED set on the re-exec'd child: caps
STILL missing = setcap did not take (noxattr/overlayfs) -> marker +
honest log, never a second attempt) then the decline marker
(<configDir>/watch-setup-state.json, {v,identity,reason,at}: matching
identity = declined/failed before for THIS binary -> silent skip; a
binary upgrade changes the identity and re-offers), else
grantAndRestart: `writeScript` (prodWriteScript: a 0700 temp-dir
grantScript that locates setcap, runs `setcap
cap_sys_admin,cap_dac_read_search+ep <exe>`, verifies with getcap; the
exe single-quote-escaped though it is our own path) run under
`escalate` (prodEscalate: `pkexec /bin/sh <script>` bounded by
escalateTimeout 3m; escalateError maps pkexec 126=dismissed /
127=auth-fail / other=script-exit + stderr tail), then on success
`reExec` (reexec_linux.go: syscall.Exec of the resolved capable binary
with os.Args + envAttempted=1 -- the leftover single-instance socket
fd closes on exec, the child's ipc.Listen self-heal recovers it;
reexec_other.go errors, never reached). Marker cleared on success,
written on decline/failure with a retry hint pointing at `setup-watch`.
`Attempt(ctx, out)` is the forced twin the CLI `setup-watch` command
AND the app's SetupWatch bound method (the config editor's
"Set up full-filesystem watching" button, internal/app
watchsetupcmd.go) use: same escalation, ignores the marker +
setupEnabled, prints to out, clears the marker on success, NEVER
re-execs (caps stick to the binary and apply at the next launch) --
the in-app retry for a user who declined the startup prompt. Imports only stdlib +
golang.org/x/sys/unix + internal/platform (no config/watch cycle).
