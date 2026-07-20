package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
)

// pickLine is one seeded telemetry record: query "rep", two shown
// file rows, the given path picked. The literal JSON is the data
// contract the priors reader consumes.
func pickLine(ts, picked, other string) string {
	return fmt.Sprintf(`{"v":1,"ts":%q,"query":"rep","blendActive":true,"joined":true,"refined":false,`+
		`"shown":[{"rank":0,"kind":"file","path":%q,"class":1,"effClass":1,"align":0,"boost":0,"recency":0,"cwd":0,"penalty":0,"isDir":false,"depth":2,"ext":".txt"},`+
		`{"rank":1,"kind":"file","path":%q,"class":1,"effClass":1,"align":0,"boost":0,"recency":0,"cwd":0,"penalty":0,"isDir":false,"depth":2,"ext":".txt"}],`+
		`"picked":{"rank":0,"kind":"file","path":%q,"action":"open","revealed":false}}`,
		ts, picked, other, picked)
}

func seedTelemetry(t *testing.T, picks int, picked, other string) {
	t.Helper()
	dir, err := config.Dir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	var data []byte
	ts := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i < picks; i++ {
		data = append(data, pickLine(ts, picked, other)...)
		data = append(data, '\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, priorsTelemetryLog), data, 0o600))
}

// priorsFixture builds a real on-disk root with two sibling files
// (Startup's async index build swaps in a fresh walk of the roots, so
// in-memory-only entries would be wiped) and a Manager over it, with
// the entries also pre-added for the pre-build window. Returns the
// manager and the two absolute paths (one, two).
func priorsFixture(t *testing.T) (*index.Manager, string, string) {
	t.Helper()
	root := t.TempDir()
	one := filepath.Join(root, "report_one.txt")
	two := filepath.Join(root, "report_two.txt")
	require.NoError(t, os.WriteFile(one, []byte("a"), 0o600))
	require.NoError(t, os.WriteFile(two, []byte("b"), 0o600))
	m := index.NewManager([]string{root}, nil, 10)
	require.NoError(t, m.Add(root, "report_one.txt", false))
	require.NoError(t, m.Add(root, "report_two.txt", false))
	return m, one, two
}

func searchPaths(a *App, q string) []string {
	rs := a.Search(q)
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Path
	}
	return out
}

func TestStartPriorsDisabledIsInert(t *testing.T) {
	m, _, _ := priorsFixture(t)
	a, _ := newTestApp(t, m, Options{
		Frecency: config.DefaultFrecency(),
		Priors:   config.PriorsConfig{Disabled: true},
	})
	a.Startup(context.Background())
	require.False(t, a.priorsConfigured())
	b := m.Blend()
	require.NotNil(t, b, "frecency still wires its blend")
	require.Nil(t, b.Prior, "a disabled priors layer must leave the blend Prior-free")
	a.kickPriorsRefresh() // must be a silent no-op
	require.False(t, a.priorsConfigured())
}

// TestStartPriorsPinsPickedRow is the end-to-end path: seeded
// telemetry picks flip the delivered order for that exact query, and
// only for it.
func TestStartPriorsPinsPickedRow(t *testing.T) {
	m, one, two := priorsFixture(t)
	a, _ := newTestApp(t, m, Options{
		Frecency: config.DefaultFrecency(),
	})
	seedTelemetry(t, 2, two, one)
	a.Startup(context.Background())
	require.True(t, a.priorsConfigured())
	require.NotNil(t, m.Blend().Prior, "the blend must carry the priors resolver")

	// The initial refresh (and the index build) are asynchronous.
	require.Eventually(t, func() bool {
		got := searchPaths(a, "rep")
		return len(got) == 2 && got[0] == two
	}, 3*time.Second, 5*time.Millisecond, "the remembered pick must pin the row for its query")
	// A different query has no exact memory; with both rows sharing
	// extension and folder the rate nudges tie and the engine order
	// stands.
	require.Equal(t, []string{one, two}, searchPaths(a, "report_"))
}

// TestStartPriorsWithFrecencyDisabled: priors alone still activate
// the blend (a prior-only Blend), and the exact-query pin works.
func TestStartPriorsWithFrecencyDisabled(t *testing.T) {
	m, one, two := priorsFixture(t)
	a, _ := newTestApp(t, m, Options{
		Frecency: config.FrecencyConfig{Disabled: true},
	})
	seedTelemetry(t, 2, two, one)
	a.Startup(context.Background())
	require.Nil(t, a.frecencyStore(), "frecency stays off")
	b := m.Blend()
	require.NotNil(t, b, "a prior-only blend must reach the Manager")
	require.NotNil(t, b.Prior)
	require.Eventually(t, func() bool {
		got := searchPaths(a, "rep")
		return len(got) == 2 && got[0] == two
	}, 3*time.Second, 5*time.Millisecond)
}

// TestOpenKicksPriorsRefresh: a successful activation re-reads the
// sources, so picks recorded by the telemetry layer land in the
// tables without any timer.
func TestOpenKicksPriorsRefresh(t *testing.T) {
	m, one, two := priorsFixture(t)
	a, _ := newTestApp(t, m, Options{
		Frecency: config.FrecencyConfig{Disabled: true},
	})
	a.Startup(context.Background())
	require.Eventually(t, a.priorsConfigured, time.Second, 5*time.Millisecond)

	// No data yet: engine order, once the index build has settled.
	require.Eventually(t, func() bool {
		got := searchPaths(a, "rep")
		return len(got) == 2 && got[0] == one
	}, 3*time.Second, 5*time.Millisecond, "pre-data: engine order")

	// New telemetry appears (as the telemetry layer would append it),
	// then an activation succeeds: the next generation picks it up.
	seedTelemetry(t, 2, two, one)
	require.NoError(t, a.Open(two))
	require.Eventually(t, func() bool {
		got := searchPaths(a, "rep")
		return len(got) == 2 && got[0] == two
	}, 3*time.Second, 5*time.Millisecond, "the post-activation refresh must load the new picks")
}

// TestPriorsRefreshCoalesces: kicks while a rebuild runs coalesce
// into one pending re-run, and Shutdown stops re-arms.
func TestPriorsRefreshCoalesces(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	a.Startup(context.Background())
	require.Eventually(t, a.priorsConfigured, time.Second, 5*time.Millisecond)
	for i := 0; i < 20; i++ {
		a.kickPriorsRefresh()
	}
	// Drain: the busy flag must clear on its own.
	require.Eventually(t, func() bool {
		a.priorsMu.Lock()
		defer a.priorsMu.Unlock()
		return !a.priorsBusy && !a.priorsAgain
	}, 2*time.Second, 5*time.Millisecond)

	a.shutdownPriors()
	a.kickPriorsRefresh() // after close: must not re-arm
	a.priorsMu.Lock()
	defer a.priorsMu.Unlock()
	require.True(t, a.priorsClosed)
	require.False(t, a.priorsBusy)
}

// TestPriorsCorruptSourcesDegrade: a corrupt frecency.json (and a
// garbage telemetry log) log once and leave the layer running with
// whatever parsed.
func TestPriorsCorruptSourcesDegrade(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	dir, err := config.Dir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, frecencyFileName), []byte("garbage"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, priorsTelemetryLog), []byte("also garbage\n"), 0o600))
	a.Startup(context.Background())
	require.Eventually(t, func() bool {
		a.priorsMu.Lock()
		defer a.priorsMu.Unlock()
		return a.priorsStore != nil && !a.priorsBusy
	}, 2*time.Second, 5*time.Millisecond)
	a.priorsMu.Lock()
	defer a.priorsMu.Unlock()
	q, e, d := a.priorsStore.Counts()
	require.Zero(t, q+e+d, "nothing parsed, tables empty, no crash")
}

// TestApplyConfigPriorsTogglesLive: the search.priors sectionAppliers
// row -- clearing the escape hatch in a live pass builds the layer
// and installs the blend Prior; setting it drops both.
func TestApplyConfigPriorsTogglesLive(t *testing.T) {
	m, _, _ := priorsFixture(t)
	a, _ := newTestApp(t, m, Options{Frecency: config.DefaultFrecency()})
	a.frecOnce.Do(a.startFrecency)
	off := config.Default()
	off.Search.Priors.Disabled = true
	seedBaseline(a, off)
	require.False(t, a.priorsConfigured())

	next := config.Default()
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "search.priors")
	require.Empty(t, res.Errors)
	require.True(t, a.priorsConfigured(), "clearing search.priors.disabled builds the layer live")
	require.NotNil(t, m.Blend().Prior, "the live enable installs the blend resolver")

	res = a.applyConfig(&off, "test")
	require.Contains(t, res.Applied, "search.priors")
	require.False(t, a.priorsConfigured(), "the escape hatch drops the layer live")
	if b := m.Blend(); b != nil {
		require.Nil(t, b.Prior, "the resolver is detached on disable")
	}
	a.shutdownPriors()
}

// TestApplyConfigFrecencyPreservesPrior pins the #48-vs-live-apply
// interaction: a live frecency config change (either direction) must
// not drop an enabled priors layer's blend resolver -- the frecency
// applier rebuilds the SAME blend the priors resolver rides.
func TestApplyConfigFrecencyPreservesPrior(t *testing.T) {
	m, _, _ := priorsFixture(t)
	a, _ := newTestApp(t, m, Options{Frecency: config.DefaultFrecency()})
	a.frecOnce.Do(a.startFrecency)
	priorsOff := config.Default()
	priorsOff.Search.Priors.Disabled = true
	seedBaseline(a, priorsOff)

	on := config.Default()
	a.applyConfig(&on, "test")
	require.NotNil(t, m.Blend().Prior)

	// Disable frecency: the blend rebuild keeps the Prior (a
	// prior-only blend still activates).
	noFrec := on
	noFrec.Search.Frecency.Disabled = true
	res := a.applyConfig(&noFrec, "test")
	require.Contains(t, res.Applied, "search.frecency")
	require.NotNil(t, m.Blend(), "a prior-only blend stays installed")
	require.NotNil(t, m.Blend().Prior, "the priors resolver survives a frecency disable")

	// Re-enable frecency: still preserved through the rebuild.
	res = a.applyConfig(&on, "test")
	require.Contains(t, res.Applied, "search.frecency")
	require.NotNil(t, m.Blend().Prior, "the priors resolver survives a frecency rebuild")
	a.shutdownPriors()
}
