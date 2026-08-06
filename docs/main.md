# main.go

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`main.go` -- glue only: embeds `frontend/dist` (go:embed) and calls
cli.Execute(app.Version, runGUI); runGUI configures the window
(frameless, always-on-top, start-hidden, hide-on-close,
non-resizable, sized by app.PreviewWindowSize() (internal/app
previewsize.go: fresh config.Load, same standalone-read pattern as
translucent.go; an explicit preview.enabled=false opt-out -- the
pane is ON by default since config v8 -- or any config error = the
configured base size window.width/height -- the app.WindowSize()
read, internal/app size.go; Load repairs zero/too-small values to
the 780x550 defaults / 320x240 floors even on error -- while the
enabled pane widens to preview.windowWidth/Height, defaults
1100x700) -- the SAME
two values are wired into app Options WindowWidth/WindowHeight so
the positioning math always matches the native window; zero
Options fall back to the defaults via the unexported
App.windowSize(), which keeps newTestApp wiring-free), binds the
App object and wires OnStartup / OnDomReady /
OnShutdown. When app.WindowTranslucent() (internal/app
translucent.go: fresh config.Load, window.translucent, any error =
false) reports true, runGUI adds BackgroundColour = zero RGBA
(alpha 0) + Linux{WindowIsTranslucent: true, WebviewGpuPolicy:
Never} for the per-pixel-alpha window; the GPU policy MUST stay
pinned to Never -- wails' nil-Linux default (#2977 workaround)
lives only in the nil branch, so an unpinned non-nil Linux block
silently flips it to OnDemand -- plus wailsOpts.Mac =
app.MacWindowOptions() (internal/app macwindow.go: fresh
config.Load, nil on flag-off/any error; flag-on =
{WindowIsTranslucent (the NSVisualEffectView BehindWindow frosted
glass -- wails v2.13.0 has NO raw setOpaque:NO passthrough, and
Spotlight is vibrancy anyway), WebviewIsTransparent
(drawsBackground=NO), Appearance tracking the theme: the light
builtin -> NSAppearanceNameVibrantLight, everything else -> the
"NSAppearanceNameVibrantDark" literal (wails ships no VibrantDark
constant; AppearanceType is a plain string passed verbatim to
[NSAppearance appearanceNamed:])}; the pure decision half
macWindowOptionsFor is headless-tested, and options.Mac is read
ONLY by the darwin frontend so linux behavior cannot change) --
and with the flag off all three fields
stay nil, byte-identical to the pre-flag call (CI screenshots run
flag-off). RunOptions.ConfigWindow instead routes to
runConfigWindow: the SETTINGS WINDOW process (900x720, ordinary
decorated/resizable/not-on-top window, Options.Mode
StartupModeConfig) -- a second process because Wails v2 gives one
window per process and the searchbar's is a hide-on-blur panel, so
settings could never be one of its modes. Zero-arg invocation boots the GUI exactly
as before the
CLI existed (CI screenshots rely on that). Deliberately has NO test
file and stays minimal (see coverage note below).
