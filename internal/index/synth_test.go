package index

import (
	"fmt"
	"math/rand"
	"strings"
)

// Synthetic name material: a realistic mix of code-ish and document-ish
// words. "data" is deliberately frequent (common-substring benchmark);
// several words start with "re" (prefix-heavy benchmark); the marker
// token "zzqx" appears only in planted rare names.
var synthWords = []string{
	"data", "report", "readme", "render", "result", "cache", "config",
	"index", "image", "server", "client", "backup", "music", "video",
	"photo", "invoice", "notes", "draft", "spec", "model", "utils",
	"parser", "widget", "button", "search", "window", "theme", "logger",
	"token", "session", "main", "helper",
}

var synthExts = []string{
	".go", ".ts", ".md", ".txt", ".json", ".png", ".pdf", ".log",
	".yaml", ".html", ".css", ".zip", ".csv", ".sql", ".xml", ".doc",
}

// synthRareEvery plants one "zzqx" marker name per this many files.
const synthRareEvery = 37500

func upperFirst(w string) string {
	return strings.ToUpper(w[:1]) + w[1:]
}

// synthFileName generates a unique, realistic file name (words,
// snake/camel case, digits, extension). i must be unique per call.
func synthFileName(rng *rand.Rand, i int) string {
	w1 := synthWords[rng.Intn(len(synthWords))]
	w2 := synthWords[rng.Intn(len(synthWords))]
	ext := synthExts[rng.Intn(len(synthExts))]
	switch rng.Intn(4) {
	case 0:
		return fmt.Sprintf("%s_%s_%d%s", w1, w2, i, ext)
	case 1:
		return fmt.Sprintf("%s%s%d%s", w1, upperFirst(w2), i, ext)
	case 2:
		return fmt.Sprintf("%s-%d%s", w1, i, ext)
	default:
		return fmt.Sprintf("%s%s_%d%s", upperFirst(w1), upperFirst(w2), i, ext)
	}
}

// buildSynthStore builds a store with total entries (about 1/20th of
// them directories, nested randomly under /bench) entirely in memory --
// no disk IO. Deterministic for a given seed. Uses the walker's
// appendEntry fast path; all generated names are unique by
// construction.
func buildSynthStore(seed int64, total int) *Store {
	rng := rand.New(rand.NewSource(seed))
	st := NewStore()
	root := "/bench"
	dirPaths := []string{root}
	dirIDs := []uint32{st.internDir(root)}

	count := 0
	numDirs := total / 20
	for i := 0; i < numDirs && count < total; i++ {
		pi := rng.Intn(len(dirPaths))
		name := fmt.Sprintf("%s_%d", synthWords[rng.Intn(len(synthWords))], i)
		st.appendEntry(dirIDs[pi], dirPaths[pi], name, true)
		full := joinDir(dirPaths[pi], name)
		dirPaths = append(dirPaths, full)
		dirIDs = append(dirIDs, st.dirIndex[full])
		count++
	}
	for i := 0; count < total; i++ {
		di := rng.Intn(len(dirPaths))
		name := synthFileName(rng, i)
		if i%synthRareEvery == 0 {
			name = fmt.Sprintf("zzqx_marker_%d.bin", i)
		}
		st.appendEntry(dirIDs[di], dirPaths[di], name, false)
		count++
	}
	return st
}
