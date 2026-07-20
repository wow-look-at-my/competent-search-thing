package config

import (
	"encoding/json"
	"fmt"
	"sort"
)

// The v7 migration step: the boolean-polarity rename. Every negative
// enable/disable-style switch in config.json gets the affirmative
// "enabled" spelling with its VALUE INVERTED -- a pure rename, zero
// behavior change for every config. Split from migrate.go (which
// carries the version ladder and the earlier steps) on the arbiter.go
// own-file precedent.

// polarityV7Pair is one section's old/new switch pair as raw
// pointers, so absent keys are distinguishable from explicit values.
type polarityV7Pair struct {
	Disabled *bool `json:"disabled"`
	Enabled  *bool `json:"enabled"`
}

// polarityV7Raw is the minimal raw-document shape the v7 step reads:
// every pre-v7 negative-polarity switch beside its affirmative
// replacement, because the step must resolve a document carrying
// BOTH spellings deterministically (the new key wins; see
// polarityMigrate). It reads raw -- the on-disk JSON bytes -- for
// the same reason the v6 step does: the old keys no longer exist on
// the Config struct.
type polarityV7Raw struct {
	Search struct {
		FuzzyDisabled *bool          `json:"fuzzyDisabled"`
		FuzzyEnabled  *bool          `json:"fuzzyEnabled"`
		Frecency      polarityV7Pair `json:"frecency"`
		Priors        polarityV7Pair `json:"priors"`
		Arbiter       polarityV7Pair `json:"arbiter"`
	} `json:"search"`
	Watcher struct {
		SweepDisabled *bool `json:"sweepDisabled"`
		SweepEnabled  *bool `json:"sweepEnabled"`
	} `json:"watcher"`
	Plugins struct {
		Disabled *bool                     `json:"disabled"`
		Enabled  *bool                     `json:"enabled"`
		Entries  map[string]polarityV7Pair `json:"entries"`
	} `json:"plugins"`
	Rewrites []polarityV7Pair `json:"rewrites"`
	Tray     polarityV7Pair   `json:"tray"`
	History  struct {
		PersistDisabled *bool `json:"persistDisabled"`
		PersistEnabled  *bool `json:"persistEnabled"`
	} `json:"history"`
	Stats polarityV7Pair `json:"stats"`
}

// polarityMigrate resolves one old/new switch pair against the
// struct's parsed field (dst): an old key explicitly present with no
// new key beside it lands as the INVERTED value on the new key
// (announced per key); a document carrying BOTH spellings keeps the
// new key exactly as parsed and drops the old one (announced -- the
// new spelling is authoritative, deterministic by rule); an absent
// old key changes nothing (absent stays absent; Normalize later
// repairs nil to the default, ON, exactly what the absent negative
// key meant). The Save-back serializes the struct, which no longer
// has the old fields, so old keys are dropped from disk either way.
func (c *Config) polarityMigrate(oldKey, newKey string, oldVal, newVal *bool, dst **bool) {
	switch {
	case oldVal == nil:
		// Nothing to migrate: the new key (or its absent default) is
		// already what the struct parsed.
	case newVal != nil:
		c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
			"both %s and %s were present; kept %s=%v and dropped the old %s key",
			oldKey, newKey, newKey, *newVal, oldKey))
	default:
		inv := !*oldVal
		*dst = &inv
		c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
			"migrated %s=%v -> %s=%v", oldKey, *oldVal, newKey, inv))
	}
}

// migrateBoolPolarity is the v7 step (see the version ladder in
// migrate.go): resolve every renamed switch via polarityMigrate, in
// a fixed order (top-level sections in config order, plugin entries
// sorted by id, rewrite rules by index) so MigrationNotes are
// deterministic. nil raw behaves as "no old keys present".
func (c *Config) migrateBoolPolarity(raw []byte) {
	var old polarityV7Raw
	if len(raw) > 0 {
		// Best-effort, the migrateRankingDefaults stance: Load already
		// parsed these bytes, so a failure here just means "no old
		// keys present".
		_ = json.Unmarshal(raw, &old)
	}
	c.polarityMigrate("search.fuzzyDisabled", "search.fuzzyEnabled",
		old.Search.FuzzyDisabled, old.Search.FuzzyEnabled, &c.Search.FuzzyEnabled)
	c.polarityMigrate("search.frecency.disabled", "search.frecency.enabled",
		old.Search.Frecency.Disabled, old.Search.Frecency.Enabled, &c.Search.Frecency.Enabled)
	c.polarityMigrate("search.priors.disabled", "search.priors.enabled",
		old.Search.Priors.Disabled, old.Search.Priors.Enabled, &c.Search.Priors.Enabled)
	c.polarityMigrate("search.arbiter.disabled", "search.arbiter.enabled",
		old.Search.Arbiter.Disabled, old.Search.Arbiter.Enabled, &c.Search.Arbiter.Enabled)
	c.polarityMigrate("watcher.sweepDisabled", "watcher.sweepEnabled",
		old.Watcher.SweepDisabled, old.Watcher.SweepEnabled, &c.Watcher.SweepEnabled)
	c.polarityMigrate("plugins.disabled", "plugins.enabled",
		old.Plugins.Disabled, old.Plugins.Enabled, &c.Plugins.Enabled)
	ids := make([]string, 0, len(old.Plugins.Entries))
	for id := range old.Plugins.Entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		pair := old.Plugins.Entries[id]
		if pair.Disabled == nil {
			continue
		}
		// The raw key came from the same document the struct parsed,
		// so the entry exists in the map; guard anyway.
		e, ok := c.Plugins.Entries[id]
		if !ok {
			continue
		}
		key := "plugins.entries." + id
		c.polarityMigrate(key+".disabled", key+".enabled", pair.Disabled, pair.Enabled, &e.Enabled)
		c.Plugins.Entries[id] = e
	}
	for i := range old.Rewrites {
		if i >= len(c.Rewrites) {
			break // cannot happen for a document Load parsed; guard anyway
		}
		if old.Rewrites[i].Disabled == nil {
			continue
		}
		key := fmt.Sprintf("rewrites[%d]", i)
		c.polarityMigrate(key+".disabled", key+".enabled",
			old.Rewrites[i].Disabled, old.Rewrites[i].Enabled, &c.Rewrites[i].Enabled)
	}
	c.polarityMigrate("tray.disabled", "tray.enabled",
		old.Tray.Disabled, old.Tray.Enabled, &c.Tray.Enabled)
	c.polarityMigrate("history.persistDisabled", "history.persistEnabled",
		old.History.PersistDisabled, old.History.PersistEnabled, &c.History.PersistEnabled)
	c.polarityMigrate("stats.disabled", "stats.enabled",
		old.Stats.Disabled, old.Stats.Enabled, &c.Stats.Enabled)
}
