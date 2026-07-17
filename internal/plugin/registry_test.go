package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeProvider is a scriptable in-package provider for dispatch tests.
type fakeProvider struct {
	pid     string
	name    string
	bangs   []string
	deb     time.Duration
	matchFn func(query string, focused *AppInfo) (string, int, bool)
	queryFn func(ctx context.Context, req Request) ([]Result, []string, error)

	calls   atomic.Int32
	lastReq atomic.Pointer[Request]
}

func (f *fakeProvider) id() string { return f.pid }
func (f *fakeProvider) displayName() string {
	if f.name != "" {
		return f.name
	}
	return f.pid
}
func (f *fakeProvider) bangNames() []string     { return f.bangs }
func (f *fakeProvider) debounce() time.Duration { return f.deb }
func (f *fakeProvider) match(query string, focused *AppInfo) (string, int, bool) {
	if f.matchFn == nil {
		return "", 0, false
	}
	return f.matchFn(query, focused)
}
func (f *fakeProvider) query(ctx context.Context, req Request) ([]Result, []string, error) {
	f.calls.Add(1)
	f.lastReq.Store(&req)
	if f.queryFn == nil {
		return nil, nil, nil
	}
	return f.queryFn(ctx, req)
}

// matchAll accepts every query with no boost.
func matchAll(query string, _ *AppInfo) (string, int, bool) {
	return strings.TrimSpace(query), 0, true
}

// answer returns a queryFn producing one fixed-title result.
func answer(title string, delay time.Duration) func(context.Context, Request) ([]Result, []string, error) {
	return func(context.Context, Request) ([]Result, []string, error) {
		if delay > 0 {
			time.Sleep(delay)
		}
		return []Result{{Title: title, Score: fptr(50)}}, nil, nil
	}
}

// logCapture is a goroutine-safe log sink.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logCapture) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func (l *logCapture) joined() string { return strings.Join(l.all(), "\n") }

// newTestRegistry assembles a registry directly from fake providers
// (bypassing New, which only knows manifests and builtins). Throttle
// window 0 = every log line captured.
func newTestRegistry(t *testing.T, sigils []string, aliases map[string]string, providers ...provider) (*Registry, *logCapture) {
	t.Helper()
	lc := &logCapture{}
	r := &Registry{
		byID:     map[string]provider{},
		bangs:    NewBangSet(sigils, aliases),
		logf:     lc.logf,
		throttle: newLogThrottle(lc.logf, 0, nil),
	}
	for _, p := range providers {
		r.register(p)
	}
	return r, lc
}

// collectEmissions returns an emit func plus the channel it feeds.
func collectEmissions() (func(Emission), chan Emission) {
	ch := make(chan Emission, 16)
	return func(e Emission) { ch <- e }, ch
}

// recvEmission waits for one emission or fails the test.
func recvEmission(t *testing.T, ch <-chan Emission) Emission {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an emission")
		return Emission{}
	}
}

// requireNoEmission asserts nothing arrives within the window.
func requireNoEmission(t *testing.T, ch <-chan Emission, within time.Duration) {
	t.Helper()
	select {
	case e := <-ch:
		t.Fatalf("unexpected emission from %q", e.Plugin)
	case <-time.After(within):
	}
}

func TestProviderTimeout(t *testing.T) {
	ext := &externalProvider{m: &Manifest{TimeoutMS: 250}}
	require.Equal(t, 250*time.Millisecond, providerTimeout(ext))
	zero := &externalProvider{m: &Manifest{}}
	require.Equal(t, time.Duration(defaultTimeoutMS)*time.Millisecond, providerTimeout(zero))
	require.Equal(t, builtinTimeout, providerTimeout(&fakeProvider{pid: "b"}))
}

func TestClampDebounce(t *testing.T) {
	require.Equal(t, time.Duration(0), clampDebounce(-5))
	require.Equal(t, time.Duration(0), clampDebounce(0))
	require.Equal(t, 250*time.Millisecond, clampDebounce(250))
	require.Equal(t, maxDebounce, clampDebounce(2001))
	require.Equal(t, maxDebounce, clampDebounce(1<<30))
}

func TestApplyBoost(t *testing.T) {
	rs := []Result{{Title: "a", Score: fptr(95)}, {Title: "b"}}
	applyBoost(rs, 0)
	require.Nil(t, rs[1].Score, "boost 0 leaves results untouched")
	applyBoost(rs, 10)
	require.Equal(t, float64(100), *rs[0].Score)
	require.Equal(t, float64(60), *rs[1].Score)
}

func TestFilterContext(t *testing.T) {
	full := &RequestContext{
		FocusedApp:    &AppInfo{Name: "firefox"},
		RunningApps:   []AppInfo{{Name: "a"}},
		InstalledApps: []InstalledApp{{Name: "b"}},
	}
	tests := []struct {
		name     string
		rc       *RequestContext
		declared []string
		want     *RequestContext
	}{
		{name: "nil snapshot", rc: nil, declared: []string{"focused"}, want: nil},
		{name: "nothing declared", rc: full, declared: nil, want: nil},
		{name: "focused only", rc: full, declared: []string{"focused"},
			want: &RequestContext{FocusedApp: full.FocusedApp}},
		{name: "running and installed", rc: full, declared: []string{"running", "installed"},
			want: &RequestContext{RunningApps: full.RunningApps, InstalledApps: full.InstalledApps}},
		{name: "declared but empty snapshot", rc: &RequestContext{}, declared: []string{"focused", "running", "installed"}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, filterContext(tt.rc, tt.declared))
		})
	}
}

// fakeTransport scripts transport responses for externalProvider tests.
type fakeTransport struct {
	fn      func(ctx context.Context, req Request) (*Response, error)
	lastReq atomic.Pointer[Request]
}

func (f *fakeTransport) roundTrip(ctx context.Context, req Request) (*Response, error) {
	f.lastReq.Store(&req)
	if f.fn == nil {
		return &Response{V: 1}, nil
	}
	return f.fn(ctx, req)
}

func TestExternalProviderFillsSettingsAndFiltersContext(t *testing.T) {
	tr := &fakeTransport{}
	p := &externalProvider{
		m:        &Manifest{ID: "x", Name: "X", Context: []string{"focused"}, TimeoutMS: 1000},
		tr:       tr,
		settings: json.RawMessage(`{"unit":"c"}`),
	}
	req := baseRequest("q", "q", 3, &RequestContext{
		FocusedApp:    &AppInfo{Name: "firefox"},
		RunningApps:   []AppInfo{{Name: "r"}},
		InstalledApps: []InstalledApp{{Name: "i"}},
	})
	_, _, err := p.query(context.Background(), req)
	require.NoError(t, err)
	sent := tr.lastReq.Load()
	require.Equal(t, json.RawMessage(`{"unit":"c"}`), sent.Settings)
	require.NotNil(t, sent.Context)
	require.Equal(t, "firefox", sent.Context.FocusedApp.Name)
	require.Nil(t, sent.Context.RunningApps, "undeclared parts filtered out")
	require.Nil(t, sent.Context.InstalledApps)
}

func TestExternalProviderUndeclaredContextOmitted(t *testing.T) {
	tr := &fakeTransport{}
	p := &externalProvider{m: &Manifest{ID: "x", TimeoutMS: 1000}, tr: tr, settings: json.RawMessage("{}")}
	req := baseRequest("q", "q", 1, &RequestContext{FocusedApp: &AppInfo{Name: "f"}})
	_, _, err := p.query(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, tr.lastReq.Load().Context)
}

func TestExternalProviderSanitizesResponse(t *testing.T) {
	tr := &fakeTransport{fn: func(_ context.Context, _ Request) (*Response, error) {
		return &Response{Results: []Result{
			{Title: "keep", Action: &Action{Type: ActionSetQuery, Value: "internal"}},
			{Title: ""}, // dropped
			{Title: "forbidden", Action: &Action{Type: ActionRunCommand, Argv: []string{"rm"}}},
		}}, nil
	}}
	p := &externalProvider{m: &Manifest{ID: "x", TimeoutMS: 1000}, tr: tr, settings: json.RawMessage("{}")}
	results, dropped, err := p.query(context.Background(), baseRequest("q", "q", 1, nil))
	require.NoError(t, err)
	require.Len(t, results, 1, "empty title and unpermitted run_command dropped")
	require.Equal(t, "keep", results[0].Title)
	require.Nil(t, results[0].Action, "internal-only action stripped from external plugins")
	require.NotEmpty(t, dropped)
}

func TestExternalProviderTransportError(t *testing.T) {
	tr := &fakeTransport{fn: func(context.Context, Request) (*Response, error) {
		return nil, errors.New("connection refused")
	}}
	p := &externalProvider{m: &Manifest{ID: "x", TimeoutMS: 1000}, tr: tr, settings: json.RawMessage("{}")}
	_, _, err := p.query(context.Background(), baseRequest("q", "q", 1, nil))
	require.ErrorContains(t, err, "connection refused")
}

func TestExternalProviderMatchAndDebounce(t *testing.T) {
	trig := &Trigger{Prefix: "=", DebounceMS: 5000}
	require.NoError(t, trig.Compile())
	p := &externalProvider{m: &Manifest{ID: "x", Trigger: trig}}
	stripped, boost, ok := p.match("=2+2", nil)
	require.True(t, ok)
	require.Equal(t, "2+2", stripped)
	require.Zero(t, boost)
	require.Equal(t, maxDebounce, p.debounce(), "unclamped manifest debounce clamped here")

	bare := &externalProvider{m: &Manifest{ID: "y"}} // no trigger
	_, _, ok = bare.match("anything", nil)
	require.False(t, ok, "trigger-less plugins never match non-targeted queries")
	require.Zero(t, bare.debounce())
}

// manifestFor builds a minimal valid command manifest for New tests.
func manifestFor(id string, bangs ...string) *Manifest {
	return &Manifest{
		ID:        id,
		Name:      strings.ToUpper(id),
		Type:      TypeCommand,
		Bangs:     bangs,
		TimeoutMS: 1000,
		Command:   &CommandSpec{Argv: []string{"true"}},
	}
}

func TestNewBuildsProvidersAndCollectsErrors(t *testing.T) {
	m1 := manifestFor("alpha", "alpha")
	m2 := manifestFor("beta", "beta", "alpha") // duplicate bang
	loadErr := errors.New("plugins/broken/manifest.json: parsing: bad")
	r := New(Options{
		Manifests:  []*Manifest{m1, m2},
		LoadErrors: []error{loadErr},
		Sigils:     []string{"!!"}, // invalid: recorded, defaults apply
		Logf:       func(string, ...any) {},
	})
	defer r.Close()

	require.Contains(t, r.byID, "alpha")
	require.Contains(t, r.byID, "beta")
	require.Len(t, r.providers, 4, "two manifests + the app and apps builtins")

	joined := errors.Join(r.Errors()...).Error()
	require.Contains(t, joined, "plugins/broken/manifest.json")
	require.Contains(t, joined, "sigil")
	require.Contains(t, joined, `bang "alpha" already registered`)

	pid, bang, ok := r.bangs.Resolve("alpha")
	require.True(t, ok)
	require.Equal(t, "alpha", pid, "first registration wins")
	require.Equal(t, "alpha", bang)
}

func TestNewSkipsDisabledProviders(t *testing.T) {
	r := New(Options{
		Manifests: []*Manifest{manifestFor("alpha", "alpha"), manifestFor("beta", "beta")},
		Entries:   map[string]Entry{"alpha": {Disabled: true}},
		Logf:      func(string, ...any) {},
	})
	defer r.Close()
	require.NotContains(t, r.byID, "alpha")
	require.Contains(t, r.byID, "beta")
	_, _, ok := r.bangs.Resolve("alpha")
	require.False(t, ok, "disabled plugins register no bangs")
}

func TestNewAllDisabledSkipsEverything(t *testing.T) {
	loadErr := errors.New("still surfaced")
	r := New(Options{
		Manifests:   []*Manifest{manifestFor("alpha", "alpha")},
		LoadErrors:  []error{loadErr},
		AllDisabled: true,
		Logf:        func(string, ...any) {},
	})
	defer r.Close()
	require.Empty(t, r.providers)
	require.Nil(t, r.suggest)
	require.Equal(t, []error{loadErr}, r.Errors())
}

func TestNewSettingsPassThrough(t *testing.T) {
	r := New(Options{
		Manifests: []*Manifest{manifestFor("alpha"), manifestFor("beta")},
		Entries: map[string]Entry{
			"alpha": {Settings: json.RawMessage(`{"a":1}`)},
		},
		Logf: func(string, ...any) {},
	})
	defer r.Close()
	require.Equal(t, json.RawMessage(`{"a":1}`), r.byID["alpha"].(*externalProvider).settings)
	require.Equal(t, json.RawMessage(`{}`), r.byID["beta"].(*externalProvider).settings,
		"absent settings become the empty object")
}

func TestNewDuplicateManifestIDSkipped(t *testing.T) {
	r := New(Options{
		Manifests: []*Manifest{manifestFor("alpha"), manifestFor("alpha")},
		Logf:      func(string, ...any) {},
	})
	defer r.Close()
	require.Len(t, r.providers, 3, "one manifest survives beside the two builtins")
	require.ErrorContains(t, errors.Join(r.Errors()...), "already taken")
}

func TestNewSharedHTTPClientAndClose(t *testing.T) {
	h1 := httpManifest("http://127.0.0.1:1/q", nil)
	h2 := httpManifest("http://127.0.0.1:2/q", nil)
	h2.ID = "web2"
	r := New(Options{Manifests: []*Manifest{h1, h2}, Logf: func(string, ...any) {}})
	c1 := r.byID["web"].(*externalProvider).tr.(*httpTransport).client
	c2 := r.byID["web2"].(*externalProvider).tr.(*httpTransport).client
	require.Same(t, c1, c2, "one shared client per registry")
	require.Same(t, r.client, c1)
	r.Close() // releases idle connections; must not panic

	cmdOnly := New(Options{Manifests: []*Manifest{manifestFor("alpha")}, Logf: func(string, ...any) {}})
	require.Nil(t, cmdOnly.client, "no HTTP plugins: no client")
	cmdOnly.Close()
}

func TestNewUnknownTypeRecorded(t *testing.T) {
	m := &Manifest{ID: "weird", Type: "carrier-pigeon"}
	r := New(Options{Manifests: []*Manifest{m}, Logf: func(string, ...any) {}})
	defer r.Close()
	require.NotContains(t, r.byID, "weird")
	require.ErrorContains(t, errors.Join(r.Errors()...), "unknown type")
}

func TestNewRegistersBuiltins(t *testing.T) {
	r := New(Options{Logf: func(string, ...any) {}})
	defer r.Close()
	require.NotNil(t, r.suggest)
	require.Contains(t, r.byID, "bangs")
	require.Contains(t, r.byID, "app")
	require.Contains(t, r.byID, "apps")
	require.Len(t, r.providers, 2, "the suggestions provider stays out of the normal fan-out")
	require.Empty(t, r.Errors())

	pid, bang, ok := r.bangs.Resolve("launch")
	require.True(t, ok)
	require.Equal(t, "apps", pid)
	require.Equal(t, "launch", bang)
	pid, _, ok = r.bangs.Resolve("quit")
	require.True(t, ok)
	require.Equal(t, "app", pid)
}

func TestNewRegistersOpenWindowsOnlyWithSeam(t *testing.T) {
	// Nil seam (a session that cannot enumerate windows): the provider
	// does not exist at all -- not even as a reserved id.
	r := New(Options{Logf: func(string, ...any) {}})
	defer r.Close()
	require.NotContains(t, r.byID, "windows")

	// Non-nil seam: registered into the normal fan-out.
	r2 := New(Options{
		OpenWindows: func() []WindowInfo { return nil },
		Logf:        func(string, ...any) {},
	})
	defer r2.Close()
	require.Contains(t, r2.byID, "windows")
	require.IsType(t, &windowsProvider{}, r2.byID["windows"])
	require.Empty(t, r2.byID["windows"].bangNames(), "no bangs to claim")

	// The standard per-entry disable knob kills it despite the seam.
	r3 := New(Options{
		OpenWindows: func() []WindowInfo { return nil },
		Entries:     map[string]Entry{"windows": {Disabled: true}},
		Logf:        func(string, ...any) {},
	})
	defer r3.Close()
	require.NotContains(t, r3.byID, "windows")
}

func TestDispatchOpenWindowsEmitsUnsanitized(t *testing.T) {
	getter := func() []WindowInfo {
		return []WindowInfo{{ID: "42", Title: "Mozilla Firefox", App: "firefox", PID: 9}}
	}
	r, _ := newTestRegistry(t, nil, nil, newWindowsProvider(getter))
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "fire", 5, nil, emit)
	require.Equal(t, TargetInfo{}, info, "all-queries matching is not bang targeting")

	e := recvEmission(t, ch)
	require.Equal(t, "windows", e.Plugin)
	require.Equal(t, "Open Windows", e.Name)
	require.EqualValues(t, 5, e.Gen)
	require.Len(t, e.Results, 1)
	require.Equal(t, &Action{Type: ActionActivateWindow, Window: "42"}, e.Results[0].Action,
		"builtins bypass the sanitizer, so the internal-only action survives")
}

func TestDispatchOpenWindowsSkipsShortAndTargetedQueries(t *testing.T) {
	var calls atomic.Int32
	getter := func() []WindowInfo {
		calls.Add(1)
		return []WindowInfo{{ID: "1", Title: "xy", App: "xy"}}
	}
	other := &fakeProvider{pid: "other", bangs: []string{"other"}, queryFn: answer("ok", 0)}
	r, _ := newTestRegistry(t, nil, nil, newWindowsProvider(getter), other)
	emit, ch := collectEmissions()

	// One rune: below the all-queries minimum, nothing dispatched.
	r.Dispatch(context.Background(), "x", 1, nil, emit)
	requireNoEmission(t, ch, 100*time.Millisecond)
	require.Zero(t, calls.Load())

	// A bang-targeted query goes ONLY to its provider.
	r.Dispatch(context.Background(), "!other xy", 2, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "other", e.Plugin)
	requireNoEmission(t, ch, 100*time.Millisecond)
	require.Zero(t, calls.Load(), "bang targeting bypasses the windows provider entirely")
}

func TestDispatchOpenWindowsPanickingGetterIsolated(t *testing.T) {
	angry := newWindowsProvider(func() []WindowInfo { panic("window source bug") })
	calm := &fakeProvider{pid: "calm", matchFn: matchAll, queryFn: answer("ok", 0)}
	r, lc := newTestRegistry(t, nil, nil, angry, calm)
	emit, ch := collectEmissions()

	r.Dispatch(context.Background(), "hello", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "calm", e.Plugin, "other providers are unaffected")
	require.Eventually(t, func() bool {
		return strings.Contains(lc.joined(), "panic during dispatch: window source bug")
	}, time.Second, 10*time.Millisecond)
}

func TestNewDisablesBuiltinsPerID(t *testing.T) {
	r := New(Options{
		Entries: map[string]Entry{"bangs": {Disabled: true}, "apps": {Disabled: true}},
		Logf:    func(string, ...any) {},
	})
	defer r.Close()
	require.Nil(t, r.suggest)
	require.NotContains(t, r.byID, "apps")
	require.Contains(t, r.byID, "app")
	_, _, ok := r.bangs.Resolve("launch")
	require.False(t, ok, "disabled builtins register no bangs")
}

func TestNewManifestCannotShadowBuiltin(t *testing.T) {
	evil := manifestFor("evil", "quit") // wants the builtin quit bang
	poser := manifestFor("app")         // wants a builtin id
	r := New(Options{Manifests: []*Manifest{evil, poser}, Logf: func(string, ...any) {}})
	defer r.Close()

	joined := errors.Join(r.Errors()...).Error()
	require.Contains(t, joined, `bang "quit" already registered`)
	require.Contains(t, joined, "already taken")

	pid, _, ok := r.bangs.Resolve("quit")
	require.True(t, ok)
	require.Equal(t, "app", pid, "the builtin keeps its bang")
	require.IsType(t, &appCommandProvider{}, r.byID["app"], "the builtin keeps its id")
}
