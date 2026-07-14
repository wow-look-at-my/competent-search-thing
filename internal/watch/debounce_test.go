package watch

import (
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
)

func evt(path string) fsnotify.Event {
	return fsnotify.Event{Name: path, Op: fsnotify.Create}
}

func TestDebouncerQuietWindow(t *testing.T) {
	d := debouncer{quiet: 250 * time.Millisecond, maxAge: time.Second, maxPending: 100}
	t0 := time.Now()

	_, pending := d.deadline()
	require.False(t, pending, "empty debouncer has no deadline")
	require.False(t, d.due(t0))

	require.False(t, d.add(evt("/a"), t0), "below the size cap")
	dl, pending := d.deadline()
	require.True(t, pending)
	require.Equal(t, t0.Add(250*time.Millisecond), dl, "quiet window from the last event")

	// A second event 100ms later pushes the quiet deadline out.
	require.False(t, d.add(evt("/b"), t0.Add(100*time.Millisecond)))
	dl, _ = d.deadline()
	require.Equal(t, t0.Add(350*time.Millisecond), dl)

	require.False(t, d.due(t0.Add(349*time.Millisecond)))
	require.True(t, d.due(t0.Add(350*time.Millisecond)))
}

func TestDebouncerMaxAgeCapsTheDrizzle(t *testing.T) {
	d := debouncer{quiet: 250 * time.Millisecond, maxAge: time.Second, maxPending: 100}
	t0 := time.Now()

	// One event every 200ms keeps resetting the quiet window; the
	// max-age cap (1s after the FIRST event) bounds the flush anyway.
	for i := 0; i < 5; i++ {
		require.False(t, d.add(evt("/f"), t0.Add(time.Duration(i)*200*time.Millisecond)))
	}
	// Last event at +800ms: quiet would flush at +1050ms, max age wins
	// at +1000ms.
	dl, pending := d.deadline()
	require.True(t, pending)
	require.Equal(t, t0.Add(time.Second), dl, "max age caps the quiet window")
	require.False(t, d.due(t0.Add(999*time.Millisecond)))
	require.True(t, d.due(t0.Add(time.Second)))
}

func TestDebouncerSizeCapAndTake(t *testing.T) {
	d := debouncer{quiet: 250 * time.Millisecond, maxAge: time.Second, maxPending: 4}
	t0 := time.Now()
	for i := 0; i < 3; i++ {
		require.False(t, d.add(evt("/x"), t0))
	}
	require.True(t, d.add(evt("/y"), t0), "4th event hits maxPending=4")

	batch := d.take()
	require.Len(t, batch, 4)
	require.Equal(t, evt("/y"), batch[3], "arrival order preserved")
	_, pending := d.deadline()
	require.False(t, pending, "take resets the debouncer")

	// The next burst starts a fresh first-event clock.
	t1 := t0.Add(5 * time.Second)
	require.False(t, d.add(evt("/z"), t1))
	dl, _ := d.deadline()
	require.Equal(t, t1.Add(250*time.Millisecond), dl)
	require.Equal(t, []fsnotify.Event{evt("/z")}, d.take())
}
