package frecency

import (
	"math"
	"os"
	"path/filepath"
)

// ProcTree is the injectable process-tree seam DeriveCwd walks. The
// production implementation (a LATER phase, in the app-context
// capture path: /proc/PID/cwd readlinks, children via /proc/*/stat
// ppid, the stat tpgid as the Foreground hint, X11-only and
// best-effort) is not in this package -- B1 ships only the pure
// heuristic.
type ProcTree interface {
	// Children returns pid's direct children.
	Children(pid int) []int
	// Cwd returns pid's working directory; an error means unreadable
	// (a cross-user /proc readlink fails; expected).
	Cwd(pid int) (string, error)
	// Foreground returns the tpgid-style foreground-process hint for
	// pid's tree, when available.
	Foreground(pid int) (int, bool)
}

// maxProcDepth bounds the descendant walk (and, with the visited set,
// makes a glitched cyclic tree harmless).
const maxProcDepth = 32

// DeriveCwd finds the working directory that best represents "where
// the focused app is": the directory the engine blend will boost
// results under. Selection: the Foreground hint on the root is
// consulted first and its cwd wins when meaningful; otherwise the
// tree is walked depth-first (root included, Children order) and the
// DEEPEST process with a meaningful cwd wins, ties keeping the
// earlier find. A cwd is NOT meaningful when it is unreadable, "/",
// or the user's home (os.UserHomeDir) -- a terminal's own cwd is ~
// and a GUI app parks at / or ~, so those contribute nothing; the
// shell or editor CHILD holds the real signal. ok=false means no
// process offered a meaningful cwd.
func DeriveCwd(tree ProcTree, rootPID int) (string, bool) {
	if tree == nil || rootPID <= 0 {
		return "", false
	}
	home, _ := os.UserHomeDir()
	meaningful := func(pid int) (string, bool) {
		cwd, err := tree.Cwd(pid)
		if err != nil || cwd == "" {
			return "", false
		}
		cwd = filepath.Clean(cwd)
		if cwd == "/" || (home != "" && cwd == filepath.Clean(home)) {
			return "", false
		}
		return cwd, true
	}
	if fg, ok := tree.Foreground(rootPID); ok {
		if cwd, ok := meaningful(fg); ok {
			return cwd, true
		}
	}
	best, bestDepth := "", -1
	visited := map[int]bool{}
	var walk func(pid, depth int)
	walk = func(pid, depth int) {
		if visited[pid] || depth > maxProcDepth {
			return
		}
		visited[pid] = true
		if cwd, ok := meaningful(pid); ok && depth > bestDepth {
			best, bestDepth = cwd, depth
		}
		for _, child := range tree.Children(pid) {
			walk(child, depth+1)
		}
	}
	walk(rootPID, 0)
	return best, bestDepth >= 0
}

// CwdDepthFalloff halves the cwd boost per path component below the
// direct-child level (the scale companion to the Penalty constants:
// weight is in the engine blend's score units).
const CwdDepthFalloff = 0.5

// CwdBoost scores how close path sits to the focused app's working
// directory: the full weight for cwd itself and its direct children,
// then CwdDepthFalloff per extra component (cwd/a = weight, cwd/a/b =
// weight/2, ...). Containment is component-wise -- /projX is not
// under /proj -- with / and \ both separating, like PathPenalty. A
// zero or negative weight disables the boost, and an empty or root
// cwd boosts nothing (everything is "under" /; that is no signal).
func CwdBoost(path, cwd string, weight float64) float64 {
	if weight <= 0 || path == "" {
		return 0
	}
	cwdComps := splitComponents(cwd)
	if len(cwdComps) == 0 {
		return 0
	}
	pathComps := splitComponents(path)
	if len(pathComps) < len(cwdComps) {
		return 0
	}
	for i, c := range cwdComps {
		if pathComps[i] != c {
			return 0
		}
	}
	extra := len(pathComps) - len(cwdComps)
	if extra <= 1 {
		return weight
	}
	return weight * math.Pow(CwdDepthFalloff, float64(extra-1))
}
