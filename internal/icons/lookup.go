package icons

import (
	"os"
	"path/filepath"
	"strings"
)

// iconExts is the candidate file-extension order: PNG preferred, SVG
// second (WebKit renders both natively). XPM is deliberately
// unsupported -- nothing modern renders it and the frontend cannot.
var iconExts = [...]string{".png", ".svg"}

// maxInheritDepth caps Inherits expansion so a pathological theme
// tree cannot recurse forever (the seen-set already breaks cycles;
// the cap bounds honest-but-deep chains).
const maxInheritDepth = 8

// maxThemeIndexBytes caps how large an index.theme this package will
// read (hicolor's, the biggest common one, is ~55KB).
const maxThemeIndexBytes = 4 << 20

// theme returns the lazily-parsed index for a theme name, caching
// hits AND misses (nil = the theme exists in no base dir). The
// index.theme comes from the FIRST base dir that has one; icon files
// are later searched in every base dir, because a theme's directories
// routinely span bases (e.g. user overrides in ~/.icons over
// /usr/share/icons). Callers hold s.mu (or run inside the init
// once).
func (s *Service) theme(name string) *themeIndex {
	if t, ok := s.themes[name]; ok {
		return t
	}
	var t *themeIndex
	if safeIconName(name) {
		for _, base := range s.iconBases {
			idx := filepath.Join(base, name, "index.theme")
			if fi, err := os.Stat(idx); err != nil || !fi.Mode().IsRegular() || fi.Size() > maxThemeIndexBytes {
				continue
			}
			data, err := os.ReadFile(idx)
			if err == nil {
				t = parseThemeIndex(data)
				break
			}
		}
	}
	s.themes[name] = t
	return t
}

// buildChain expands the detected theme name through its Inherits
// chain (depth-first, cycle-guarded, depth-capped), then appends
// "Adwaita" and "hicolor" when absent: hicolor is the spec-mandated
// last resort but ships almost no mimetype icons, so Adwaita sits in
// front of it as the pragmatic net. An empty detected name yields
// just the two fallbacks.
func (s *Service) buildChain(detected string) []string {
	var chain []string
	seen := map[string]bool{}
	var add func(name string, depth int)
	add = func(name string, depth int) {
		if name == "" || depth > maxInheritDepth || seen[name] {
			return
		}
		seen[name] = true
		chain = append(chain, name)
		if t := s.theme(name); t != nil {
			for _, inh := range t.inherits {
				add(inh, depth+1)
			}
		}
	}
	add(detected, 0)
	for _, fb := range []string{"Adwaita", "hicolor"} {
		if !seen[fb] {
			seen[fb] = true
			chain = append(chain, fb)
		}
	}
	return chain
}

// lookupThemed finds the icon file for a themed icon name at the
// wanted physical pixel size: for each theme in the chain, first an
// exact size match, then the closest size; after every theme, the
// unthemed fallbacks (loose files directly in the icon base dirs,
// then the pixmap dirs). Returns "" on a miss. Callers hold s.mu.
func (s *Service) lookupThemed(name string, size int) string {
	for _, themeName := range s.chain {
		t := s.theme(themeName)
		if t == nil {
			continue
		}
		for _, d := range t.dirs {
			if !d.matches(size) {
				continue
			}
			if p := s.firstFile(themeName, d.path, name); p != "" {
				return p
			}
		}
		best, bestDist := "", int(^uint(0)>>1)
		for _, d := range t.dirs {
			dist := d.distance(size)
			if dist >= bestDist {
				continue
			}
			if p := s.firstFile(themeName, d.path, name); p != "" {
				best, bestDist = p, dist
			}
		}
		if best != "" {
			return best
		}
	}
	for _, base := range s.iconBases {
		if p := s.firstFileIn(base, name); p != "" {
			return p
		}
	}
	for _, dir := range s.pixmapDirs {
		if p := s.firstFileIn(dir, name); p != "" {
			return p
		}
	}
	return ""
}

// firstFile probes every icon base dir for
// <base>/<theme>/<subdir>/<name>.<ext>, PNG before SVG, and returns
// the first usable file.
func (s *Service) firstFile(theme, subdir, name string) string {
	for _, base := range s.iconBases {
		if p := s.firstFileIn(filepath.Join(base, theme, subdir), name); p != "" {
			return p
		}
	}
	return ""
}

// firstFileIn probes <dir>/<name>.<ext> for each extension.
func (s *Service) firstFileIn(dir, name string) string {
	for _, ext := range iconExts {
		p := filepath.Join(dir, name+ext)
		if s.usableFile(p) {
			return p
		}
	}
	return ""
}

// usableFile reports whether path is a regular file whose size is
// within (0, MaxFileBytes]. Oversized icons are invisible to the
// lookup, so the search continues to the next candidate.
func (s *Service) usableFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0 && fi.Size() <= s.maxFileBytes
}

// safeIconName rejects the empty name and anything smelling of path
// traversal: a path separator (either flavor) or a ".." segment.
// Themed names are path COMPONENTS, never paths.
func safeIconName(name string) bool {
	return name != "" && !strings.ContainsAny(name, `/\`) && !strings.Contains(name, "..")
}

// stripIconExt drops one trailing .png/.svg/.xpm from a .desktop
// Icon= value, per the spec ("should" carry no extension, but files
// in the wild do); any other dotted suffix is part of the name
// (org.gnome.Calculator).
func stripIconExt(name string) string {
	ext := filepath.Ext(name)
	for _, known := range [...]string{".png", ".svg", ".xpm"} {
		if strings.EqualFold(ext, known) {
			return strings.TrimSuffix(name, ext)
		}
	}
	return name
}
