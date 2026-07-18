package preview

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newTestAICache(t *testing.T) (*AICache, *fakeClock, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aicache.json")
	c := NewAICache(path)
	clk := newFakeClock()
	c.Now = clk.now
	return c, clk, path
}

func TestAICacheRoundtrip(t *testing.T) {
	c, _, path := newTestAICache(t)
	_, ok := c.Get("m", "what is go?")
	require.False(t, ok, "a missing file starts empty")
	require.NoError(t, c.Put("m", "what is go?", "a language"))

	answer, ok := c.Get("m", "what is go?")
	require.True(t, ok)
	require.Equal(t, "a language", answer)

	_, ok = c.Get("other-model", "what is go?")
	require.False(t, ok, "the model is part of the key")

	// A fresh cache over the same file serves the persisted answer.
	c2 := NewAICache(path)
	require.NoError(t, c2.Load())
	answer, ok = c2.Get("m", "what is go?")
	require.True(t, ok)
	require.Equal(t, "a language", answer)
}

func TestAICacheFileShapeAndPerms(t *testing.T) {
	c, _, path := newTestAICache(t)
	require.NoError(t, c.Put("m", "p", "a"))

	fi, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var file aiCacheFile
	require.NoError(t, json.Unmarshal(data, &file), "every write leaves a valid file")
	require.Equal(t, 1, file.V)
	require.Len(t, file.Entries, 1)
	require.Equal(t, aiKey("m", "p"), file.Entries[0].K)
	require.Equal(t, "m", file.Entries[0].Model)
	require.Equal(t, "p", file.Entries[0].Prompt)
	require.Equal(t, "a", file.Entries[0].Answer)
	require.False(t, file.Entries[0].At.IsZero())
}

func TestAICacheLRUEvictionByRecency(t *testing.T) {
	c, clk, _ := newTestAICache(t)
	for i := 0; i < aiCacheMaxEntries; i++ {
		clk.advance(time.Second)
		require.NoError(t, c.Put("m", fmt.Sprintf("prompt-%d", i), "answer"))
	}
	require.Equal(t, aiCacheMaxEntries, c.Len())

	// Touch the oldest entry so it is no longer the eviction victim.
	clk.advance(time.Second)
	_, ok := c.Get("m", "prompt-0")
	require.True(t, ok)

	// The next Put evicts the now-oldest entry: prompt-1.
	clk.advance(time.Second)
	require.NoError(t, c.Put("m", "one-more", "answer"))
	require.Equal(t, aiCacheMaxEntries, c.Len())
	_, ok = c.Get("m", "prompt-0")
	require.True(t, ok, "the refreshed entry survived")
	_, ok = c.Get("m", "prompt-1")
	require.False(t, ok, "the least-recently-used entry was evicted")
	_, ok = c.Get("m", "one-more")
	require.True(t, ok)
}

func TestAICacheRecencySurvivesReload(t *testing.T) {
	c, clk, path := newTestAICache(t)
	require.NoError(t, c.Put("m", "old", "a1"))
	clk.advance(time.Minute)
	require.NoError(t, c.Put("m", "new", "a2"))
	clk.advance(time.Minute)
	_, ok := c.Get("m", "old") // refresh "old" past "new"
	require.True(t, ok)

	c2 := NewAICache(path)
	clk2 := &fakeClock{t: clk.t.Add(time.Hour)}
	c2.Now = clk2.now
	require.NoError(t, c2.Load())
	// Fill to the cap; the persisted recency makes "new" (not "old")
	// the first eviction victim.
	for i := 0; c2.Len() < aiCacheMaxEntries; i++ {
		clk2.advance(time.Second)
		require.NoError(t, c2.Put("m", fmt.Sprintf("fill-%d", i), "x"))
	}
	clk2.advance(time.Second)
	require.NoError(t, c2.Put("m", "overflow", "x"))
	_, ok = c2.Get("m", "old")
	require.True(t, ok)
	_, ok = c2.Get("m", "new")
	require.False(t, ok, "persisted LRU order decided the eviction")
}

func TestAICacheCorruptFileStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aicache.json")
	require.NoError(t, os.WriteFile(path, []byte("{broken"), 0o600))
	c := NewAICache(path)
	var logged []string
	c.Logf = func(format string, v ...any) { logged = append(logged, fmt.Sprintf(format, v...)) }

	err := c.Load()
	require.Error(t, err)
	require.Len(t, logged, 1, "the corrupt-file note logs once")
	require.Contains(t, logged[0], "starting empty")

	// The cache stays fully usable; lazy load never re-runs.
	_, ok := c.Get("m", "p")
	require.False(t, ok)
	require.NoError(t, c.Put("m", "p", "a"))
	_, ok = c.Get("m", "p")
	require.True(t, ok)
	require.Len(t, logged, 1)

	// The rewrite left a valid file.
	c2 := NewAICache(path)
	require.NoError(t, c2.Load())
	answer, ok := c2.Get("m", "p")
	require.True(t, ok)
	require.Equal(t, "a", answer)
}

func TestAICacheUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aicache.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"v":99,"entries":[]}`), 0o600))
	c := NewAICache(path)
	require.ErrorContains(t, c.Load(), "unsupported version 99")
}

func TestAICacheLoadNormalizesHandEditedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aicache.json")
	older := time.Unix(1_000, 0).UTC()
	newer := time.Unix(2_000, 0).UTC()
	entries := []aiCacheEntry{
		{K: "", Model: "m", Prompt: "keyless", Answer: "dropped", At: newer},
		{K: "dup", Model: "m", Prompt: "old dup", Answer: "old", At: older},
		{K: "dup", Model: "m", Prompt: "new dup", Answer: "new", At: newer},
		{K: "big", Model: "m", Prompt: strings.Repeat("p", aiCachePromptCap+100), Answer: strings.Repeat("a", aiCacheAnswerCap+100), At: newer},
	}
	data, err := json.Marshal(aiCacheFile{V: 1, Entries: entries})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	c := NewAICache(path)
	require.NoError(t, c.Load())
	require.Equal(t, 2, c.Len(), "keyless dropped, duplicate collapsed")
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		require.LessOrEqual(t, len(e.Prompt), aiCachePromptCap)
		require.LessOrEqual(t, len(e.Answer), aiCacheAnswerCap)
		if e.K == "dup" {
			require.Equal(t, "new", e.Answer, "the newest duplicate wins")
		}
	}
}

func TestAICachePutAppliesCapsButKeysFullPrompt(t *testing.T) {
	c, _, path := newTestAICache(t)
	longPrompt := strings.Repeat("q", aiCachePromptCap+500)
	longAnswer := strings.Repeat("a", aiCacheAnswerCap+500)
	require.NoError(t, c.Put("m", longPrompt, longAnswer))

	answer, ok := c.Get("m", longPrompt)
	require.True(t, ok, "the key hashes the FULL prompt")
	require.Len(t, answer, aiCacheAnswerCap, "the stored answer is capped")
	_, ok = c.Get("m", longPrompt[:aiCachePromptCap])
	require.False(t, ok, "the capped stored prompt is not the key")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var file aiCacheFile
	require.NoError(t, json.Unmarshal(data, &file))
	require.Len(t, file.Entries, 1)
	require.Len(t, file.Entries[0].Prompt, aiCachePromptCap)
	require.Len(t, file.Entries[0].Answer, aiCacheAnswerCap)
}

func TestAICacheMemoryOnly(t *testing.T) {
	c := NewAICache("")
	require.NoError(t, c.Load())
	require.NoError(t, c.Put("m", "p", "a"))
	answer, ok := c.Get("m", "p")
	require.True(t, ok)
	require.Equal(t, "a", answer)
}

func TestAICacheMemorySurvivesFailedWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on unix directory semantics")
	}
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	require.NoError(t, os.WriteFile(blocker, []byte("file"), 0o644))
	c := NewAICache(filepath.Join(blocker, "aicache.json")) // MkdirAll fails: parent is a file

	require.Error(t, c.Put("m", "p", "a"))
	answer, ok := c.Get("m", "p")
	require.True(t, ok, "the in-memory entry stands despite the failed write")
	require.Equal(t, "a", answer)
}

func TestAIKeyDistinguishesModelAndPrompt(t *testing.T) {
	require.NotEqual(t, aiKey("m1", "p"), aiKey("m2", "p"))
	require.NotEqual(t, aiKey("m", "p1"), aiKey("m", "p2"))
	// The NUL separator keeps (ab, c) and (a, bc) apart.
	require.NotEqual(t, aiKey("ab", "c"), aiKey("a", "bc"))
	require.Equal(t, aiKey("m", "p"), aiKey("m", "p"))
}
