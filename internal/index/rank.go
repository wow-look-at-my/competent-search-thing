package index

import (
	"cmp"
	"path/filepath"
	"strings"
)

// cand is a ranking candidate: one matched entry plus the precomputed
// cheap ranking keys. The expensive lexicographic path tiebreak is
// evaluated lazily in candCompare only on full key ties. score is the
// fuzzy alignment score (fuzzy.go) and stays 0 for every non-fuzzy
// class, so the score comparison below is a no-op outside the fuzzy
// tier and every non-fuzzy ordering is byte-identical to before.
type cand struct {
	id      int32
	pathLen int32
	score   int32
	class   uint8
	isDir   bool
}

// candCompare orders candidates best-first: match class, then (within
// the fuzzy class only) higher alignment score, then directories before
// files, then shorter full path, then the numeric-aware lexicographic
// full-path order (numorder.go): aligned digit runs compare
// numerically DESCENDING so datestamped/versioned families deliver
// newest first, and every other difference keeps the plain byte
// order. Paths are unique per store, so this is a total order.
//
// classFuzzy shares ordinal 3 with path-mode classPathSub, but a query
// is exactly one mode and path-mode candidates always carry score 0, so
// the score comparison never fires for them.
func (s *Store) candCompare(a, b cand) int {
	if a.class != b.class {
		return cmp.Compare(a.class, b.class)
	}
	if a.class == classFuzzy && a.score != b.score {
		return cmp.Compare(b.score, a.score) // higher score ranks first
	}
	if a.isDir != b.isDir {
		if a.isDir {
			return -1
		}
		return 1
	}
	if a.pathLen != b.pathLen {
		return cmp.Compare(a.pathLen, b.pathLen)
	}
	return compareJoinedNumeric(
		s.dirs[s.parent[a.id]], s.nameBytes(a.id),
		s.dirs[s.parent[b.id]], s.nameBytes(b.id),
	)
}

// topK is a bounded worst-at-top (max) heap of the best limit
// candidates seen so far. Pushing a candidate worse than the current
// worst of a full heap costs one comparison.
type topK struct {
	s     *Store
	limit int
	items []cand
}

func newTopK(s *Store, limit int) *topK {
	return &topK{s: s, limit: limit}
}

// add offers a candidate to the heap.
func (t *topK) add(c cand) {
	if len(t.items) < t.limit {
		t.items = append(t.items, c)
		t.up(len(t.items) - 1)
		return
	}
	if t.s.candCompare(c, t.items[0]) < 0 {
		t.items[0] = c
		t.down(0)
	}
}

func (t *topK) up(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if t.s.candCompare(t.items[i], t.items[p]) <= 0 {
			return
		}
		t.items[i], t.items[p] = t.items[p], t.items[i]
		i = p
	}
}

func (t *topK) down(i int) {
	n := len(t.items)
	for {
		worst := i
		if l := 2*i + 1; l < n && t.s.candCompare(t.items[l], t.items[worst]) > 0 {
			worst = l
		}
		if r := 2*i + 2; r < n && t.s.candCompare(t.items[r], t.items[worst]) > 0 {
			worst = r
		}
		if worst == i {
			return
		}
		t.items[i], t.items[worst] = t.items[worst], t.items[i]
		i = worst
	}
}

// joinedLen returns the byte length of joinDir(dir, name) for a name of
// nameLen bytes, without building the string.
func joinedLen(dir string, nameLen int) int {
	if strings.HasSuffix(dir, string(filepath.Separator)) {
		return len(dir) + nameLen
	}
	return len(dir) + 1 + nameLen
}

// compareJoined lexicographically compares the virtual full paths
// joinDir(da, na) and joinDir(db, nb) without allocating them.
func compareJoined(da string, na []byte, db string, nb []byte) int {
	la := joinedLen(da, len(na))
	lb := joinedLen(db, len(nb))
	n := min(la, lb)
	for i := 0; i < n; i++ {
		ca := joinedAt(da, na, i)
		cb := joinedAt(db, nb, i)
		if ca != cb {
			return cmp.Compare(ca, cb)
		}
	}
	return cmp.Compare(la, lb)
}

// joinedAt returns byte i of the virtual string joinDir(dir, name).
// i must be within joinedLen(dir, len(name)).
func joinedAt(dir string, name []byte, i int) byte {
	if i < len(dir) {
		return dir[i]
	}
	i -= len(dir)
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		if i == 0 {
			return byte(filepath.Separator)
		}
		i--
	}
	return name[i]
}
