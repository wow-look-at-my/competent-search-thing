// Package schemas embeds the formal JSON Schema documents shipped in
// this directory so the Go side can serve them without a disk
// dependency. The app's GetConfigSchema bound method hands the config
// schema to the frontend config editor, which validates edits
// client-side and derives field descriptions/defaults from it. The
// package deliberately contains no functions or statements -- it is
// pure embedded data.
package schemas

import _ "embed"

// ConfigSchemaJSON is the embedded config.schema.json document
// (draft 2020-12) -- the authoritative shape of config.json, kept in
// lockstep with internal/config by that package's schema tests.
//
//go:embed config.schema.json
var ConfigSchemaJSON []byte
