package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// currentRootsVersion is the rootsVersion stamp this build writes.
// Version history:
//
//	0 (or absent) -- legacy: the default index root was the user's
//	  home directory.
//	2 -- whole-filesystem default roots plus the system exclude
//	  patterns (Everything-style: index everything, skip virtual and
//	  volatile trees).
//	3 -- high-churn "noise" exclude defaults for the watch layer
//	  (see noiseExcludes): configs still carrying every base pattern
//	  get the missing noise patterns appended; curated or emptied
//	  exclude lists are stamped only, with an informational note.
//	4 -- the macOS firmlink-dedup exclude (see firmlinkExcludesFor):
//	  /System/Volumes/Data exposes the same files macOS also shows at
//	  the canonical firmlinked paths (/Users, /Applications, ...), so
//	  a whole-filesystem walk without it indexes almost half the disk
//	  twice. Same default-shaped gate and stamp-only fallback as v3;
//	  a no-op on every other OS.
//	5 -- the macOS noise excludes (see darwinNoiseExcludesFor):
//	  unambiguous cache/derived/temp trees (Caches, Xcode DerivedData,
//	  code-signature manifests, /private/var/folders). Same
//	  default-shaped gate and stamp-only fallback as v3/v4; a no-op on
//	  every other OS. Deliberately a NEW version step rather than an
//	  extension of v4: configs already stamped 4 by a v4-era build
//	  must still receive it.
//	6 -- the ranking-defaults flip (see migrateRankingDefaults): the
//	  local ranking log (search.telemetry) becomes ALWAYS ON -- its
//	  old opt-in "enabled" switch and retainQueries are removed
//	  outright, an explicit `"enabled": false` included (the log is
//	  private by staying on the machine; there is deliberately no off
//	  state) -- while the two learned layers (search.priors,
//	  search.arbiter) turn ON by default with their opt-in "enabled"
//	  switches becoming opt-out debug escape hatches. (The v6-era
//	  builds spelled the opt-out `"disabled": true`; since v7 the
//	  affirmative `"enabled": false` is the one spelling, and the v7
//	  step below converts the intermediate shape.)
//	7 -- the boolean-polarity rename (see migrateBoolPolarity in
//	  migrate_v7.go): every negative enable/disable switch gets the
//	  affirmative "enabled" spelling with its VALUE INVERTED, a pure
//	  rename with zero behavior change -- search.fuzzyDisabled ->
//	  search.fuzzyEnabled; search.frecency/priors/arbiter.disabled ->
//	  .enabled; watcher.sweepDisabled -> watcher.sweepEnabled;
//	  plugins.disabled and plugins.entries.<id>.disabled -> .enabled;
//	  rewrites[].disabled -> rewrites[].enabled; tray.disabled ->
//	  tray.enabled; history.persistDisabled ->
//	  history.persistEnabled; stats.disabled -> stats.enabled. Old
//	  keys explicitly present in the file land as the inverted new
//	  key (announced per key); absent keys stay absent (defaults
//	  preserved: missing means ON, exactly as the missing negative
//	  key did); a file carrying BOTH spellings keeps the new key and
//	  drops the old one, announced. preview.enabled (already
//	  affirmative) and window.translucent (an affirmative flag, not
//	  enable/disable naming) are untouched, and search.telemetry
//	  stays switchless by design.
//	8 -- the preview pane turns ON by default (see
//	  migratePreviewDefaultOn): preview.enabled becomes the
//	  affirmative *bool (nil/absent = ON, Normalize repairs to
//	  explicit true) and a pre-v8 stored false -- the plain-bool
//	  era's machine handwriting on every save, not a user choice
//	  (the feature was opt-in; deliberate off was expressed by never
//	  opting in) -- is reset to on with a loud note naming the
//	  opt-out. A false saved AFTER this flip is stamped
//	  rootsVersion >= 8 and never revisited: a real opt-out,
//	  respected forever.
const currentRootsVersion = 8

// CurrentRootsVersion returns the rootsVersion stamp this build
// writes. Writers that must preserve the field across a full-file
// rewrite (the app's GUI save path forces the on-disk value so a save
// can never reset it to 0 and re-trigger the Load migrations) use it
// as the fallback when no on-disk value is available.
func CurrentRootsVersion() int { return currentRootsVersion }

// baseExcludes returns the name-based exclude patterns every platform
// defaults to. A fresh slice on every call so callers may append.
// This is deliberately frozen as the v2-era set: migrations use it to
// recognize a default-shaped exclude list, so new default patterns go
// in noiseExcludes (or a future sibling), never here.
func baseExcludes() []string { return []string{".git", "node_modules", ".cache"} }

// noiseExcludes returns the high-churn name-based exclude patterns
// added in rootsVersion 3 for the watch layer: VCS internals and tool
// caches that generate heavy filesystem event traffic and near-zero
// search value. A fresh slice on every call so callers may append.
func noiseExcludes() []string {
	return []string{".hg", ".svn", "__pycache__", ".mypy_cache", ".pytest_cache", ".ruff_cache", ".tox", ".nox", ".venv"}
}

// defaultRoots returns the whole-system default index roots for this
// process (defaultRootsFor over the real GOOS and environment).
func defaultRoots() []string { return defaultRootsFor(runtime.GOOS, os.Getenv) }

// defaultRootsFor returns the whole-system default index roots for
// goos: "/" on Linux and macOS, the system drive on Windows
// (%SystemDrive%, falling back to C:). goos and getenv are parameters
// so tests cover the Windows shape headlessly (the hotkeyPlan
// convention in internal/app).
func defaultRootsFor(goos string, getenv func(string) string) []string {
	if goos != "windows" {
		return []string{"/"}
	}
	drive := getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	return []string{strings.TrimRight(drive, `\`) + `\`}
}

// systemExcludesFor returns the system exclude patterns a
// whole-filesystem walk needs on goos: the Linux/macOS virtual and
// volatile trees (/proc, /sys, /dev, /run, /tmp, /var/tmp -- full-path
// patterns, pruned only when a walk reaches that exact path) plus the
// ext* lost+found directories by base name. Windows has no such trees,
// so it gets none and its defaults stay the base-name patterns only.
func systemExcludesFor(goos string) []string {
	if goos == "windows" {
		return nil
	}
	return []string{"/proc", "/sys", "/dev", "/run", "/tmp", "/var/tmp", "lost+found"}
}

// firmlinkExcludesFor returns the macOS firmlink-dedup exclude
// patterns for goos. Since Catalina the writable APFS Data volume is
// mounted at /System/Volumes/Data while firmlinks expose its content
// AGAIN at the canonical paths (/Users, /Applications, /usr/local,
// ...), so a whole-filesystem walk from "/" without this exclude
// indexes roughly 45% of the disk twice -- double the entries, double
// the RAM, double the build time. The canonical spellings stay
// indexed in full; only the duplicate /System/Volumes/Data view is
// skipped. goos is a parameter so tests cover the darwin shape
// headlessly (the defaultRootsFor convention). A fresh slice on every
// call so callers may append.
func firmlinkExcludesFor(goos string) []string {
	if goos != "darwin" {
		return nil
	}
	return []string{"/System/Volumes/Data"}
}

// darwinNoiseExcludesFor returns the macOS noise exclude patterns for
// goos, added in rootsVersion 5: the base-name cache and
// code-signature trees (Caches -- ~/Library/Caches and every app's
// cache dir; DerivedData -- Xcode's build products; _CodeSignature
// and CodeResources -- per-bundle signature manifests, thousands of
// identically-named entries with zero search value) plus
// /private/var/folders, macOS's real per-user temp tree (the /tmp and
// /var/tmp system excludes cover only the symlinked spellings). goos
// is a parameter so tests cover the darwin shape headlessly (the
// defaultRootsFor convention). Deliberately NOT here: wholesale
// .app/.framework bundle-internals exclusion and Application Support
// filtering -- those change user-visible search semantics and need an
// owner decision. A fresh slice on every call so callers may append.
func darwinNoiseExcludesFor(goos string) []string {
	if goos != "darwin" {
		return nil
	}
	return []string{"Caches", "DerivedData", "_CodeSignature", "CodeResources", "/private/var/folders"}
}

// defaultExcludes returns the full default exclude set for this
// process: the base name patterns, the high-churn noise patterns,
// then the GOOS's system, firmlink-dedup, and macOS noise patterns.
func defaultExcludes() []string {
	ex := append(baseExcludes(), noiseExcludes()...)
	ex = append(ex, systemExcludesFor(runtime.GOOS)...)
	ex = append(ex, firmlinkExcludesFor(runtime.GOOS)...)
	return append(ex, darwinNoiseExcludesFor(runtime.GOOS)...)
}

// legacyDefaultRoots returns the pre-v2 default root set -- the user's
// home directory, absolutized, falling back to the current directory
// -- used to recognize configs still holding the old written default.
func legacyDefaultRoots() []string {
	root, err := os.UserHomeDir()
	if err != nil || root == "" {
		root = "."
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return []string{root}
}

// migrateRoots upgrades a config from an older rootsVersion in place,
// applying each version step it is missing (migrateRootsFor over the
// real GOOS; goos steers the exclude sets -- the roots default is the
// process's own, identical on linux and darwin anyway). The v2 step:
// a config whose roots are still the legacy default (exactly the home
// directory) -- or that never chose roots at all -- moves to the
// whole-filesystem default roots, with the missing system exclude
// patterns appended; customized roots are left exactly as written.
// The v3 step: a config whose excludes still contain every base
// pattern (the default shape) gets the missing high-churn noise
// patterns appended; a curated-away or explicitly emptied list is
// left untouched and only announced. The v4 step: the same policy for
// the macOS firmlink-dedup exclude (firmlinkExcludesFor) -- on any
// other OS the step has nothing to add and only the stamp advances.
// The v5 step: the same policy again for the macOS noise excludes
// (darwinNoiseExcludesFor).
// The v6 step (migrateRankingDefaults) reads raw -- the on-disk JSON
// bytes -- to ANNOUNCE the ranking-defaults flip: for
// priors/arbiter the old opt-in `"enabled"` key happens to be the
// same spelling v7 lands on (explicit values parse straight into the
// struct; absent means the new default, on), so the step only emits
// the turn-on/kept-off notes, while the telemetry keys (enabled and
// retainQueries) are dropped outright by the rewrite -- the ranking
// log is always on now. nil raw behaves as "no old keys present".
// The v7 step (migrateBoolPolarity, migrate_v7.go) converts every
// remaining negative-polarity switch in raw to its affirmative
// "enabled" spelling with the value inverted -- including the
// v6-era `"disabled"` spelling of the priors/arbiter opt-outs.
// Patterns the user wrote are never touched or reordered either way.
// Every user-visible change is recorded in MigrationNotes for the app
// to log loudly at startup -- the index scope never changes silently.
// Returns true when the config file should be rewritten (a version
// bump alone counts, so the migration runs once, not on every load).
func (c *Config) migrateRoots(raw []byte) bool { return c.migrateRootsFor(runtime.GOOS, raw) }

func (c *Config) migrateRootsFor(goos string, raw []byte) bool {
	if c.RootsVersion >= currentRootsVersion {
		return false
	}
	if c.RootsVersion < 2 {
		legacy := legacyDefaultRoots()
		onLegacyDefault := len(c.Roots) == 0 ||
			(len(c.Roots) == 1 && c.Roots[0] == legacy[0])
		if onLegacyDefault {
			c.Roots = defaultRoots()
			c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
				"index roots upgraded to the whole-filesystem default (%s); edit roots in config.json to revert -- the first rescan will re-walk everything",
				strings.Join(c.Roots, ", ")))
			if added := c.mergeExcludes(systemExcludesFor(goos)); len(added) > 0 {
				c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
					"system exclude patterns added for whole-filesystem indexing: %s",
					strings.Join(added, ", ")))
			}
		}
	}
	// v3: the noise-exclude policy. Only a list still carrying every
	// base pattern is default-shaped enough to extend; anything else
	// was curated (or explicitly emptied) and stays the user's.
	if c.RootsVersion < 3 {
		if c.hasAllBaseExcludes() {
			if added := c.mergeExcludes(noiseExcludes()); len(added) > 0 {
				c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
					"high-churn exclude patterns added for the watch layer: %s; remove any of them in config.json to index those trees",
					strings.Join(added, ", ")))
			}
		} else {
			c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
				"new default exclude patterns exist (%s) but your customized exclude list was left unchanged",
				strings.Join(noiseExcludes(), ", ")))
		}
	}
	// v4: the macOS firmlink dedup, same default-shaped gate. On a
	// non-darwin goos there is no new default, so nothing is added or
	// announced and only the stamp advances.
	if fl := firmlinkExcludesFor(goos); c.RootsVersion < 4 && len(fl) > 0 {
		if c.hasAllBaseExcludes() {
			if added := c.mergeExcludes(fl); len(added) > 0 {
				c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
					"macOS firmlink exclude added: %s (macOS shows the same files at /Users, /Applications, ...; indexing both nearly doubles the index and its RAM); remove it in config.json to index the Data volume twice",
					strings.Join(added, ", ")))
			}
		} else {
			c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
				"a new default exclude exists (%s, the macOS firmlink duplicate view of /Users, /Applications, ...) but your customized exclude list was left unchanged",
				strings.Join(fl, ", ")))
		}
	}
	// v5: the macOS noise excludes, same default-shaped gate and
	// non-darwin no-op as v4.
	if dn := darwinNoiseExcludesFor(goos); c.RootsVersion < 5 && len(dn) > 0 {
		if c.hasAllBaseExcludes() {
			if added := c.mergeExcludes(dn); len(added) > 0 {
				c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
					"macOS noise exclude patterns added: %s (app caches, Xcode build products, code-signature manifests, and the real temp tree); remove any of them in config.json to index those trees",
					strings.Join(added, ", ")))
			}
		} else {
			c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
				"new default exclude patterns exist (%s, the macOS cache/derived/temp noise set) but your customized exclude list was left unchanged",
				strings.Join(dn, ", ")))
		}
	}
	// v6: the ranking-defaults flip.
	if c.RootsVersion < 6 {
		c.migrateRankingDefaults(raw)
	}
	// v7: the boolean-polarity rename (migrate_v7.go).
	if c.RootsVersion < 7 {
		c.migrateBoolPolarity(raw)
	}
	// v8: the preview pane turns on by default.
	if c.RootsVersion < 8 {
		c.migratePreviewDefaultOn()
	}
	c.RootsVersion = currentRootsVersion
	return true
}

// migratePreviewDefaultOn is the v8 step (see the version ladder
// above): the preview pane becomes ON by default. A pre-v8 stored
// `"preview": {"enabled": false}` is treated as never-chosen -- the
// plain-bool era serialized false on every app-written save (Default()
// on first run, GUI saves, drag commits, earlier migration
// save-backs), so a stored false is almost always the machine's
// handwriting, and a deliberate off state was expressed by never
// opting in -- and is dropped to nil for Normalize to repair to
// explicit true, announced loudly with the opt-out. The parsed *bool
// distinguishes present-false from absent by itself, so this step
// needs no raw-bytes read (the key kept its name and type shape on
// the wire). Post-flip opt-outs are safe by construction: every save
// from this build stamps rootsVersion 8, and this step never runs at
// or above 8.
func (c *Config) migratePreviewDefaultOn() {
	const optOut = "set preview.enabled=false (config editor or config.json) to turn it off"
	switch {
	case c.Preview.Enabled == nil:
		c.MigrationNotes = append(c.MigrationNotes,
			"the preview pane is now ON by default; "+optOut)
	case !*c.Preview.Enabled:
		c.Preview.Enabled = nil // pre-flip machine-written false: not a user choice
		c.MigrationNotes = append(c.MigrationNotes,
			"the preview pane is now ON by default and the machine-written preview.enabled=false in your config was reset; "+optOut)
	}
}

// rankingV6Raw is the minimal raw-document shape the v6 step reads:
// the pre-v6 opt-in switches (and retainQueries), as pointers so an
// absent key is distinguishable from an explicit false, plus the
// v6-era "disabled" spelling a hand-edited under-stamped file may
// already carry (it gates the turn-on announcement; the v7 step owns
// converting it).
type rankingV6Raw struct {
	Search struct {
		Priors struct {
			Enabled  *bool `json:"enabled"`
			Disabled *bool `json:"disabled"`
		} `json:"priors"`
		Telemetry struct {
			Enabled       *bool `json:"enabled"`
			RetainQueries *bool `json:"retainQueries"`
		} `json:"telemetry"`
		Arbiter struct {
			Enabled  *bool `json:"enabled"`
			Disabled *bool `json:"disabled"`
		} `json:"arbiter"`
	} `json:"search"`
}

// migrateRankingDefaults is the v6 step. search.telemetry -- the
// local ranking log -- becomes ALWAYS ON: there is deliberately no
// off state anymore (the log is private by staying on the machine),
// so every old key is dropped outright -- an explicit
// `"enabled": false` is overruled by design and announced, and
// retainQueries goes with it (query text is always recorded now).
// The learned layers (search.priors, search.arbiter) flip to ON by
// default; their pre-v6 opt-in "enabled" keys carry the same
// spelling (and, for explicit values, the same meaning) as the
// affirmative v7 switches, so explicit values parse straight into
// the struct and this step only ANNOUNCES the flip: absent old key
// -> the new default (on) applies, unless the file already carries
// the v6-era `"disabled": true` opt-out (converted by the v7 step);
// an explicit `"enabled": false` is a deliberate opt-out and is
// respected. Every user-visible flip lands in MigrationNotes; the
// Save-back drops the removed keys so UnknownKeys never flags
// leftovers.
func (c *Config) migrateRankingDefaults(raw []byte) {
	var old rankingV6Raw
	if len(raw) > 0 {
		// Best-effort: Load already parsed these bytes, so a failure
		// here (nil raw from a direct migrate call, tests) just means
		// "no old keys present".
		_ = json.Unmarshal(raw, &old)
	}
	// The ranking log: previously off (opted out or never opted in)
	// means this load turns it on -- say so. Already opted in means
	// nothing changes beyond the key drop.
	if e := old.Search.Telemetry.Enabled; e == nil || !*e {
		c.MigrationNotes = append(c.MigrationNotes,
			"ranking telemetry is now always on; the log is local-only and never leaves this machine")
	}
	var turnedOn, keptOff []string
	apply := func(section string, enabled *bool, disabled *bool) {
		switch {
		case enabled == nil:
			// No old opt-in key: the new default (on) applies -- unless
			// the file already carries the v6-era disabled:true opt-out
			// (the v7 step converts it to enabled:false), which stays.
			if disabled == nil || !*disabled {
				turnedOn = append(turnedOn, section)
			}
		case !*enabled:
			keptOff = append(keptOff, section)
		}
	}
	apply("search.priors", old.Search.Priors.Enabled, old.Search.Priors.Disabled)
	apply("search.arbiter", old.Search.Arbiter.Enabled, old.Search.Arbiter.Disabled)
	if len(turnedOn) > 0 {
		c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
			"the learned ranking layers are now on by default: %s turned on (everything stays on this machine; set the section's enabled flag to false in config.json to turn one back off)",
			strings.Join(turnedOn, ", ")))
	}
	if len(keptOff) > 0 {
		c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
			"your ranking opt-outs were preserved: %s stay off (\"enabled\" is an opt-out switch now -- the layers default on)",
			strings.Join(keptOff, ", ")))
	}
	if rq := old.Search.Telemetry.RetainQueries; rq != nil {
		if !*rq {
			c.MigrationNotes = append(c.MigrationNotes,
				"search.telemetry.retainQueries was removed: query text is now always recorded in the local ranking log (it never leaves this machine)")
		} else {
			c.MigrationNotes = append(c.MigrationNotes,
				"search.telemetry.retainQueries was removed (query text was already recorded; the log is local-only)")
		}
	}
}

// hasAllBaseExcludes reports whether every base exclude pattern is
// present in the config's exclude list -- the marker that the list is
// still default-shaped rather than curated.
func (c *Config) hasAllBaseExcludes() bool {
	have := make(map[string]struct{}, len(c.Excludes))
	for _, p := range c.Excludes {
		have[p] = struct{}{}
	}
	for _, p := range baseExcludes() {
		if _, ok := have[p]; !ok {
			return false
		}
	}
	return true
}

// mergeExcludes appends the patterns not already present, preserving
// everything the user wrote, and returns the ones it added.
func (c *Config) mergeExcludes(patterns []string) []string {
	have := make(map[string]struct{}, len(c.Excludes))
	for _, p := range c.Excludes {
		have[p] = struct{}{}
	}
	var added []string
	for _, p := range patterns {
		if _, ok := have[p]; ok {
			continue
		}
		c.Excludes = append(c.Excludes, p)
		added = append(added, p)
	}
	return added
}
