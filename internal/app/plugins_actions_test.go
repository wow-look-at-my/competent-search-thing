package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

func TestRunPluginActionCopyText(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("calc", plugin.Action{Type: plugin.ActionCopyText, Value: "42"}))
	require.True(t, r.has("clipboard:42"))
	require.False(t, r.has("hide"), "copy_text keeps the bar open")

	require.Error(t, a.RunPluginAction("calc", plugin.Action{Type: plugin.ActionCopyText}),
		"empty value is rejected")
}

func TestRunPluginActionCopyTextErrors(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	require.Error(t, a.RunPluginAction("calc", plugin.Action{Type: plugin.ActionCopyText, Value: "x"}),
		"no clipboard before Startup")

	a2, _, _ := newPluginTestApp(t)
	boom := errors.New("no clipboard")
	a2.rt.clipboardSetText = func(context.Context, string) error { return boom }
	require.ErrorIs(t, a2.RunPluginAction("calc", plugin.Action{Type: plugin.ActionCopyText, Value: "x"}), boom)
}

func TestRunPluginActionOpenPath(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("files", plugin.Action{Type: plugin.ActionOpenPath, Value: "/tmp/report.pdf"}))
	require.Equal(t, []string{"resolve:/tmp/report.pdf", "mint", "open:/tmp/report.pdf", "hide"}, r.callNames())
}

func TestRunPluginActionOpenPathValidation(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	for _, bad := range []string{"", "relative/path", "./x", "tmp"} {
		require.Error(t, a.RunPluginAction("files", plugin.Action{Type: plugin.ActionOpenPath, Value: bad}), "path %q", bad)
	}
	require.Empty(t, r.callNames(), "invalid paths never reach the launcher")

	boom := errors.New("no handler")
	a.plat.open = func(string, []string) error { return boom }
	require.ErrorIs(t, a.RunPluginAction("files", plugin.Action{Type: plugin.ActionOpenPath, Value: "/tmp/x"}), boom)
	require.False(t, r.has("hide"), "a failed open does not hide the bar")
}

func TestRunPluginActionOpenURL(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("web", plugin.Action{Type: plugin.ActionOpenURL, Value: "https://example.com/x"}))
	require.Equal(t, []string{"resolve:https://example.com/x", "mint", "open:https://example.com/x", "hide"}, r.callNames())
}

func TestRunPluginActionOpenURLValidation(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	for _, bad := range []string{
		"",
		"ftp://example.com",
		"javascript:alert(1)",
		"http://",     // no host
		"example.com", // no scheme
		"file:///etc/passwd",
	} {
		require.Error(t, a.RunPluginAction("web", plugin.Action{Type: plugin.ActionOpenURL, Value: bad}), "url %q", bad)
	}
	require.Empty(t, r.callNames(), "invalid URLs never reach the launcher")
}

func TestRunPluginActionRunCommand(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("apps", plugin.Action{Type: plugin.ActionRunCommand, Argv: []string{"firefox", "--new-window"}}))
	require.Equal(t, []string{"run:firefox --new-window", "hide"}, r.callNames())
}

func TestRunPluginActionRunCommandValidation(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	tooMany := make([]string, maxActionArgvEntries+1)
	for i := range tooMany {
		tooMany[i] = "x"
	}
	for name, argv := range map[string][]string{
		"nil argv":        nil,
		"empty argv":      {},
		"17 entries":      tooMany,
		"empty entry":     {"a", ""},
		"oversized entry": {strings.Repeat("y", maxActionArgvEntryBytes+1)},
	} {
		require.Error(t, a.RunPluginAction("apps", plugin.Action{Type: plugin.ActionRunCommand, Argv: argv}), name)
	}
	require.Empty(t, r.callNames(), "invalid argv never reaches the launcher")

	boom := errors.New("spawn failed")
	a.plat.run = func([]string, []string) error { return boom }
	require.ErrorIs(t, a.RunPluginAction("apps", plugin.Action{Type: plugin.ActionRunCommand, Argv: []string{"firefox"}}), boom)
	require.False(t, r.has("hide"), "a failed spawn does not hide the bar")
}

func TestRunPluginActionRejectsUnknownTypes(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	for _, typ := range []string{"", "set_query", "explode"} {
		require.Error(t, a.RunPluginAction("p", plugin.Action{Type: typ, Value: "x"}), "type %q", typ)
	}
	require.Empty(t, r.callNames())
}

func TestRunPluginActionActivateWindow(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("windows", plugin.Action{Type: plugin.ActionActivateWindow, Window: "4294967295"}))
	require.Equal(t, []string{"activateWindow:4294967295", "hide"}, r.callNames(),
		"the seam gets the parsed id, then the bar hides")
}

func TestRunPluginActionActivateWindowValidation(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	for _, bad := range []string{"", "abc", "-1", "1.5", "0x10", "4294967296", " 7"} {
		require.Error(t, a.RunPluginAction("windows", plugin.Action{Type: plugin.ActionActivateWindow, Window: bad}),
			"window id %q", bad)
	}
	require.Empty(t, r.callNames(), "invalid window ids never reach the platform, and the bar stays")

	boom := errors.New("no X server")
	a.plat.activateWindow = func(uint32) error { return boom }
	require.ErrorIs(t, a.RunPluginAction("windows", plugin.Action{Type: plugin.ActionActivateWindow, Window: "7"}), boom)
	require.False(t, r.has("hide"), "a failed activation does not hide the bar")
}

func TestRunBuiltinRescanWithoutWatcher(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.Error(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "rescan"}),
		"no rescanner yet: friendly error")
	require.False(t, r.has("hide"))
}

func TestRunBuiltinRescanRequestsAndHides(t *testing.T) {
	dir := t.TempDir()
	m := index.NewManager([]string{dir}, nil, 0)
	a, r := newTestApp(t, m, Options{})
	a.Startup(context.Background())
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	require.Eventually(t, func() bool { return watchUp(a) },
		20*time.Second, 10*time.Millisecond, "watch layer comes up after the initial build")

	require.NoError(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "rescan"}))
	require.True(t, r.has("hide"))
}

func TestRunBuiltinReloadSwapsRegistry(t *testing.T) {
	a, r, f := newPluginTestApp(t)
	f2 := &fakeDispatcher{}
	a.newRegistry = func() dispatcher { return f2 }

	require.NoError(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "reload"}))
	require.Equal(t, 1, f.closedCount(), "the old registry is closed")
	require.True(t, r.has("hide"))

	a.QueryPlugins("x", 9)
	require.Equal(t, 1, f2.callCount(), "dispatch reaches the new registry")
	require.Equal(t, 0, f.callCount(), "the old registry is out of the loop")
}

func TestRunBuiltinConfigOpensEditor(t *testing.T) {
	// !config summons the in-app config editor now: with the bar
	// already visible (the bang was typed into it), that is just the
	// mode event -- no file open, no hide, the bar switches modes.
	a, r, _ := newPluginTestApp(t)
	a.DomReady(context.Background())
	a.showOnCursorDisplay() // the bar is visible, as it is when a bang runs
	require.NoError(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "config"}))
	require.Len(t, r.emitted(eventConfigOpen), 1, "the editor mode event fires")
	require.False(t, r.has("hide"), "the bar stays up, switching modes")
	for _, c := range r.callNames() {
		require.NotContains(t, c, "open:", "no file opens; the editor's escape hatch owns that")
	}
}

func TestRunBuiltinVersionCopiesWithoutHiding(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "version"}))
	require.True(t, r.has("clipboard:"+Version))
	require.False(t, r.has("hide"), "version keeps the bar open")
}

func TestRunBuiltinQuit(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.NoError(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "quit"}))
	require.Equal(t, []string{"quit"}, r.callNames())

	before, _ := newTestApp(t, nil, Options{})
	require.Error(t, before.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "quit"}),
		"quit needs the runtime context")
}

func TestRunBuiltinUnknown(t *testing.T) {
	a, r, _ := newPluginTestApp(t)
	require.Error(t, a.RunPluginAction("app", plugin.Action{Type: plugin.ActionRunBuiltin, Value: "explode"}))
	require.Empty(t, r.callNames())
}
