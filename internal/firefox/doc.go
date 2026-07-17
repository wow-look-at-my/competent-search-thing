// Package firefox reads the user's OWN local Firefox data for the
// frequent-sites and open-tabs result sections: profile discovery
// (profiles.ini in the classic, snap, and flatpak locations), the
// places.sqlite history query behind "frequently visited", the
// recovery.jsonlz4 session snapshot behind "open tabs" (decoded by an
// in-package mozLz4 / LZ4-block decoder), and snapshot caches so
// queries never block on -- or re-parse -- either file.
//
// Everything stays on the machine: the history file is copied to a
// private temp directory, queried read-only with the pure-Go
// modernc.org/sqlite driver (no cgo -- the windows cross-build must
// keep linking), and the copy is deleted; the session snapshot is a
// single read-only file read. Nothing is ever transmitted.
//
// The package is pure and fully headless-testable, like
// internal/gsettings: discovery operates on explicit base directories,
// the query takes an injectable now, and the caches take injectable
// clock/fetch/mtime seams. The Wails app wiring (discovery over the
// real home, the plugin getters, shutdown) lands in internal/app.
package firefox
