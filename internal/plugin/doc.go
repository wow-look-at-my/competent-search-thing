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
//     from the query and resolved to a single provider.
//
// Transports, the provider registry, and the dispatch pipeline build
// on these types in later files. File search never waits on plugins:
// plugin results arrive asynchronously and render below the instant
// file results.
package plugin
