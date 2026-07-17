package plugin

// Integration tests driving the SHIPPED example plugins in
// examples/plugins through the real pipeline: LoadDir -> New ->
// Registry.Dispatch -> transport -> sanitizer -> emission. calc and
// ps run the real python3 scripts (skipped when python3 is absent;
// CI ubuntu has it); color runs the real colorhttp.Handler behind an
// httptest server.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/competent-search-thing/examples/plugins/color-http/colorhttp"
)

// examplesDir locates the in-repo examples/plugins directory relative
// to this package.
func examplesDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", "plugins"))
	require.NoError(t, err)
	_, err = os.Stat(dir)
	require.Nil(t, err)

	return dir
}

// requirePython3 skips the test when python3 is not on PATH (the calc
// and ps example plugins are python3 scripts).
func requirePython3(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available on this system")
	}
}

// recvWithin waits up to d for one emission. Python interpreter
// startup on a busy CI machine makes the usual 3s helper too tight.
func recvWithin(t *testing.T, ch <-chan Emission, d time.Duration) Emission {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(d):
		t.Fatal("timed out waiting for an emission")
		return Emission{}
	}
}

// writePluginDir creates root/name/manifest.json and returns the
// plugin directory, for tests that assemble manifests on the fly.
func writePluginDir(t *testing.T, root, name, manifestJSON string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifestJSON), 0o644))
	return dir
}

// newExamplesRegistry loads the real examples/plugins directory --
// validating all three shipped manifests -- and builds a Registry.
func newExamplesRegistry(t *testing.T) (*Registry, *logCapture) {
	t.Helper()
	manifests, errs := LoadDir(examplesDir(t))
	require.Empty(t, errs, "every shipped example manifest must load cleanly")
	ids := make([]string, 0, len(manifests))
	for _, m := range manifests {
		ids = append(ids, m.ID)
	}
	require.ElementsMatch(t, []string{"calc", "color", "ps"}, ids)

	lc := &logCapture{}
	r := New(Options{Manifests: manifests, Logf: lc.logf})
	t.Cleanup(r.Close)
	require.Empty(t, r.Errors(), "examples must not collide with builtin ids or bangs")
	return r, lc
}

// testRunningApps is the app-context snapshot the ps tests dispatch
// with.
func testRunningApps() *RequestContext {
	return &RequestContext{RunningApps: []AppInfo{
		{Name: "firefox", Exe: "/usr/bin/firefox", Title: "Mozilla Firefox", PID: 42},
		{Name: "bash", Exe: "/bin/bash", Title: "", PID: 7},
	}}
}

func TestExampleManifestsValidate(t *testing.T) {
	manifests, errs := LoadDir(examplesDir(t))
	require.Empty(t, errs)
	byID := map[string]*Manifest{}
	for _, m := range manifests {
		byID[m.ID] = m
	}

	calc := byID["calc"]
	require.NotNil(t, calc)
	require.Equal(t, TypeCommand, calc.Type)
	require.Equal(t, "Calculator", calc.Name)
	require.NotNil(t, calc.Trigger)
	require.Equal(t, "=", calc.Trigger.Prefix)
	require.Equal(t, []string{"calc", "c"}, calc.Bangs)
	require.Equal(t, 1500, calc.TimeoutMS)

	color := byID["color"]
	require.NotNil(t, color)
	require.Equal(t, TypeHTTP, color.Type)
	require.Equal(t, "Color preview", color.Name)
	require.Equal(t, "#", color.Trigger.Prefix)
	require.Equal(t, "http://127.0.0.1:8765/query", color.HTTP.URL)

	ps := byID["ps"]
	require.NotNil(t, ps)
	require.Equal(t, TypeCommand, ps.Type)
	require.Nil(t, ps.Trigger, "ps is bang-targeted only")
	require.Equal(t, []string{"ps"}, ps.Bangs)
	require.Equal(t, []string{"running"}, ps.Context)
}

func TestExampleCalcEndToEnd(t *testing.T) {
	requirePython3(t)
	r, _ := newExamplesRegistry(t)

	t.Run("prefix dispatch", func(t *testing.T) {
		emit, ch := collectEmissions()
		info := r.Dispatch(context.Background(), "=2+2", 5, nil, emit)
		require.Equal(t, TargetInfo{}, info)
		e := recvWithin(t, ch, 5*time.Second)
		require.Equal(t, Emission{
			Plugin: "calc",
			Name:   "Calculator",
			Gen:    5,
			Results: []Result{{
				Title:       "4",
				Subtitle:    "2+2 =",
				Icon:        "calculator",
				Badge:       "CALC",
				AccentColor: "#a6e3a1",
				Score:       fptr(100),
				Fields: []Field{
					{Label: "Hex", Value: "0x4"},
					{Label: "Binary", Value: "0b100"},
				},
				Action: &Action{Type: ActionCopyText, Value: "4"},
			}},
		}, e)
		// Only calc emits: apps-search also matches but this registry
		// has no InstalledApps snapshot.
		requireNoEmission(t, ch, 200*time.Millisecond)
	})

	t.Run("targeted via registered short bang", func(t *testing.T) {
		emit, ch := collectEmissions()
		info := r.Dispatch(context.Background(), "!c 3*3", 6, nil, emit)
		require.Equal(t, TargetInfo{Targeted: true, Plugin: "calc", Name: "Calculator", Bang: "c"}, info,
			`"c" is itself a registered bang, so it resolves exactly and stays canonical`)
		e := recvWithin(t, ch, 5*time.Second)
		require.Equal(t, "calc", e.Plugin)
		require.Equal(t, int64(6), e.Gen)
		require.Len(t, e.Results, 1)
		require.Equal(t, "9", e.Results[0].Title)
	})

	t.Run("targeted via unique prefix", func(t *testing.T) {
		emit, ch := collectEmissions()
		info := r.Dispatch(context.Background(), "!ca 3*3", 7, nil, emit)
		require.Equal(t, TargetInfo{Targeted: true, Plugin: "calc", Name: "Calculator", Bang: "calc"}, info,
			`"ca" uniquely prefixes the registered bang "calc"`)
		e := recvWithin(t, ch, 5*time.Second)
		require.Equal(t, "calc", e.Plugin)
		require.Equal(t, "9", e.Results[0].Title)
	})

	t.Run("float division", func(t *testing.T) {
		emit, ch := collectEmissions()
		r.Dispatch(context.Background(), "=1/4", 8, nil, emit)
		e := recvWithin(t, ch, 5*time.Second)
		require.Len(t, e.Results, 1)
		require.Equal(t, Result{
			Title:       "0.25",
			Subtitle:    "1/4 =",
			Icon:        "calculator",
			Badge:       "CALC",
			AccentColor: "#a6e3a1",
			Score:       fptr(100),
			Action:      &Action{Type: ActionCopyText, Value: "0.25"},
		}, e.Results[0], "floats carry no Hex/Binary fields")
	})

	t.Run("non-arithmetic is silent", func(t *testing.T) {
		emit, ch := collectEmissions()
		r.Dispatch(context.Background(), "=import os", 9, nil, emit)
		requireNoEmission(t, ch, 2*time.Second) // empty result lists never emit
	})
}

func TestExamplePsEndToEnd(t *testing.T) {
	requirePython3(t)
	r, _ := newExamplesRegistry(t)

	t.Run("filters by needle", func(t *testing.T) {
		emit, ch := collectEmissions()
		info := r.Dispatch(context.Background(), "!ps fire", 3, testRunningApps(), emit)
		require.Equal(t, TargetInfo{Targeted: true, Plugin: "ps", Name: "Processes", Bang: "ps"}, info)
		e := recvWithin(t, ch, 5*time.Second)
		require.Equal(t, Emission{
			Plugin: "ps",
			Name:   "Processes",
			Gen:    3,
			Results: []Result{{
				Title:    "firefox",
				Subtitle: "Mozilla Firefox",
				Icon:     "app",
				Badge:    "PS",
				Score:    fptr(100),
				Fields: []Field{
					{Label: "PID", Value: "42"},
					{Label: "Exe", Value: "/usr/bin/firefox"},
				},
				Action: &Action{Type: ActionCopyText, Value: "42"},
			}},
		}, e)
	})

	t.Run("empty needle lists everything", func(t *testing.T) {
		emit, ch := collectEmissions()
		// "!ps " -- the trailing space makes it a targeted dispatch with
		// an empty stripped query ("!ps" alone would be a bang
		// suggestion, not a dispatch).
		info := r.Dispatch(context.Background(), "!ps ", 4, testRunningApps(), emit)
		require.True(t, info.Targeted)
		e := recvWithin(t, ch, 5*time.Second)
		require.Len(t, e.Results, 2)
		require.Equal(t, "firefox", e.Results[0].Title)
		require.Equal(t, "bash", e.Results[1].Title)
		require.Equal(t, "/bin/bash", e.Results[1].Subtitle, "no window title: the exe stands in")
		require.Equal(t, &Action{Type: ActionCopyText, Value: "7"}, e.Results[1].Action)
	})
}

// TestUndeclaredContextOmittedFromRequests proves the privacy knob on
// the wire: a manifest that declares NO context gets a request with
// no "context" field at all, even when the app has a snapshot. The
// plugin is a shell script that echoes the raw request back as a
// result subtitle.
func TestUndeclaredContextOmittedFromRequests(t *testing.T) {
	requireSh(t)
	root := t.TempDir()
	dir := writePluginDir(t, root, "echo", `{
        "v": 1,
        "id": "echo",
        "type": "command",
        "bangs": ["echo"],
        "command": { "argv": ["./run.sh"] }
    }`)
	writeScript(t, dir, "run.sh", `req=$(cat)
esc=$(printf '%s' "$req" | sed 's/\\/\\\\/g; s/"/\\"/g')
printf '{"results":[{"title":"echo","subtitle":"%s"}]}' "$esc"
`)
	manifests, errs := LoadDir(root)
	require.Empty(t, errs)
	require.Len(t, manifests, 1)
	lc := &logCapture{}
	r := New(Options{Manifests: manifests, Logf: lc.logf})
	defer r.Close()

	emit, ch := collectEmissions()
	info := r.Dispatch(context.Background(), "!echo hi", 2, testRunningApps(), emit)
	require.True(t, info.Targeted)
	e := recvWithin(t, ch, 5*time.Second)
	require.Len(t, e.Results, 1)
	echoed := e.Results[0].Subtitle
	require.Contains(t, echoed, `"query":"!echo hi"`)
	require.Contains(t, echoed, `"stripped":"hi"`)
	require.NotContains(t, echoed, `"context"`, "undeclared context must be absent from the request")
	require.NotContains(t, echoed, "firefox")
}

func TestExampleColorHTTPEndToEnd(t *testing.T) {
	srv := httptest.NewServer(colorhttp.Handler())
	defer srv.Close()

	// The shipped manifest pins the documented localhost URL; rewrite
	// just the URL to point at the test server.
	root := t.TempDir()
	writePluginDir(t, root, "color", fmt.Sprintf(`{
        "v": 1,
        "id": "color",
        "name": "Color preview",
        "type": "http",
        "trigger": { "prefix": "#" },
        "bangs": ["color"],
        "timeout_ms": 1500,
        "http": { "url": %q }
    }`, srv.URL))
	manifests, errs := LoadDir(root)
	require.Empty(t, errs)
	require.Len(t, manifests, 1)
	lc := &logCapture{}
	r := New(Options{Manifests: manifests, Logf: lc.logf})
	defer r.Close()

	t.Run("hex color yields a swatch", func(t *testing.T) {
		emit, ch := collectEmissions()
		info := r.Dispatch(context.Background(), "#ff8800", 11, nil, emit)
		require.Equal(t, TargetInfo{}, info)
		e := recvWithin(t, ch, 5*time.Second)
		require.Equal(t, Emission{
			Plugin: "color",
			Name:   "Color preview",
			Gen:    11,
			Results: []Result{{
				Title:       "#ff8800",
				Subtitle:    "rgb(255, 136, 0)",
				Icon:        "star",
				Badge:       "COLOR",
				AccentColor: "#ff8800",
				Score:       fptr(100),
				Fields: []Field{
					{Label: "R", Value: "255"},
					{Label: "G", Value: "136"},
					{Label: "B", Value: "0"},
					{Label: "H", Value: "32"},
					{Label: "S", Value: "100"},
					{Label: "L", Value: "50"},
				},
				Action: &Action{Type: ActionCopyText, Value: "#ff8800"},
			}},
		}, e)
	})

	t.Run("non-color is silent", func(t *testing.T) {
		emit, ch := collectEmissions()
		r.Dispatch(context.Background(), "#zz", 12, nil, emit)
		requireNoEmission(t, ch, 1*time.Second) // the handler answers an empty result list
	})
}

// TestExampleTimeoutKillsSlowPlugin pins the pipeline's robustness
// against a hung example-style plugin: minimum timeout, a script that
// sleeps far past it, no emission, prompt return.
func TestExampleTimeoutKillsSlowPlugin(t *testing.T) {
	requirePython3(t)
	root := t.TempDir()
	dir := writePluginDir(t, root, "sleepy", `{
        "v": 1,
        "id": "sleepy",
        "type": "command",
        "trigger": { "prefix": "=" },
        "timeout_ms": 100,
        "command": { "argv": ["python3", "slow.py"] }
    }`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "slow.py"),
		[]byte("import time\ntime.sleep(10)\n"), 0o644))
	manifests, errs := LoadDir(root)
	require.Empty(t, errs)
	lc := &logCapture{}
	r := New(Options{Manifests: manifests, Logf: lc.logf})
	defer r.Close()

	emit, ch := collectEmissions()
	start := time.Now()
	r.Dispatch(context.Background(), "=2+2", 1, nil, emit)
	require.Less(t, time.Since(start), 2*time.Second, "Dispatch never blocks on providers")
	requireNoEmission(t, ch, 1500*time.Millisecond)
	require.Contains(t, lc.joined(), "plugin sleepy:", "the timeout kill is logged")
}
