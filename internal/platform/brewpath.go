package platform

import (
	"path/filepath"
	"strings"
)

// BrewCellar is a Homebrew versioned install path taken apart:
// <prefix>/Cellar/<formula>/<version>/<rest...>. The fields carry the
// four structural parts; nothing here touches the filesystem -- the
// caller decides what the derived spellings mean and whether they
// exist.
type BrewCellar struct {
	// Prefix is the Homebrew prefix the Cellar sits under
	// (/home/linuxbrew/.linuxbrew, /opt/homebrew, /usr/local, a
	// per-user ~/.linuxbrew, ...). It is read from the path itself,
	// never from a hardcoded prefix list.
	Prefix string
	// Formula is the Cellar subdirectory naming the installed formula.
	Formula string
	// Version is the version-pinned directory the next upgrade
	// abandons.
	Version string
	// Rest is the path below the version directory, e.g.
	// "bin/competent-search-thing".
	Rest string
}

// ParseBrewCellar recognizes an absolute path shaped like a Homebrew
// Cellar install, <prefix>/Cellar/<formula>/<version>/<rest...>, and
// takes it apart. Rules:
//
//   - the path must be absolute (a relative path can never be the
//     command of anything that outlives this process) and is Cleaned
//     before parsing;
//   - formula, version and rest must all be non-empty -- rest is at
//     least one component below the version directory;
//   - "Cellar" is matched as an exact, separator-bounded path
//     component (Homebrew's spelling); when it appears more than
//     once, the LAST such occurrence is the one taken apart -- the
//     deeper structure is the install layout of the binary itself
//     (e.g. a formula vendoring another Cellar tree under libexec).
//
// ok is false for anything else; the zero BrewCellar accompanies it.
func ParseBrewCellar(path string) (BrewCellar, bool) {
	if !filepath.IsAbs(path) {
		return BrewCellar{}, false
	}
	path = filepath.Clean(path)
	sep := string(filepath.Separator)
	marker := sep + "Cellar" + sep
	i := strings.LastIndex(path, marker)
	if i < 0 {
		return BrewCellar{}, false
	}
	prefix := path[:i]
	if prefix == "" {
		// The Cellar sits directly under the filesystem root; the
		// prefix IS the root.
		prefix = sep
	}
	parts := strings.SplitN(path[i+len(marker):], sep, 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return BrewCellar{}, false
	}
	return BrewCellar{Prefix: prefix, Formula: parts[0], Version: parts[1], Rest: parts[2]}, true
}

// brewStableCandidates derives the upgrade-stable spellings of a
// versioned Cellar path, in preference order: the linked
// <prefix>/<rest> (Homebrew links Cellar/<formula>/<version>/bin/x to
// <prefix>/bin/x), then the opt <prefix>/opt/<formula>/<rest> (the
// opt symlink survives upgrades even when the formula is unlinked).
// nil when exe is not a Cellar path. Purely structural: the caller
// must still prove a candidate is the running binary before using it.
func brewStableCandidates(exe string) []string {
	bc, ok := ParseBrewCellar(exe)
	if !ok {
		return nil
	}
	return []string{
		filepath.Join(bc.Prefix, bc.Rest),
		filepath.Join(bc.Prefix, "opt", bc.Formula, bc.Rest),
	}
}
