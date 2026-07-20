package watch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFSEPathTranslator(t *testing.T) {
	var tr fsePathTranslator
	tr.add("/private/tmp", "/tmp")
	tr.add("/private/var/x", "/var/x")
	tr.add("/same", "/same") // identical spellings: dropped
	tr.add("", "/whatever")  // empty resolved: dropped

	require.Equal(t, "/tmp/a/b.txt", tr.translate("/private/tmp/a/b.txt"))
	require.Equal(t, "/tmp", tr.translate("/private/tmp"), "the exact root translates too")
	require.Equal(t, "/var/x/y", tr.translate("/private/var/x/y"))
	require.Equal(t, "/private/tmpfoo", tr.translate("/private/tmpfoo"),
		"prefix matching is path-boundary aware, never a bare string prefix")
	require.Equal(t, "/elsewhere/f", tr.translate("/elsewhere/f"), "no pair: verbatim")
	require.Equal(t, "/same/f", tr.translate("/same/f"), "identical pairs never registered")

	var zero fsePathTranslator
	require.Equal(t, "/anything", zero.translate("/anything"), "the zero value translates nothing")
}

func TestFSEDecide(t *testing.T) {
	roots := []string{"/r", "/tmp"}
	var tr fsePathTranslator
	tr.add("/private/tmp", "/tmp")

	cases := []struct {
		name     string
		path     string
		flags    uint32
		emit     string
		overflow bool
		ok       bool
	}{
		{"created file in root emits", "/r/a.txt", fseItemCreated, "/r/a.txt", false, true},
		{"removed emits", "/r/a.txt", fseItemRemoved, "/r/a.txt", false, true},
		{"renamed emits", "/r/dir", fseItemRenamed, "/r/dir", false, true},
		{"mount emits", "/r/vol", fseMount, "/r/vol", false, true},
		{"unmount emits", "/r/vol", fseUnmount, "/r/vol", false, true},
		{"root change emits (defensive; WatchRoot is never set)", "/r", fseRootChanged, "/r", false, true},
		{"modified-only is noise", "/r/a.txt", fseItemModified, "", false, false},
		{"metadata-only is noise", "/r/a.txt", fseItemInodeMetaMod | fseItemXattrMod | fseItemChangeOwner | fseItemFinderInfoMod, "", false, false},
		{"created wins over accompanying noise bits", "/r/a.txt", fseItemCreated | fseItemModified, "/r/a.txt", false, true},
		{"zero flags fail open into a reconcile", "/r/somewhere", 0, "/r/somewhere", false, true},
		{"history-done-only is dropped", "/r", fseHistoryDone, "", false, false},
		{"must-scan-subdirs is overflow AND emits its subtree root", "/r/deep", fseMustScanSubDirs, "/r/deep", true, true},
		{"user-dropped is overflow", "/r/x", fseUserDropped | fseItemCreated, "/r/x", true, true},
		{"kernel-dropped outside the roots still overflows, no emit", "/outside", fseKernelDropped, "", true, false},
		{"id-wrap is overflow", "/r/x", fseEventIdsWrapped, "/r/x", true, true},
		{"outside every root is dropped", "/outside/f", fseItemCreated, "", false, false},
		{"the resolved spelling translates back into scope", "/private/tmp/f", fseItemCreated, "/tmp/f", false, true},
		{"the exact translated root is in scope", "/private/tmp", fseItemCreated, "/tmp", false, true},
		{"trailing slash is cleaned", "/r/dir/", fseItemCreated, "/r/dir", false, true},
		{"the root itself is in scope", "/r", fseItemCreated, "/r", false, true},
	}
	for _, tc := range cases {
		emit, overflow, ok := fseDecide(tc.path, tc.flags, roots, tr)
		require.Equal(t, tc.emit, emit, tc.name)
		require.Equal(t, tc.overflow, overflow, tc.name)
		require.Equal(t, tc.ok, ok, tc.name)
	}
}

func TestFSEDecideRootSlashSeesEverything(t *testing.T) {
	// The default config's root "/" resolves to itself (no translator
	// pairs) and every absolute path is in scope -- the fanotify
	// whole-superblock analogue.
	emit, overflow, ok := fseDecide("/private/tmp/x", fseItemCreated, []string{"/"}, fsePathTranslator{})
	require.True(t, ok)
	require.False(t, overflow)
	require.Equal(t, "/private/tmp/x", emit,
		"with no configured-root symlink pair, the real spelling passes through (matching the walker)")
}
