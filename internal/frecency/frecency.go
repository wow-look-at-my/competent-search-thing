// Package frecency holds the PURE half of result prioritization: a
// decaying open-count store (files the user actually opened through
// the bar rank up), a noise-path penalty classifier (deep cache/temp
// trees rank down), a budget-bounded recency probe (max of
// atime/mtime as the cold-start signal when no open history exists
// yet), and a focused-app working-directory heuristic (results near
// where the focused terminal/editor sits rank up). Nothing in this
// package is wired into the engine or the app; the intended blend,
// implemented later IN THE ENGINE, is:
//
//	score = match quality
//	      + frecency boost            (Store.Boost)
//	      + recency bonus             (Probe, cold-start only: applied
//	                                   when the frecency boost is zero)
//	      + cwd proximity             (CwdBoost over DeriveCwd's dir)
//	      - noise penalty             (PathPenalty)
//
// with a strong frecency boost allowed to jump ranking tiers (an
// often-opened substring match may outrank an untouched prefix
// match). The Signals struct bundles the three sources for that
// future blend; its zero value degrades to no-ops, the codebase's
// usual pattern.
//
// Recency caveat: most modern Linux mounts use relatime, which
// updates atime at most once a day (and only when older than mtime),
// so atime is a COARSE signal. mtime still catches the important
// cold-start cases -- a file just downloaded or just written. The
// probe therefore takes max(atime, mtime) on Linux and plain mtime on
// darwin/windows.
package frecency

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Defaults for Options zero values.
const (
	// DefaultHalfLife is how long an open takes to count half as
	// much: two weeks, so last month's habit still matters but last
	// quarter's does not.
	DefaultHalfLife = 14 * 24 * time.Hour
	// DefaultCap bounds the store; past it the lowest decayed counts
	// are pruned.
	DefaultCap = 4096
)

// Options configures a Store. Zero values mean: HalfLife
// DefaultHalfLife, Cap DefaultCap, Now time.Now, Persist false
// (memory only -- the disk is neither read nor written).
type Options struct {
	HalfLife time.Duration
	Cap      int
	Now      func() time.Time
	Persist  bool
}

// Entry is one path's stored state, as Entries reports it: the RAW
// stored values, NOT decayed to now (Boost is the decayed view).
type Entry struct {
	Count     float64
	LastTouch time.Time
}

// Store keeps the decaying open counts. All methods are safe for
// concurrent use and nil-receiver-safe (a nil *Store no-ops: Boost 0,
// RecordOpen/Load nil error, Entries empty) -- queries take the read
// lock, mutations the write lock, the index.Manager convention.
type Store struct {
	mu      sync.RWMutex
	path    string
	opts    Options
	entries map[string]Entry
}

// New creates a store backed by the versioned JSON file at path.
// Options zero values are replaced by the documented defaults;
// Persist false keeps everything in memory only.
func New(path string, opts Options) *Store {
	if opts.HalfLife <= 0 {
		opts.HalfLife = DefaultHalfLife
	}
	if opts.Cap <= 0 {
		opts.Cap = DefaultCap
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Store{path: path, opts: opts, entries: map[string]Entry{}}
}

// decayed is count reduced by half per HalfLife elapsed between last
// and now. A non-positive elapse (clock skew, a hand-edited future
// timestamp) decays nothing.
func (s *Store) decayed(count float64, last, now time.Time) float64 {
	dt := now.Sub(last)
	if dt <= 0 {
		return count
	}
	return count * math.Exp2(-float64(dt)/float64(s.opts.HalfLife))
}

// RecordOpen counts one open of path, now: the stored count first
// decays to now, then gains 1, and LastTouch resets. An empty path is
// skipped silently (paths are never trimmed -- whitespace is legal in
// file names). A persisting store then rewrites the file; the
// in-memory update survives a failed write, history-style, so
// in-session ranking works even with disk problems.
func (s *Store) RecordOpen(path string) error {
	if s == nil || path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opts.Now()
	e := s.entries[path]
	count := 1.0
	if !e.LastTouch.IsZero() {
		count = s.decayed(e.Count, e.LastTouch, now) + 1
	}
	s.entries[path] = Entry{Count: count, LastTouch: now}
	s.pruneLocked(now)
	if !s.opts.Persist {
		return nil
	}
	return s.saveLocked()
}

// Boost returns path's current decayed count: 0 for paths never
// opened. Read-only -- it never rewrites the entry or the file, so
// querying causes no write amplification.
func (s *Store) Boost(path string) float64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[path]
	if !ok {
		return 0
	}
	return s.decayed(e.Count, e.LastTouch, s.opts.Now())
}

// Entries returns a defensive copy of the raw stored state (counts
// NOT decayed to now), for tests and diagnostics. Never nil.
func (s *Store) Entries() map[string]Entry {
	if s == nil {
		return map[string]Entry{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Entry, len(s.entries))
	for p, e := range s.entries {
		out[p] = e
	}
	return out
}

// pruneLocked enforces the cap: when the store exceeds it, only the
// Cap highest decayed counts survive (ties: newer LastTouch, then
// lexicographic path, so pruning is deterministic).
func (s *Store) pruneLocked(now time.Time) {
	if len(s.entries) <= s.opts.Cap {
		return
	}
	type ranked struct {
		path  string
		score float64
		touch time.Time
	}
	all := make([]ranked, 0, len(s.entries))
	for p, e := range s.entries {
		all = append(all, ranked{p, s.decayed(e.Count, e.LastTouch, now), e.LastTouch})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		if !all[i].touch.Equal(all[j].touch) {
			return all[i].touch.After(all[j].touch)
		}
		return all[i].path < all[j].path
	})
	for _, r := range all[s.opts.Cap:] {
		delete(s.entries, r.path)
	}
}

// fileFormat is the persisted shape: {"v":1,"entries":{path:{c,t}}}.
type fileFormat struct {
	V       int                  `json:"v"`
	Entries map[string]fileEntry `json:"entries"`
}

type fileEntry struct {
	C float64   `json:"c"`
	T time.Time `json:"t"`
}

// Load replaces the in-memory state with the persisted one. A missing
// file (or a memory-only store) is an empty store and no error; an
// unreadable, non-JSON, or wrong-version file starts empty and
// returns the reason once, for logging -- the store stays fully
// usable either way. Loaded entries keep their stored Count/LastTouch
// verbatim (decay happens on read, in Boost), but a hand-edited file
// may hold anything, so entries with an empty path, a non-positive
// count, or a zero time are dropped, and the cap is enforced.
func (s *Store) Load() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = map[string]Entry{}
	if !s.opts.Persist {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", s.path, err)
	}
	var raw fileFormat
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%s is not a frecency JSON file: %v", s.path, err)
	}
	if raw.V != 1 {
		return fmt.Errorf("%s has unsupported frecency version %d (want 1)", s.path, raw.V)
	}
	for p, e := range raw.Entries {
		if p == "" || e.C <= 0 || math.IsNaN(e.C) || math.IsInf(e.C, 0) || e.T.IsZero() {
			continue
		}
		s.entries[p] = Entry{Count: e.C, LastTouch: e.T}
	}
	s.pruneLocked(s.opts.Now())
	return nil
}

// saveLocked writes the store as versioned JSON, atomically: a temp
// file in the target directory (chmodded 0600 -- what the user opens
// is their business), then renamed over the destination, so a crash
// mid-write never leaves a truncated file. The parent directory is
// created first, like config.Save and history do.
func (s *Store) saveLocked() error {
	out := fileFormat{V: 1, Entries: make(map[string]fileEntry, len(s.entries))}
	for p, e := range s.entries {
		out.Entries[p] = fileEntry{C: e.Count, T: e.LastTouch}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding frecency: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".frecency-*.tmp")
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
		err = os.Rename(name, s.path)
	}
	if err != nil {
		os.Remove(name)
		return fmt.Errorf("writing %s: %w", s.path, err)
	}
	return nil
}
