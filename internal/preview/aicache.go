package preview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// AI answer cache bounds. Entries past the count cap evict by oldest
// recency stamp; the string caps bound what one entry can put on disk.
const (
	aiCacheVersion    = 1
	aiCacheMaxEntries = 128
	aiCachePromptCap  = 2 << 10  // stored prompt bytes (the KEY hashes the full prompt)
	aiCacheAnswerCap  = 32 << 10 // stored answer bytes
)

// aiCacheEntry is one persisted answer. K is the lookup key --
// sha256 hex over model + NUL + the FULL prompt -- so the capped
// stored prompt can never alias two long prompts. At doubles as the
// LRU recency stamp (Get refreshes it).
type aiCacheEntry struct {
	K      string    `json:"k"`
	Model  string    `json:"model"`
	Prompt string    `json:"prompt"`
	Answer string    `json:"answer"`
	At     time.Time `json:"at"`
}

// aiCacheFile is the on-disk shape: {"v":1,"entries":[...]}.
type aiCacheFile struct {
	V       int            `json:"v"`
	Entries []aiCacheEntry `json:"entries"`
}

// AICache is a persistent LRU of AI answers, following
// internal/history's atomic-persist pattern: lazy one-shot load,
// temp-file-then-rename 0600 writes, and an in-memory list that
// updates even when the disk write fails. An empty path keeps the
// cache memory-only (nothing is ever read or written). Thread-safe.
type AICache struct {
	// Now is the recency clock (default time.Now).
	Now func() time.Time
	// Logf receives the one-shot corrupt-file note on the lazy load
	// (nil = silent).
	Logf func(format string, v ...any)

	mu      sync.Mutex
	path    string
	loaded  bool
	loadErr error
	entries []aiCacheEntry // unordered; At carries recency
}

// NewAICache builds a cache persisting at path ("" = memory-only).
func NewAICache(path string) *AICache {
	return &AICache{path: path}
}

func (c *AICache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// aiKey is the lookup key for one (model, prompt) pair.
func aiKey(model, prompt string) string {
	h := sha256.New()
	h.Write([]byte(model))
	h.Write([]byte{0})
	h.Write([]byte(prompt))
	return hex.EncodeToString(h.Sum(nil))
}

// Load forces the lazy load and reports its outcome: a missing file
// (or a memory-only cache) is empty and nil; a corrupt file starts
// empty and returns the reason for one-shot logging. Idempotent --
// the file is read at most once per cache.
func (c *AICache) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureLoadedLocked()
	return c.loadErr
}

// ensureLoadedLocked performs the one-shot lazy load. Callers hold mu.
func (c *AICache) ensureLoadedLocked() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.loadErr = c.loadLocked()
	if c.loadErr != nil && c.Logf != nil {
		c.Logf("preview: AI cache: %v (starting empty)", c.loadErr)
	}
}

// loadLocked reads and normalizes the persisted entries.
func (c *AICache) loadLocked() error {
	if c.path == "" {
		return nil
	}
	data, err := os.ReadFile(c.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", c.path, err)
	}
	var file aiCacheFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("%s is not a valid AI cache file: %v", c.path, err)
	}
	if file.V != aiCacheVersion {
		return fmt.Errorf("%s has unsupported version %d", c.path, file.V)
	}
	// A hand-edited file may hold anything: drop keyless rows, keep
	// the newest occurrence per key, re-apply the string caps, and
	// enforce the entry cap by recency.
	byKey := map[string]aiCacheEntry{}
	for _, e := range file.Entries {
		if e.K == "" {
			continue
		}
		e.Prompt = capString(e.Prompt, aiCachePromptCap)
		e.Answer = capString(e.Answer, aiCacheAnswerCap)
		if prev, ok := byKey[e.K]; !ok || e.At.After(prev.At) {
			byKey[e.K] = e
		}
	}
	entries := make([]aiCacheEntry, 0, len(byKey))
	for _, e := range byKey {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].At.Before(entries[j].At) })
	if len(entries) > aiCacheMaxEntries {
		entries = entries[len(entries)-aiCacheMaxEntries:]
	}
	c.entries = entries
	return nil
}

// Get returns the cached answer for (model, prompt) and refreshes its
// recency. The recency bump is persisted best-effort so LRU order
// survives restarts.
func (c *AICache) Get(model, prompt string) (string, bool) {
	key := aiKey(model, prompt)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureLoadedLocked()
	for i := range c.entries {
		if c.entries[i].K == key {
			c.entries[i].At = c.now()
			c.saveLocked() // best-effort; the hit stands regardless
			return c.entries[i].Answer, true
		}
	}
	return "", false
}

// Put stores one answer under (model, prompt), evicting the
// least-recently-used entries past the cap, and persists the list.
// The in-memory state updates even when the disk write fails, so the
// session keeps its cache through disk problems.
func (c *AICache) Put(model, prompt, answer string) error {
	entry := aiCacheEntry{
		K:      aiKey(model, prompt),
		Model:  model,
		Prompt: capString(prompt, aiCachePromptCap),
		Answer: capString(answer, aiCacheAnswerCap),
		At:     c.now(),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureLoadedLocked()
	replaced := false
	for i := range c.entries {
		if c.entries[i].K == entry.K {
			c.entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		c.entries = append(c.entries, entry)
	}
	for len(c.entries) > aiCacheMaxEntries {
		oldest := 0
		for i := range c.entries {
			if c.entries[i].At.Before(c.entries[oldest].At) {
				oldest = i
			}
		}
		c.entries = append(c.entries[:oldest], c.entries[oldest+1:]...)
	}
	return c.saveLocked()
}

// Len reports the entry count (test hook).
func (c *AICache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureLoadedLocked()
	return len(c.entries)
}

// saveLocked writes the entries atomically: a temp file in the target
// directory, chmodded 0600 (answers may quote anything the user asked
// about), then renamed over the destination -- a crash mid-write never
// leaves a truncated file. Memory-only caches skip the disk entirely.
// Callers hold mu.
func (c *AICache) saveLocked() error {
	if c.path == "" {
		return nil
	}
	ordered := make([]aiCacheEntry, len(c.entries))
	copy(ordered, c.entries)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })
	data, err := json.MarshalIndent(aiCacheFile{V: aiCacheVersion, Entries: ordered}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding AI cache: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".aicache-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, c.path)
	}
	if err != nil {
		os.Remove(name)
		return fmt.Errorf("writing %s: %w", c.path, err)
	}
	return nil
}
