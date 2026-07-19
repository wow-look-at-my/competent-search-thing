package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T, maxKB int) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "telemetry.jsonl")
	s := New(path, maxKB)
	s.now = func() time.Time { return time.Date(2026, 7, 19, 12, 34, 56, 0, time.UTC) }
	return s, path
}

func fileRecord(query, path string) Record {
	return Record{
		Query:       query,
		BlendActive: true,
		Joined:      true,
		Shown: []ShownRow{
			{Rank: 0, Kind: KindFile, Path: path, Class: 1, EffClass: 0, Align: 0,
				Boost: 2.31, Recency: 0.42, Penalty: 0.1, Depth: 4, Ext: ".md"},
			{Rank: 1, Kind: KindPlugin, Plugin: "apps-search", Score: 90},
		},
		Picked: PickedRow{Rank: 0, Kind: KindFile, Path: path, Action: ActionOpen},
	}
}

// readLines returns the file's newline-separated JSON records, parsed.
func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), "line %q", line)
		out = append(out, m)
	}
	return out
}

func TestAppendWritesOneStampedLinePerRecord(t *testing.T) {
	s, path := testStore(t, 64)
	require.NoError(t, s.Append(fileRecord("rep", "/home/u/reports/q3.md")))
	require.NoError(t, s.Append(fileRecord("other", "/home/u/notes.txt")))

	recs := readLines(t, path)
	require.Len(t, recs, 2)
	first := recs[0]
	require.Equal(t, float64(1), first["v"], "the version is stamped")
	require.Equal(t, "2026-07-19T12:34:56Z", first["ts"], "the timestamp is stamped RFC3339 UTC")
	require.Equal(t, "rep", first["query"])
	require.Equal(t, true, first["blendActive"])
	require.Equal(t, true, first["joined"])
	require.Equal(t, false, first["refined"])

	st, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), st.Mode().Perm(), "the log is user-only")
}

func TestShownRowMarshalIsKindShaped(t *testing.T) {
	s, path := testStore(t, 64)
	require.NoError(t, s.Append(fileRecord("rep", "/home/u/reports/q3.md")))
	rec := readLines(t, path)[0]
	shown, ok := rec["shown"].([]any)
	require.True(t, ok)
	require.Len(t, shown, 2)

	file, ok := shown[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "file", file["kind"])
	require.Equal(t, "/home/u/reports/q3.md", file["path"])
	// Every feature field is explicit on file rows, zeros included --
	// class 0 (exact) must never be ambiguous with "omitted".
	for _, key := range []string{"rank", "class", "effClass", "align", "boost", "recency", "cwd", "penalty", "isDir", "depth", "ext"} {
		require.Contains(t, file, key, "file rows carry %q explicitly", key)
	}
	require.NotContains(t, file, "plugin")
	require.NotContains(t, file, "score")

	plug, ok := shown[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, map[string]any{
		"rank": float64(1), "kind": "plugin", "plugin": "apps-search", "score": float64(90),
	}, plug, "plugin rows carry exactly rank/kind/plugin/score")
}

func TestAppendPreservesExplicitVersionAndTS(t *testing.T) {
	s, path := testStore(t, 64)
	rec := fileRecord("rep", "/a/b")
	rec.V = 7
	rec.TS = "2020-01-01T00:00:00Z"
	require.NoError(t, s.Append(rec))
	got := readLines(t, path)[0]
	require.Equal(t, float64(7), got["v"])
	require.Equal(t, "2020-01-01T00:00:00Z", got["ts"])
}

func TestAppendRotatesAtCap(t *testing.T) {
	s, path := testStore(t, 1) // 1 KiB cap; each record is a few hundred bytes
	long := strings.Repeat("x", 400)
	require.NoError(t, s.Append(fileRecord("q1", "/home/u/"+long)))
	require.NoError(t, s.Append(fileRecord("q2", "/home/u/"+long)))
	// The third append would cross 1024 bytes: the live file rotates.
	require.NoError(t, s.Append(fileRecord("q3", "/home/u/"+long)))

	live := readLines(t, path)
	require.Len(t, live, 1)
	require.Equal(t, "q3", live[0]["query"])
	old := readLines(t, path+".1")
	require.Len(t, old, 2)
	require.Equal(t, "q1", old[0]["query"])

	// The next rotation REPLACES the .1 generation: at most two files.
	require.NoError(t, s.Append(fileRecord("q4", "/home/u/"+long)))
	require.NoError(t, s.Append(fileRecord("q5", "/home/u/"+long)))
	old = readLines(t, path+".1")
	require.Equal(t, "q3", old[0]["query"], "rotation clobbers the previous .1")
	require.NoFileExists(t, path+".2")
}

func TestAppendNeverRotatesAnEmptyFile(t *testing.T) {
	s, path := testStore(t, 1)
	huge := fileRecord("big", "/home/u/"+strings.Repeat("y", 3000))
	require.NoError(t, s.Append(huge), "an over-cap record still lands in the fresh file")
	require.NoFileExists(t, path+".1")
	require.Len(t, readLines(t, path), 1)
}

func TestNilStoreIsANoOp(t *testing.T) {
	var s *Store
	require.NoError(t, s.Append(fileRecord("q", "/a/b")))
	require.Empty(t, s.Path())
}

func TestNewRepairsNonPositiveCap(t *testing.T) {
	s := New("/tmp/x/telemetry.jsonl", 0)
	require.Equal(t, int64(defaultMaxSizeKB)*1024, s.maxSize)
	s = New("/tmp/x/telemetry.jsonl", -3)
	require.Equal(t, int64(defaultMaxSizeKB)*1024, s.maxSize)
	require.Equal(t, "/tmp/x/telemetry.jsonl", s.Path())
}

func TestConcurrentAppendsNeverInterleave(t *testing.T) {
	s, path := testStore(t, 1024)
	var wg sync.WaitGroup
	errs := make(chan error, 16*8)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				errs <- s.Append(fileRecord("conc", "/home/u/conc.txt"))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, readLines(t, path), 16*8, "every record is one intact line")
}

func TestValidatePickReport(t *testing.T) {
	okFile := PickReport{
		Query: "rep",
		Shown: []ShownRef{
			{Kind: KindFile, Path: "/home/u/a.txt"},
			{Kind: KindPlugin, Plugin: "calc", Score: 90},
		},
		Picked: PickedRef{Rank: 0, Action: ActionOpen},
	}
	require.NoError(t, ValidatePickReport(okFile))

	reveal := okFile
	reveal.Picked = PickedRef{Rank: 0, Action: ActionReveal, Revealed: true}
	require.NoError(t, ValidatePickReport(reveal))

	plugPick := okFile
	plugPick.Picked = PickedRef{Rank: 1, Action: "copy_text"}
	require.NoError(t, ValidatePickReport(plugPick))

	mutate := func(fn func(*PickReport)) PickReport {
		r := okFile
		r.Shown = append([]ShownRef(nil), okFile.Shown...)
		fn(&r)
		return r
	}
	bad := map[string]PickReport{
		"empty shown":        mutate(func(r *PickReport) { r.Shown = nil }),
		"too many rows":      mutate(func(r *PickReport) { r.Shown = make([]ShownRef, MaxShownRows+1) }),
		"long query":         mutate(func(r *PickReport) { r.Query = strings.Repeat("q", maxQueryBytes+1) }),
		"relative file path": mutate(func(r *PickReport) { r.Shown[0].Path = "a.txt" }),
		"empty file path":    mutate(func(r *PickReport) { r.Shown[0].Path = "" }),
		"long file path":     mutate(func(r *PickReport) { r.Shown[0].Path = "/" + strings.Repeat("p", maxPathBytes) }),
		"file with plugin":   mutate(func(r *PickReport) { r.Shown[0].Plugin = "calc" }),
		"file with score":    mutate(func(r *PickReport) { r.Shown[0].Score = 10 }),
		"bad plugin id":      mutate(func(r *PickReport) { r.Shown[1].Plugin = "no spaces" }),
		"plugin with path":   mutate(func(r *PickReport) { r.Shown[1].Path = "/x" }),
		"score too high":     mutate(func(r *PickReport) { r.Shown[1].Score = 101 }),
		"negative score":     mutate(func(r *PickReport) { r.Shown[1].Score = -1 }),
		"unknown kind":       mutate(func(r *PickReport) { r.Shown[0].Kind = "window" }),
		"rank negative":      mutate(func(r *PickReport) { r.Picked.Rank = -1 }),
		"rank past end":      mutate(func(r *PickReport) { r.Picked.Rank = 2 }),
		"file bad action":    mutate(func(r *PickReport) { r.Picked.Action = "copy_text" }),
		"file revealed lie":  mutate(func(r *PickReport) { r.Picked.Revealed = true }),
		"reveal flag lie":    mutate(func(r *PickReport) { r.Picked = PickedRef{Rank: 0, Action: ActionReveal, Revealed: false} }),
		"plugin bad action":  mutate(func(r *PickReport) { r.Picked = PickedRef{Rank: 1, Action: "Copy Text!"} }),
		"plugin revealed":    mutate(func(r *PickReport) { r.Picked = PickedRef{Rank: 1, Action: "copy_text", Revealed: true} }),
	}
	for name, rep := range bad {
		require.Error(t, ValidatePickReport(rep), "case %q must fail", name)
	}
}

func TestAppendReportsUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	s := New(filepath.Join(dir, "sub", "telemetry.jsonl"), 64)
	require.Error(t, s.Append(fileRecord("q", "/a/b")))
}
