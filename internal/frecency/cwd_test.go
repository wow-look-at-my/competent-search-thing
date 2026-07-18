package frecency

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeTree scripts a process tree: children edges, per-pid cwds
// (missing = unreadable), and an optional foreground hint.
type fakeTree struct {
	children map[int][]int
	cwds     map[int]string
	fg       map[int]int
}

func (f *fakeTree) Children(pid int) []int { return f.children[pid] }

func (f *fakeTree) Cwd(pid int) (string, error) {
	cwd, ok := f.cwds[pid]
	if !ok {
		return "", errors.New("readlink: permission denied")
	}
	return cwd, nil
}

func (f *fakeTree) Foreground(pid int) (int, bool) {
	fg, ok := f.fg[pid]
	return fg, ok
}

func TestDeriveCwd(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	home := "/home/tester"
	proj := "/home/tester/proj"

	cases := []struct {
		name   string
		tree   *fakeTree
		root   int
		want   string
		wantOK bool
	}{
		{
			name: "terminal shell editor chain picks the editor cwd",
			// The user's hedge scenario: the terminal's own cwd is ~,
			// the shell's is ~, vim's is the real signal.
			tree: &fakeTree{
				children: map[int][]int{1: {2}, 2: {3}},
				cwds:     map[int]string{1: home, 2: home, 3: proj},
			},
			root: 1, want: proj, wantOK: true,
		},
		{
			name: "foreground hint wins over a deeper descendant",
			tree: &fakeTree{
				children: map[int][]int{1: {2}, 2: {3}},
				cwds:     map[int]string{1: home, 2: "/home/tester/fg", 3: "/home/tester/deep"},
				fg:       map[int]int{1: 2},
			},
			root: 1, want: "/home/tester/fg", wantOK: true,
		},
		{
			name: "foreground hint with unreadable cwd falls back to the walk",
			tree: &fakeTree{
				children: map[int][]int{1: {2}},
				cwds:     map[int]string{1: home, 2: proj},
				fg:       map[int]int{1: 99},
			},
			root: 1, want: proj, wantOK: true,
		},
		{
			name: "foreground hint at root dir falls back to the walk",
			tree: &fakeTree{
				children: map[int][]int{1: {2}, 2: {3}},
				cwds:     map[int]string{2: "/", 3: proj},
				fg:       map[int]int{1: 2},
			},
			root: 1, want: proj, wantOK: true,
		},
		{
			name: "foreground hint at home falls back to the walk",
			tree: &fakeTree{
				children: map[int][]int{1: {2}},
				cwds:     map[int]string{1: proj, 2: home},
				fg:       map[int]int{1: 2},
			},
			root: 1, want: proj, wantOK: true,
		},
		{
			name: "deepest meaningful cwd wins without a hint",
			tree: &fakeTree{
				children: map[int][]int{1: {2}, 2: {3}},
				cwds:     map[int]string{1: home, 2: "/home/tester/shallow", 3: "/home/tester/deep"},
			},
			root: 1, want: "/home/tester/deep", wantOK: true,
		},
		{
			name: "equal depth keeps the first child found",
			tree: &fakeTree{
				children: map[int][]int{1: {2, 3}},
				cwds:     map[int]string{2: "/home/tester/first", 3: "/home/tester/second"},
			},
			root: 1, want: "/home/tester/first", wantOK: true,
		},
		{
			name: "root itself can be the signal",
			// A terminal launched from a project directory.
			tree: &fakeTree{
				children: map[int][]int{},
				cwds:     map[int]string{1: proj},
			},
			root: 1, want: proj, wantOK: true,
		},
		{
			name: "gui app parked at slash contributes nothing",
			tree: &fakeTree{
				children: map[int][]int{1: {2}},
				cwds:     map[int]string{1: "/", 2: "/"},
			},
			root: 1, wantOK: false,
		},
		{
			name: "everything at home contributes nothing",
			tree: &fakeTree{
				children: map[int][]int{1: {2}},
				cwds:     map[int]string{1: home, 2: home + "/"},
			},
			root: 1, wantOK: false,
		},
		{
			name: "all cwds unreadable",
			tree: &fakeTree{
				children: map[int][]int{1: {2, 3}},
				cwds:     map[int]string{},
			},
			root: 1, wantOK: false,
		},
		{
			name: "cyclic tree terminates and still finds the signal",
			tree: &fakeTree{
				children: map[int][]int{1: {2}, 2: {1, 3}, 3: {3}},
				cwds:     map[int]string{3: proj},
			},
			root: 1, want: proj, wantOK: true,
		},
		{
			name: "empty cwd string is unreadable",
			tree: &fakeTree{
				children: map[int][]int{},
				cwds:     map[int]string{1: ""},
			},
			root: 1, wantOK: false,
		},
		{
			name:   "non-positive root pid",
			tree:   &fakeTree{},
			root:   0,
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DeriveCwd(tc.tree, tc.root)
			require.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				require.Equal(t, tc.want, got)
			} else {
				require.Empty(t, got)
			}
		})
	}
}

func TestDeriveCwdNilTree(t *testing.T) {
	got, ok := DeriveCwd(nil, 1)
	require.False(t, ok)
	require.Empty(t, got)
}

func TestDeriveCwdDepthCap(t *testing.T) {
	t.Setenv("HOME", "/home/tester")
	// A linear chain deeper than maxProcDepth: the meaningful cwd
	// sits past the cap and must NOT be found.
	children := map[int][]int{}
	cwds := map[int]string{}
	last := maxProcDepth + 5
	for pid := 1; pid < last; pid++ {
		children[pid] = []int{pid + 1}
	}
	cwds[last] = "/home/tester/too-deep"
	_, ok := DeriveCwd(&fakeTree{children: children, cwds: cwds}, 1)
	require.False(t, ok, "the walk is bounded at maxProcDepth")
}

func TestCwdBoost(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		cwd    string
		weight float64
		want   float64
	}{
		{"direct child gets full weight", "/proj/file.go", "/proj", 10, 10},
		{"the cwd itself gets full weight", "/proj", "/proj", 10, 10},
		{"one extra level halves", "/proj/sub/file.go", "/proj", 10, 5},
		{"two extra levels quarter", "/proj/a/b/file.go", "/proj", 10, 2.5},
		{"unrelated path", "/elsewhere/file.go", "/proj", 10, 0},
		{"sibling name prefix is not containment", "/projX/file.go", "/proj", 10, 0},
		{"parent of cwd", "/", "/proj", 10, 0},
		{"zero weight disables", "/proj/file.go", "/proj", 0, 0},
		{"negative weight disables", "/proj/file.go", "/proj", -3, 0},
		{"empty cwd", "/proj/file.go", "", 10, 0},
		{"root cwd is no signal", "/proj/file.go", "/", 10, 0},
		{"empty path", "", "/proj", 10, 0},
		{"deep cwd direct child", "/a/b/c/d/file", "/a/b/c/d", 10, 10},
		{"windows separators mix", `C:\proj\file.go`, "C:/proj", 10, 10},
		{"trailing separator on cwd", "/proj/file.go", "/proj/", 10, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.InDelta(t, tc.want, CwdBoost(tc.path, tc.cwd, tc.weight), 1e-9)
		})
	}
}
