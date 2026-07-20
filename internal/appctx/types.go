package appctx

// AppInfo describes one running (or the focused) application. It
// mirrors internal/plugin's AppInfo shape but deliberately does not
// import it: the app layer converts when building request payloads,
// keeping OS data collection decoupled from the wire protocol.
type AppInfo struct {
	Name  string // application identity (WM_CLASS class, exe base name, ...)
	Exe   string // executable path; often empty (cross-user /proc, denied handles)
	Title string // window title; empty where the OS offers none
	PID   int
}

// InstalledApp describes one installed application. Exec is kept raw
// in .desktop Exec-line syntax (field codes and all); the plugin
// layer's parser handles quoting and %-codes when launching. Icon is
// the platform icon ref kept verbatim for the icon-lookup layer
// (internal/icons): the raw .desktop Icon= value on linux (a themed
// name or an absolute path), the absolute .app bundle path on darwin
// (resolved via Info.plist + .icns extraction); empty on windows for
// now (no .ico extraction).
type InstalledApp struct {
	Name string
	Exec string
	ID   string // stable identity: desktop-file name, registry subkey, bundle name
	Icon string
}

// WindowInfo describes one open top-level window (the builtin Open
// Windows search feeds on these). ID is the window-system identity --
// an X11 window id -- that ActivateWindow-style calls consume later;
// App is the owning application's identity (WM_CLASS class, exe base
// name, ...).
type WindowInfo struct {
	ID    uint32
	Title string
	App   string
	PID   int
}

// Snapshot is an immutable copy of the cached app context at one
// point in time. Mutating its slices or the Focused struct never
// affects the Cache.
type Snapshot struct {
	Focused   *AppInfo // nil when no focused app was captured
	Running   []AppInfo
	Installed []InstalledApp
	Windows   []WindowInfo
}
