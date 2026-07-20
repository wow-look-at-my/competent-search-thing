package arbiter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fileRowJSON / pluginRowJSON / pickJSON build telemetry log lines --
// the literal JSONL shapes are the data contract this reader
// consumes.
func fileRowJSON(rank int, path, ext string, class, depth int) string {
	return fmt.Sprintf(`{"rank":%d,"kind":"file","path":%q,"class":%d,"effClass":%d,"align":7,"boost":1.5,"recency":0.25,"cwd":0.5,"penalty":0.1,"isDir":false,"depth":%d,"ext":%q}`,
		rank, path, class, class, depth, ext)
}

func pluginRowJSON(rank int, plugin string, score int) string {
	return fmt.Sprintf(`{"rank":%d,"kind":"plugin","plugin":%q,"score":%d}`, rank, plugin, score)
}

func pickJSON(ts, query string, joined bool, pickedRank int, pickedKind string, rows ...string) string {
	return fmt.Sprintf(`{"v":1,"ts":%q,"query":%q,"blendActive":true,"joined":%v,"refined":false,"shown":[%s],"picked":{"rank":%d,"kind":%q,"action":"open","revealed":false}}`,
		ts, query, joined, strings.Join(rows, ","), pickedRank, pickedKind)
}

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "telemetry.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return p
}

func TestReadLogFileMissingIsEmpty(t *testing.T) {
	imps, err := ReadLogFile(filepath.Join(t.TempDir(), "nope.jsonl"))
	require.NoError(t, err)
	require.Nil(t, imps)
}

func TestReadLogFileProjectsRecords(t *testing.T) {
	ts := "2026-07-19T15:04:05Z"
	p := writeLog(t,
		pickJSON(ts, "rep", true, 2, "plugin",
			fileRowJSON(0, "/home/me/report.txt", ".txt", 1, 3),
			pluginRowJSON(1, "apps-search", 63),
			pluginRowJSON(2, "firefox-tabs", 85),
			pluginRowJSON(3, "firefox-tabs", 55),
		),
	)
	imps, err := ReadLogFile(p)
	require.NoError(t, err)
	require.Len(t, imps, 1)
	imp := imps[0]
	require.Equal(t, "rep", imp.Query)
	require.True(t, imp.Joined)
	require.Equal(t, 2, imp.Picked)
	want, terr := time.Parse(time.RFC3339, ts)
	require.NoError(t, terr)
	require.True(t, imp.TS.Equal(want))
	require.Len(t, imp.Rows, 4)

	f := imp.Rows[0]
	require.Equal(t, KindFile, f.Kind)
	require.Equal(t, 1, f.Class)
	require.Equal(t, 7, f.Align)
	require.InDelta(t, 1.5, f.Boost, 1e-12)
	require.InDelta(t, 0.25, f.Recency, 1e-12)
	require.InDelta(t, 0.5, f.Cwd, 1e-12)
	require.InDelta(t, 0.1, f.Penalty, 1e-12)
	require.Equal(t, 3, f.Depth)
	require.Equal(t, ".txt", f.Ext)
	require.Equal(t, "rep", f.Query)
	require.Equal(t, want.Local().Hour(), f.Hour, "the pick hour feeds the time-of-day feature")

	require.Equal(t, 1, imp.Rows[1].Priority, "apps-search derives the one production priority")
	require.Equal(t, 0, imp.Rows[2].Priority)
	require.Equal(t, 0, imp.Rows[2].SourceRank, "first firefox-tabs row")
	require.Equal(t, 1, imp.Rows[3].SourceRank, "second firefox-tabs row counts within its source")
	require.Equal(t, "firefox-tabs", imp.Rows[3].Plugin)
	require.Equal(t, 55, imp.Rows[3].Score)
}

func TestReadLogFileTolerance(t *testing.T) {
	good := pickJSON("2026-07-19T15:04:05Z", "q", true, 0, "file",
		fileRowJSON(0, "/a/b.txt", ".txt", 0, 2), fileRowJSON(1, "/a/c.txt", ".txt", 1, 2))
	p := writeLog(t,
		`not json at all`,
		`{"v":2,"ts":"2026-01-01T00:00:00Z","shown":[{"kind":"file"}],"picked":{"rank":0}}`, // wrong version
		pickJSON("bad-ts", "q2", false, 1, "file",
			fileRowJSON(0, "/a/d.txt", ".txt", 0, 2), fileRowJSON(1, "/a/e.txt", ".txt", 1, 2)), // bad ts still parses
		pickJSON("2026-07-19T15:04:05Z", "q3", true, 9, "file", fileRowJSON(0, "/a/f.txt", ".txt", 0, 2)),                            // pick out of range
		`{"v":1,"ts":"2026-01-01T00:00:00Z","joined":true,"shown":[{"rank":0,"kind":"widget"}],"picked":{"rank":0,"kind":"widget"}}`, // unknown row kind
		good,
		`{"v":1,"truncated...`, // torn final line
	)
	imps, err := ReadLogFile(p)
	require.NoError(t, err)
	require.Len(t, imps, 2, "only the bad-ts record and the good record survive")
	require.True(t, imps[0].TS.IsZero(), "an unparseable timestamp reads as zero")
	require.False(t, imps[0].Joined)
	require.Equal(t, "q", imps[1].Query)
}

func TestReadLogFileOversizedIsIgnored(t *testing.T) {
	p := filepath.Join(t.TempDir(), "huge.jsonl")
	f, err := os.Create(p)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(maxSourceFileBytes+1))
	require.NoError(t, f.Close())
	imps, rerr := ReadLogFile(p)
	require.NoError(t, rerr)
	require.Nil(t, imps)
}

func TestReadLogFileRealIOErrors(t *testing.T) {
	// A directory stats fine and fails to read as a file: the one
	// shape that must surface as a real error for the app's one-shot
	// log line.
	_, err := ReadLogFile(t.TempDir())
	require.Error(t, err)
	require.Contains(t, err.Error(), "arbiter:")

	// A path THROUGH a regular file fails at stat (ENOTDIR), the
	// other real-IO-error branch.
	f := filepath.Join(t.TempDir(), "plain")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o600))
	_, err = ReadLogFile(filepath.Join(f, "telemetry.jsonl"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "arbiter:")
}
