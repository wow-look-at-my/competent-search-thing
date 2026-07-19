package watch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDebouncerQuietWindow(t *testing.T) {
	d := debouncer{quiet: 250 * time.Millisecond, maxAge: time.Second, maxPending: 100}
	t0 := time.Now()

	_, pending := d.deadline()
	require.False(t, pending, "empty debouncer has no deadline")
	require.False(t, d.due(t0))

	require.False(t, d.add("/a", t0), "below the size cap")
	dl, pending := d.deadline()
	require.True(t, pending)
	require.Equal(t, t0.Add(250*time.Millisecond), dl, "quiet window from the last arrival")

	// A second path 100ms later pushes the quiet deadline out.
	require.False(t, d.add("/b", t0.Add(100*time.Millisecond)))
	dl, _ = d.deadline()
	require.Equal(t, t0.Add(350*time.Millisecond), dl)

	require.False(t, d.due(t0.Add(349*time.Millisecond)))
	require.True(t, d.due(t0.Add(350*time.Millisecond)))
}

func TestDebouncerMaxAgeCapsTheDrizzle(t *testing.T) {
	d := debouncer{quiet: 250 * time.Millisecond, maxAge: time.Second, maxPending: 100}
	t0 := time.Now()

	// One event every 200ms -- all on the SAME path, so the set never
	// grows -- keeps resetting the quiet window; the max-age cap (1s
	// after the FIRST arrival) bounds the flush anyway.
	for i := 0; i < 5; i++ {
		require.False(t, d.add("/f", t0.Add(time.Duration(i)*200*time.Millisecond)))
	}
	require.Len(t, d.order, 1, "re-marking a pending path never grows the set")
	// Last arrival at +800ms: quiet would flush at +1050ms, max age
	// wins at +1000ms.
	dl, pending := d.deadline()
	require.True(t, pending)
	require.Equal(t, t0.Add(time.Second), dl, "max age caps the quiet window")
	require.False(t, d.due(t0.Add(999*time.Millisecond)))
	require.True(t, d.due(t0.Add(time.Second)))
}

func TestDebouncerSizeCapAndTake(t *testing.T) {
	d := debouncer{quiet: 250 * time.Millisecond, maxAge: time.Second, maxPending: 4}
	t0 := time.Now()
	for _, p := range []string{"/a", "/b", "/c"} {
		require.False(t, d.add(p, t0))
	}
	require.False(t, d.add("/a", t0), "a duplicate does not advance the size cap")
	require.True(t, d.add("/d", t0), "4th UNIQUE path hits maxPending=4")

	batch := d.take()
	require.Equal(t, []string{"/a", "/b", "/c", "/d"}, batch, "first-arrival order preserved")
	_, pending := d.deadline()
	require.False(t, pending, "take resets the debouncer")

	// The next burst starts a fresh first-arrival clock, and a path
	// taken earlier pends again like any other.
	t1 := t0.Add(5 * time.Second)
	require.False(t, d.add("/a", t1))
	dl, _ := d.deadline()
	require.Equal(t, t1.Add(250*time.Millisecond), dl)
	require.Equal(t, []string{"/a"}, d.take())
}

func TestDebouncerDedupKeepsFirstArrivalOrder(t *testing.T) {
	d := debouncer{quiet: 250 * time.Millisecond, maxAge: time.Second, maxPending: 100}
	t0 := time.Now()

	require.False(t, d.add("/x", t0))
	require.False(t, d.add("/y", t0.Add(10*time.Millisecond)))
	// N events on one path collapse into that path's original entry;
	// the duplicate still refreshes the quiet window.
	for i := 0; i < 20; i++ {
		require.False(t, d.add("/x", t0.Add(time.Duration(20+i)*time.Millisecond)))
	}
	dl, _ := d.deadline()
	require.Equal(t, t0.Add(289*time.Millisecond), dl, "the last duplicate (at +39ms) reset the quiet window")

	require.Equal(t, []string{"/x", "/y"}, d.take(), "one pending entry per path, in first-arrival order")
}
