package app

// The config schema sidecar: on every startup the embedded
// config.schema.json is written next to config.json, so the
// "$schema": "./config.schema.json" reference Save stamps into the
// config (see internal/config.SchemaRef) resolves to a schema that
// version-matches the RUNNING binary -- editors get validation and
// completion for exactly the fields this build understands. The write
// is skipped when the on-disk bytes already match, and the sidecar's
// file name never equals config.json, so the config-dir watcher's
// hot-apply hook (which reacts to the config.json path only) can
// never loop on it.

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/schemas"
)

// schemaSidecarName is the sidecar's file name inside the config
// directory -- the target of config.SchemaRef's relative reference.
const schemaSidecarName = "config.schema.json"

// startSchemaSidecar refreshes the schema sidecar once, at Startup
// (before the config-dir watcher comes up, so the write predates any
// watching). Best effort: a failure logs and the app runs on -- the
// sidecar only feeds editor tooling.
func (a *App) startSchemaSidecar() {
	wrote, err := writeSchemaSidecar()
	if err != nil {
		log.Printf("config: schema sidecar: %v", err)
		return
	}
	if wrote {
		log.Printf("config: refreshed the schema sidecar (%s)", schemaSidecarName)
	}
}

// writeSchemaSidecar writes the embedded config schema to
// <configDir>/config.schema.json unless the file already holds
// exactly those bytes. The write is atomic (temp-file-then-rename,
// the config.Save pattern) at the sidecar's documented 0644 perms.
// It reports whether a write happened.
func writeSchemaSidecar() (bool, error) {
	dir, err := config.Dir()
	if err != nil {
		return false, err
	}
	path := filepath.Join(dir, schemaSidecarName)
	want := schemas.ConfigSchemaJSON
	if have, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(have, want) {
		return false, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".schema-*.tmp")
	if err != nil {
		return false, fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	_, err = tmp.Write(want)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name)
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}
