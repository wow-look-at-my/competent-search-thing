package index

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Path-aware query support (Everything-style semantics).
//
// A query that contains a path separator is matched case-insensitively
// against the full path (parent dir + separator + name); queries without
// a separator keep the existing name-only fast path untouched. The
// implementation exploits the interned parent-dir table: because entry
// names can never contain a separator (validateName) while a path-mode
// query always does, every occurrence of the query in a full path
// V + name (V = lowered parent dir + trailing separator) either lies
// entirely within V or straddles the dir/name join as q = S + R with S
// a separator-terminated suffix of V and R a prefix of the name. A
// per-query plan therefore precomputes, per interned dir, whether q
// occurs in V (every child matches) plus the boundary remainders R to
// test against the name blob; the entry scan then costs one dir-table
// lookup plus at most a few prefix compares per entry.

// Path-mode match classes, in ranking order. They share candCompare's
// ordinal space with the name-mode classes; a query is exactly one mode,
// so the two sets never mix within a result list.
const (
	classPathExact  uint8 = iota // query equals the full path
	classPathSuffix              // full path ends with the query
	classPathPrefix              // full path starts with the query
	classPathSub                 // query occurs elsewhere in the full path
)

const (
	// sepByte is the path separator that triggers and drives path mode.
	sepByte = byte(filepath.Separator)
	// sepStr is sepByte as a string constant.
	sepStr = string(filepath.Separator)
	// maxPathQueryLen caps path-mode patterns so remainder offsets fit
	// in uint16; longer queries return no results.
	maxPathQueryLen = 1024
	// minShardDirs keeps the per-dir plan build single-threaded for
	// small dir tables, where fan-out costs more than it saves.
	minShardDirs = 512
)

// hasPathSep reports whether the lowered pattern switches the query
// into path mode: it contains the native separator, or '/' on OSes
// where the native separator differs (users type both; '/' is then
// normalized to the native separator before matching).
func hasPathSep(pat []byte) bool {
	if bytes.IndexByte(pat, sepByte) >= 0 {
		return true
	}
	return sepByte != '/' && bytes.IndexByte(pat, '/') >= 0
}

// pathRem is one boundary split of the query q = S + R at a separator:
// R = q[k:] must be a prefix of the entry name for the split to match.
// fullDir records that S covered ALL of V (the occurrence starts at
// byte 0 of the full path), so a whole-name R is an exact path match.
type pathRem struct {
	k       uint16
	fullDir bool
}

// dirPathInfo is the per-dir precomputed match state for one query.
type dirPathInfo struct {
	full    bool   // q occurs within V: every live child matches
	qPrefix bool   // V starts with q: children are full-path prefix matches
	remLo   uint32 // rems[remLo:remHi] are this dir's boundary splits
	remHi   uint32
}

// pathPlan is the pooled per-query precomputation: one dirPathInfo per
// interned dir plus a shared arena of boundary remainders.
type pathPlan struct {
	q     string // normalized lowered query
	seps  []int  // separator positions i in q with i < len(q)-1
	infos []dirPathInfo
	rems  []pathRem
}

var pathPlanPool = sync.Pool{New: func() any { return new(pathPlan) }}

// queryPath is the path-mode counterpart of Query, reached when the
// pattern contains a separator. It shares the shard/heap/selectTop
// machinery and the candCompare tie-breaks with the name scan.
func (s *Store) queryPath(pat []byte, limit int) []Result {
	if len(pat) > maxPathQueryLen {
		return nil
	}
	if sepByte != '/' {
		for i, b := range pat {
			if b == '/' {
				pat[i] = sepByte
			}
		}
	}
	plan := s.buildPathPlan(string(pat))
	defer pathPlanPool.Put(plan)
	if planDead(plan) {
		return nil
	}

	n := s.Len()
	workers := runtime.NumCPU()
	if max := (n + minShardEntries - 1) / minShardEntries; workers > max {
		workers = max
	}
	heaps := make([]*topK, workers)
	if workers == 1 {
		heaps[0] = newTopK(s, limit)
		s.scanRangePath(plan, 0, n, heaps[0])
	} else {
		per := (n + workers - 1) / workers
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			lo := w * per
			hi := min(lo+per, n)
			if lo >= hi {
				heaps[w] = newTopK(s, limit)
				continue
			}
			wg.Add(1)
			go func(w, lo, hi int) {
				defer wg.Done()
				h := newTopK(s, limit)
				s.scanRangePath(plan, lo, hi, h)
				heaps[w] = h
			}(w, lo, hi)
		}
		wg.Wait()
	}
	return s.selectTop(heaps, limit)
}

// buildPathPlan precomputes the per-dir match state for the lowered,
// normalized query qs. The dir loop is sharded across NumCPU disjoint
// ranges of s.dirs with per-shard remainder arenas, stitched serially
// into the plan arena afterwards.
func (s *Store) buildPathPlan(qs string) *pathPlan {
	plan := pathPlanPool.Get().(*pathPlan)
	plan.q = qs
	plan.rems = plan.rems[:0]
	plan.seps = plan.seps[:0]
	for i := 0; i < len(qs)-1; i++ {
		if qs[i] == sepByte {
			plan.seps = append(plan.seps, i)
		}
	}
	nd := len(s.dirs)
	if cap(plan.infos) >= nd {
		plan.infos = plan.infos[:nd]
	} else {
		plan.infos = make([]dirPathInfo, nd)
	}

	workers := runtime.NumCPU()
	if max := (nd + minShardDirs - 1) / minShardDirs; workers > max {
		workers = max
	}
	if workers <= 1 {
		for d := 0; d < nd; d++ {
			plan.infos[d], plan.rems = matchDir(qs, plan.seps, s.dirsLower[d], plan.rems)
		}
		return plan
	}
	arenas := make([][]pathRem, workers)
	per := (nd + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo := w * per
		hi := min(lo+per, nd)
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			var rems []pathRem
			for d := lo; d < hi; d++ {
				plan.infos[d], rems = matchDir(qs, plan.seps, s.dirsLower[d], rems)
			}
			arenas[w] = rems
		}(w, lo, hi)
	}
	wg.Wait()
	// Stitch: concatenate the per-shard arenas and rebase each shard's
	// remLo/remHi from arena-local to plan-global offsets.
	for w := 0; w < workers; w++ {
		base := uint32(len(plan.rems))
		plan.rems = append(plan.rems, arenas[w]...)
		if base == 0 {
			continue
		}
		for d := w * per; d < min((w+1)*per, nd); d++ {
			plan.infos[d].remLo += base
			plan.infos[d].remHi += base
		}
	}
	return plan
}

// planDead reports a plan no entry can match: no dir contains the
// query and no dir has a boundary split. Checking the dir table (one
// bool per dir) is far cheaper than scanning every entry against it.
func planDead(plan *pathPlan) bool {
	if len(plan.rems) > 0 {
		return false
	}
	for i := range plan.infos {
		if plan.infos[i].full {
			return false
		}
	}
	return true
}

// matchDir computes one dir's dirPathInfo for query qs (seps = its
// separator split positions), appending boundary remainders to rems.
// dl is the lowered dir path; its virtual form V is dl plus a trailing
// separator unless dl is a filesystem root already ending in one.
func matchDir(qs string, seps []int, dl string, rems []pathRem) (dirPathInfo, []pathRem) {
	rootForm := strings.HasSuffix(dl, sepStr)
	lenV := len(dl)
	if !rootForm {
		lenV++
	}
	last := len(qs) - 1
	var info dirPathInfo
	// q occurs within V: entirely inside dl, or ending exactly at V's
	// final separator. rootForm is excluded from the second clause so a
	// doubled-separator query never false-matches the root dir.
	info.full = strings.Contains(dl, qs) ||
		(!rootForm && qs[last] == sepByte && strings.HasSuffix(dl, qs[:last]))
	// V starts with q: within dl's start, or q == dl + separator.
	info.qPrefix = strings.HasPrefix(dl, qs) ||
		(!rootForm && len(qs) == len(dl)+1 && qs[last] == sepByte && qs[:len(dl)] == dl)
	info.remLo = uint32(len(rems))
	for _, i := range seps {
		k := i + 1
		// S = q[:k] ends with a separator, as does V; S is a suffix of V
		// iff q[:i] is a suffix of dl (in root form dl's own trailing
		// separator is the shared final one, so S is tested verbatim).
		var ok bool
		if rootForm {
			ok = strings.HasSuffix(dl, qs[:k])
		} else {
			ok = strings.HasSuffix(dl, qs[:i])
		}
		if ok {
			rems = append(rems, pathRem{k: uint16(k), fullDir: k == lenV})
		}
	}
	info.remHi = uint32(len(rems))
	return info, rems
}

// scanRangePath classifies entries [lo, hi) against the plan and feeds
// live matches into h. Per entry it is one dir-table lookup plus, for
// dirs with boundary splits, an alloc-free prefix compare per split.
func (s *Store) scanRangePath(plan *pathPlan, lo, hi int, h *topK) {
	const noMatch = ^uint8(0)
	for e := lo; e < hi; e++ {
		if s.flags[e]&flagTombstone != 0 {
			continue
		}
		info := &plan.infos[s.parent[e]]
		if !info.full && info.remLo == info.remHi {
			continue
		}
		class := noMatch
		if info.full {
			class = classPathSub
			if info.qPrefix {
				class = classPathPrefix
			}
		}
		nb := s.lowerNameBytes(int32(e))
		for _, r := range plan.rems[info.remLo:info.remHi] {
			rem := plan.q[r.k:]
			if len(nb) < len(rem) || string(nb[:len(rem)]) != rem {
				continue
			}
			c := classPathSub
			whole := len(rem) == len(nb)
			switch {
			case r.fullDir && whole:
				c = classPathExact
			case whole:
				c = classPathSuffix
			case r.fullDir:
				c = classPathPrefix
			}
			if c < class {
				class = c
				if class == classPathExact {
					break
				}
			}
		}
		if class == noMatch {
			continue
		}
		nameLen := int(s.origOff[e+1] - 1 - s.origOff[e])
		pathLen := joinedLen(s.dirs[s.parent[e]], nameLen)
		h.add(cand{id: int32(e), pathLen: int32(pathLen), class: class, isDir: s.flags[e]&flagDir != 0})
	}
}
