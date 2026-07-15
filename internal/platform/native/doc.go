// Package native is the thin OS glue of the platform layer: global
// hotkey registration, cursor and monitor queries, the app-context
// source (AppSource, the per-OS appctx.Source implementation feeding
// focused/running/installed application data to the plugin system),
// and (macOS only) native window moves. Package platform holds every
// piece of logic that can be pure; this package only translates
// between those pure types and the operating system.
//
// This package deliberately has NO test files: its code needs a live
// display server / window system, which headless CI and unit-test
// environments do not have (go-toolchain skips coverage for packages
// without test files -- that exemption is used on purpose here). In
// exchange, everything in this package must stay minimal, defensive,
// and obviously correct:
//
//   - every entry point degrades instead of failing hard: no display
//     server means (ok=false / an error return), never a crash;
//   - per-OS files stick to well-worn API patterns (X11 core protocol
//     via the pure-Go jezek/xgb, user32 via syscall, Cocoa via a small
//     cgo shim);
//   - anything with branching worth testing belongs in package
//     platform, not here.
//
// Per-OS notes:
//
//   - Linux: X11 only, via jezek/xgb (no cgo in this package). Under a
//     Wayland-only session (no XWayland DISPLAY) the X connection
//     fails and callers fall back: no global hotkey, the window
//     centers on the current monitor instead of following the cursor,
//     and the app-context source reports ok=false (EWMH properties
//     are how it reads the focused/running apps; installed apps come
//     from XDG .desktop scans and keep working).
//     golang.design/x/hotkey is NOT used on Linux on purpose: its X11
//     backend panics the whole process from an init() when no X
//     display is reachable (verified in v0.6.1), which would kill the
//     app on headless/Wayland systems instead of degrading.
//   - Windows: golang.design/x/hotkey (RegisterHotKey, no cgo) plus
//     user32 syscalls for cursor/monitors and windows
//     (foreground/EnumWindows), kernel32 for process image paths, and
//     the registry uninstall keys for installed software.
//     Compile-checked only on Windows; CI builds linux/amd64.
//   - macOS: golang.design/x/hotkey (CGEventTap; needs the
//     Accessibility permission, Register errors without it) plus a
//     small Cocoa cgo shim for cursor/screens/window moves and
//     NSWorkspace app queries. The shim converts between Cocoa's
//     bottom-left-origin global coordinates and the top-left-origin
//     virtual desktop used everywhere else; installed apps are a
//     plain /Applications + ~/Applications bundle scan.
//     Compile-checked only on macOS.
package native
