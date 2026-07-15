package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
