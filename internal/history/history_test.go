package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func tempStore(t *testing.T, persist bool) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.json")
	return New(path, persist), path
}

func TestAddAppendsOldestToNewest(t *testing.T) {
	s, _ := tempStore(t, false)
	require.NoError(t, s.Add("alpha"))
	require.NoError(t, s.Add("beta"))
	require.NoError(t, s.Add("gamma"))
	require.Equal(t, []string{"alpha", "beta", "gamma"}, s.Entries())
}

func TestAddTrimsWhitespace(t *testing.T) {
	s, _ := tempStore(t, false)
	require.NoError(t, s.Add("  spaced out \t"))
	require.Equal(t, []string{"spaced out"}, s.Entries())
}

func TestAddSkipsBlankEntries(t *testing.T) {
	s, path := tempStore(t, true)
	require.NoError(t, s.Add(""))
	require.NoError(t, s.Add("   \t\n"))
	require.Empty(t, s.Entries())
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), "blank adds must not create the file")
}

func TestAddMovesExactMatchToNewest(t *testing.T) {
	s, _ := tempStore(t, false)
	for _, e := range []string{"a", "b", "c", "a"} {
		require.NoError(t, s.Add(e))
	}
	require.Equal(t, []string{"b", "c", "a"}, s.Entries(), "dedup moves, never duplicates")

	// Trimming happens before the dedup match.
	require.NoError(t, s.Add("  b  "))
	require.Equal(t, []string{"c", "a", "b"}, s.Entries())
}

func TestAddCapDropsOldest(t *testing.T) {
	s, _ := tempStore(t, false)
	for i := 0; i < maxEntries; i++ {
		require.NoError(t, s.Add(fmt.Sprintf("e%03d", i)))
	}
	require.Len(t, s.Entries(), maxEntries, "exactly at the cap nothing drops")
	require.Equal(t, "e000", s.Entries()[0])

	require.NoError(t, s.Add("overflow"))
	got := s.Entries()
	require.Len(t, got, maxEntries)
	require.Equal(t, "e001", got[0], "the oldest entry dropped")
	require.Equal(t, "overflow", got[maxEntries-1])
}

func TestEntriesReturnsDefensiveCopy(t *testing.T) {
	s, _ := tempStore(t, false)
	require.NoError(t, s.Add("keep"))
	got := s.Entries()
	got[0] = "mutated"
	require.Equal(t, []string{"keep"}, s.Entries(), "callers cannot mutate the store")

	empty := New(filepath.Join(t.TempDir(), "h.json"), false).Entries()
	require.NotNil(t, empty)
	require.Empty(t, empty)
}

func TestPersistWritesValidJSONWith0600(t *testing.T) {
	s, path := tempStore(t, true)
	require.NoError(t, s.Add("first"))
	require.NoError(t, s.Add("second"))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var onDisk []string
	require.NoError(t, json.Unmarshal(data, &onDisk))
	require.Equal(t, []string{"first", "second"}, onDisk)
	require.True(t, strings.HasSuffix(string(data), "\n"), "file ends with a newline")

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestPersistWriteIsAtomic(t *testing.T) {
	s, path := tempStore(t, true)
	require.NoError(t, s.Add("only"))

	// The temp file was renamed away: nothing but history.json remains.
	names, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, names, 1)
	require.Equal(t, "history.json", names[0].Name())
}

func TestPersistCreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "er", "history.json")
	s := New(path, true)
	require.NoError(t, s.Add("made it"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "made it")
}

func TestMemoryOnlyWritesNothing(t *testing.T) {
	s, path := tempStore(t, false)
	require.NoError(t, s.Add("private"))
	require.Equal(t, []string{"private"}, s.Entries(), "in-session recall still works")

	names, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Empty(t, names, "a memory-only store never touches the disk")
}

func TestLoadMissingFileIsEmptyAndNoError(t *testing.T) {
	s, _ := tempStore(t, true)
	require.NoError(t, s.Load())
	require.Empty(t, s.Entries())
}

func TestLoadValidRoundTrip(t *testing.T) {
	s1, path := tempStore(t, true)
	for _, e := range []string{"one", "two", "three"} {
		require.NoError(t, s1.Add(e))
	}

	s2 := New(path, true)
	require.NoError(t, s2.Load())
	require.Equal(t, []string{"one", "two", "three"}, s2.Entries())
}

func TestLoadCorruptFileStartsEmptyAndReturnsError(t *testing.T) {
	cases := map[string]string{
		"not json":         "{nope",
		"json object":      `{"a":1}`,
		"json string":      `"just a string"`,
		"array of numbers": `[1,2,3]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, path := tempStore(t, true)
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
			err := s.Load()
			require.Error(t, err)
			require.Contains(t, err.Error(), path, "the error names the file for logging")
			require.Empty(t, s.Entries())

			// The store stays fully usable and the next Add rewrites a
			// valid file over the corrupt one.
			require.NoError(t, s.Add("recovered"))
			s2 := New(path, true)
			require.NoError(t, s2.Load())
			require.Equal(t, []string{"recovered"}, s2.Entries())
		})
	}
}

func TestLoadJSONNullIsEmptyAndNoError(t *testing.T) {
	s, path := tempStore(t, true)
	require.NoError(t, os.WriteFile(path, []byte("null"), 0o600))
	require.NoError(t, s.Load())
	require.Empty(t, s.Entries())
}

func TestLoadUnreadablePathReturnsError(t *testing.T) {
	dir := t.TempDir() // a directory is unreadable as a file
	s := New(dir, true)
	err := s.Load()
	require.Error(t, err)
	require.Empty(t, s.Entries())
}

func TestLoadNormalizesHandEditedFiles(t *testing.T) {
	s, path := tempStore(t, true)
	require.NoError(t, os.WriteFile(path,
		[]byte(`["  a  ", "", "b", "   ", "a", "c"]`), 0o600))
	require.NoError(t, s.Load())
	require.Equal(t, []string{"b", "a", "c"}, s.Entries(),
		"trimmed, blanks dropped, duplicate keeps its newest occurrence")
}

func TestLoadCapsOversizedFiles(t *testing.T) {
	raw := make([]string, maxEntries+7)
	for i := range raw {
		raw[i] = fmt.Sprintf("q%03d", i)
	}
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	s, path := tempStore(t, true)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	require.NoError(t, s.Load())
	got := s.Entries()
	require.Len(t, got, maxEntries)
	require.Equal(t, "q007", got[0], "the oldest overflow dropped")
	require.Equal(t, raw[len(raw)-1], got[maxEntries-1])
}

func TestLoadMemoryOnlyIgnoresExistingFile(t *testing.T) {
	s1, path := tempStore(t, true)
	require.NoError(t, s1.Add("from an earlier persisting run"))

	s2 := New(path, false)
	require.NoError(t, s2.Load())
	require.Empty(t, s2.Entries(),
		"persistDisabled means memory only: the file is not even read")
}

func TestLoadClearsPreviousEntries(t *testing.T) {
	s, _ := tempStore(t, true)
	require.NoError(t, s.Add("stale"))
	require.NoError(t, s.Load()) // file holds only "stale"; reload keeps it
	require.Equal(t, []string{"stale"}, s.Entries())

	m := New(filepath.Join(t.TempDir(), "h.json"), false)
	require.NoError(t, m.Add("gone after load"))
	require.NoError(t, m.Load())
	require.Empty(t, m.Entries(), "Load replaces the in-memory list")
}

func TestAddSaveFailureKeepsMemoryEntry(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	// The parent "directory" is a regular file, so MkdirAll fails.
	s := New(filepath.Join(blocker, "sub", "history.json"), true)
	err := s.Add("survives")
	require.Error(t, err)
	require.Equal(t, []string{"survives"}, s.Entries(),
		"in-session recall works even when persisting fails")
}

func TestConcurrentAddsAreSafe(t *testing.T) {
	s, path := tempStore(t, true)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			require.NoError(t, s.Add(fmt.Sprintf("g%02d", i)))
		}(i)
	}
	wg.Wait()
	require.Len(t, s.Entries(), 16)

	s2 := New(path, true)
	require.NoError(t, s2.Load())
	require.Len(t, s2.Entries(), 16, "the last atomic write holds all entries")
}
