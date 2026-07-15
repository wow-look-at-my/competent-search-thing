// Package appctx collects app-context for the plugin system: which
// application was focused when the search bar was summoned, which
// applications are running, and which are installed. Plugins that
// declare the matching context parts in their manifests receive this
// data in their request payloads.
//
// This package is pure and fully headless-testable. It contains:
//
//   - the context data types (types.go) -- deliberately independent of
//     internal/plugin's wire types; the app layer converts, keeping
//     data collection decoupled from the wire protocol;
//   - the Source interface (source.go) that the OS glue in
//     internal/platform/native implements;
//   - the Cache (cache.go): goroutine-safe snapshots over a Source --
//     a synchronous focused-app capture for the hotkey path plus
//     single-flight async refreshes for the running/installed lists,
//     so summoning the bar never waits on the OS;
//   - the XDG .desktop scanner (desktop.go) and /proc reader (proc.go)
//     used by the Linux Source, with injectable directories/roots for
//     tests.
//
// Everything degrades: a nil Source, a missing X server, or an
// unreadable directory yields empty context, never an error the app
// has to handle.
package appctx
