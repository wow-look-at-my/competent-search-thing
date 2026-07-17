// Package plugin implements the searchbar's plugin system: manifests
// discovered on disk describe external providers (command subprocesses
// or HTTP endpoints) that answer queries with "virtual results"
// (calculator answers, color swatches, ...) rendered below the file
// results.
//
// This package is pure and fully headless-testable. It contains:
//
//   - the versioned JSON wire protocol (schema.go) and the sanitizer
//     that clamps and validates everything a plugin returns before it
//     reaches the UI;
//   - trigger matching (trigger.go): prefix / regex / all-queries
//     paths plus an optional focused-app gate and score boost;
//   - manifest loading and validation (manifest.go);
//   - the bang registry (bangs.go): "!name" style targeting parsed
//     from the query and resolved to a single provider;
//   - the transports (command.go: one subprocess per query; http.go:
//     POST with a shared keep-alive client), both capped and killed
//     by the per-plugin timeout;
//   - the provider registry and async dispatch pipeline
//     (registry.go): New(Options) -> Registry, Dispatch fanning each
//     query out to matching providers on their own goroutines;
//   - the builtin providers (builtin_bangs.go bang suggestions,
//     builtin_app.go app commands, builtin_apps.go installed-app
//     launcher, builtin_apps_search.go untargeted app search,
//     builtin_openwindows.go open-window search,
//     builtin_firefox.go frequently-visited Firefox sites,
//     builtin_tabs.go open Firefox tabs) -- trusted in-process
//     providers that may emit the internal-only actions.
//
// The Wails app wiring (bound methods, events, cancellation by query
// generation) lands in internal/app. File search never waits on
// plugins: plugin results arrive asynchronously and render below the
// instant file results.
package plugin
