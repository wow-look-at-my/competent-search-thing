package appctx

// Source produces app-context data from the operating system. The
// implementations live in internal/platform/native (X11/EWMH on
// Linux, user32 + the registry on Windows, NSWorkspace on macOS);
// this package only defines the seam so the Cache and its tests stay
// headless.
//
// Every method reports ok=false when the data cannot be collected
// (no X server, denied API, ...) -- never an error, never a panic;
// the feature simply degrades.
type Source interface {
	// FocusedApp identifies the currently focused application. Called
	// synchronously at hotkey-press, before the bar window steals
	// focus, so it must be fast.
	FocusedApp() (AppInfo, bool)

	// RunningApps lists the applications that currently have windows,
	// deduplicated per process.
	RunningApps() ([]AppInfo, bool)

	// InstalledApps lists the installed applications (terminal-only
	// apps pre-filtered where the OS marks them).
	InstalledApps() ([]InstalledApp, bool)

	// OpenWindows lists the open top-level windows with their titles,
	// for the builtin window-title search. ok only where windows can
	// genuinely be enumerated: X11 today; Wayland offers no sanctioned
	// way to see other apps' windows, and windows/darwin are not
	// implemented yet.
	OpenWindows() ([]WindowInfo, bool)
}
