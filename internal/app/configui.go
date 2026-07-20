package app

// The config editor surface: the bound methods behind the in-app
// config GUI (GetConfigSchema/GetConfigForEdit/SaveConfig/
// OpenConfigFile) and the summon-into-config plumbing (showConfig +
// the pendingConfig latch). The live-apply engine those methods feed
// lives in configapply.go.

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/schemas"
)

const (
	// eventConfigOpen tells the frontend to enter config-editor mode;
	// no payload. It is always emitted AFTER eventShown on a summon
	// (the frontend's app:shown handler clears and re-renders the bar;
	// an earlier config event would be clobbered).
	eventConfigOpen = "config:open"
	// eventConfigChanged reports a config.json change that did not
	// come from the GUI (an external edit hot-applied, or a failed
	// reload); payload configChangedEvent.
	eventConfigChanged = "config:changed"
)

// configChangedEvent is the eventConfigChanged payload.
type configChangedEvent struct {
	// Applied lists the sections applied live by this reload.
	Applied []string `json:"applied"`
	// Pending lists changed sections whose live applier has not
	// landed yet (empty today: the applier table is total).
	Pending []string `json:"pending"`
	// NextLaunch lists the ruled next-launch knobs that changed --
	// today only "window.translucent" (see configapply.go).
	NextLaunch []string `json:"nextLaunch,omitempty"`
	// Error carries the reload failure when the edited file could not
	// be loaded (the previous config stays applied).
	Error string `json:"error,omitempty"`
}

// ConfigForEdit is GetConfigForEdit's result: everything the editor
// needs to start a session over the current configuration.
type ConfigForEdit struct {
	// ConfigJSON is the current configuration, freshly loaded and
	// normalized, as indented JSON -- the editor's starting document.
	ConfigJSON string `json:"configJson"`
	// Path is the config file's absolute path (shown to the user; the
	// escape hatch opens it).
	Path string `json:"path"`
	// LoadWarning carries a non-fatal load problem (corrupt file fell
	// back to defaults, unresolvable dir); empty when the load was
	// clean.
	LoadWarning string `json:"loadWarning,omitempty"`
	// UnknownKeys lists JSON keys present in the on-disk file that the
	// config schema does not know ("$schema" is a known reserved key
	// and never listed). They survive hand-editing but are DROPPED by
	// a GUI save, so the editor warns and points at the open-the-file
	// escape hatch.
	UnknownKeys []string `json:"unknownKeys,omitempty"`
}

// SaveResult is SaveConfig's result: whether the save landed, and
// what the live-apply pass did with it.
type SaveResult struct {
	OK bool `json:"ok"`
	// Error is the save failure (decode or write); empty on success.
	Error string `json:"error,omitempty"`
	// Applied and Pending mirror ApplyResult for the pass that ran
	// after a successful save (Pending is empty today: the applier
	// table is total).
	Applied []string `json:"applied"`
	Pending []string `json:"pending"`
	// ApplyErrors carries per-section apply failures (the save itself
	// still succeeded; the config is on disk).
	ApplyErrors []string `json:"applyErrors,omitempty"`
	// NextLaunch lists the ruled next-launch knobs the save changed --
	// today only "window.translucent" (see configapply.go); the editor
	// surfaces it honestly, by name.
	NextLaunch []string `json:"nextLaunch,omitempty"`
}

// GetConfigSchema returns the embedded config.schema.json document;
// the frontend editor validates edits against it and derives field
// descriptions and defaults from it.
func (a *App) GetConfigSchema() string {
	return string(schemas.ConfigSchemaJSON)
}

// GetConfigForEdit loads the current configuration for the editor: the
// normalized document as indented JSON, the file path, any load
// warning, and the on-disk file's unknown keys (dropped on a GUI
// save; the editor warns about them).
func (a *App) GetConfigForEdit() ConfigForEdit {
	out := ConfigForEdit{}
	p, perr := config.Path()
	if perr == nil {
		out.Path = p
		if raw, err := os.ReadFile(p); err == nil {
			out.UnknownKeys = config.UnknownKeys(raw)
		}
	}
	cfg, lerr := config.Load()
	switch {
	case lerr != nil:
		out.LoadWarning = lerr.Error()
	case perr != nil:
		out.LoadWarning = perr.Error()
	}
	data, err := config.Encode(cfg)
	if err != nil {
		// Unreachable in practice (Config always marshals); degrade to
		// an explicit warning rather than an empty editor.
		out.LoadWarning = err.Error()
		return out
	}
	out.ConfigJSON = string(data)
	return out
}

// SaveConfig strictly decodes raw (the editor's document), preserves
// the app-managed rootsVersion stamp, normalizes, saves atomically,
// and applies the result live (see configapply.go). Unknown fields
// fail the decode -- the editor validated against the schema, so an
// unknown field here means a typo worth surfacing, never something to
// silently drop.
func (a *App) SaveConfig(raw string) SaveResult {
	var next config.Config
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return SaveResult{Error: decodeErrMsg(raw, err)}
	}
	if dec.More() {
		return SaveResult{Error: "trailing data after the config object"}
	}
	// The rootsVersion stamp is app-managed: force the on-disk value
	// so a GUI save can never reset it to 0 and re-trigger the Load
	// migrations (which would rewrite roots/excludes underneath the
	// user). Load always yields the current stamp -- even a corrupt
	// file falls back to stamped defaults -- with the build's own
	// stamp as the belt-and-suspenders floor.
	cur, _ := config.Load()
	next.RootsVersion = cur.RootsVersion
	if next.RootsVersion <= 0 {
		next.RootsVersion = config.CurrentRootsVersion()
	}
	next.Normalize()
	if err := config.Save(next); err != nil {
		return SaveResult{Error: err.Error()}
	}
	// Record the saved bytes' checksum so the config-dir watcher can
	// tell this self-write from an external edit (configapply.go).
	// Encode returns exactly what Save wrote.
	if data, err := config.Encode(next); err == nil {
		a.setLastSavedSum(sha256.Sum256(data))
	}
	res := a.applyConfig(&next, "gui-save")
	return SaveResult{OK: true, Applied: res.Applied, Pending: res.Pending, ApplyErrors: res.Errors, NextLaunch: res.NextLaunch}
}

// OpenConfigFile opens config.json itself with the operating system's
// default handler -- the GUI escape hatch for hand edits (unknown
// keys, opaque plugin settings). External edits hot-apply through the
// config-dir watcher, so the GUI flow stays live either way.
func (a *App) OpenConfigFile() error {
	return a.openConfigFile()
}

// decodeErrMsg turns a strict-decode failure into a user-readable
// message: syntax errors get a line number, type and unknown-field
// errors already name the offending field.
func decodeErrMsg(raw string, err error) string {
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		return fmt.Sprintf("line %d: %v", lineOfOffset(raw, syn.Offset), err)
	}
	var typ *json.UnmarshalTypeError
	if errors.As(err, &typ) {
		return fmt.Sprintf("line %d: field %q wants a %s, got %s",
			lineOfOffset(raw, typ.Offset), typ.Field, typ.Type, typ.Value)
	}
	return err.Error()
}

// lineOfOffset maps a byte offset into raw to its 1-based line.
func lineOfOffset(raw string, off int64) int {
	if off > int64(len(raw)) {
		off = int64(len(raw))
	}
	return 1 + strings.Count(raw[:off], "\n")
}

// showConfig summons the bar into config-editor mode: the IPC/CLI
// "config" command, the !config bang, and the tray "Open config" item
// all funnel here. Before the frontend can render, the summon is
// latched (pendingShow + pendingConfig, the toggle pattern) and
// DomReady executes it; a hidden bar takes the exact
// capture-context-then-show path other summons use; a visible bar
// just gets the mode event. eventConfigOpen always FOLLOWS eventShown.
// Goroutine-safe and pre-Startup-safe like the other IPC handlers.
func (a *App) showConfig() {
	a.mu.Lock()
	if !a.domReady {
		a.pendingShow = true
		a.pendingConfig = true
		a.mu.Unlock()
		return
	}
	visible := a.visible
	a.mu.Unlock()
	if !visible {
		a.captureAppContext()
		a.showOnCursorDisplay()
	}
	a.emitEvent(eventConfigOpen)
}

// setLastSavedSum records the checksum of the last GUI-saved
// config.json bytes; getLastSavedSum reads it back. The config-dir
// watcher compares the on-disk file against it to skip re-applying
// the app's own writes.
func (a *App) setLastSavedSum(sum [sha256.Size]byte) {
	a.cfgMu.Lock()
	a.lastSavedSum = sum
	a.cfgMu.Unlock()
}

func (a *App) getLastSavedSum() [sha256.Size]byte {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.lastSavedSum
}

// startConfigState seeds the live-apply engine's baseline with the
// configuration the app started under (a fresh Load, the standalone
// read pattern translucent.go/buildStats use; the load error, if any,
// was already reported by main.go's own Load). Runs once, from
// Startup, BEFORE the theme/config watcher comes up so an external
// edit always diffs against a real baseline.
func (a *App) startConfigState() {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("config: editor baseline: %v (diffing against the returned defaults)", err)
	}
	a.cfgMu.Lock()
	a.cfgCurrent = &cfg
	a.cfgMu.Unlock()
}
