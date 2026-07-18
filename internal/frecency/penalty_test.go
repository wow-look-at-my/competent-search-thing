package frecency

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathPenaltyTable(t *testing.T) {
	cases := []struct {
		name string
		path string
		want float64
	}{
		{"empty", "", 0},
		{"root", "/", 0},
		{"clean shallow file", "/home/u/Downloads/log.txt", 0},
		{"clean project file", "/home/u/src/proj/main.go", 0},
		{"tmp location", "/tmp/logging_analysis/log.txt", PenaltyNoiseDir},
		{"var tmp location", "/var/tmp/scratch/log.txt", PenaltyNoiseDir},
		{"dot dir", "/home/u/.config/app/settings.json", PenaltyDotDir},
		{"dotfile base is exempt", "/home/u/.bashrc", 0},
		{"dot dir base is exempt", "/home/u/.cache", 0},
		{"cache dir", "/home/u/.cache/thing", PenaltyNoiseDir},
		{"uppercase Cache counts", "/home/u/app/Cache/f.bin", PenaltyNoiseDir},
		{"TEMP counts", "/home/u/app/TEMP/f.bin", PenaltyNoiseDir},
		{"node_modules", "/home/u/proj/node_modules/x/i.js", PenaltyNoiseDir},
		{"git dir", "/home/u/proj/.git/objects/ab", PenaltyNoiseDir},
		{"noise dirs stack", "/home/u/.cache/tmp/f", 2 * PenaltyNoiseDir},
		{"noise plus dot dir", "/home/u/.mozilla/cache/f", PenaltyDotDir + PenaltyNoiseDir},
		{"depth seven", "/a/b/c/d/e/f/g.txt", PenaltyPerDepth},
		{"depth eight", "/a/b/c/d/e/f/g/h.txt", 2 * PenaltyPerDepth},
		{"depth capped", "/a/b/c/d/e/f/g/h/i/j/k/l/m/n/o/p.txt", PenaltyDepthMax},
		{"windows separators", `C:\Users\u\AppData\Cache\f.bin`, PenaltyNoiseDir},
		{"trailing separator", "/home/u/.cache/thing/", PenaltyNoiseDir},
		{"doubled separators", "//home//u//.cache//thing", PenaltyNoiseDir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.InDelta(t, tc.want, PathPenalty(tc.path), 1e-9)
		})
	}
}

// TestPathPenaltyUserScenario pins the ordering from the motivating
// report: searching "log.txt" must not prefer a temp file 300
// directories deep in ~/.cache over the file just downloaded or the
// one in /tmp/logging_analysis the user was just working in. The
// penalties only NUDGE: /tmp pays its location penalty but stays far
// cheaper than deep cache noise, so recency/frecency can lift it.
func TestPathPenaltyUserScenario(t *testing.T) {
	downloads := "/home/u/Downloads/log.txt"
	tmpWork := "/tmp/logging_analysis/log.txt"
	deepCache := "/home/u/.cache" + strings.Repeat("/d", 300) + "/log.txt"

	pDownloads := PathPenalty(downloads)
	pTmp := PathPenalty(tmpWork)
	pCache := PathPenalty(deepCache)

	require.Less(t, pDownloads, pTmp,
		"a clean Downloads file beats the /tmp workspace on penalty alone")
	require.Less(t, pTmp, pCache,
		"the /tmp workspace stays scoreable above deep cache noise")
	require.LessOrEqual(t, pCache, PenaltyMax, "penalties are bounded")

	// A moderately deep real cache path still costs more than the
	// /tmp workspace.
	firefoxCache := "/home/u/.cache/mozilla/firefox/x9.default/cache2/entries/AB12"
	require.Less(t, pTmp, PathPenalty(firefoxCache))
}

func TestPathPenaltyDepthAloneStaysBelowNoise(t *testing.T) {
	deepClean := "/home/u/src/mono/repo/services/api/internal/handlers/v2/util/deep.go"
	require.Greater(t, PathPenalty(deepClean), 0.0, "deep nesting costs something")
	require.Less(t, PathPenalty(deepClean), PathPenalty("/tmp/x/f"),
		"a deep but clean tree never outweighs a noise-class location")
}

func TestPathPenaltyClampsAtMax(t *testing.T) {
	noisy := strings.Repeat("/.cache", 10) + "/f"
	require.InDelta(t, PenaltyMax, PathPenalty(noisy), 1e-9)
}
