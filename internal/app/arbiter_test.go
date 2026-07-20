package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/plugin"
)

// arbTabPickLine is one seeded telemetry record where the delivered
// list held a file row above a firefox-tabs row and the user picked
// the TAB -- the cross-source arbitration signal. The literal JSON is
// the data contract the arbiter reader consumes.
func arbTabPickLine(ts, filePath string) string {
	return fmt.Sprintf(`{"v":1,"ts":%q,"query":"rep","blendActive":true,"joined":true,"refined":false,`+
		`"shown":[{"rank":0,"kind":"file","path":%q,"class":1,"effClass":1,"align":0,"boost":0,"recency":0,"cwd":0,"penalty":0,"isDir":false,"depth":4,"ext":".txt"},`+
		`{"rank":1,"kind":"plugin","plugin":"firefox-tabs","score":85}],`+
		`"picked":{"rank":1,"kind":"plugin","plugin":"firefox-tabs","action":"open_url","revealed":false}}`,
		ts, filePath)
}

// arbExtPickLine is one seeded record where two equal-class file rows
// were shown and the user picked the .md one at rank 1 -- the
// within-file-list learning signal.
func arbExtPickLine(ts, txtPath, mdPath string) string {
	return fmt.Sprintf(`{"v":1,"ts":%q,"query":"rep","blendActive":true,"joined":true,"refined":false,`+
		`"shown":[{"rank":0,"kind":"file","path":%q,"class":1,"effClass":1,"align":0,"boost":0,"recency":0,"cwd":0,"penalty":0,"isDir":false,"depth":4,"ext":".txt"},`+
		`{"rank":1,"kind":"file","path":%q,"class":1,"effClass":1,"align":0,"boost":0,"recency":0,"cwd":0,"penalty":0,"isDir":false,"depth":4,"ext":".md"}],`+
		`"picked":{"rank":1,"kind":"file","path":%q,"action":"open","revealed":false}}`,
		ts, txtPath, mdPath, mdPath)
}

// seedArbLog writes lines to <configDir>/telemetry.jsonl (append
// with mode true).
func seedArbLog(t *testing.T, lines []string, appendTo bool) {
	t.Helper()
	dir, err := config.Dir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	flags := os.O_CREATE | os.O_WRONLY
	if appendTo {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(filepath.Join(dir, arbiterLogName), flags, 0o600)
	require.NoError(t, err)
	for _, l := range lines {
		_, err = f.WriteString(l + "\n")
		require.NoError(t, err)
	}
	require.NoError(t, f.Close())
}

func repeatLines(n int, mk func() string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = mk()
	}
	return out
}

func nowTS() string { return time.Now().UTC().Format(time.RFC3339) }

// tabsEmission builds a two-row firefox-tabs emission (engine order:
// the LOWER-scored row first, so a model reorder is observable).
func tabsEmission() plugin.Emission {
	s80, s90 := 80.0, 90.0
	return plugin.Emission{
		Plugin: "firefox-tabs",
		Name:   "Open Tabs",
		Gen:    1,
		Results: []plugin.Result{
			{Title: "low", Score: &s80},
			{Title: "high", Score: &s90},
		},
	}
}

func TestStartArbiterDisabledIsInert(t *testing.T) {
	m, _, _ := priorsFixture(t)
	a, _ := newTestApp(t, m, Options{Frecency: config.DefaultFrecency()})
	a.Startup(context.Background())
	require.False(t, a.arbiterConfigured())
	b := m.Blend()
	require.NotNil(t, b, "frecency still wires its blend")
	require.Nil(t, b.Model, "a disabled arbiter must leave the blend Model-free")

	em := tabsEmission()
	got := a.arbitrateEmission("rep", em)
	require.Equal(t, em, got, "emissions pass through untouched")
	require.Equal(t, 0, got.Priority)
	a.kickArbiterRefresh() // must be a silent no-op
	a.noteArbiterPick()
	require.False(t, a.arbiterConfigured())
}

func TestArbiterGateKeepsThinLogInert(t *testing.T) {
	m, one, two := priorsFixture(t)
	a, _ := newTestApp(t, m, Options{
		Frecency: config.DefaultFrecency(),
		Arbiter:  config.ArbiterConfig{Enabled: true},
	})
	seedArbLog(t, repeatLines(10, func() string { return arbTabPickLine(nowTS(), one) }), false)
	a.Startup(context.Background())
	require.True(t, a.arbiterConfigured())
	require.NotNil(t, m.Blend().Model, "the blend carries the resolver even while the gate is closed")

	l := a.arbLayer()
	require.Eventually(t, func() bool {
		return l.store.Current() == nil && strings.Contains(l.store.Reason(), "10 joined picks")
	}, 3*time.Second, 5*time.Millisecond, "10 picks must refuse the %d-pick gate", 200)

	// Inert = byte-identical: the delivered file order is exactly the
	// manager's own, and emissions pass through untouched.
	require.Equal(t, resultPathsApp(m.Query("rep", 0)), searchPaths(a, "rep"))
	require.Equal(t, []string{one, two}, searchPaths(a, "rep"))
	em := tabsEmission()
	require.Equal(t, em, a.arbitrateEmission("rep", em))
}

func TestArbiterActivatesAndArbitratesAcrossSources(t *testing.T) {
	m, one, _ := priorsFixture(t)
	a, _ := newTestApp(t, m, Options{
		Frecency: config.DefaultFrecency(),
		Arbiter:  config.ArbiterConfig{Enabled: true},
	})
	seedArbLog(t, repeatLines(260, func() string { return arbTabPickLine(nowTS(), one) }), false)
	a.Startup(context.Background())
	l := a.arbLayer()
	require.NotNil(t, l)
	require.Eventually(t, func() bool { return l.store.Current() != nil },
		5*time.Second, 5*time.Millisecond, "260 tab picks must pass the gate; reason: %s", l.store.Reason())
	require.Contains(t, l.store.Reason(), "active:")

	// A live query stashes its file impression for the cross-source
	// comparison...
	_ = a.Search("rep")
	require.NotNil(t, l.lookup("rep"), "an active arbiter stashes impressions")

	// ...and the tabs emission for the same query is promoted above
	// the files, its rows re-ordered by model score (the habitual
	// winner's score feature ranks the 90 row over the 80 row).
	got := a.arbitrateEmission("rep", tabsEmission())
	require.Equal(t, 1, got.Priority, "the habitually picked tabs section floats above the files")
	require.Equal(t, []string{"high", "low"}, emissionTitles(got))

	// A query with no stashed impression re-orders rows but never
	// touches placement (nothing to compare against)...
	other := a.arbitrateEmission("unseen-query", tabsEmission())
	require.Equal(t, 0, other.Priority)
	require.Equal(t, []string{"high", "low"}, emissionTitles(other))

	// ...and an already-prioritized section keeps its own priority.
	apps := tabsEmission()
	apps.Plugin = "apps-search"
	apps.Priority = 1
	require.Equal(t, 1, a.arbitrateEmission("rep", apps).Priority)

	// An empty emission passes through.
	empty := plugin.Emission{Plugin: "firefox-tabs", Results: []plugin.Result{}}
	require.Equal(t, empty, a.arbitrateEmission("rep", empty))
}

func TestArbiterFileSeamPrefersLearnedExtension(t *testing.T) {
	// The within-file seam end to end: the engine's deterministic
	// order puts report_a.txt first (equal class, equal length,
	// lexicographic); 260 logged .md picks flip the delivered order
	// for the same query shape once the model activates -- a nudge
	// WITHIN the prefix class, never an inversion of a better class.
	root := t.TempDir()
	txt := filepath.Join(root, "report_a.txt")
	md := filepath.Join(root, "report_bb.md")
	require.NoError(t, os.WriteFile(txt, []byte("a"), 0o600))
	require.NoError(t, os.WriteFile(md, []byte("b"), 0o600))
	m := index.NewManager([]string{root}, nil, 10)
	require.NoError(t, m.Add(root, "report_a.txt", false))
	require.NoError(t, m.Add(root, "report_bb.md", false))

	a, _ := newTestApp(t, m, Options{
		Frecency: config.DefaultFrecency(),
		Arbiter:  config.ArbiterConfig{Enabled: true},
	})
	seedArbLog(t, repeatLines(260, func() string { return arbExtPickLine(nowTS(), txt, md) }), false)
	require.Equal(t, []string{txt, md}, searchPaths(a, "rep"), "pre-arbiter engine order")
	a.Startup(context.Background())
	l := a.arbLayer()
	require.Eventually(t, func() bool { return l.store.Current() != nil },
		5*time.Second, 5*time.Millisecond, "reason: %s", l.store.Reason())
	require.Eventually(t, func() bool {
		got := searchPaths(a, "rep")
		return len(got) == 2 && got[0] == md
	}, 3*time.Second, 5*time.Millisecond, "the learned extension preference must lift the .md row")
}

func TestApplyArbiterLiveToggle(t *testing.T) {
	m, one, _ := priorsFixture(t)
	a, _ := newTestApp(t, m, Options{Frecency: config.DefaultFrecency()})
	seedArbLog(t, repeatLines(10, func() string { return arbTabPickLine(nowTS(), one) }), false)
	a.Startup(context.Background())
	require.False(t, a.arbiterConfigured())
	seedBaseline(a, config.Default())

	next := config.Default()
	next.Search.Arbiter.Enabled = true
	res := a.applyConfig(&next, "test")
	require.Contains(t, res.Applied, "search.arbiter")
	require.Empty(t, res.Errors)
	require.True(t, a.arbiterConfigured(), "enable brings the layer up live")
	require.NotNil(t, m.Blend().Model)

	off := config.Default()
	res = a.applyConfig(&off, "test")
	require.Contains(t, res.Applied, "search.arbiter")
	require.False(t, a.arbiterConfigured(), "disable swaps the layer out live")
	if b := m.Blend(); b != nil {
		require.Nil(t, b.Model, "disable detaches the blend resolver")
	}
	em := tabsEmission()
	require.Equal(t, em, a.arbitrateEmission("rep", em), "disabled = untouched emissions")
}

func TestNoteArbiterPickKicksRetrain(t *testing.T) {
	m, one, _ := priorsFixture(t)
	a, _ := newTestApp(t, m, Options{Arbiter: config.ArbiterConfig{Enabled: true}})
	seedArbLog(t, repeatLines(10, func() string { return arbTabPickLine(nowTS(), one) }), false)
	a.Startup(context.Background())
	l := a.arbLayer()
	require.Eventually(t, func() bool { return strings.Contains(l.store.Reason(), "10 joined picks") },
		3*time.Second, 5*time.Millisecond)

	// 250 more picks land in the log; the every-N counter retrains
	// and the gate opens.
	seedArbLog(t, repeatLines(250, func() string { return arbTabPickLine(nowTS(), one) }), true)
	for i := 0; i < 50; i++ {
		a.noteArbiterPick()
	}
	require.Eventually(t, func() bool { return l.store.Current() != nil },
		5*time.Second, 5*time.Millisecond, "the 50th appended pick must trigger a retrain; reason: %s", l.store.Reason())
}

func TestArbiterReadErrorDegrades(t *testing.T) {
	// telemetry.jsonl as a DIRECTORY is the real-IO-error shape: the
	// refresh logs once and trains on nothing (inert, 0 picks).
	a, _ := newTestApp(t, nil, Options{Arbiter: config.ArbiterConfig{Enabled: true}})
	dir, err := config.Dir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, arbiterLogName), 0o755))
	a.Startup(context.Background())
	l := a.arbLayer()
	require.NotNil(t, l)
	require.Eventually(t, func() bool { return strings.Contains(l.store.Reason(), "0 joined picks") },
		3*time.Second, 5*time.Millisecond)
}

// resultPathsApp mirrors internal/index's test helper for app tests.
func resultPathsApp(res []index.Result) []string {
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = r.Path
	}
	return out
}

func emissionTitles(em plugin.Emission) []string {
	out := make([]string, len(em.Results))
	for i, r := range em.Results {
		out[i] = r.Title
	}
	return out
}
