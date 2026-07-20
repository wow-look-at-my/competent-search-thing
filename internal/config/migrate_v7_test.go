package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The TestMigrateV7* tests pin the boolean-polarity rename: every
// negative enable/disable switch becomes the affirmative "enabled"
// spelling with its value inverted -- a PURE RENAME, zero behavior
// change. The Load-driven matrix covers every renamed key times
// {explicitly true, explicitly false, absent}, asserting the
// effective in-memory config AND the rewritten file; the both-keys
// document keeps the new spelling deterministically.

// v7Key describes one renamed switch for the matrix: how to build a
// v6-stamped raw document carrying the OLD key at a value, where the
// old and new keys live in the on-disk document, and how to read the
// effective enabled state off the loaded Config.
type v7Key struct {
	oldKey    string   // dotted old name, as the migration note spells it
	newKey    string   // dotted new name
	doc       string   // raw config with %s where the old key's value goes
	bothDoc   string   // raw config carrying old=%s AND new=%s
	oldPath   []string // the old key's location in the decoded document
	newPath   []string // the new key's location in the decoded document
	effective func(c *Config) bool
}

func v7Keys() []v7Key {
	sect := func(section, oldName, newName string, effective func(c *Config) bool) v7Key {
		return v7Key{
			oldKey:    section + "." + oldName,
			newKey:    section + "." + newName,
			doc:       `{"rootsVersion": 6, "roots": ["/"], "` + section + `": {"` + oldName + `": %s}}`,
			bothDoc:   `{"rootsVersion": 6, "roots": ["/"], "` + section + `": {"` + oldName + `": %s, "` + newName + `": %s}}`,
			oldPath:   []string{section, oldName},
			newPath:   []string{section, newName},
			effective: effective,
		}
	}
	return []v7Key{
		{
			oldKey:    "search.fuzzyDisabled",
			newKey:    "search.fuzzyEnabled",
			doc:       `{"rootsVersion": 6, "roots": ["/"], "search": {"fuzzyDisabled": %s}}`,
			bothDoc:   `{"rootsVersion": 6, "roots": ["/"], "search": {"fuzzyDisabled": %s, "fuzzyEnabled": %s}}`,
			oldPath:   []string{"search", "fuzzyDisabled"},
			newPath:   []string{"search", "fuzzyEnabled"},
			effective: func(c *Config) bool { return Enabled(c.Search.FuzzyEnabled) },
		},
		{
			oldKey:    "search.frecency.disabled",
			newKey:    "search.frecency.enabled",
			doc:       `{"rootsVersion": 6, "roots": ["/"], "search": {"frecency": {"disabled": %s}}}`,
			bothDoc:   `{"rootsVersion": 6, "roots": ["/"], "search": {"frecency": {"disabled": %s, "enabled": %s}}}`,
			oldPath:   []string{"search", "frecency", "disabled"},
			newPath:   []string{"search", "frecency", "enabled"},
			effective: func(c *Config) bool { return Enabled(c.Search.Frecency.Enabled) },
		},
		{
			oldKey:    "search.priors.disabled",
			newKey:    "search.priors.enabled",
			doc:       `{"rootsVersion": 6, "roots": ["/"], "search": {"priors": {"disabled": %s}}}`,
			bothDoc:   `{"rootsVersion": 6, "roots": ["/"], "search": {"priors": {"disabled": %s, "enabled": %s}}}`,
			oldPath:   []string{"search", "priors", "disabled"},
			newPath:   []string{"search", "priors", "enabled"},
			effective: func(c *Config) bool { return Enabled(c.Search.Priors.Enabled) },
		},
		{
			oldKey:    "search.arbiter.disabled",
			newKey:    "search.arbiter.enabled",
			doc:       `{"rootsVersion": 6, "roots": ["/"], "search": {"arbiter": {"disabled": %s}}}`,
			bothDoc:   `{"rootsVersion": 6, "roots": ["/"], "search": {"arbiter": {"disabled": %s, "enabled": %s}}}`,
			oldPath:   []string{"search", "arbiter", "disabled"},
			newPath:   []string{"search", "arbiter", "enabled"},
			effective: func(c *Config) bool { return Enabled(c.Search.Arbiter.Enabled) },
		},
		{
			oldKey:    "watcher.sweepDisabled",
			newKey:    "watcher.sweepEnabled",
			doc:       `{"rootsVersion": 6, "roots": ["/"], "watcher": {"sweepDisabled": %s}}`,
			bothDoc:   `{"rootsVersion": 6, "roots": ["/"], "watcher": {"sweepDisabled": %s, "sweepEnabled": %s}}`,
			oldPath:   []string{"watcher", "sweepDisabled"},
			newPath:   []string{"watcher", "sweepEnabled"},
			effective: func(c *Config) bool { return Enabled(c.Watcher.SweepEnabled) },
		},
		{
			oldKey:    "plugins.disabled",
			newKey:    "plugins.enabled",
			doc:       `{"rootsVersion": 6, "roots": ["/"], "plugins": {"disabled": %s}}`,
			bothDoc:   `{"rootsVersion": 6, "roots": ["/"], "plugins": {"disabled": %s, "enabled": %s}}`,
			oldPath:   []string{"plugins", "disabled"},
			newPath:   []string{"plugins", "enabled"},
			effective: func(c *Config) bool { return Enabled(c.Plugins.Enabled) },
		},
		{
			oldKey:    "plugins.entries.calc.disabled",
			newKey:    "plugins.entries.calc.enabled",
			doc:       `{"rootsVersion": 6, "roots": ["/"], "plugins": {"entries": {"calc": {"disabled": %s}}}}`,
			bothDoc:   `{"rootsVersion": 6, "roots": ["/"], "plugins": {"entries": {"calc": {"disabled": %s, "enabled": %s}}}}`,
			oldPath:   []string{"plugins", "entries", "calc", "disabled"},
			newPath:   []string{"plugins", "entries", "calc", "enabled"},
			effective: func(c *Config) bool { return Enabled(c.Plugins.Entries["calc"].Enabled) },
		},
		{
			oldKey:    "rewrites[0].disabled",
			newKey:    "rewrites[0].enabled",
			doc:       `{"rootsVersion": 6, "roots": ["/"], "rewrites": [{"name": "n", "pattern": "p", "replacement": "https://x.test/$0", "disabled": %s}]}`,
			bothDoc:   `{"rootsVersion": 6, "roots": ["/"], "rewrites": [{"name": "n", "pattern": "p", "replacement": "https://x.test/$0", "disabled": %s, "enabled": %s}]}`,
			oldPath:   []string{"rewrites", "0", "disabled"},
			newPath:   []string{"rewrites", "0", "enabled"},
			effective: func(c *Config) bool { return Enabled(c.Rewrites[0].Enabled) },
		},
		sect("tray", "disabled", "enabled",
			func(c *Config) bool { return Enabled(c.Tray.Enabled) }),
		sect("history", "persistDisabled", "persistEnabled",
			func(c *Config) bool { return Enabled(c.History.PersistEnabled) }),
		sect("stats", "disabled", "enabled",
			func(c *Config) bool { return Enabled(c.Stats.Enabled) }),
	}
}

// docLookup walks a decoded JSON document by path (array indices as
// decimal strings), returning the value and whether it exists.
func docLookup(doc any, path []string) (any, bool) {
	cur := doc
	for _, seg := range path {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[seg]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			var i int
			if _, err := fmt.Sscanf(seg, "%d", &i); err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// TestMigrateV7Matrix drives every renamed key through Load with the
// old key explicitly true, explicitly false, and absent: the
// effective state never changes (old disabled-ish true = off, false =
// on, absent = on), the rewritten file carries the inverted new key
// and no old key, the note announces explicit conversions, and a
// second Load has nothing left to do.
func TestMigrateV7Matrix(t *testing.T) {
	for _, k := range v7Keys() {
		for _, tc := range []struct {
			name        string
			value       string // the old key's raw value; "" = absent
			wantOn      bool
			wantNote    bool
			wantNewDisk any // the new key's on-disk value; nil = must be absent
		}{
			{"explicit true", "true", false, true, false},
			{"explicit false", "false", true, true, true},
			{"absent", "", true, false, func() any {
				// The always-written switches materialize their default
				// (true) via Normalize; rewrites[0].enabled deliberately
				// stays absent (nil = enabled, user rules never grow keys).
				if strings.HasPrefix(k.oldKey, "rewrites") {
					return nil
				}
				return true
			}()},
		} {
			t.Run(k.oldKey+" "+tc.name, func(t *testing.T) {
				setConfigDir(t)
				doc := `{"rootsVersion": 6, "roots": ["/"]}`
				if tc.value != "" {
					doc = fmt.Sprintf(k.doc, tc.value)
				} else if strings.HasPrefix(k.oldKey, "rewrites") {
					// The absent case still needs the rule itself so the
					// per-rule switch has somewhere to be absent from.
					doc = `{"rootsVersion": 6, "roots": ["/"], "rewrites": [{"name": "n", "pattern": "p", "replacement": "https://x.test/$0"}]}`
				} else if strings.HasPrefix(k.oldKey, "plugins.entries") {
					// Same for the per-entry switch: the entry exists, the
					// switch is absent.
					doc = `{"rootsVersion": 6, "roots": ["/"], "plugins": {"entries": {"calc": {}}}}`
				}
				p := writeConfig(t, doc)

				c, err := Load()
				require.NoError(t, err)
				require.Equal(t, currentRootsVersion, c.RootsVersion)
				require.Equal(t, tc.wantOn, k.effective(&c),
					"effective state must match the old semantics exactly (pure rename)")
				wantNote := fmt.Sprintf("migrated %s=%s -> %s=%v", k.oldKey, tc.value, k.newKey, tc.wantOn)
				joined := strings.Join(c.MigrationNotes, "\n")
				if tc.wantNote {
					require.Contains(t, joined, wantNote, "explicit old keys announce their conversion")
				} else {
					require.NotContains(t, joined, "migrated "+k.oldKey,
						"absent old keys migrate nothing and announce nothing")
				}

				onDisk := readRawConfig(t, p)
				_, oldPresent := docLookup(onDisk, k.oldPath)
				require.False(t, oldPresent, "the old key never survives the rewrite")
				got, newPresent := docLookup(onDisk, k.newPath)
				if tc.wantNewDisk == nil {
					require.False(t, newPresent, "%s stays absent (nil = enabled)", k.newKey)
				} else {
					require.True(t, newPresent, "%s must be on disk", k.newKey)
					require.Equal(t, tc.wantNewDisk, got)
				}

				again, err := Load()
				require.NoError(t, err)
				require.Empty(t, again.MigrationNotes, "the second load has nothing left to migrate")
				require.Equal(t, tc.wantOn, k.effective(&again), "the effective state round-trips")
			})
		}
	}
}

// TestMigrateV7BothKeysPresent pins the deterministic winner: a
// document carrying BOTH spellings keeps the new key exactly as
// written, drops the old one, and says so.
func TestMigrateV7BothKeysPresent(t *testing.T) {
	for _, k := range v7Keys() {
		// old=true says OFF, new=true says ON: the new key must win,
		// proving the old value is discarded rather than merged.
		t.Run(k.oldKey, func(t *testing.T) {
			setConfigDir(t)
			p := writeConfig(t, fmt.Sprintf(k.bothDoc, "true", "true"))

			c, err := Load()
			require.NoError(t, err)
			require.True(t, k.effective(&c), "the new key wins the conflict")
			joined := strings.Join(c.MigrationNotes, "\n")
			require.Contains(t, joined,
				fmt.Sprintf("both %s and %s were present; kept %s=true and dropped the old %s key",
					k.oldKey, k.newKey, k.newKey, k.oldKey))

			onDisk := readRawConfig(t, p)
			_, oldPresent := docLookup(onDisk, k.oldPath)
			require.False(t, oldPresent)
			got, newPresent := docLookup(onDisk, k.newPath)
			require.True(t, newPresent)
			require.Equal(t, true, got)
		})
	}
}

// TestMigrateV7RoundTripStable pins the post-migration steady state:
// a migrated file re-saves byte-identically through Load -> Save (no
// oscillation), and a GUI-style full rewrite at the preserved
// rootsVersion never re-triggers the migration.
func TestMigrateV7RoundTripStable(t *testing.T) {
	setConfigDir(t)
	p := writeConfig(t, `{
		"rootsVersion": 6,
		"roots": ["/"],
		"search": {"fuzzyDisabled": true, "priors": {"disabled": true}},
		"tray": {"disabled": true},
		"history": {"persistDisabled": false},
		"stats": {"disabled": false}
	}`)

	c, err := Load()
	require.NoError(t, err)
	first, err := os.ReadFile(p)
	require.NoError(t, err)

	require.NoError(t, Save(c))
	second, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, string(first), string(second), "Load -> Save is a fixed point after migration")

	again, err := Load()
	require.NoError(t, err)
	require.Empty(t, again.MigrationNotes)
	require.False(t, Enabled(again.Search.FuzzyEnabled))
	require.False(t, Enabled(again.Search.Priors.Enabled))
	require.False(t, Enabled(again.Tray.Enabled))
	require.True(t, Enabled(again.History.PersistEnabled))
	require.True(t, Enabled(again.Stats.Enabled))
}

// TestMigrateV7FromLegacy chains the whole ladder: a version-0 config
// carrying pre-v7 switches lands on the v7 shape in one load, with
// the older steps' work intact.
func TestMigrateV7FromLegacy(t *testing.T) {
	setConfigDir(t)
	p := writeConfig(t, `{
		"roots": ["/data"],
		"excludes": [".git", "node_modules", ".cache"],
		"search": {"fuzzyDisabled": true},
		"watcher": {"sweepDisabled": true},
		"plugins": {"disabled": true, "entries": {"calc": {"disabled": true}, "ps": {"disabled": false}}},
		"tray": {"disabled": true}
	}`)

	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.False(t, Enabled(c.Search.FuzzyEnabled))
	require.False(t, Enabled(c.Watcher.SweepEnabled))
	require.False(t, Enabled(c.Plugins.Enabled))
	require.False(t, Enabled(c.Plugins.Entries["calc"].Enabled))
	require.True(t, Enabled(c.Plugins.Entries["ps"].Enabled))
	require.False(t, Enabled(c.Tray.Enabled))

	joined := strings.Join(c.MigrationNotes, "\n")
	require.Contains(t, joined, "migrated search.fuzzyDisabled=true -> search.fuzzyEnabled=false")
	require.Contains(t, joined, "migrated plugins.entries.calc.disabled=true -> plugins.entries.calc.enabled=false")
	require.Contains(t, joined, "migrated plugins.entries.ps.disabled=false -> plugins.entries.ps.enabled=true")
	require.Contains(t, joined, "migrated tray.disabled=true -> tray.enabled=false")

	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Empty(t, UnknownKeys(data), "no old keys survive the Save-back")
	require.NotContains(t, string(data), "sweepDisabled")
	require.NotContains(t, string(data), "fuzzyDisabled")
}

// TestMigrateV7NoteOrderDeterministic pins the note order: fixed
// config-section order, entries sorted by id.
func TestMigrateV7NoteOrderDeterministic(t *testing.T) {
	raw := []byte(`{
		"rootsVersion": 6,
		"roots": ["/"],
		"stats": {"disabled": true},
		"plugins": {"entries": {"zeta": {"disabled": true}, "alpha": {"disabled": true}}},
		"search": {"fuzzyDisabled": true}
	}`)
	var c Config
	require.NoError(t, json.Unmarshal(raw, &c))
	require.True(t, c.migrateRootsFor("linux", raw))
	require.Equal(t, []string{
		"migrated search.fuzzyDisabled=true -> search.fuzzyEnabled=false",
		"migrated plugins.entries.alpha.disabled=true -> plugins.entries.alpha.enabled=false",
		"migrated plugins.entries.zeta.disabled=true -> plugins.entries.zeta.enabled=false",
		"migrated stats.disabled=true -> stats.enabled=false",
	}, c.MigrationNotes)
}

// TestMigrateV7DirectDriverTolerance mirrors the v6 direct-driver
// stance: nil raw (no document) migrates nothing and never panics.
func TestMigrateV7DirectDriver(t *testing.T) {
	c := Config{RootsVersion: 6, Roots: []string{"/"}}
	require.True(t, c.migrateRootsFor("linux", nil))
	require.Empty(t, c.MigrationNotes, "nil raw = no old keys present")
	require.Equal(t, currentRootsVersion, c.RootsVersion)
	require.False(t, c.migrateRootsFor("linux", nil), "stamped current = nothing left to do")
}
