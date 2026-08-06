# internal/ipc

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/ipc` -- the single-instance unix-socket IPC layer, pure
and headless-tested. SocketPath: $COMPETENT_SEARCH_SOCKET override,
else $XDG_RUNTIME_DIR/competent-search-thing.sock, else a per-uid
name under os.TempDir(); ConfigSocketPath is its twin for the
SETTINGS WINDOW process ($COMPETENT_SEARCH_CONFIG_SOCKET, else the
same rules over competent-search-thing-config.sock) -- a separate
socket so the two processes are separately single-instanced.
ONE request per conn (2s conn deadline,
4 KiB line cap), one newline-terminated JSON object each way --
JSON is the ONLY wire shape (the legacy v1 line protocol is
DELETED): request
{"cmd":"toggle|show|hide|config|quit|version|ping"}
with unknown JSON fields IGNORED on both sides (the documented
tolerance contract), response {"ok":true} (ping) /
{"ok":true,"version":v,"build":b} / {"ok":true,"accepted":cmd} /
{"ok":false,"error":"not ready"|"unknown command"|
"invalid request"}; a request line that does not parse as JSON --
incl. the old protocol's bare command words and arbitrary garbage
-- earns "invalid request" and runs nothing (pinned by
TestNonJSONRequestsAreRejected; the bare "quit" word included, a8
parity). The version reply's `build` is the vcs-revision stamp
(OwnBuild: 12-char prefix via ReadBuildInfo, empty on unstamped dev
builds, omitted then) -- the version-SKEW discriminator, because
app.Version is a constant that never bumps across releases; the
tolerance contract is what made adding the field safe both ways.
Commands answer "not ready"
until SetHandlers wires the app (nil handler members stay not
ready; version/ping always answer), and the ack is
written BEFORE the toggle/show/hide/config/quit handler runs --
ack = accepted,
not completed, so an app whose main thread is briefly stalled
(startup indexing) can never time the client out; the handler then
runs on the same conn goroutine, so Close still waits for in-flight
handlers. Handlers.Quit is the new-instance-wins handshake's
graceful half (internal/app wires it to the same runtime-quit seam
as the !quit builtin). The config command (config_cmd_test.go)
rides handlerFor
exactly like the others, so only its own JSON shapes are pinned.
Send(path, cmd, timeout) speaks JSON and returns a parsed
Reply{OK, Accepted, Version, Build, Err, Raw, Parsed} + NotReady()
+ UnknownCommand() (an older JSON daemon rejecting a newer
command) -- ALL
reply parsing lives here, callers branch on fields, never wire
strings. A reply line that does not parse as JSON -- incl. every
pre-JSON daemon reply line -- comes back in-band (Raw set, Parsed
false, OK false, empty Err), which callers classify as "no healthy
instance"
(TestSendReturnsOldDaemonReplyInBand pins one conn, no retry);
only transport failures are errors, dial
failures wrapped in ErrNotRunning (test with IsNotRunning); timeout
is ONE absolute deadline across dial + exchange. SELF-HEALING
LISTEN (takeover.go; the death-race incident fix -- a client racing
a crashing daemon's death read "connection reset by peer" and hit a
dead-end exit 1): Listen = ListenWith(path, version, zero
ListenOptions); on EADDRINUSE -- and ONLY there; the no-file cold
start stays one syscall, zero probes -- it takes a flock on
<socket>.lock (serializes concurrent deciders, ~3s cap, kernel-
released on death so never stale; timeout concedes
ErrAlreadyRunning; windows: no-op stub) and probes the holder with
up to 3 VERSION round-trips (500ms per attempt, 250ms apart; one
round-trip carries health AND the skew data), because a bare unix
connect(2) succeeds as soon as the kernel queues the backlog entry
-- connect success is NOT liveness (the old connect-only probe
called a seconds-wide dying daemon "already running"). Verdicts: a
parsed JSON reply = healthy -> same version+build =
ErrAlreadyRunning (byte-identical caller contract; "not ready"
daemons answer version, so booting is healthy), different =
version skew -> new instance wins; connect refused/ENOENT = dead
-> remove + retry once (today's stale-file recovery, now explicit);
a raw non-JSON reply = pre-JSON legacy daemon -> new instance wins
(no quit exists there); all attempts reset/EOF/timeout =
unresponsive -> takeover. The REPLACE ladder: skewed holders first
get {"cmd":"quit"} (ack-first means the reply precedes their
shutdown; unknown-command/not-ready = older daemon -> fall
through), then SIGTERM to the EXACT pid read off the probe
connection's peer credentials (SO_PEERCRED linux /
LOCAL_PEERPID+LOCAL_PEERCRED darwin / none elsewhere --
peercred_*.go; credentials are the LISTENER's captured at connect
time, so a backlog-queued conn to a frozen daemon still yields
them) gated on same-uid + kill(pid,0) liveness + (linux) a
/proc exe/comm identity check (procIdentAt; " (deleted)" tolerated,
comm compared 15-byte-truncated; POSITIVE mismatch or foreign uid =
loud refusal + ErrAlreadyRunning, absent metadata fails open --
darwin has no /proc and same-uid+liveness is its floor). NEVER any
pattern kill. Then a bounded release wait (2s, poll 100ms: file
gone / pid ESRCH / connect refused -- wait BEFORE bind, because a
gracefully quitting old daemon unlinks the path itself), then
unlink+bind REGARDLESS, logging a survivor pid loudly; a bind
losing to a concurrent cold-start binder concedes
ErrAlreadyRunning. Everything logs, unconditionally ("ipc: ..."
inline-metric lines; ListenOptions.Logf, nil = log.Printf, the cli
wires log.Printf). ListenOptions also carries Build (empty =
OwnBuild), Kill (tests MUST record -- in-process fake daemons
report the test's own pid) and the ProbeTimeout/ProbeGap/
ReleaseWait knobs (test speed), plus unexported dial/sleep/
peerCred/procIdent/getuid/ownBase seams; at most ONE integration
test signals a real process, an owned re-exec'd child
(integration_test.go). After listening the file is
chmodded 0600 (filesystem perms are the only auth). Close is
idempotent + nil-safe: stops the accept loop, unlinks the socket
VERIFIED (lstat identity captured at bind -- fstat of the fd sees
only the anonymous sockfs inode -- so a force-replaced zombie that
un-wedges into its own Close never unlinks a successor's live
socket; windows keeps plain unlink-on-close), waits for in-flight
conns. Handlers run on conn
goroutines and must be goroutine-safe. Deliberately NO schema in
schemas/ (an internal two-party protocol, like history.json).
