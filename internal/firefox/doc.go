// Package firefox reads the user's OWN local Firefox data for the
// frequent-sites result section: profile discovery (profiles.ini in
// the classic, snap, and flatpak locations), the places.sqlite history
// query behind "frequently visited", and a snapshot cache so queries
// never block on the database.
//
// Everything stays on the machine: the history file is copied to a
// private temp directory, queried read-only with the pure-Go
// modernc.org/sqlite driver (no cgo -- the windows cross-build must
// keep linking), and the copy is deleted. Nothing is ever transmitted.
//
// The package is pure and fully headless-testable, like
// internal/gsettings: discovery operates on explicit base directories,
// the query takes an injectable now, and the cache takes injectable
// clock/fetch seams. The Wails app wiring (discovery over the real
// home, the plugin getter, shutdown) lands in internal/app.
package firefox
