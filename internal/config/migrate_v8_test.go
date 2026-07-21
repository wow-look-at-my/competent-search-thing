package config

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// The v8 step: the preview pane turns ON by default. A pre-v8 stored
// preview.enabled=false is the plain-bool era's machine handwriting
// (every app-written save serialized the field), NOT a user opt-out
// -- the feature was opt-in, and deliberate off was expressed by
// never opting in -- so it is reset to on with a loud note. A false
// stamped at rootsVersion >= 8 is a real post-flip opt-out and is
// never revisited.

func TestMigrateV8PreflipFalseResetsToOn(t *testing.T) {
	setConfigDir(t)
	p := writeConfig(t, `{
		"roots": ["/data"],
		"rootsVersion": 7,
		"excludes": [".git"],
		"preview": {"enabled": false, "textMaxKB": 512}
	}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t, Bool(true), c.Preview.Enabled, "the machine-written pre-flip false is reset to on")
	require.Equal(t, 512, c.Preview.TextMaxKB, "the rest of the preview section is untouched")

	require.Len(t, c.MigrationNotes, 1)
	require.Contains(t, c.MigrationNotes[0], "preview pane is now ON by default")
	require.Contains(t, c.MigrationNotes[0], "was reset", "the reset variant of the note names the overridden value")
	require.Contains(t, c.MigrationNotes[0], "preview.enabled=false", "the note names the opt-out")

	// Persisted: the file now carries the v8 stamp and the repaired
	// explicit true, so the next load migrates nothing.
	doc := readRawConfig(t, p)
	require.EqualValues(t, currentRootsVersion, doc["rootsVersion"])
	require.Equal(t, true, doc["preview"].(map[string]any)["enabled"])

	again, err := Load()
	require.NoError(t, err)
	require.Empty(t, again.MigrationNotes, "the second load has nothing left to migrate")
	require.Equal(t, Bool(true), again.Preview.Enabled)
}

func TestMigrateV8AbsentKeyTurnsOnWithNote(t *testing.T) {
	setConfigDir(t)
	p := writeConfig(t, `{"roots": ["/data"], "rootsVersion": 7, "excludes": [".git"]}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, Bool(true), c.Preview.Enabled, "absent = never chosen = the new default (on)")
	require.Len(t, c.MigrationNotes, 1)
	require.Contains(t, c.MigrationNotes[0], "preview pane is now ON by default")
	require.NotContains(t, c.MigrationNotes[0], "was reset", "nothing was overridden; the note only announces the new default")
	require.Contains(t, c.MigrationNotes[0], "preview.enabled=false")

	doc := readRawConfig(t, p)
	require.Equal(t, true, doc["preview"].(map[string]any)["enabled"])
}

func TestMigrateV8PreflipTrueStaysQuiet(t *testing.T) {
	setConfigDir(t)
	writeConfig(t, `{
		"roots": ["/data"],
		"rootsVersion": 7,
		"excludes": [".git"],
		"preview": {"enabled": true}
	}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, Bool(true), c.Preview.Enabled)
	require.Empty(t, c.MigrationNotes, "an already-on pane sees no behavior change and earns no note")
}

func TestMigrateV8PostflipFalseIsRespected(t *testing.T) {
	setConfigDir(t)
	// A false stamped at the current version is a real opt-out made
	// AFTER the flip: no migration runs, Normalize never repairs an
	// explicit false, and the pane stays off forever.
	raw := `{"roots": ["/data"], "rootsVersion": ` +
		strconv.Itoa(currentRootsVersion) + `, "excludes": [".git"], "preview": {"enabled": false}}`
	p := writeConfig(t, raw)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, Bool(false), c.Preview.Enabled, "a post-flip opt-out is respected forever")
	require.False(t, Enabled(c.Preview.Enabled))
	require.Empty(t, c.MigrationNotes)

	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, raw, string(data), "an already-current file is not rewritten")
}

// TestMigrateV8DirectDriver pins the three switch arms on the bare
// struct (the migrateRootsFor direct-driver convention): nil ->
// announce only, false -> reset + announce, true -> silent.
func TestMigrateV8DirectDriver(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enabled   *bool
		wantNote  string
		wantReset bool
	}{
		{name: "absent announces", enabled: nil, wantNote: "preview pane is now ON by default"},
		{name: "false resets", enabled: Bool(false), wantNote: "was reset", wantReset: true},
		{name: "true is silent", enabled: Bool(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{RootsVersion: 7, Roots: []string{"/"}, Preview: PreviewConfig{Enabled: tc.enabled}}
			require.True(t, c.migrateRootsFor("linux", nil))
			require.Equal(t, currentRootsVersion, c.RootsVersion)
			if tc.wantNote == "" {
				require.Empty(t, c.MigrationNotes)
				require.Equal(t, tc.enabled, c.Preview.Enabled, "an explicit true passes through")
				return
			}
			require.Len(t, c.MigrationNotes, 1)
			require.Contains(t, c.MigrationNotes[0], tc.wantNote)
			if tc.wantReset {
				require.Nil(t, c.Preview.Enabled, "the pre-flip false is dropped for Normalize to repair to true")
			}
		})
	}
}

// TestMigrateV8RawFalseRoundTrip drives the raw-bytes shape: the
// *bool parse alone distinguishes present-false from absent, so the
// step needs no raw-document read -- pinned here so a future struct
// reshape that loses the distinction fails loudly.
func TestMigrateV8RawFalseRoundTrip(t *testing.T) {
	var c Config
	require.NoError(t, json.Unmarshal([]byte(`{"rootsVersion": 7, "preview": {"enabled": false}}`), &c))
	require.NotNil(t, c.Preview.Enabled, "present-false parses as a non-nil pointer")
	require.False(t, *c.Preview.Enabled)

	var absent Config
	require.NoError(t, json.Unmarshal([]byte(`{"rootsVersion": 7}`), &absent))
	require.Nil(t, absent.Preview.Enabled, "absent parses as nil")
}
