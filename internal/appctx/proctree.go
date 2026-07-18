package appctx

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

// ProcTree bounds. The scan caps how many processes it records and
// the foreground search how many it visits, so a pathological proc
// tree can never make a capture expensive.
const (
	procTreeMaxProcs   = 8192
	procTreeMaxVisited = 512
)

// ProcTree reads one point-in-time snapshot of a /proc-style process
// tree: children via one memoized scan of every <root>/<pid>/stat's
// ppid field, working directories via readlink <root>/<pid>/cwd, and
// the foreground hint via the stat tpgid field (the terminal's
// foreground process group). It structurally implements
// internal/frecency's ProcTree seam for the focused-app cwd
// derivation (this package deliberately does not import frecency).
//
// A ProcTree is a SNAPSHOT: the internal/app capture path builds a
// fresh one per summon, so the memoized scan is read at most once and
// never goes stale. Everything is best-effort -- unreadable stats or
// cwd links (cross-user /proc, racing exits) simply contribute
// nothing, the proc.go convention.
type ProcTree struct {
	root     string
	once     sync.Once
	children map[int][]int
	tpgid    map[int]int
}

// NewProcTree creates a snapshot reader over a /proc-style directory
// (production "/proc"; tests use fixture trees). The directory is not
// touched until the first Children or Foreground call.
func NewProcTree(root string) *ProcTree {
	return &ProcTree{root: root}
}

// scan reads every numeric <root>/<pid>/stat once, building the
// ppid -> children and pid -> tpgid maps. Child lists are sorted
// numerically so walk order is deterministic.
func (t *ProcTree) scan() {
	t.once.Do(func() {
		t.children = map[int][]int{}
		t.tpgid = map[int]int{}
		entries, err := os.ReadDir(t.root)
		if err != nil {
			return
		}
		seen := 0
		for _, e := range entries {
			pid, err := strconv.Atoi(e.Name())
			if err != nil || pid <= 0 {
				continue
			}
			if seen >= procTreeMaxProcs {
				break
			}
			data, err := os.ReadFile(filepath.Join(t.root, e.Name(), "stat"))
			if err != nil {
				continue // racing exit or unreadable; expected
			}
			ppid, tpgid, ok := parseStatFields(data)
			if !ok {
				continue
			}
			seen++
			t.children[ppid] = append(t.children[ppid], pid)
			t.tpgid[pid] = tpgid
		}
		for _, kids := range t.children {
			sort.Ints(kids)
		}
	})
}

// parseStatFields extracts the ppid (field 4) and tpgid (field 8)
// from a /proc/<pid>/stat line. The comm field (2) is parenthesized
// and may itself contain spaces and parentheses, so parsing starts
// after the LAST ')'.
func parseStatFields(data []byte) (ppid, tpgid int, ok bool) {
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, 0, false
	}
	fields := bytes.Fields(data[i+1:])
	// After comm: state ppid pgrp session tty_nr tpgid ...
	if len(fields) < 6 {
		return 0, 0, false
	}
	ppid, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return 0, 0, false
	}
	tpgid, err = strconv.Atoi(string(fields[5]))
	if err != nil {
		return 0, 0, false
	}
	return ppid, tpgid, true
}

// Children returns pid's direct children, numerically sorted; nil for
// a childless or unknown pid.
func (t *ProcTree) Children(pid int) []int {
	t.scan()
	return t.children[pid]
}

// Cwd returns pid's working directory via the cwd symlink. An error
// means unreadable (cross-user /proc readlink fails; expected).
func (t *ProcTree) Cwd(pid int) (string, error) {
	return os.Readlink(filepath.Join(t.root, strconv.Itoa(pid), "cwd"))
}

// Foreground walks pid's subtree breadth-first (pid included, sorted
// child order, bounded) and returns the first positive tpgid it finds
// -- the foreground process group of the tree's controlling terminal,
// which for a terminal emulator is the shell or whatever the shell
// currently runs. ok=false when no process in the tree has one (a
// plain GUI app: tpgid is -1 without a controlling terminal).
func (t *ProcTree) Foreground(pid int) (int, bool) {
	t.scan()
	visited := map[int]bool{}
	queue := []int{pid}
	for len(queue) > 0 && len(visited) < procTreeMaxVisited {
		cur := queue[0]
		queue = queue[1:]
		if cur <= 0 || visited[cur] {
			continue
		}
		visited[cur] = true
		if tp, known := t.tpgid[cur]; known && tp > 0 {
			return tp, true
		}
		queue = append(queue, t.children[cur]...)
	}
	return 0, false
}
