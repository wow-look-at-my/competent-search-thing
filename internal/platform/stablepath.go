package platform

import (
	"os"
	"os/exec"
	"path/filepath"
)

// StableExecutable picks the spelling of the running binary's path
// that is worth writing into places that outlive this process -- e.g.
// the command of a GNOME custom keybinding. os.Executable is fully
// resolved on Linux (it reads /proc/self/exe), so under a symlinked
// install layout -- Homebrew's versioned Cellar, the Nix store, stow
// -- it names a version-pinned directory that dies on the next
// upgrade, while the stable path the user actually installed (the
// PATH shim, or the symlink they launched) keeps pointing at the
// current version. Preference order:
//
//  1. the exec.LookPath hit for the binary's base name, kept
//     UNRESOLVED -- the shim/symlink spelling is exactly the stable
//     part -- but only when it names the very binary this process is
//     running: a PATH hit that is a different file (another install,
//     the wrong version first in PATH) is rejected;
//  2. the structural Homebrew candidates derived from exe itself
//     (ParseBrewCellar): the linked <prefix>/<rest>, then the opt
//     <prefix>/opt/<formula>/<rest>. These need no cooperation from
//     PATH or argv[0] -- exactly what the dominant broken-launch
//     context lacks: gsd spawns the keybinding command "<Cellar
//     path> toggle" with its own shim-less PATH, and when no
//     instance is running that very process boots the GUI, so
//     argv[0] IS the versioned Cellar path;
//  3. args0 (os.Args[0]) when it is absolute or resolvable against
//     the working directory, exists, and passes the same same-binary
//     guard -- again kept unresolved;
//  4. exe itself, unchanged.
//
// exe must be the absolute path of the running binary ("" or a
// relative path is the caller's bug and simply falls through to being
// returned as-is); args0 may be "" when unknown. The same-binary
// guard follows symlinks (os.Stat) and compares file identity with
// os.SameFile -- the equivalent of realpath equality, plus hardlink
// tolerance -- so a shim to the running binary is accepted while a
// same-named foreign binary never is.
func StableExecutable(exe, args0 string) string {
	if p, err := exec.LookPath(filepath.Base(exe)); err == nil &&
		filepath.IsAbs(p) && sameFileOnDisk(p, exe) {
		return p
	}
	// The brew candidates come BEFORE args0: in the gsd-boot context
	// above, args0 is the versioned Cellar path and trivially passes
	// the same-binary guard -- consulting it first would re-register
	// the doomed versioned spelling on every startup.
	for _, p := range brewStableCandidates(exe) {
		if sameFileOnDisk(p, exe) {
			return p
		}
	}
	if args0 != "" {
		p := args0
		if !filepath.IsAbs(p) {
			abs, err := filepath.Abs(p)
			if err != nil {
				return exe
			}
			p = abs
		}
		if sameFileOnDisk(p, exe) {
			return p
		}
	}
	return exe
}

// sameFileOnDisk reports whether candidate and exe both exist and are
// the same file once symlinks are followed.
func sameFileOnDisk(candidate, exe string) bool {
	ci, err := os.Stat(candidate)
	if err != nil {
		return false
	}
	ei, err := os.Stat(exe)
	if err != nil {
		return false
	}
	return os.SameFile(ci, ei)
}
