package frecency

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSignalsZeroValueDegrades(t *testing.T) {
	var s Signals
	require.Equal(t, 0.0, s.Boost("/p"), "nil store boosts nothing")
	require.Equal(t, 0.0, s.CwdBoost("/p"), "no cwd boosts nothing")
	require.Nil(t, s.Recency(context.Background(), []string{"/p"}, time.Second),
		"nil probe answers nothing")
	require.InDelta(t, PenaltyNoiseDir, s.Penalty("/tmp/x/f"), 1e-9,
		"the penalty is pure and stays live")
}

func TestSignalsPassThrough(t *testing.T) {
	clock := newClock()
	store := New(filepath.Join(t.TempDir(), "frecency.json"), Options{Now: clock.Now})
	require.NoError(t, store.RecordOpen("/p"))

	mod := clock.Now().Add(-time.Hour)
	fake := newCountingLstat(map[string]time.Time{"/p": mod})
	probe := NewProbe(ProbeOptions{Lstat: fake.lstat, Now: clock.Now})

	s := Signals{Store: store, Probe: probe, Cwd: "/proj", CwdWeight: 10}
	require.Equal(t, 1.0, s.Boost("/p"))
	require.Equal(t, 0.0, s.Boost("/unknown"))
	require.InDelta(t, 10.0, s.CwdBoost("/proj/file"), 1e-9)
	require.InDelta(t, PenaltyDotDir, s.Penalty("/home/u/.config/x"), 1e-9)
	got := s.Recency(context.Background(), []string{"/p"}, time.Second)
	require.Equal(t, mod, got["/p"])
}

func TestSignalsProbeDefaultsWork(t *testing.T) {
	// The full default path: a real file through a zero-options
	// probe wrapped in Signals.
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	s := Signals{Probe: NewProbe(ProbeOptions{})}
	got := s.Recency(context.Background(), []string{path}, 5*time.Second)
	require.False(t, got[path].IsZero())
}
