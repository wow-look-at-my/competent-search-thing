package app

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Outside-roots hint: an absolute-path query that matches nothing in
// the index, but names something that actually exists on disk OUTSIDE
// every configured root, gets one synthetic result instead of a silent
// empty list -- the file itself, carrying a hint that says which top
// directory to add to roots. Activating it works like any file row
// (Open/Reveal take the path directly and never consult the index).
// An existing path INSIDE the roots stays hint-free on purpose: that
// is an indexing gap (build still running, fresh file), not a scope
// gap, and suggesting a config change would be wrong.

// outsideRootsHint returns the synthetic result for query q, and
// whether all four conditions hold: q cleans to an absolute path, the
// index returned nothing (the caller checked), the path exists on disk
// (lstat through the seam; symlinks count as themselves, matching the
// walker), and it lies outside every configured root.
func (a *App) outsideRootsHint(q string) (Result, bool) {
	p := filepath.Clean(q)
	if !filepath.IsAbs(p) {
		return Result{}, false
	}
	fi, err := a.plat.lstat(p)
	if err != nil {
		return Result{}, false
	}
	if a.manager == nil || pathWithinAny(p, a.manager.Roots()) {
		return Result{}, false
	}
	return Result{
		Path:  p,
		Name:  filepath.Base(p),
		IsDir: fi.IsDir(),
		Hint:  fmt.Sprintf("outside indexed roots -- add %s to roots in config.json", topComponent(p)),
	}, true
}

// pathWithinAny reports whether path equals one of the roots or lies
// underneath one, absolutizing and cleaning each root the same way the
// walker's root normalization does (index.isWithin semantics, ported).
func pathWithinAny(path string, roots []string) bool {
	for _, r := range roots {
		if a, err := filepath.Abs(r); err == nil {
			r = a
		}
		r = filepath.Clean(r)
		if path == r {
			return true
		}
		if !strings.HasSuffix(r, string(filepath.Separator)) {
			r += string(filepath.Separator)
		}
		if strings.HasPrefix(path, r) {
			return true
		}
	}
	return false
}

// topComponent returns the first path component of a clean absolute
// path -- "/etc" for "/etc/hosts", the path itself for "/" -- keeping
// the volume name on Windows (`C:\Users` for `C:\Users\me\x`). That is
// the directory worth suggesting as a new root: adding the deepest
// parent would fix exactly one query.
func topComponent(p string) string {
	vol := filepath.VolumeName(p)
	rest := strings.TrimPrefix(p[len(vol):], string(filepath.Separator))
	if i := strings.IndexByte(rest, filepath.Separator); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return p
	}
	return vol + string(filepath.Separator) + rest
}
