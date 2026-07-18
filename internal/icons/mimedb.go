package icons

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// mimeDB answers "which mimetype is this file name, and which themed
// icon names should represent that mimetype", built from the
// shared-mime-info files under <datadir>/mime: globs2 (name globs)
// and generic-icons (mime -> fallback icon name). A zero mimeDB (no
// data files found) simply answers "" / generic everywhere.
type mimeDB struct {
	// literal globs (no wildcard at all, e.g. "makefile"):
	// case-sensitive entries exact, the rest keyed folded.
	literalCS map[string]mimeEntry
	literalCI map[string]mimeEntry
	// suffix globs ("*.ext" -- a leading '*' and a wildcard-free
	// rest): keyed by the rest ("suffix"), exact for cs entries,
	// folded otherwise. Longest matching suffix wins, then weight.
	suffixCS map[string]mimeEntry
	suffixCI map[string]mimeEntry
	// the few complex globs ('[', '?', or a mid-string '*'), matched
	// via path.Match in weight order.
	complex []complexGlob
	// generic-icons: mimetype -> themed icon name fallback.
	generic map[string]string
}

// mimeEntry is one glob's target with the weight used to break ties
// between entries that land on the same map key.
type mimeEntry struct {
	mime   string
	weight int
}

// complexGlob is one glob that needs full pattern matching. For
// case-insensitive entries the pattern is pre-folded and matched
// against the folded name.
type complexGlob struct {
	pattern string
	mime    string
	weight  int
	cs      bool
}

// loadMimeDB parses <dir>/mime/globs2 and <dir>/mime/generic-icons
// from every data dir in order. The first dir that defines a given
// exact glob (or generic-icons mime) wins; later duplicates are
// dropped, matching XDG precedence. Missing files are fine.
func loadMimeDB(dataDirs []string) *mimeDB {
	db := &mimeDB{
		literalCS: map[string]mimeEntry{},
		literalCI: map[string]mimeEntry{},
		suffixCS:  map[string]mimeEntry{},
		suffixCI:  map[string]mimeEntry{},
		generic:   map[string]string{},
	}
	seenGlob := map[string]bool{}
	for _, dir := range dataDirs {
		db.loadGlobs2(filepath.Join(dir, "mime", "globs2"), seenGlob)
		db.loadGenericIcons(filepath.Join(dir, "mime", "generic-icons"))
	}
	// Highest weight first so the first path.Match hit wins; ties
	// keep parse order (sort stability).
	stableSortByWeight(db.complex)
	return db
}

// loadGlobs2 parses one globs2 file: "weight:mimetype:glob[:flags]"
// per line, #-comments and malformed lines skipped. The only flag
// honored is "cs" (case-sensitive glob). seenGlob dedupes exact glob
// text across data dirs (first dir wins).
func (db *mimeDB) loadGlobs2(path string, seenGlob map[string]bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 4)
		if len(parts) < 3 {
			continue
		}
		weight, err := strconv.Atoi(parts[0])
		if err != nil || weight < 0 {
			continue
		}
		mime, glob := parts[1], parts[2]
		if mime == "" || glob == "" || !strings.Contains(mime, "/") {
			continue
		}
		if seenGlob[glob] {
			continue
		}
		seenGlob[glob] = true
		cs := false
		if len(parts) == 4 {
			for _, flag := range strings.Split(parts[3], ",") {
				if flag == "cs" {
					cs = true
				}
			}
		}
		db.addGlob(glob, mimeEntry{mime: mime, weight: weight}, cs)
	}
}

// addGlob classifies one glob into the literal, suffix, or complex
// tier.
func (db *mimeDB) addGlob(glob string, e mimeEntry, cs bool) {
	rest := strings.TrimPrefix(glob, "*")
	switch {
	case !strings.ContainsAny(glob, "*?["):
		putHigher(pick(cs, db.literalCS, db.literalCI), foldUnless(cs, glob), e)
	case len(rest) == len(glob)-1 && rest != "" && !strings.ContainsAny(rest, "*?["):
		putHigher(pick(cs, db.suffixCS, db.suffixCI), foldUnless(cs, rest), e)
	default:
		pat := foldUnless(cs, glob)
		if _, err := path.Match(pat, ""); err != nil {
			return // malformed pattern
		}
		db.complex = append(db.complex, complexGlob{pattern: pat, mime: e.mime, weight: e.weight, cs: cs})
	}
}

// loadGenericIcons parses one generic-icons file: "mimetype:iconname"
// per line; first dir wins per mimetype.
func (db *mimeDB) loadGenericIcons(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		mime, icon, ok := strings.Cut(line, ":")
		if !ok || mime == "" || icon == "" {
			continue
		}
		if _, dup := db.generic[mime]; !dup {
			db.generic[mime] = icon
		}
	}
}

// MimeForName resolves a bare file name (no directory) to a mimetype,
// or "" when nothing matches. Precedence: literal name > longest
// suffix > complex glob; case-sensitive entries only match exactly,
// everything else folds.
func (db *mimeDB) MimeForName(name string) string {
	if name == "" {
		return ""
	}
	if e, ok := db.literalCS[name]; ok {
		return e.mime
	}
	folded := strings.ToLower(name)
	if e, ok := db.literalCI[folded]; ok {
		return e.mime
	}
	// Longest suffix first: walk every start offset from 0 (whole
	// name; '*' matches empty) and take the first offset with a hit.
	// At the same offset a higher weight wins, tie goes cs. When
	// folding changed the byte length (Kelvin sign and friends) the
	// offsets no longer align, so scan the two spaces independently.
	if len(folded) == len(name) {
		for i := 0; i < len(name); i++ {
			e, csOK := db.suffixCS[name[i:]]
			ci, ciOK := db.suffixCI[folded[i:]]
			switch {
			case csOK && (!ciOK || e.weight >= ci.weight):
				return e.mime
			case ciOK:
				return ci.mime
			}
		}
	} else {
		for i := 0; i < len(name); i++ {
			if e, ok := db.suffixCS[name[i:]]; ok {
				return e.mime
			}
		}
		for i := 0; i < len(folded); i++ {
			if e, ok := db.suffixCI[folded[i:]]; ok {
				return e.mime
			}
		}
	}
	for _, cg := range db.complex {
		cand := folded
		if cg.cs {
			cand = name
		}
		if ok, _ := path.Match(cg.pattern, cand); ok {
			return cg.mime
		}
	}
	return ""
}

// IconNames builds the themed-icon-name candidate chain for a
// mimetype, most specific first: the mime itself with '/' -> '-'
// (application/pdf -> application-pdf), the generic-icons entry, and
// the media-class generic (text/image/audio/video/font-x-generic;
// anything else application-x-generic). Deduped, order kept.
func (db *mimeDB) IconNames(mime string) []string {
	if mime == "" {
		return nil
	}
	names := []string{strings.ReplaceAll(mime, "/", "-")}
	if g := db.generic[mime]; g != "" {
		names = append(names, g)
	}
	class := "application"
	switch c, _, _ := strings.Cut(mime, "/"); c {
	case "text", "image", "audio", "video", "font":
		class = c
	}
	names = append(names, class+"-x-generic")
	out := names[:0]
	seen := map[string]bool{}
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// pick returns cs ? a : b.
func pick(cs bool, a, b map[string]mimeEntry) map[string]mimeEntry {
	if cs {
		return a
	}
	return b
}

// foldUnless lowercases s for case-insensitive entries.
func foldUnless(cs bool, s string) string {
	if cs {
		return s
	}
	return strings.ToLower(s)
}

// putHigher stores e under key unless an entry with a strictly higher
// or equal weight is already there (first entry wins ties).
func putHigher(m map[string]mimeEntry, key string, e mimeEntry) {
	if old, ok := m[key]; ok && old.weight >= e.weight {
		return
	}
	m[key] = e
}

// stableSortByWeight orders complex globs highest weight first,
// keeping parse order between equal weights (insertion sort: the
// slice is tiny).
func stableSortByWeight(globs []complexGlob) {
	for i := 1; i < len(globs); i++ {
		for j := i; j > 0 && globs[j-1].weight < globs[j].weight; j-- {
			globs[j-1], globs[j] = globs[j], globs[j-1]
		}
	}
}
