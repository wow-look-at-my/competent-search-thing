# internal/launch

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/launch` -- the pure decision half of "focus and raise on
launch" (README "Focus and raise on launch" holds the user-facing
capability matrix), exhaustively unit-tested headless; the
OS/display glue lives behind internal/platform/native seams wired
by internal/app launch.go. launch.go: `Target`/`ClassifyTarget`
(URL = scheme+host parse, else file path; URI = file:// form;
IsDir steers handler resolution to inode/directory), `Handler`
(DesktopID/Exec/WMClass/Exe/DBusActivatable/StartupNotify/
Terminal), `Credential` + kinds (none / x11-sn / wayland-gdk /
wayland-xdg), `ShouldMint` (unresolved handlers always mint;
resolved ones only with StartupNotify or DBusActivatable -- GLib's
dangling-busy-cursor gating), `CredentialEnv` (DESKTOP_STARTUP_ID
+ XDG_ACTIVATION_TOKEN, BOTH carrying the same id -- launchees
pick whichever their toolkit understands, and Firefox >= 108
forwards it through its remoting), `LogLine` (the one-per-launch
log format), `ValidDesktopID` (bare *.desktop name). exec.go:
`ExpandExec` -- .desktop Exec tokenization (parseDesktopExec-
compatible quoting) with target substitution: %f/%F = raw path
(URL verbatim for URL targets, documented divergence), %u/%U =
URI, %% literal, %i/%c/%k + deprecated codes drop, unknown codes
keep their percent, NO target code = target appended last
(xdg-open-style divergence from GLib's silent no-file launch), and
an Exec whose program token does not survive a target-less
expansion is unlaunchable (nil -- the target must never become
argv[0]). dbus.go: `ApplicationDBusCall` derives the
org.freedesktop.Application call (bus name = id sans .desktop,
validated; path = "."->"/" + "-"->"_"; Open with URIs / Activate;
platform-data = desktop-startup-id + activation-token) and
`DBusActivate` performs it over a private never-autolaunched
session-bus conn (ctx-bounded; the method call itself
D-Bus-activates the service -- that IS the launch); tested against
a throwaway dbus-daemon like internal/portal. watcher.go: the
X-side raise watcher -- `XWindow`/`XState` (stacking order,
bottom-to-top), `NewIdentity` (pid + startup id + lowercased
WM_CLASS hints from StartupWMClass/exe base/argv0 base),
`RunWatcher` polls (default 6s deadline / 200ms interval,
ctx-bounded): an ACTIVE window matching the identity ends the
watch silently (self-raised; never double-activate), a NEW window
(not in the Before snapshot) matching pid/startup-id/class is
activated once (topmost match wins), and at the deadline the
most-recently-used EXISTING window matching the class hints
(highest _NET_WM_USER_TIME, ties toward the top of the stack) is
raised -- the editor-tab-into-running-instance fix -- else one
quiet give-up log. sn.go: `SNRemoveMessage` (the libsn `remove:
ID="..."` wire string, backslash-escaped, NUL-terminated) +
`SNChunks` (20-byte ClientMessage chunks, last zero-padded) behind
native.RemoveStartupSequence.
