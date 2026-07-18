package appctx

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DesktopDirs builds the XDG application-directory search list, in
// precedence order: $XDG_DATA_HOME (default ~/.local/share via $HOME)
// first, then each entry of $XDG_DATA_DIRS (default
// /usr/local/share:/usr/share), each with "applications" appended.
// getenv is injectable for tests (pass os.Getenv for the real list).
// Empty list entries are skipped, duplicates keep their first
// (highest-precedence) position, and when both $XDG_DATA_HOME and
// $HOME are unset the home entry is simply absent.
func DesktopDirs(getenv func(string) string) []string {
	var bases []string
	dataHome := getenv("XDG_DATA_HOME")
	if dataHome == "" {
		if home := getenv("HOME"); home != "" {
			dataHome = filepath.Join(home, ".local", "share")
		}
	}
	if dataHome != "" {
		bases = append(bases, dataHome)
	}
	dataDirs := getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	// XDG defines the separator as ":" regardless of platform.
	for _, d := range strings.Split(dataDirs, ":") {
		if d != "" {
			bases = append(bases, d)
		}
	}
	dirs := make([]string, 0, len(bases))
	seen := make(map[string]bool, len(bases))
	for _, b := range bases {
		dir := filepath.Join(b, "applications")
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// ScanDesktopDirs parses the .desktop files directly inside each dir
// (no recursion; the extension match is case-sensitive) into launcher
// entries, sorted by Name (then ID) for determinism. An entry is kept
// only when its [Desktop Entry] section has Type=Application and
// non-empty Name and Exec, and is not marked NoDisplay, Hidden, or
// Terminal. Exec is kept raw -- field codes and quoting are the
// plugin layer's problem. ID is the file's base name (the desktop
// id), and per XDG precedence a file in an earlier dir shadows every
// later file with the same id BY PRESENCE, even when it parses to
// nothing (a Hidden=true copy in ~/.local/share is how users disable
// a system-wide app). Unreadable dirs and files are skipped silently.
func ScanDesktopDirs(dirs []string) []InstalledApp {
	var apps []InstalledApp
	seen := make(map[string]bool)
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			id := e.Name()
			if e.IsDir() || !strings.HasSuffix(id, ".desktop") || seen[id] {
				continue
			}
			seen[id] = true // earlier dirs shadow later ones by presence
			app, ok := parseDesktopFile(filepath.Join(dir, id))
			if !ok {
				continue
			}
			app.ID = id
			apps = append(apps, app)
		}
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Name != apps[j].Name {
			return apps[i].Name < apps[j].Name
		}
		return apps[i].ID < apps[j].ID
	})
	return apps
}

// parseDesktopFile reads one .desktop file's [Desktop Entry] keys.
// The format is INI-like: blank lines and #-comments skipped, keys
// only honored inside [Desktop Entry] sections, whitespace around "="
// trimmed, lines without "=" ignored, duplicate keys last-wins.
// Localized keys such as Name[fr] are different keys and thus
// ignored. ok=false for entries that are not displayable
// applications, and for unreadable or oversized-line (binary junk)
// files.
func parseDesktopFile(path string) (InstalledApp, bool) {
	f, err := os.Open(path)
	if err != nil {
		return InstalledApp{}, false
	}
	defer f.Close()

	var (
		inEntry                     bool
		typ, name, exec, icon       string
		noDisplay, hidden, terminal bool
	)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if !inEntry {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		val := strings.TrimSpace(line[eq+1:])
		switch strings.TrimSpace(line[:eq]) {
		case "Type":
			typ = val
		case "Name":
			name = val
		case "Exec":
			exec = val
		case "Icon":
			icon = val
		case "NoDisplay":
			noDisplay = strings.EqualFold(val, "true")
		case "Hidden":
			hidden = strings.EqualFold(val, "true")
		case "Terminal":
			terminal = strings.EqualFold(val, "true")
		}
	}
	if sc.Err() != nil {
		return InstalledApp{}, false
	}
	if typ != "Application" || name == "" || exec == "" || noDisplay || hidden || terminal {
		return InstalledApp{}, false
	}
	return InstalledApp{Name: name, Exec: exec, Icon: icon}, true
}
