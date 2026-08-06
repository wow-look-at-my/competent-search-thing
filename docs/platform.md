# internal/platform

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/platform` -- the PURE half of the platform layer, fully
unit-tested headlessly: `ParseHotkey` ("alt+space", "ctrl+shift+k";
modifiers ctrl/control, shift, alt/option, super/win/cmd/meta; keys
space/tab/enter/return/esc/escape/a-z/0-9/f1-f12/arrows; unknown
token -> error naming it) into an OS-neutral `Hotkey{Mods,Key}`;
`StableExecutable(exe, args0)` (stablepath.go: the stable spelling
of the running binary's path for anything that outlives the process
-- exec.LookPath(base) hit kept UNRESOLVED, else the structural
Homebrew candidates (brewpath.go: `ParseBrewCellar` splits an
absolute <prefix>/Cellar/<formula>/<version>/<rest> path at its
LAST separator-bounded "Cellar" component, prefix read from the
path itself -- no hardcoded prefix list; candidates = linked
<prefix>/<rest> then opt <prefix>/opt/<formula>/<rest>, and they
precede args0 because in the gsd-boot context args0 IS the
versioned Cellar path), else abs/Abs-resolved args0, else exe,
every candidate same-binary-guarded via os.Stat+os.SameFile so a
foreign same-named binary never wins; tested with real tempdir
trees, symlinks and t.Setenv(PATH), no seams);
`ResolvedExecutable(path)` (resolvedpath.go: the COUNTERPART --
Abs + EvalSymlinks to the real regular file, ok=false on any
failure; consumed ONLY by the app's setcap grant hint, because
setcap refuses symlinks while StableExecutable deliberately
prefers them; same real-tempdir test style);
geometry (`Rect`, `Display{Rect,Work,Primary}`, `PickDisplay`,
`BarPosition` = centered, top at H/3 - winH/3, clamped;
`DisplayForWindow` by window center; `WailsPosition` translating
absolute coords to Wails' current-monitor-relative
WindowSetPosition; `Display.UsableRect` = Work when it has area
else Rect (linux Xinerama fills Work with the full geometry), and
`ClampSize(area, w, h, minW, minH)` = the ONE clamp-to-screen rule
every window-sizing path shares -- area-capped per axis, floors
win over a pathological tiny area, zero-area axes unclamped); open/reveal argv construction (`OpenCommands` /
`RevealCommands`: linux xdg-open / dbus-send --print-reply
FileManager1.ShowItems with xdg-open-parent fallback, darwin open /
open -R, windows rundll32 FileProtocolHandler / explorer /select,)
and `Launcher` (injectable `Run`/`Start` seams + `Logf`, BOTH
env-carrying now: Start(argv, extraEnv) returns (pid, wait, err)
and appends extraEnv to the inherited child environment -- nil =
byte-identical old behavior, never os.Setenv -- while
OpenEnv/RevealEnv/Launch thread the launch-credential env through
(Open/Reveal stay as nil-env wrappers); RevealCommands takes the
startupID injected into the ShowItems startup-id argument ("" =
the old call); `Launch(argv, extraEnv)` runs ONE resolved handler
command line under the same observed-grace semantics and returns
the child pid for the raise watcher; every
spawn logs its exact argv; Open/Reveal observe the child for a 1.5s
grace window -- a non-zero exit inside it returns an error with
captured stderr (unlinked-temp-file capture, never a pipe a
grandchild could block or SIGPIPE on), logs, and falls through to
the next candidate; a child still running at expiry is success,
reaper-logged if it fails later; `Run` stays fire-and-forget for
plugin run_command but logs spawns and reaper-logs failures); session
detection (session.go: `DetectSession(getenv)` -- XDG_SESSION_TYPE
"wayland"/"x11" wins, else WAYLAND_DISPLAY, else DISPLAY, else
unknown; Desktop = raw XDG_CURRENT_DESKTOP;
`Session.IsGNOME` = any colon-separated segment equals "gnome"
case-insensitively); and the fps meter's data shapes (power.go:
`PowerInfo` {MaxFPS, LowPowerMode, ThermalState} -- the darwin
display/power probe result -- `ThermalStateString`, and
`UncapStatus` + String(), the WebKit near-60 uncap outcomes kept
in lockstep with platform_darwin.h's CS_UNCAP_* codes).
