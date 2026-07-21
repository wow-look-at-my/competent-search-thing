package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The TestMigrateV6* tests pin the ranking-defaults flip.
// search.telemetry -- the local ranking log -- is ALWAYS ON now:
// every old key (enabled, retainQueries) is dropped outright, an
// explicit enabled:false included (overruled by design; the log is
// private by staying on the machine). The learned layers
// (search.priors, search.arbiter) turn on by default: their old
// opt-in "enabled" keys carry the same spelling as the v7-era
// affirmative switches, so explicit values parse straight through
// and an explicit enabled:false survives as the opt-out. A config
// migrating from below 6 lands directly on the v7 shape (the v7
// polarity step runs in the same pass). The raw-bytes matrix drives
// migrateRootsFor directly (the TestMigrateV4* convention); the
// Load-driven test proves the rewrite drops every old key.

// v6Raw builds a v5-stamped raw config whose search section is
// exactly section -> body (empty section = search absent entirely).
func v6Raw(section, body string) []byte {
	search := "{}"
	if section != "" {
		search = `{"` + section + `": ` + body + `}`
	}
	return []byte(`{"roots": ["/"], "rootsVersion": 5, "excludes": [".git"], "search": ` + search + `}`)
}

// telemetryAlwaysOnNote returns the always-on note, or "" when none
// fired.
func telemetryAlwaysOnNote(c *Config) string {
	for _, n := range c.MigrationNotes {
		if strings.Contains(n, "ranking telemetry is now always on") {
			return n
		}
	}
	return ""
}

func TestMigrateV6TelemetryAlwaysOn(t *testing.T) {
	cases := map[string]struct {
		body     string
		wantNote bool
	}{
		"absent":              {"", true},
		"explicit false":      {`{"enabled": false}`, true}, // overruled by design
		"explicit true":       {`{"enabled": true}`, false}, // was already on
		"new-shape leftovers": {`{"maxSizeKB": 128}`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw := v6Raw("telemetry", tc.body)
			if tc.body == "" {
				raw = v6Raw("", "")
			}
			var c Config
			require.NoError(t, json.Unmarshal(raw, &c))
			require.True(t, c.migrateRootsFor("linux", raw))
			require.Equal(t, currentRootsVersion, c.RootsVersion)
			note := telemetryAlwaysOnNote(&c)
			if tc.wantNote {
				require.Contains(t, note, "local-only and never leaves this machine",
					"a previously-off log announces the flip in the owner's words")
			} else {
				require.Empty(t, note, "an already-on log has nothing to announce")
			}
		})
	}
}

func TestMigrateV6LearnedLayersMatrix(t *testing.T) {
	sections := []struct {
		name    string
		enabled func(c *Config) *bool
	}{
		{"priors", func(c *Config) *bool { return c.Search.Priors.Enabled }},
		{"arbiter", func(c *Config) *bool { return c.Search.Arbiter.Enabled }},
	}
	for _, sec := range sections {
		full := "search." + sec.name
		t.Run(sec.name+" absent turns on with a note", func(t *testing.T) {
			raw := v6Raw("", "")
			var c Config
			require.NoError(t, json.Unmarshal(raw, &c))
			require.True(t, c.migrateRootsFor("linux", raw))
			require.True(t, Enabled(sec.enabled(&c)), "absent old key = the new default (on)")
			require.Len(t, c.MigrationNotes, 3, "the telemetry note, the learned-layers note, and the v8 preview note")
			require.Contains(t, c.MigrationNotes[1], "on by default")
			require.Contains(t, c.MigrationNotes[1], full)
		})
		t.Run(sec.name+" explicit enabled:false is preserved", func(t *testing.T) {
			raw := v6Raw(sec.name, `{"enabled": false}`)
			var c Config
			require.NoError(t, json.Unmarshal(raw, &c))
			require.True(t, c.migrateRootsFor("linux", raw))
			require.False(t, Enabled(sec.enabled(&c)), "an explicit opt-out is respected")
			var offNote string
			for _, n := range c.MigrationNotes {
				if strings.Contains(n, "opt-outs were preserved") {
					offNote = n
				}
			}
			require.Contains(t, offNote, full, "the preserved opt-out is announced")
			for _, n := range c.MigrationNotes {
				if strings.Contains(n, "on by default") {
					require.NotContains(t, n, full, "an opted-out section is never announced as turned on")
				}
			}
		})
		t.Run(sec.name+" explicit enabled:true stays on without a flip note", func(t *testing.T) {
			raw := v6Raw(sec.name, `{"enabled": true}`)
			var c Config
			require.NoError(t, json.Unmarshal(raw, &c))
			require.True(t, c.migrateRootsFor("linux", raw))
			require.True(t, Enabled(sec.enabled(&c)), "already opted in stays on")
			for _, n := range c.MigrationNotes {
				if strings.Contains(n, "on by default") {
					require.NotContains(t, n, full, "no behavior flip = no flip announcement")
				}
			}
		})
	}
}

func TestMigrateV6EraDisabledShapeSurvives(t *testing.T) {
	// A pre-v6-stamped file already carrying the v6-era shape's
	// disabled:true keeps its opt-out (the v7 polarity step converts
	// the key) and earns no flip note.
	raw := v6Raw("priors", `{"disabled": true}`)
	var c Config
	require.NoError(t, json.Unmarshal(raw, &c))
	require.True(t, c.migrateRootsFor("linux", raw))
	require.False(t, Enabled(c.Search.Priors.Enabled), "the opt-out survives, converted to enabled:false")
	joined := strings.Join(c.MigrationNotes, "\n")
	require.Contains(t, joined, "migrated search.priors.disabled=true -> search.priors.enabled=false",
		"the v7 polarity step announces the key conversion")
	for _, n := range c.MigrationNotes {
		if strings.Contains(n, "on by default") {
			require.NotContains(t, n, "search.priors")
		}
	}
}

func TestMigrateV6RetainQueriesDropNotes(t *testing.T) {
	// Present-and-false: the behavior changes (queries are recorded
	// now), and the note says so.
	rawFalse := v6Raw("telemetry", `{"enabled": true, "retainQueries": false}`)
	var c Config
	require.NoError(t, json.Unmarshal(rawFalse, &c))
	require.True(t, c.migrateRootsFor("linux", rawFalse))
	joined := strings.Join(c.MigrationNotes, "\n")
	require.Contains(t, joined, "retainQueries was removed: query text is now always recorded")

	// Present-and-true: only the key drop is announced.
	rawTrue := v6Raw("telemetry", `{"enabled": true, "retainQueries": true}`)
	var c2 Config
	require.NoError(t, json.Unmarshal(rawTrue, &c2))
	require.True(t, c2.migrateRootsFor("linux", rawTrue))
	joined = strings.Join(c2.MigrationNotes, "\n")
	require.Contains(t, joined, "retainQueries was removed (query text was already recorded")

	// Absent: no retainQueries note at all.
	rawAbsent := v6Raw("", "")
	var c3 Config
	require.NoError(t, json.Unmarshal(rawAbsent, &c3))
	require.True(t, c3.migrateRootsFor("linux", rawAbsent))
	require.NotContains(t, strings.Join(c3.MigrationNotes, "\n"), "retainQueries")
}

// TestMigrateV6LoadRewritesOldKeys drives the whole path through
// Load: the old opt-in keys and retainQueries vanish from the
// rewritten file (UnknownKeys stays clean), the telemetry opt-out is
// overruled (always on -- only maxSizeKB survives), the priors
// opt-out lands as enabled:false on disk (the v7 shape), and a
// second load migrates nothing.
func TestMigrateV6LoadRewritesOldKeys(t *testing.T) {
	setConfigDir(t)
	p := writeConfig(t, `{
		"roots": ["/"],
		"rootsVersion": 5,
		"excludes": [".git"],
		"search": {
			"telemetry": {"enabled": false, "maxSizeKB": 128, "retainQueries": false},
			"priors": {"enabled": false},
			"arbiter": {}
		}
	}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.Equal(t, 128, c.Search.Telemetry.MaxSizeKB, "the configured size cap survives the flip")
	require.Equal(t, Bool(false), c.Search.Priors.Enabled, "the explicit priors opt-out is preserved")
	require.Equal(t, Bool(true), c.Search.Arbiter.Enabled, "the absent section gets the new default (on)")
	require.Equal(t, []string{".git"}, c.Excludes, "the v6 step never touches excludes")

	joined := strings.Join(c.MigrationNotes, "\n")
	require.Contains(t, joined, "ranking telemetry is now always on")
	require.Contains(t, joined, "on by default")
	require.Contains(t, joined, "search.arbiter")
	require.Contains(t, joined, "opt-outs were preserved")
	require.Contains(t, joined, "search.priors")
	require.Contains(t, joined, "retainQueries was removed")

	// The rewrite drops every old key: UnknownKeys sees nothing, and
	// the new shape is on disk.
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Empty(t, UnknownKeys(data), "no old keys survive the Save-back")
	require.NotContains(t, string(data), "retainQueries")
	doc := readRawConfig(t, p)
	search := doc["search"].(map[string]any)
	tel := search["telemetry"].(map[string]any)
	require.Equal(t, map[string]any{"maxSizeKB": float64(128)}, tel,
		"telemetry keeps ONLY the size bound -- no switch of either polarity exists")
	require.Equal(t, map[string]any{"enabled": false}, search["priors"].(map[string]any))
	require.Equal(t, map[string]any{"enabled": true}, search["arbiter"].(map[string]any))

	again, err := Load()
	require.NoError(t, err)
	require.Empty(t, again.MigrationNotes, "the second load has nothing left to migrate")
	require.Equal(t, Bool(false), again.Search.Priors.Enabled)
}

func TestMigrateV6Idempotent(t *testing.T) {
	raw := v6Raw("priors", `{"enabled": false}`)
	var c Config
	require.NoError(t, json.Unmarshal(raw, &c))
	require.True(t, c.migrateRootsFor("linux", raw))
	require.False(t, c.migrateRootsFor("linux", raw), "stamped 6 = nothing left to do")
}
