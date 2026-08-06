package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
)

func TestGetStartupMode(t *testing.T) {
	bar, _ := newTestApp(t, nil, Options{})
	assert.Equal(t, StartupModeSearch, bar.GetStartupMode())

	cfgWin, _ := newTestApp(t, nil, Options{ConfigWindow: true})
	assert.Equal(t, StartupModeConfig, cfgWin.GetStartupMode())
}

// The settings window starts NONE of the searchbar's machinery: no
// index build, no hotkey, no tray. (newTestApp stubs the builders, so
// the seam recorder is the observable.)
func TestConfigWindowStartupBringsUpNothingElse(t *testing.T) {
	mgr := index.NewManager([]string{t.TempDir()}, nil, 20)
	a, r := newTestApp(t, mgr, Options{ConfigWindow: true})
	a.Startup(context.Background())

	assert.False(t, r.has("startHotkey"), "no global hotkey in the settings window")
	assert.Zero(t, mgr.Len(), "and no index build")
	a.DomReady(context.Background())
	assert.False(t, r.has("show"), "nothing shows the bar; the window is already up")
}

// A second `config` invocation reaches the open window over its own
// socket and raises it -- plainly, without the bar's cursor-display
// positioning.
func TestConfigWindowIPCShowRaisesIt(t *testing.T) {
	srv, path := newTestIPC(t)
	a, r := newTestApp(t, nil, Options{ConfigWindow: true, IPC: srv})
	a.Startup(context.Background())
	a.DomReady(context.Background())

	rep, err := ipc.Send(path, ipc.CmdShow, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
	require.Eventually(t, func() bool { return r.has("show") },
		5*time.Second, 5*time.Millisecond, "the open settings window is raised")
	assert.False(t, r.has("setPos"), "a raise never repositions an ordinary window")
	assert.Empty(t, r.emitted(eventShown), "and never emits the bar's show event")
}

// A raise arriving before the frontend can render is latched and
// executed by DomReady, like every other pre-init summon.
func TestConfigWindowRaiseLatchesUntilDomReady(t *testing.T) {
	a, r := newTestApp(t, nil, Options{ConfigWindow: true})
	a.Startup(context.Background())

	a.raiseConfigWindow()
	require.False(t, r.has("show"))
	a.DomReady(context.Background())
	assert.True(t, r.has("show"))
	assert.False(t, r.has("setPos"))
}

func TestCloseConfigWindowQuitsOnlyTheSettingsWindow(t *testing.T) {
	bar, barRec := newTestApp(t, nil, Options{})
	bar.Startup(context.Background())
	bar.CloseConfigWindow()
	assert.False(t, barRec.has("quit"), "Esc in the searchbar must never quit the app")

	cfgWin, rec := newTestApp(t, nil, Options{ConfigWindow: true})
	cfgWin.Startup(context.Background())
	cfgWin.CloseConfigWindow()
	assert.True(t, rec.has("quit"))
}

// The settings window edits config.json; it has nothing to APPLY to,
// and the running searchbar picks the change up through its own
// config watcher. The save must still land on disk.
func TestConfigWindowSaveWritesWithoutApplying(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	mgr := index.NewManager([]string{dir}, nil, 20)
	a, _ := newTestApp(t, mgr, Options{ConfigWindow: true})
	a.Startup(context.Background())

	res := a.SaveConfig(`{"maxResults": 7}`)
	require.True(t, res.OK, "error: %s", res.Error)
	assert.Empty(t, res.Applied, "nothing is applied live in the settings window")
	assert.Empty(t, res.ApplyErrors)

	cfg, err := config.Load()
	require.NoError(t, err)
	assert.Equal(t, 7, cfg.MaxResults, "but the file is written")
	assert.Equal(t, 20, mgr.MaxResults(), "and this process's own manager is untouched")
}
