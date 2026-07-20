package plugin

// Registry construction (New) and builtin-registration tests plus the
// open-windows dispatch trio, split from registry_test.go (which
// keeps the shared fakes and the provider/transport tests).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
	require.Len(t, r.providers, 5, "two manifests + the app, apps and apps-search builtins")

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
	require.Len(t, r.providers, 4, "one manifest survives beside the three fan-out builtins")
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
	require.Contains(t, r.byID, "apps-search")
	require.Len(t, r.providers, 3, "the suggestions provider stays out of the normal fan-out")
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
		Entries: map[string]Entry{
			"bangs":       {Disabled: true},
			"apps":        {Disabled: true},
			"apps-search": {Disabled: true},
		},
		Logf: func(string, ...any) {},
	})
	defer r.Close()
	require.Nil(t, r.suggest)
	require.NotContains(t, r.byID, "apps")
	require.NotContains(t, r.byID, "apps-search")
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
