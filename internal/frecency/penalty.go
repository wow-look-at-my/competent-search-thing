package frecency

import "strings"

// Penalty scale. PathPenalty returns a value in [0, PenaltyMax]; the
// engine blend weights it into score units. The constants are the
// contract the tests pin RELATIVE orderings against:
//
//   - each noise-class directory component (cache/temp/vcs trees)
//     costs PenaltyNoiseDir,
//   - each other hidden (dot) directory component costs
//     PenaltyDotDir,
//   - depth beyond DepthThreshold components costs PenaltyPerDepth
//     each, capped at PenaltyDepthMax -- so depth alone can never
//     outweigh a noise-class component: a deep but clean project tree
//     stays cheaper than anything under /tmp, which stays cheaper
//     than a deep cache tree.
//
// This is a RANK NUDGE, never an exclusion: a penalized path still
// matches and still surfaces when nothing cleaner does, and
// frecency/recency signals can outweigh it in the blend.
const (
	PenaltyNoiseDir = 0.3
	PenaltyDotDir   = 0.1
	PenaltyPerDepth = 0.05
	PenaltyDepthMax = 0.25
	DepthThreshold  = 6
	PenaltyMax      = 1.0
)

// noiseDirs are the lowercased directory names treated as cache/temp/
// vcs noise. "tmp" also covers /tmp and /var/tmp locations by their
// own component.
var noiseDirs = map[string]struct{}{
	".cache":       {},
	"cache":        {},
	".git":         {},
	"node_modules": {},
	"tmp":          {},
	".tmp":         {},
	"temp":         {},
	".temp":        {},
}

// PathPenalty scores how noisy a path's LOCATION is, in
// [0, PenaltyMax]. Only the directory chain is scored -- the final
// component never contributes, so a dotfile target like ~/.bashrc or
// an explicit search for the ".cache" directory itself is not
// penalized for its own name. Each directory component scores at most
// once: noise-class names (see noiseDirs, matched case-insensitively,
// so Cache and TEMP count) cost PenaltyNoiseDir, other dot-dirs
// PenaltyDotDir. Deep nesting past DepthThreshold total components
// adds PenaltyPerDepth per extra component, capped at
// PenaltyDepthMax. Both / and \ separate components, so windows paths
// classify identically.
func PathPenalty(path string) float64 {
	comps := splitComponents(path)
	if len(comps) == 0 {
		return 0
	}
	total := 0.0
	for _, c := range comps[:len(comps)-1] { // the base never contributes
		if _, ok := noiseDirs[strings.ToLower(c)]; ok {
			total += PenaltyNoiseDir
		} else if strings.HasPrefix(c, ".") && c != "." && c != ".." {
			total += PenaltyDotDir
		}
	}
	if extra := len(comps) - DepthThreshold; extra > 0 {
		depth := float64(extra) * PenaltyPerDepth
		if depth > PenaltyDepthMax {
			depth = PenaltyDepthMax
		}
		total += depth
	}
	if total > PenaltyMax {
		total = PenaltyMax
	}
	return total
}

// splitComponents splits on both separators and drops empties (so
// leading /, doubled separators, and trailing separators do not
// produce phantom components).
func splitComponents(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
}
