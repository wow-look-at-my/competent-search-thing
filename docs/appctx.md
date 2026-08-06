# internal/appctx

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/appctx` -- app-context collection for the plugin system,
pure and headless-tested: the data types (AppInfo / InstalledApp
(incl. Icon -- the platform icon ref: .desktop Icon= on linux, the
.app bundle path on darwin, empty on windows) /
WindowInfo (ID uint32/Title/App/PID) /
Snapshot -- deliberately NOT internal/plugin's wire types, the app
layer converts), the `Source` seam (FocusedApp/RunningApps/
InstalledApps/OpenWindows) implemented by
internal/platform/native, and `Cache` (mutex-guarded, injectable
clock): `CaptureFocused` = synchronous focused-app read at
hotkey-press BEFORE the window steals focus;
`RefreshRunningAsync` / `RefreshInstalledAsync` /
`RefreshWindowsAsync` = single-flight
background refreshes that never block callers and keep old data on
failure; `EnsureFreshInstalled(ttl)` re-kicks only when the last
SUCCESSFUL installed refresh is older than ttl; `Snapshot()` =
immutable copies. A zero-value or nil-Source Cache no-ops
everything (degraded). desktop.go = XDG .desktop scanning with
injectable dirs (`DesktopDirs(getenv)`: $XDG_DATA_HOME else
~/.local/share, then $XDG_DATA_DIRS else
/usr/local/share:/usr/share, each + /applications, deduped;
`ScanDesktopDirs`: flat per-dir scan of *.desktop files, [Desktop
Entry] needs Type=Application + non-empty Name/Exec,
NoDisplay/Hidden/Terminal skipped, Exec kept RAW for the plugin
layer's parser, ID = file name, earlier dirs shadow later ones BY
PRESENCE (a Hidden local copy disables a system app), localized
Name[xx] ignored, sorted by Name). proc.go = `ProcInfo(procRoot,
pid)` readlink exe + trimmed comm, each empty on error (cross-user
/proc exe readlink fails; expected). proctree.go = `ProcTree`
(NewProcTree(root)), the production process-tree SNAPSHOT behind
the frecency cwd derivation (structurally satisfies
frecency.ProcTree; this package deliberately imports frecency only
in tests): Children from ONE memoized scan of every <root>/N/stat
ppid (parse after the LAST ')' -- comm may hold spaces/parens;
capped 8192, child lists sorted), Cwd = readlink <root>/N/cwd,
Foreground = bounded BFS for the first positive stat tpgid (the
terminal's foreground process group; a plain GUI tree has none).
The app builds a FRESH one per capture (plat.procTree factory), so
the memoized scan can never go stale; fixture-dir tested,
including the DeriveCwd end-to-end pair.
