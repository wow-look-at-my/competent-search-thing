package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// A summoned bar on the primary test display: the drag tests start
// from a placed 780x550 window at BarPosition (570, 177).
func summonedApp(t *testing.T) (*App, *seamRecorder) {
	t.Helper()
	// The classic bar-only shape: the pane is ON by default (config
	// v8), so base-mode drag semantics need the explicit opt-out --
	// commits must persist into window.width/height, not the preview
	// pair (TestResizeCommitWritesPreviewSizeWhileMounted covers the
	// pane-mounted twin with its own explicitly-enabled app).
	a, r := newTestApp(t, nil, Options{WindowWidth: 780, WindowHeight: 550, ResultsWidth: 780,
		Preview: config.PreviewConfig{Enabled: config.Bool(false)}})
	a.plat.goos = "linux"
	r.cursorOK = true
	r.cursorX, r.cursorY = 960, 540
	r.displays = testDisplays()
	a.Startup(context.Background())
	a.showOnCursorDisplay()
	require.Equal(t, []int{570}, r.setPosX)
	require.Equal(t, []int{177}, r.setPosY)
	return a, r
}

func TestResizeDragCentersAboutDisplay(t *testing.T) {
	a, r := summonedApp(t)

	a.ResizeDrag(900, 550)
	require.True(t, r.has("setWindowSize:900x550"))
	// About-center: the new x is the display center minus half the new
	// width -- (1920-900)/2 = 510, a 60px shift left for a 120px
	// growth -- while the anchored top y stays 177.
	require.Equal(t, []int{570, 510}, r.setPosX)
	require.Equal(t, []int{177, 177}, r.setPosY)
	w, h := a.windowSize()
	require.Equal(t, [2]int{900, 550}, [2]int{w, h}, "the dragged size is the live desired size")
}

func TestResizeDragClampsToWorkAreaAndAnchoredTop(t *testing.T) {
	a, r := summonedApp(t)

	// Absurd drag values clamp to the primary's work area (1920x1040
	// starting at y=40), and the anchored top (177) additionally caps
	// the height at the work area's bottom: 40+1040-177 = 903.
	a.ResizeDrag(5000, 5000)
	require.True(t, r.has("setWindowSize:1920x903"))

	// Below-floor values clamp up to the config floors.
	a.ResizeDrag(10, 10)
	require.True(t, r.has("setWindowSize:320x240"))
}

func TestResizeDragDedupesUnchangedFrames(t *testing.T) {
	a, r := summonedApp(t)
	a.ResizeDrag(900, 550)
	before := len(r.callNames())
	a.ResizeDrag(900, 550) // identical frame: no native calls, no moves
	require.Equal(t, before, len(r.callNames()))
}

func TestResizeCommitPersistsWindowSize(t *testing.T) {
	a, r := summonedApp(t)
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)

	require.NoError(t, a.ResizeCommit(900, 600))
	require.True(t, r.has("setWindowSize:900x600"))

	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	require.NoError(t, err)
	var onDisk struct {
		Schema string `json:"$schema"`
		Window struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"window"`
	}
	require.NoError(t, json.Unmarshal(data, &onDisk))
	require.Equal(t, 900, onDisk.Window.Width, "ONE commit write carries the dragged width")
	require.Equal(t, 600, onDisk.Window.Height)
	require.Equal(t, config.SchemaRef, onDisk.Schema, "the save stamps the $schema reference")

	require.NotEqual(t, [32]byte{}, a.getLastSavedSum(),
		"the self-write checksum is recorded so the config watcher skips this save")
	require.Equal(t, 900, a.GetPreviewConfig().ResultsWidth,
		"the live results-column width follows the persisted window.width")

	// The live-apply baseline was patched: a no-op external reload
	// diffs nothing against the dragged size.
	a.cfgMu.Lock()
	cur := a.cfgCurrent
	a.cfgMu.Unlock()
	if cur != nil {
		require.Equal(t, 900, cur.Window.Width)
		require.Equal(t, 600, cur.Window.Height)
	}
}

func TestResizeCommitWritesPreviewSizeWhileMounted(t *testing.T) {
	a, r := newTestApp(t, nil, Options{WindowWidth: 1600, WindowHeight: 800, ResultsWidth: 780,
		Preview: config.PreviewConfig{Enabled: config.Bool(true), WindowWidth: 1600, WindowHeight: 800}})
	a.plat.goos = "linux"
	r.cursorOK = true
	r.cursorX, r.cursorY = 960, 540
	r.displays = testDisplays()
	a.Startup(context.Background())
	a.showOnCursorDisplay()
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)

	require.NoError(t, a.ResizeCommit(1400, 700))

	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	require.NoError(t, err)
	var onDisk struct {
		Window struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"window"`
		Preview struct {
			WindowWidth  int `json:"windowWidth"`
			WindowHeight int `json:"windowHeight"`
		} `json:"preview"`
	}
	require.NoError(t, json.Unmarshal(data, &onDisk))
	require.Equal(t, 1400, onDisk.Preview.WindowWidth,
		"with the pane mounted, the drag describes the widened layout")
	require.Equal(t, 700, onDisk.Preview.WindowHeight)
	require.Equal(t, config.DefaultWindowWidth, onDisk.Window.Width,
		"the flag-off base size stays untouched")
}

func TestResizeWaylandResizesWithoutMoving(t *testing.T) {
	a, r := newTestApp(t, nil, Options{WindowWidth: 780, WindowHeight: 550})
	a.plat.detectSession = func() platform.Session { return platform.Session{Kind: platform.SessionWayland} }
	a.plat.windowWorkArea = func() (platform.Rect, bool) {
		return platform.Rect{X: 0, Y: 0, W: 1280, H: 720}, true
	}
	a.Startup(context.Background())

	a.ResizeDrag(5000, 700)
	require.True(t, r.has("setWindowSize:1280x700"), "the clamp rides the work-area probe")
	require.False(t, r.has("setPos"), "the compositor owns placement; drags never move the window")
	require.False(t, r.has("moveWindow"))
}

func TestResizePreStartupIsNoOp(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	a.ResizeDrag(900, 600)
	require.NoError(t, a.ResizeCommit(900, 600))
	require.Empty(t, r.callNames())
	_, err := os.Stat(filepath.Join(dir, "config.json"))
	require.True(t, os.IsNotExist(err), "nothing was applied, nothing is persisted")
}

func TestHideClearsDragAnchor(t *testing.T) {
	a, r := summonedApp(t)
	a.ResizeDrag(900, 550)
	a.mu.Lock()
	require.True(t, a.dragActive)
	a.mu.Unlock()

	a.Hide()
	a.mu.Lock()
	require.False(t, a.dragActive, "a hide drops the in-flight drag anchor")
	require.False(t, a.dragPosOK)
	a.mu.Unlock()
	require.True(t, r.has("hide"))
}
