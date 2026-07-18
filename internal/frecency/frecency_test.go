package frecency

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock is the injectable Now for deterministic decay math.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func tempStore(t *testing.T, opts Options) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frecency.json")
	return New(path, opts), path
}

func TestRecordOpenFirstOpenCountsOne(t *testing.T) {
	clock := newClock()
	s, _ := tempStore(t, Options{Now: clock.Now})
	require.NoError(t, s.RecordOpen("/home/u/notes.txt"))
	require.Equal(t, 1.0, s.Boost("/home/u/notes.txt"))
	require.Equal(t, 0.0, s.Boost("/home/u/other.txt"), "unknown paths boost 0")
}

func TestBoostDecaysByHalfLife(t *testing.T) {
	clock := newClock()
	s, _ := tempStore(t, Options{Now: clock.Now})
	require.NoError(t, s.RecordOpen("/p"))

	clock.Advance(DefaultHalfLife)
	require.InDelta(t, 0.5, s.Boost("/p"), 1e-9)

	clock.Advance(DefaultHalfLife)
	require.InDelta(t, 0.25, s.Boost("/p"), 1e-9)
}

func TestCustomHalfLife(t *testing.T) {
	clock := newClock()
	s, _ := tempStore(t, Options{HalfLife: time.Hour, Now: clock.Now})
	require.NoError(t, s.RecordOpen("/p"))
	clock.Advance(30 * time.Minute)
	require.InDelta(t, 0.7071, s.Boost("/p"), 1e-3, "half a half-life = 1/sqrt(2)")
}

func TestRecordOpenDecaysBeforeIncrementing(t *testing.T) {
	clock := newClock()
	s, _ := tempStore(t, Options{Now: clock.Now})
	require.NoError(t, s.RecordOpen("/p"))
	clock.Advance(DefaultHalfLife)
	require.NoError(t, s.RecordOpen("/p"))
	require.InDelta(t, 1.5, s.Boost("/p"), 1e-9, "0.5 decayed + 1 new")

	raw := s.Entries()["/p"]
	require.InDelta(t, 1.5, raw.Count, 1e-9)
	require.Equal(t, clock.Now(), raw.LastTouch, "LastTouch resets on open")
}

func TestFutureLastTouchDoesNotDecay(t *testing.T) {
	clock := newClock()
	s, _ := tempStore(t, Options{Now: clock.Now})
	require.NoError(t, s.RecordOpen("/p"))
	clock.Advance(-time.Hour) // clock skew: stored touch is now in the future
	require.Equal(t, 1.0, s.Boost("/p"), "non-positive elapse decays nothing")
}

func TestRecordOpenSkipsEmptyPathAndKeepsWhitespaceVerbatim(t *testing.T) {
	s, path := tempStore(t, Options{Persist: true})
	require.NoError(t, s.RecordOpen(""))
	require.Empty(t, s.Entries())
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "empty opens must not create the file")

	spaced := "/home/u/dir with  spaces/f "
	require.NoError(t, s.RecordOpen(spaced))
	// The real clock ticks between record and read: allow the tiny
	// decay.
	require.InDelta(t, 1.0, s.Boost(spaced), 1e-6, "paths are never trimmed")
}

func TestBoostIsReadOnly(t *testing.T) {
	s, path := tempStore(t, Options{Persist: true})
	require.NoError(t, s.RecordOpen("/p"))
	require.NoError(t, os.Remove(path))
	_ = s.Boost("/p")
	_ = s.Boost("/unknown")
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "Boost never writes the file back")
}

func TestCapPrunesLowestDecayedOnRecord(t *testing.T) {
	clock := newClock()
	s, _ := tempStore(t, Options{Cap: 3, Now: clock.Now})
	for _, p := range []string{"/a", "/b", "/c"} {
		require.NoError(t, s.RecordOpen(p))
		clock.Advance(time.Hour)
	}
	require.NoError(t, s.RecordOpen("/d"))
	got := s.Entries()
	require.Len(t, got, 3)
	require.NotContains(t, got, "/a", "the most-decayed count is pruned")
	require.Contains(t, got, "/d")
}

func TestCapKeepsStrongOldOverWeakNew(t *testing.T) {
	clock := newClock()
	s, _ := tempStore(t, Options{Cap: 3, Now: clock.Now})
	for i := 0; i < 5; i++ {
		require.NoError(t, s.RecordOpen("/hot"))
	}
	clock.Advance(24 * time.Hour)
	for _, p := range []string{"/x", "/y", "/z"} {
		require.NoError(t, s.RecordOpen(p))
	}
	got := s.Entries()
	require.Len(t, got, 3)
	require.Contains(t, got, "/hot", "a strong decayed count outranks fresh single opens")
	require.NotContains(t, got, "/z", "full ties keep the lexicographically first paths")
	require.Contains(t, got, "/x")
}

func TestCapTieBreakPrefersNewerTouch(t *testing.T) {
	clock := newClock()
	s, _ := tempStore(t, Options{Cap: 1, Now: clock.Now})
	require.NoError(t, s.RecordOpen("/old"))
	clock.Advance(time.Hour)
	require.NoError(t, s.RecordOpen("/new"))
	// Decayed: /old = 0.5^(1h/14d) < 1.0 = /new; /new survives both by
	// score and by touch.
	got := s.Entries()
	require.Len(t, got, 1)
	require.Contains(t, got, "/new")
}

func TestPersistWritesVersionedJSONWith0600(t *testing.T) {
	clock := newClock()
	s, path := tempStore(t, Options{Persist: true, Now: clock.Now})
	require.NoError(t, s.RecordOpen("/p"))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	require.JSONEq(t, "1", string(raw["v"]))
	require.Contains(t, string(raw["entries"]), "/p")
	require.Equal(t, byte('\n'), data[len(data)-1], "file ends with a newline")

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestPersistWriteIsAtomic(t *testing.T) {
	s, path := tempStore(t, Options{Persist: true})
	require.NoError(t, s.RecordOpen("/p"))
	names, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, names, 1, "the temp file was renamed away")
	require.Equal(t, "frecency.json", names[0].Name())
}

func TestPersistCreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "er", "frecency.json")
	s := New(path, Options{Persist: true})
	require.NoError(t, s.RecordOpen("/p"))
	_, err := os.Stat(path)
	require.NoError(t, err)
}

func TestMemoryOnlyNeverTouchesDisk(t *testing.T) {
	s, path := tempStore(t, Options{})
	require.NoError(t, s.RecordOpen("/p"))
	require.InDelta(t, 1.0, s.Boost("/p"), 1e-6)
	names, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Empty(t, names)
}

func TestLoadMissingFileIsEmptyAndNoError(t *testing.T) {
	s, _ := tempStore(t, Options{Persist: true})
	require.NoError(t, s.Load())
	require.Empty(t, s.Entries())
}

func TestLoadRoundTripAppliesDecayOnRead(t *testing.T) {
	clock := newClock()
	path := filepath.Join(t.TempDir(), "frecency.json")
	s1 := New(path, Options{Persist: true, Now: clock.Now})
	require.NoError(t, s1.RecordOpen("/p"))

	clock.Advance(DefaultHalfLife)
	s2 := New(path, Options{Persist: true, Now: clock.Now})
	require.NoError(t, s2.Load())
	require.InDelta(t, 1.0, s2.Entries()["/p"].Count, 1e-9,
		"stored count survives verbatim")
	require.InDelta(t, 0.5, s2.Boost("/p"), 1e-9, "decay applies on read")
}

func TestLoadMemoryOnlyIgnoresExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frecency.json")
	s1 := New(path, Options{Persist: true})
	require.NoError(t, s1.RecordOpen("/p"))

	s2 := New(path, Options{})
	require.NoError(t, s2.Load())
	require.Empty(t, s2.Entries(), "memory-only: the file is not even read")
}

func TestLoadCorruptFileStartsEmptyAndReturnsError(t *testing.T) {
	cases := map[string]string{
		"not json":            "{nope",
		"json array":          `[1,2,3]`,
		"json string":         `"just a string"`,
		"wrong version":       `{"v":2,"entries":{}}`,
		"missing version":     `{"entries":{}}`,
		"null":                `null`,
		"entries wrong shape": `{"v":1,"entries":{"/p":"nope"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, path := tempStore(t, Options{Persist: true})
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
			err := s.Load()
			require.Error(t, err)
			require.Contains(t, err.Error(), path, "the error names the file for logging")
			require.Empty(t, s.Entries())

			// The store stays usable and the next open rewrites a
			// valid file over the corrupt one.
			require.NoError(t, s.RecordOpen("/recovered"))
			s2 := New(path, Options{Persist: true})
			require.NoError(t, s2.Load())
			require.Contains(t, s2.Entries(), "/recovered")
		})
	}
}

func TestLoadUnreadablePathReturnsError(t *testing.T) {
	dir := t.TempDir() // a directory is unreadable as a file
	s := New(dir, Options{Persist: true})
	require.Error(t, s.Load())
	require.Empty(t, s.Entries())
}

func TestLoadDropsGarbageEntries(t *testing.T) {
	body := `{"v":1,"entries":{
		"": {"c": 3, "t": "2026-07-01T00:00:00Z"},
		"/zero-count": {"c": 0, "t": "2026-07-01T00:00:00Z"},
		"/negative": {"c": -2, "t": "2026-07-01T00:00:00Z"},
		"/zero-time": {"c": 1, "t": "0001-01-01T00:00:00Z"},
		"/good": {"c": 2.5, "t": "2026-07-01T00:00:00Z"}
	}}`
	s, path := tempStore(t, Options{Persist: true, Now: newClock().Now})
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	require.NoError(t, s.Load())
	got := s.Entries()
	require.Len(t, got, 1)
	require.InDelta(t, 2.5, got["/good"].Count, 1e-9)
}

func TestLoadEnforcesCap(t *testing.T) {
	entries := map[string]fileEntry{}
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		entries[fmt.Sprintf("/p%02d", i)] = fileEntry{C: float64(i + 1), T: base}
	}
	data, err := json.Marshal(fileFormat{V: 1, Entries: entries})
	require.NoError(t, err)

	s, path := tempStore(t, Options{Persist: true, Cap: 4, Now: newClock().Now})
	require.NoError(t, os.WriteFile(path, data, 0o600))
	require.NoError(t, s.Load())
	got := s.Entries()
	require.Len(t, got, 4)
	for _, keep := range []string{"/p09", "/p08", "/p07", "/p06"} {
		require.Contains(t, got, keep, "the highest decayed counts survive")
	}
}

func TestRecordOpenSaveFailureKeepsMemoryEntry(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	// The parent "directory" is a regular file, so MkdirAll fails.
	s := New(filepath.Join(blocker, "sub", "frecency.json"), Options{Persist: true})
	require.Error(t, s.RecordOpen("/p"))
	require.InDelta(t, 1.0, s.Boost("/p"), 1e-6,
		"in-session ranking survives disk problems")
}

func TestEntriesReturnsDefensiveCopy(t *testing.T) {
	s, _ := tempStore(t, Options{})
	require.NoError(t, s.RecordOpen("/p"))
	got := s.Entries()
	got["/p"] = Entry{Count: 99}
	got["/injected"] = Entry{Count: 1}
	require.InDelta(t, 1.0, s.Boost("/p"), 1e-6, "callers cannot mutate the store")
	require.Len(t, s.Entries(), 1)
}

func TestNilStoreNoOps(t *testing.T) {
	var s *Store
	require.NoError(t, s.RecordOpen("/p"))
	require.NoError(t, s.Load())
	require.Equal(t, 0.0, s.Boost("/p"))
	got := s.Entries()
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestConcurrentRecordAndBoost(t *testing.T) {
	s, path := tempStore(t, Options{Persist: true})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := fmt.Sprintf("/g%02d", i)
			require.NoError(t, s.RecordOpen(p))
			_ = s.Boost(p)
			_ = s.Entries()
		}(i)
	}
	wg.Wait()
	require.Len(t, s.Entries(), 16)

	s2 := New(path, Options{Persist: true})
	require.NoError(t, s2.Load())
	require.Len(t, s2.Entries(), 16, "the last atomic write holds all entries")
}
