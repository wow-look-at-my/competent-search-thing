package firefox

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// placesFile is the history database's file name inside a profile.
const placesFile = "places.sqlite"

// Profile is one discovered Firefox profile.
type Profile struct {
	// Dir is the absolute profile directory (holds places.sqlite).
	Dir string
	// Base is the profiles.ini base directory the profile came from.
	Base string
}

// BaseDirs returns the profiles.ini base directories to probe, in
// order. On Linux: the classic ~/.mozilla/firefox, the snap install
// (Ubuntu 22.04's default Firefox), and the flatpak install. On
// Windows: %APPDATA%\Mozilla\Firefox. On macOS: ~/Library/Application
// Support/Firefox (best effort; darwin is not built in CI). Unknown
// inputs (empty home, unset APPDATA) yield nil, and callers degrade.
func BaseDirs(goos, home string, getenv func(string) string) []string {
	switch goos {
	case "windows":
		appdata := getenv("APPDATA")
		if appdata == "" {
			return nil
		}
		return []string{filepath.Join(appdata, "Mozilla", "Firefox")}
	case "darwin":
		if home == "" {
			return nil
		}
		return []string{filepath.Join(home, "Library", "Application Support", "Firefox")}
	default:
		if home == "" {
			return nil
		}
		return []string{
			filepath.Join(home, ".mozilla", "firefox"),
			filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox"),
			filepath.Join(home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox"),
		}
	}
}

// DefaultBaseDirs is BaseDirs for the running process (runtime.GOOS,
// os.UserHomeDir, os.Getenv).
func DefaultBaseDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return BaseDirs(runtime.GOOS, home, os.Getenv)
}

// FindProfile picks the active default profile among the base dirs:
// each base's profiles.ini is resolved to its default profile (see
// defaultProfileDir), and when more than one base yields a live
// profile the one whose places.sqlite has the newest mtime -- the
// profile actually in use -- wins (earlier bases win ties, so the
// classic location beats an untouched snap copy). ok=false means no
// profile anywhere; callers degrade quietly.
func FindProfile(bases []string) (Profile, bool) {
	var best Profile
	var bestTime time.Time
	found := false
	for _, base := range bases {
		dir, ok := defaultProfileDir(base)
		if !ok {
			continue
		}
		mtime := placesMTime(dir)
		if !found || mtime.After(bestTime) {
			best = Profile{Dir: dir, Base: base}
			bestTime = mtime
			found = true
		}
	}
	return best, found
}

// profileSectionRe matches the [ProfileN] section names.
var profileSectionRe = regexp.MustCompile(`^Profile[0-9]+$`)

// iniSection is one [Name] block of a profiles.ini, keys in file
// order with the first occurrence of a duplicate key winning.
type iniSection struct {
	name string
	keys map[string]string
}

// defaultProfileDir resolves one base directory's profiles.ini to the
// default profile directory. Selection order (observed behavior of
// the format): the first [Install<HASH>] section's Default= value --
// the profile last used by that Firefox install -- wins; else the
// [ProfileN] section carrying Default=1; else a lone [ProfileN]
// section (several profiles with no default is ambiguous: no result).
// The resolved directory must exist, so a stale profiles.ini never
// yields a profile.
func defaultProfileDir(base string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(base, "profiles.ini"))
	if err != nil {
		return "", false
	}
	sections := parseINI(string(data))

	var profiles []iniSection
	installDefault := ""
	for _, s := range sections {
		if profileSectionRe.MatchString(s.name) {
			profiles = append(profiles, s)
			continue
		}
		if installDefault == "" && strings.HasPrefix(s.name, "Install") {
			installDefault = s.keys["Default"]
		}
	}

	resolve := func(path, isRelative string) string {
		if path == "" {
			return ""
		}
		switch {
		case isRelative == "0":
			return path
		case isRelative == "1":
			return filepath.Join(base, filepath.FromSlash(path))
		case filepath.IsAbs(path):
			// IsRelative missing: infer from the path itself.
			return path
		default:
			return filepath.Join(base, filepath.FromSlash(path))
		}
	}

	dir := ""
	if installDefault != "" {
		// The Install default names a profile by its Path value; resolve
		// through the matching section (for its IsRelative), or directly
		// when no section matches.
		dir = resolve(installDefault, "")
		for _, p := range profiles {
			if p.keys["Path"] == installDefault {
				dir = resolve(installDefault, p.keys["IsRelative"])
				break
			}
		}
	}
	if dir == "" {
		for _, p := range profiles {
			if p.keys["Default"] == "1" {
				dir = resolve(p.keys["Path"], p.keys["IsRelative"])
				break
			}
		}
	}
	if dir == "" && len(profiles) == 1 {
		dir = resolve(profiles[0].keys["Path"], profiles[0].keys["IsRelative"])
	}
	if dir == "" {
		return "", false
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", false
	}
	return dir, true
}

// parseINI reads the profiles.ini dialect: [Section] headers,
// Key=Value lines split on the first '=', values verbatim after
// whitespace trimming, ';'/'#' comment lines ignored, keys outside
// any section ignored.
func parseINI(text string) []iniSection {
	var sections []iniSection
	var cur *iniSection
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			sections = append(sections, iniSection{
				name: strings.TrimSpace(line[1 : len(line)-1]),
				keys: map[string]string{},
			})
			cur = &sections[len(sections)-1]
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if _, dup := cur.keys[k]; k == "" || dup {
			continue
		}
		cur.keys[k] = strings.TrimSpace(v)
	}
	return sections
}

// placesMTime returns the profile's places.sqlite modification time,
// or the zero time when it cannot be read (a profile that never
// browsed still counts as a profile; it just loses the newest-wins
// comparison).
func placesMTime(dir string) time.Time {
	fi, err := os.Stat(filepath.Join(dir, placesFile))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
