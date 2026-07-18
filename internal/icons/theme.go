package icons

import (
	"bufio"
	"strconv"
	"strings"
)

// dirType is a theme subdirectory's size-matching regime, per the
// freedesktop icon-theme spec.
type dirType int

const (
	typeThreshold dirType = iota // the spec default
	typeFixed
	typeScalable
)

// themeDir is one [subdir] section of an index.theme with the size
// fields resolved: Scale defaults to 1, Threshold to 2, MinSize and
// MaxSize to Size. All comparisons happen in physical pixels
// (Size*Scale), so a "16x16@2" dir with Scale=2 serves want=32.
type themeDir struct {
	path      string
	size      int
	scale     int
	minSize   int
	maxSize   int
	threshold int
	typ       dirType
}

// matches reports whether this dir serves exactly the wanted physical
// pixel size (the lookup's first pass).
func (d themeDir) matches(want int) bool {
	switch d.typ {
	case typeFixed:
		return d.size*d.scale == want
	case typeScalable:
		return d.minSize*d.scale <= want && want <= d.maxSize*d.scale
	default: // typeThreshold
		return abs(want-d.size*d.scale) <= d.threshold*d.scale
	}
}

// distance is how far this dir's size range is from the wanted
// physical pixel size (the lookup's second, closest-match pass);
// 0 means it serves the size natively.
func (d themeDir) distance(want int) int {
	switch d.typ {
	case typeFixed:
		return abs(d.size*d.scale - want)
	case typeScalable:
		switch {
		case want < d.minSize*d.scale:
			return d.minSize*d.scale - want
		case want > d.maxSize*d.scale:
			return want - d.maxSize*d.scale
		}
		return 0
	default: // typeThreshold
		switch {
		case want < (d.size-d.threshold)*d.scale:
			return d.minSize*d.scale - want
		case want > (d.size+d.threshold)*d.scale:
			return want - d.maxSize*d.scale
		}
		return 0
	}
}

// themeIndex is one parsed index.theme: the inheritance chain plus
// the icon subdirectories in file order.
type themeIndex struct {
	inherits []string
	dirs     []themeDir
}

// parseThemeIndex parses an index.theme tolerantly: INI-ish lines,
// #/; comments skipped, whitespace trimmed. Inherits comes from the
// [Icon Theme] section (comma-separated). Every OTHER section that
// carries a positive integer Size key becomes a themeDir (the
// Directories= key is deliberately ignored -- per-directory sections
// are the authoritative data and hicolor's Directories line is a
// 50KB monster). Sections without a valid Size are dropped; optional
// keys with junk values keep their defaults.
func parseThemeIndex(data []byte) *themeIndex {
	t := &themeIndex{}
	var (
		section string
		cur     map[string]string
	)
	flush := func() {
		if section == "" || section == "Icon Theme" || cur == nil {
			return
		}
		size, err := strconv.Atoi(cur["Size"])
		if err != nil || size <= 0 {
			return
		}
		d := themeDir{
			path:      section,
			size:      size,
			scale:     positiveOr(cur["Scale"], 1),
			threshold: positiveOr(cur["Threshold"], 2),
		}
		d.minSize = positiveOr(cur["MinSize"], size)
		d.maxSize = positiveOr(cur["MaxSize"], size)
		switch strings.ToLower(cur["Type"]) {
		case "fixed":
			d.typ = typeFixed
		case "scalable":
			d.typ = typeScalable
		}
		t.dirs = append(t.dirs, d)
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	// hicolor's Directories= line alone is ~50KB; give the scanner
	// room beyond its 64KB default so one giant line cannot truncate
	// the parse.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			section = strings.TrimSpace(line[1 : len(line)-1])
			cur = map[string]string{}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if section == "Icon Theme" {
			if k == "Inherits" {
				t.inherits = t.inherits[:0]
				for _, inh := range strings.Split(v, ",") {
					if inh = strings.TrimSpace(inh); inh != "" {
						t.inherits = append(t.inherits, inh)
					}
				}
			}
			continue
		}
		if cur != nil {
			cur[k] = v
		}
	}
	flush()
	return t
}

// positiveOr parses s as a positive integer, else returns def.
func positiveOr(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// abs is integer absolute value.
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
