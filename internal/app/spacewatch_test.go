package app

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// armSpaceWatch installs a recording watchSpaceChanges seam and runs
// Startup, returning the captured onChange callback.
func armSpaceWatch(t *testing.T, a *App, r *seamRecorder) func() {
	t.Helper()
	var onChange func()
	a.plat.watchSpaceChanges = func(cb func()) bool {
		r.call("watchSpaceChanges")
		onChange = cb
		return true
	}
	a.Startup(context.Background())
	require.NotNil(t, onChange, "Startup arms the observer through the seam")
	return onChange
}

func TestSpaceChangeDismissesVisibleBar(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	onChange := armSpaceWatch(t, a, r)
	a.DomReady(context.Background())
	a.showOnCursorDisplay()
	a.mu.Lock()
	require.True(t, a.visible)
	a.mu.Unlock()

	before := a.plat.now()
	onChange()

	require.True(t, r.has("hide"), "the EXISTING Hide path ran (not a bare flag flip)")
	a.mu.Lock()
	defer a.mu.Unlock()
	require.False(t, a.visible, "a Space switch dismisses the visible bar")
	require.False(t, a.lastHide.Before(before), "Hide stamped lastHide: toggle-gap semantics hold")
}

func TestSpaceChangeOnHiddenBarIsANoOp(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	onChange := armSpaceWatch(t, a, r)
	// Latch a pending show (a summon before DomReady) and stamp a
	// recognizable lastHide: the no-op branch must disturb neither.
	a.toggle()
	mark := time.Unix(1000, 0)
	a.mu.Lock()
	require.True(t, a.pendingShow)
	a.lastHide = mark
	a.mu.Unlock()

	onChange()

	require.False(t, r.has("hide"), "a hidden bar is left completely alone")
	a.mu.Lock()
	defer a.mu.Unlock()
	require.True(t, a.pendingShow, "the pending-show latch survives a Space switch")
	require.Equal(t, mark, a.lastHide, "lastHide untouched on the no-op branch")
}

func TestSpaceWatchNilSeamNeverArms(t *testing.T) {
	a, r := newTestApp(t, nil, Options{})
	// newTestApp pins the seam nil (the non-darwin shape).
	a.Startup(context.Background())
	require.False(t, r.has("watchSpaceChanges"))
}

func TestSpaceWatchSeamRefusal(t *testing.T) {
	// The observer failing to install (native returns false) is quiet:
	// nothing crashes, and the Once keeps a second Startup from
	// re-arming.
	a, r := newTestApp(t, nil, Options{})
	calls := 0
	a.plat.watchSpaceChanges = func(func()) bool {
		calls++
		return false
	}
	a.Startup(context.Background())
	a.Startup(context.Background())
	require.Equal(t, 1, calls)
	require.False(t, r.has("hide"))
}
