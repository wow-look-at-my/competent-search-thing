package platform

import "path/filepath"

// ResolvedExecutable resolves path to the real file it names: made
// absolute, then every symlink followed (filepath.EvalSymlinks). It is
// the counterpart of StableExecutable for the ONE consumer that must
// name the actual regular file rather than a stable spelling: the
// fanotify setcap grant hint. File capabilities can only be set on a
// regular file -- setcap refuses symlinks outright ("not a regular
// (non-symlink) file") -- and symlinked install layouts (Homebrew's
// linked bin/, Nix, stow) hand the process a symlink spelling, so a
// pasteable setcap command must print the resolved target. ok=false on
// any failure (empty input, unresolvable path, a dangling link);
// callers then keep whatever spelling they already had.
func ResolvedExecutable(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", false
		}
		path = abs
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}
	return resolved, true
}
