package watch

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A completed sweep pass asks the Rescanner for a rebuild once
// tombstones dominate the store, because removals only set a bit and
// the bytes come back only through a rebuild into a fresh store.

// churn adds n entries under dir and immediately removes them, leaving
// n tombstones behind. The names are distinct per call, so nothing is
// resurrected.
func churn(m manager, dir string, tag string, n int) {
	for k := 0; k < n; k++ {
		name := tag + "_" + itoa(k) + ".tmp"
		_ = m.Add(dir, name, false)
		m.Remove(filepath.Join(dir, name))
	}
}

// manager is the slice of index.Manager these tests drive.
type manager interface {
	Add(parentDir, name string, isDir bool) error
	Remove(path string) int
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// sweeperWithRescanCounter wires a Sweeper whose rebuild requests land
// in a counter instead of a real Rescanner.
func sweeperWithRescanCounter(t *testing.T, opt SweepOptions) (*Sweeper, *atomic.Int32, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "seed.txt"), nil, 0o644))
	m := buildManager(t, root, nil)
	w := newTestWatcher(t, m, newFakeNotifier())
	startWatcherRegistered(t, w)

	var calls atomic.Int32
	w.setRescanRequester(func() { calls.Add(1) })

	s := newTestSweeper(t, m, w, opt)
	return s, &calls, root
}

// TestSweepRequestsRebuildWhenTombstonesDominate is the leak gate: with
// most of the store tombstoned, a completed pass must ask for the
// rebuild that reclaims the bytes.
func TestSweepRequestsRebuildWhenTombstonesDominate(t *testing.T) {
	s, calls, root := sweeperWithRescanCounter(t, SweepOptions{
		Interval:          30 * time.Millisecond,
		CompactMinEntries: 100,
	})
	churn(s.mgr, root, "dead", 500)
	require.Greater(t, s.mgr.TombstoneRatio(), 0.30, "the fixture must be mostly tombstones")

	startSweeper(t, s)
	waitFor(t, func() bool { return calls.Load() > 0 },
		"a completed sweep asks for a compaction rebuild")
}

// TestSweepSkipsRebuildBelowThresholds pins both gates: a store that is
// small, or one whose tombstone fraction is low, must never trigger the
// walk.
func TestSweepSkipsRebuildBelowThresholds(t *testing.T) {
	t.Run("below min entries", func(t *testing.T) {
		s, calls, root := sweeperWithRescanCounter(t, SweepOptions{
			Interval:          20 * time.Millisecond,
			CompactMinEntries: 100_000, // far above the fixture
		})
		churn(s.mgr, root, "dead", 500)
		startSweeper(t, s)
		waitFor(t, func() bool { return s.Stats().Completed >= 2 }, "two passes complete")
		require.Zero(t, calls.Load(), "a small store must not trigger a rebuild")
	})

	t.Run("below ratio", func(t *testing.T) {
		s, calls, root := sweeperWithRescanCounter(t, SweepOptions{
			Interval:          20 * time.Millisecond,
			CompactMinEntries: 10,
		})
		// 200 live, 20 tombstoned: about 9%, under the 30% default.
		// The live entries must exist ON DISK, or the sweep reconciles
		// them away and they become tombstones themselves.
		for k := 0; k < 200; k++ {
			name := "live_" + itoa(k) + ".txt"
			require.NoError(t, os.WriteFile(filepath.Join(root, name), nil, 0o644))
			require.NoError(t, s.mgr.Add(root, name, false))
		}
		churn(s.mgr, root, "dead", 20)
		require.Less(t, s.mgr.TombstoneRatio(), 0.30)

		startSweeper(t, s)
		waitFor(t, func() bool { return s.Stats().Completed >= 2 }, "two passes complete")
		require.Zero(t, calls.Load(), "a mostly-live store must not trigger a rebuild")
	})
}

// TestSweepCompactionDisabled pins the opt-out.
func TestSweepCompactionDisabled(t *testing.T) {
	s, calls, root := sweeperWithRescanCounter(t, SweepOptions{
		Interval:          20 * time.Millisecond,
		CompactMinEntries: 10,
		CompactRatio:      -1, // disabled
	})
	churn(s.mgr, root, "dead", 500)
	startSweeper(t, s)
	waitFor(t, func() bool { return s.Stats().Completed >= 2 }, "two passes complete")
	require.Zero(t, calls.Load(), "a negative CompactRatio disables the check")
}

// TestSweepCompactionWithoutRescannerIsInert covers the Sweeper-only
// setup: no Rescanner wired means nothing to ask, and the pass must
// still complete normally.
func TestSweepCompactionWithoutRescannerIsInert(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "seed.txt"), nil, 0o644))
	m := buildManager(t, root, nil)
	w := newTestWatcher(t, m, newFakeNotifier())
	startWatcherRegistered(t, w)
	// Deliberately no setRescanRequester.

	s := newTestSweeper(t, m, w, SweepOptions{
		Interval:          20 * time.Millisecond,
		CompactMinEntries: 10,
	})
	churn(m, root, "dead", 300)
	startSweeper(t, s)
	waitFor(t, func() bool { return s.Stats().Completed >= 2 },
		"passes keep completing with no Rescanner to ask")
}
