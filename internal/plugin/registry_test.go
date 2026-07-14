package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func TestDispatchFastEmissionBeatsSlow(t *testing.T) {
	slow := &fakeProvider{pid: "slow", matchFn: matchAll, queryFn: answer("s", 200*time.Millisecond)}
	fast := &fakeProvider{pid: "fast", matchFn: matchAll, queryFn: answer("f", 10*time.Millisecond)}
	r, _ := newTestRegistry(t, nil, nil, slow, fast) // slow registered first
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "hello", 7, nil, emit)
	require.Equal(t, TargetInfo{}, info)

	first := recvEmission(t, ch)
	require.Equal(t, "fast", first.Plugin)
	require.Equal(t, int64(7), first.Gen)
	require.Len(t, first.Results, 1)
	second := recvEmission(t, ch)
	require.Equal(t, "slow", second.Plugin)
}

func TestDispatchCancelledContextEmitsNothing(t *testing.T) {
	p := &fakeProvider{pid: "p", matchFn: matchAll, queryFn: answer("x", 150*time.Millisecond)}
	r, _ := newTestRegistry(t, nil, nil, p)
	emit, ch := collectEmissions()

	ctx, cancel := context.WithCancel(context.Background())
	r.Dispatch(ctx, "hello", 1, nil, emit)
	time.Sleep(20 * time.Millisecond)
	cancel()
	requireNoEmission(t, ch, 400*time.Millisecond)
	require.Equal(t, int32(1), p.calls.Load(), "query ran but its result was discarded")
}

func TestDispatchDebounceCancelledBeforeQuery(t *testing.T) {
	p := &fakeProvider{pid: "p", deb: 150 * time.Millisecond, matchFn: matchAll, queryFn: answer("x", 0)}
	r, _ := newTestRegistry(t, nil, nil, p)
	emit, ch := collectEmissions()

	ctx, cancel := context.WithCancel(context.Background())
	r.Dispatch(ctx, "hello", 1, nil, emit)
	time.Sleep(20 * time.Millisecond)
	cancel()
	requireNoEmission(t, ch, 300*time.Millisecond)
	require.Equal(t, int32(0), p.calls.Load(), "cancelled during debounce: transport never called")
}

func TestDispatchDebounceElapsesThenQueries(t *testing.T) {
	p := &fakeProvider{pid: "p", deb: 30 * time.Millisecond, matchFn: matchAll, queryFn: answer("x", 0)}
	r, _ := newTestRegistry(t, nil, nil, p)
	emit, ch := collectEmissions()

	start := time.Now()
	r.Dispatch(context.Background(), "hello", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "p", e.Plugin)
	require.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)
}

func TestDispatchErrorIsolation(t *testing.T) {
	bad := &fakeProvider{pid: "bad", matchFn: matchAll, queryFn: func(context.Context, Request) ([]Result, []string, error) {
		return nil, nil, errors.New("kaboom")
	}}
	good := &fakeProvider{pid: "good", matchFn: matchAll, queryFn: answer("ok", 0)}
	r, lc := newTestRegistry(t, nil, nil, bad, good)
	emit, ch := collectEmissions()

	r.Dispatch(context.Background(), "hello", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "good", e.Plugin)
	requireNoEmission(t, ch, 100*time.Millisecond)
	require.Eventually(t, func() bool {
		return strings.Contains(lc.joined(), "plugin bad: kaboom")
	}, time.Second, 10*time.Millisecond)
}

func TestDispatchPanicRecovered(t *testing.T) {
	angry := &fakeProvider{pid: "angry", matchFn: matchAll, queryFn: func(context.Context, Request) ([]Result, []string, error) {
		panic("plugin bug")
	}}
	calm := &fakeProvider{pid: "calm", matchFn: matchAll, queryFn: answer("ok", 0)}
	r, lc := newTestRegistry(t, nil, nil, angry, calm)
	emit, ch := collectEmissions()

	r.Dispatch(context.Background(), "hello", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, "calm", e.Plugin)
	require.Eventually(t, func() bool {
		return strings.Contains(lc.joined(), "panic during dispatch: plugin bug")
	}, time.Second, 10*time.Millisecond)
}

func TestDispatchDroppedReasonsLogged(t *testing.T) {
	p := &fakeProvider{pid: "p", matchFn: matchAll, queryFn: func(context.Context, Request) ([]Result, []string, error) {
		return []Result{{Title: "ok", Score: fptr(50)}}, []string{"result 1: empty title; dropped"}, nil
	}}
	r, lc := newTestRegistry(t, nil, nil, p)
	emit, ch := collectEmissions()
	r.Dispatch(context.Background(), "hello", 1, nil, emit)
	recvEmission(t, ch)
	require.Eventually(t, func() bool {
		return strings.Contains(lc.joined(), "sanitizer: result 1: empty title; dropped")
	}, time.Second, 10*time.Millisecond)
}

func TestDispatchNoMatchNotDispatched(t *testing.T) {
	p := &fakeProvider{pid: "p", queryFn: answer("x", 0)} // matchFn nil: never matches
	r, _ := newTestRegistry(t, nil, nil, p)
	emit, ch := collectEmissions()
	r.Dispatch(context.Background(), "hello", 1, nil, emit)
	requireNoEmission(t, ch, 100*time.Millisecond)
	require.Equal(t, int32(0), p.calls.Load())
}

func TestDispatchEmptyQueryDispatchesNothing(t *testing.T) {
	p := &fakeProvider{pid: "p", matchFn: matchAll, queryFn: answer("x", 0)}
	r, _ := newTestRegistry(t, nil, nil, p)
	emit, ch := collectEmissions()
	for _, q := range []string{"", "   ", "\t"} {
		info := r.Dispatch(context.Background(), q, 1, nil, emit)
		require.Equal(t, TargetInfo{}, info)
	}
	requireNoEmission(t, ch, 100*time.Millisecond)
	require.Equal(t, int32(0), p.calls.Load())
}

func TestDispatchTargetedBypassesGatingAndIsolatesTarget(t *testing.T) {
	// "one" never matches normally: targeting must bypass match().
	one := &fakeProvider{pid: "one", name: "One", bangs: []string{"one"}, queryFn: answer("1", 0)}
	two := &fakeProvider{pid: "two", name: "Two", bangs: []string{"two"}, matchFn: matchAll, queryFn: answer("2", 0)}
	r, _ := newTestRegistry(t, nil, nil, one, two)
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "!one  2+2 ", 9, nil, emit)
	require.Equal(t, TargetInfo{Targeted: true, Plugin: "one", Name: "One", Bang: "one"}, info)

	e := recvEmission(t, ch)
	require.Equal(t, "one", e.Plugin)
	requireNoEmission(t, ch, 100*time.Millisecond)
	require.Equal(t, int32(0), two.calls.Load(), "only the target runs")

	req := one.lastReq.Load()
	require.True(t, req.Targeted)
	require.Equal(t, "one", req.Bang)
	require.Equal(t, "!one  2+2 ", req.Query)
	require.Equal(t, "2+2", req.Stripped, "rest is trimmed")
	require.Equal(t, int64(9), req.Gen)
}

func TestDispatchTargetedViaAliasAndPrefix(t *testing.T) {
	calc := &fakeProvider{pid: "calc-plugin", name: "Calculator", bangs: []string{"calc"}, queryFn: answer("4", 0)}
	r, _ := newTestRegistry(t, nil, map[string]string{"kalk": "calc"}, calc)

	t.Run("alias", func(t *testing.T) {
		emit, ch := collectEmissions()
		info := r.Dispatch(context.Background(), "!kalk 2+2", 1, nil, emit)
		require.Equal(t, TargetInfo{Targeted: true, Plugin: "calc-plugin", Name: "Calculator", Bang: "calc"}, info)
		require.Equal(t, "calc-plugin", recvEmission(t, ch).Plugin)
	})
	t.Run("unique prefix", func(t *testing.T) {
		emit, ch := collectEmissions()
		info := r.Dispatch(context.Background(), "!ca 2+2", 1, nil, emit)
		require.True(t, info.Targeted)
		require.Equal(t, "calc", info.Bang, "canonical bang, not the typed prefix")
		e := recvEmission(t, ch)
		require.Equal(t, "calc-plugin", e.Plugin)
		req := calc.lastReq.Load()
		require.Equal(t, "calc", req.Bang)
	})
}

// suggestFake builds a stand-in for the builtin suggestions provider.
func suggestFake() *fakeProvider {
	return &fakeProvider{pid: "bangs", name: "Commands", queryFn: answer("suggestion", 0)}
}

func TestDispatchBangShapedQueriesRouteToSuggestions(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "bare sigil", query: "!"},
		{name: "partial name", query: "!cal"},
		{name: "ambiguous with rest", query: "!c 2+2"},
		{name: "resolved without space", query: "!calc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calc := &fakeProvider{pid: "calc", bangs: []string{"calc", "calx"}, matchFn: matchAll, queryFn: answer("4", 0)}
			cfg := &fakeProvider{pid: "cfg", bangs: []string{"cfg"}, matchFn: matchAll, queryFn: answer("c", 0)}
			r, _ := newTestRegistry(t, nil, nil, calc, cfg)
			sg := suggestFake()
			r.suggest = sg
			emit, ch := collectEmissions()

			info := r.Dispatch(context.Background(), tt.query, 1, nil, emit)
			require.Equal(t, TargetInfo{}, info)
			e := recvEmission(t, ch)
			require.Equal(t, "bangs", e.Plugin)
			requireNoEmission(t, ch, 100*time.Millisecond)
			require.Equal(t, int32(0), calc.calls.Load(), "suggestions only: no normal dispatch")
			require.Equal(t, int32(0), cfg.calls.Load())
			req := sg.lastReq.Load()
			require.Equal(t, tt.query, req.Query)
			require.False(t, req.Targeted)
		})
	}
}

func TestDispatchSuggestionsDisabledIsSilent(t *testing.T) {
	calc := &fakeProvider{pid: "calc", bangs: []string{"calc"}, matchFn: matchAll, queryFn: answer("4", 0)}
	r, _ := newTestRegistry(t, nil, nil, calc) // r.suggest stays nil
	emit, ch := collectEmissions()
	info := r.Dispatch(context.Background(), "!ca", 1, nil, emit)
	require.Equal(t, TargetInfo{}, info)
	requireNoEmission(t, ch, 100*time.Millisecond)
	require.Equal(t, int32(0), calc.calls.Load())
}

func TestDispatchUnknownBangFallsBackToNormalPath(t *testing.T) {
	p := &fakeProvider{pid: "p", bangs: []string{"other"}, matchFn: matchAll, queryFn: answer("x", 0)}
	r, _ := newTestRegistry(t, nil, nil, p)
	r.suggest = suggestFake()
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "!zzz find me", 1, nil, emit)
	require.Equal(t, TargetInfo{}, info)
	e := recvEmission(t, ch)
	require.Equal(t, "p", e.Plugin, "no candidates: sigil text is a plain query")
	req := p.lastReq.Load()
	require.Equal(t, "!zzz find me", req.Query)
	require.False(t, req.Targeted)
}

func TestDispatchBoostAppliedAndClamped(t *testing.T) {
	p := &fakeProvider{
		pid: "p",
		matchFn: func(q string, _ *AppInfo) (string, int, bool) {
			return q, 30, true
		},
		queryFn: func(context.Context, Request) ([]Result, []string, error) {
			return []Result{
				{Title: "mid", Score: fptr(50)},
				{Title: "high", Score: fptr(90)},
				{Title: "absent"}, // nil score: default 50
			}, nil, nil
		},
	}
	r, _ := newTestRegistry(t, nil, nil, p)
	emit, ch := collectEmissions()
	r.Dispatch(context.Background(), "hello", 1, nil, emit)
	e := recvEmission(t, ch)
	require.Equal(t, float64(80), *e.Results[0].Score)
	require.Equal(t, float64(100), *e.Results[1].Score, "clamped at 100")
	require.Equal(t, float64(80), *e.Results[2].Score, "default 50 + boost")
}

func TestDispatchAppliesPerProviderTimeout(t *testing.T) {
	var hadDeadline atomic.Bool
	p := &fakeProvider{pid: "p", matchFn: matchAll, queryFn: func(ctx context.Context, _ Request) ([]Result, []string, error) {
		_, ok := ctx.Deadline()
		hadDeadline.Store(ok)
		return []Result{{Title: "x"}}, nil, nil
	}}
	r, _ := newTestRegistry(t, nil, nil, p)
	emit, ch := collectEmissions()
	r.Dispatch(context.Background(), "hello", 1, nil, emit)
	recvEmission(t, ch)
	require.True(t, hadDeadline.Load(), "query context carries the per-provider deadline")
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
	require.Len(t, r.providers, 2)

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
	require.Len(t, r.providers, 1)
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
	require.Empty(t, r.providers)
	require.ErrorContains(t, errors.Join(r.Errors()...), "unknown type")
}

// TestDispatchEndToEndCommandPlugin drives Dispatch through New with a
// real command manifest: manifest -> registry -> transport -> sanitize
// -> emission.
func TestDispatchEndToEndCommandPlugin(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	writeScript(t, dir, "run.sh", `cat > req.json
printf '%s' '{"v":1,"results":[{"title":"four","score":100,"action":{"type":"copy_text","value":"4"}}]}'
`)
	trig := &Trigger{Prefix: "="}
	require.NoError(t, trig.Compile())
	m := &Manifest{
		ID:        "calc",
		Name:      "Calculator",
		Type:      TypeCommand,
		Trigger:   trig,
		Bangs:     []string{"calc"},
		TimeoutMS: 5000,
		Command:   &CommandSpec{Argv: []string{"./run.sh"}},
		Dir:       dir,
	}
	r := New(Options{Manifests: []*Manifest{m}, Logf: t.Logf})
	defer r.Close()
	emit, ch := collectEmissions()

	info := r.Dispatch(context.Background(), "=2+2", 5, nil, emit)
	require.Equal(t, TargetInfo{}, info)
	e := recvEmission(t, ch)
	require.Equal(t, Emission{
		Plugin: "calc",
		Name:   "Calculator",
		Gen:    5,
		Results: []Result{{
			Title:  "four",
			Score:  fptr(100),
			Action: &Action{Type: ActionCopyText, Value: "4"},
		}},
	}, e)

	data, err := os.ReadFile(filepath.Join(dir, "req.json"))
	require.NoError(t, err)
	var got Request
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, "=2+2", got.Query)
	require.Equal(t, "2+2", got.Stripped)
	require.Equal(t, json.RawMessage("{}"), got.Settings)
	require.False(t, got.Targeted)
}
