package config

import (
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
const currentRootsVersion = 3

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

// defaultExcludes returns the full default exclude set for this
// process: the base name patterns, the high-churn noise patterns,
// then the GOOS's system patterns.
func defaultExcludes() []string {
	ex := append(baseExcludes(), noiseExcludes()...)
	return append(ex, systemExcludesFor(runtime.GOOS)...)
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
// applying each version step it is missing. The v2 step: a config
// whose roots are still the legacy default (exactly the home
// directory) -- or that never chose roots at all -- moves to the
// whole-filesystem default roots, with the missing system exclude
// patterns appended; customized roots are left exactly as written.
// The v3 step: a config whose excludes still contain every base
// pattern (the default shape) gets the missing high-churn noise
// patterns appended; a curated-away or explicitly emptied list is
// left untouched and only announced. Patterns the user wrote are
// never touched or reordered either way. Every user-visible change is
// recorded in MigrationNotes for the app to log loudly at startup --
// the index scope never changes silently. Returns true when the
// config file should be rewritten (a version bump alone counts, so
// the migration runs once, not on every load).
func (c *Config) migrateRoots() bool {
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
			if added := c.mergeExcludes(systemExcludesFor(runtime.GOOS)); len(added) > 0 {
				c.MigrationNotes = append(c.MigrationNotes, fmt.Sprintf(
					"system exclude patterns added for whole-filesystem indexing: %s",
					strings.Join(added, ", ")))
			}
		}
	}
	// v3: the noise-exclude policy. Only a list still carrying every
	// base pattern is default-shaped enough to extend; anything else
	// was curated (or explicitly emptied) and stays the user's.
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
	c.RootsVersion = currentRootsVersion
	return true
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
