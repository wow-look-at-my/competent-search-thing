package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/ipc"
	"github.com/wow-look-at-my/competent-search-thing/schemas"
)

func TestGetConfigSchemaServesEmbeddedSchema(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	got := a.GetConfigSchema()
	require.Equal(t, string(schemas.ConfigSchemaJSON), got)
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(got), &doc), "the schema is valid JSON")
	require.Contains(t, doc, "properties")
}

func TestGetConfigForEdit(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir) // after newTestApp, which re-points it

	// A hand-edited file with an editor hint and a typo'd key: both
	// must surface as unknown keys, and the returned document is the
	// normalized load (no unknown keys inside it).
	raw := `{"$schema": "x", "maxResults": 33, "frobnicate": 1}`
	cfgPath := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(cfgPath, []byte(raw), 0o644))

	out := a.GetConfigForEdit()
	require.Equal(t, cfgPath, out.Path)
	require.Equal(t, []string{"$schema", "frobnicate"}, out.UnknownKeys)
	require.Empty(t, out.LoadWarning)
	var got config.Config
	require.NoError(t, json.Unmarshal([]byte(out.ConfigJSON), &got))
	require.Equal(t, 33, got.MaxResults)
	require.Equal(t, config.DefaultTheme, got.Theme, "the document is normalized")
}

func TestGetConfigForEditSurfacesLoadWarning(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte("{corrupt"), 0o644))

	out := a.GetConfigForEdit()
	require.NotEmpty(t, out.LoadWarning, "a corrupt file is reported, not hidden")
	var got config.Config
	require.NoError(t, json.Unmarshal([]byte(out.ConfigJSON), &got),
		"the editor still gets a usable (default) document")
}

func TestSaveConfigRejectsUnknownField(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)

	res := a.SaveConfig(`{"maxResults": 10, "maxResultz": 11}`)
	require.False(t, res.OK)
	require.Contains(t, res.Error, "maxResultz", "the offending field is named")
	_, err := os.Stat(filepath.Join(dir, "config.json"))
	require.True(t, os.IsNotExist(err), "a rejected save writes nothing")
}

func TestSaveConfigRejectsGarbage(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	t.Setenv(config.EnvConfigDir, t.TempDir())

	res := a.SaveConfig("{\n  \"maxResults\": oops\n}")
	require.False(t, res.OK)
	require.Contains(t, res.Error, "line 2", "syntax errors carry the line")

	res = a.SaveConfig(`{"maxResults": "ten"}`)
	require.False(t, res.OK)
	require.Contains(t, res.Error, "maxResults", "type errors name the field")

	res = a.SaveConfig(`{"maxResults": 1} {"more": 1}`)
	require.False(t, res.OK)
	require.Contains(t, res.Error, "trailing data")
}

func TestSaveConfigForcesRootsVersionAndNormalizes(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)

	// The editor document carries no rootsVersion (or a doctored 0):
	// the save must stamp the on-disk value so the next Load never
	// re-runs the roots migrations over the user's config.
	res := a.SaveConfig(`{"roots": ["/data"], "rootsVersion": 0, "maxResults": 25}`)
	require.True(t, res.OK, "error: %s", res.Error)

	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(data, &onDisk))
	require.EqualValues(t, config.CurrentRootsVersion(), onDisk["rootsVersion"])
	require.EqualValues(t, "dark", onDisk["theme"], "the save normalizes (empty theme -> dark)")
}

func TestSaveConfigAppliesLive(t *testing.T) {
	mgr := index.NewManager(nil, nil, 50)
	a, _ := newTestApp(t, mgr, Options{})
	t.Setenv(config.EnvConfigDir, t.TempDir())

	res := a.SaveConfig(`{"maxResults": 7, "search": {"fuzzyDisabled": true}}`)
	require.True(t, res.OK, "error: %s", res.Error)
	require.Equal(t, 7, mgr.MaxResults(), "maxResults applied live")
	require.True(t, mgr.FuzzyDisabled(), "search.fuzzyDisabled applied live")
	require.Contains(t, res.Applied, "maxResults")
	require.Contains(t, res.Applied, "search.fuzzyDisabled")
	require.Empty(t, res.ApplyErrors)
}

func TestShowConfigLatchesUntilDomReady(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())

	a.showConfig() // frontend not ready: latched
	require.False(t, r.has("show"))
	require.Empty(t, r.emitted(eventConfigOpen))

	a.DomReady(context.Background())
	require.Len(t, r.emitted(eventShown), 1, "the latched summon shows the bar")
	require.Len(t, r.emitted(eventConfigOpen), 1, "and enters editor mode")

	// Ordering contract: config:open strictly after app:shown (the
	// frontend's app:shown handler re-renders the bar).
	r.mu.Lock()
	emits := append([]emittedEvent(nil), r.emits...)
	r.mu.Unlock()
	var shownAt, cfgAt int
	for i, e := range emits {
		switch e.name {
		case eventShown:
			shownAt = i
		case eventConfigOpen:
			cfgAt = i
		}
	}
	require.Greater(t, cfgAt, shownAt, "config:open follows app:shown")
}

func TestShowConfigWhenHiddenSummons(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	a.DomReady(context.Background())

	a.showConfig()
	require.Len(t, r.emitted(eventShown), 1, "hidden bar: the full summon path runs")
	require.Len(t, r.emitted(eventConfigOpen), 1)
}

func TestShowConfigWhenVisibleJustEmits(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	a.DomReady(context.Background())
	a.showOnCursorDisplay()
	require.Len(t, r.emitted(eventShown), 1)

	a.showConfig()
	require.Len(t, r.emitted(eventShown), 1, "no re-summon of a visible bar")
	require.Len(t, r.emitted(eventConfigOpen), 1, "just the mode event")
}

func TestHideCancelsPendingConfig(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	a.showConfig() // latched
	a.Hide()       // an IPC hide while booting wins
	a.DomReady(context.Background())
	require.Empty(t, r.emitted(eventShown), "the latched summon died with the hide")
	require.Empty(t, r.emitted(eventConfigOpen))
}

func TestOpenConfigOnStartup(t *testing.T) {
	a, r := newTestApp(t, nil, Options{OpenConfigOnStartup: true})
	a.Startup(context.Background())
	require.Empty(t, r.emitted(eventConfigOpen), "nothing before DomReady")

	a.DomReady(context.Background())
	require.Len(t, r.emitted(eventShown), 1, "OpenConfigOnStartup implies the show")
	require.Len(t, r.emitted(eventConfigOpen), 1)

	a.DomReady(context.Background())
	require.Len(t, r.emitted(eventConfigOpen), 1, "the latch executes exactly once")
}

func TestStartupWiresIPCConfigHandler(t *testing.T) {
	srv, path := newTestIPC(t)
	a, r := newTestApp(t, nil, Options{IPC: srv})
	a.plat.now = (&fakeClock{step: time.Second}).now
	a.Startup(context.Background())
	a.DomReady(context.Background())

	rep, err := ipc.Send(path, ipc.CmdConfig, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
	require.Equal(t, ipc.CmdConfig, rep.Accepted)
	require.Eventually(t, func() bool { return len(r.emitted(eventConfigOpen)) == 1 },
		5*time.Second, 5*time.Millisecond, "IPC config summons the editor")
	require.Eventually(t, func() bool { return len(r.emitted(eventShown)) == 1 },
		5*time.Second, 5*time.Millisecond)
}

func TestOpenConfigFileBoundMethod(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	a.Startup(context.Background())
	a.DomReady(context.Background())

	require.NoError(t, a.OpenConfigFile())
	require.True(t, r.has("open:"+filepath.Join(dir, "config.json")),
		"the escape hatch still opens the file itself")
}

func TestDecodeErrMsgFallback(t *testing.T) {
	// Non-JSON error values pass through untouched (e.g. the unknown
	// field error, which already names the field).
	a, _ := newTestApp(t, nil, Options{})
	t.Setenv(config.EnvConfigDir, t.TempDir())
	res := a.SaveConfig("")
	require.False(t, res.OK)
	require.NotEmpty(t, res.Error)
}
