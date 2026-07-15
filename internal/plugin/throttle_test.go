package plugin

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLogThrottleSuppressionWindow(t *testing.T) {
	var lines []string
	out := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	now := time.Unix(1000, 0)
	th := newLogThrottle(out, 5*time.Second, func() time.Time { return now })

	th.logf("a", "first %d", 1)
	th.logf("a", "suppressed")
	th.logf("b", "keys are %s", "independent")
	require.Equal(t, []string{"first 1", "keys are independent"}, lines)

	now = now.Add(5*time.Second - time.Millisecond)
	th.logf("a", "still suppressed")
	require.Len(t, lines, 2)

	now = now.Add(time.Millisecond) // exactly the window
	th.logf("a", "allowed again")
	require.Equal(t, "allowed again", lines[2])

	// The allowed line restarts the window.
	th.logf("a", "suppressed again")
	require.Len(t, lines, 3)
}

func TestLogThrottleDefaultClock(t *testing.T) {
	var n int
	th := newLogThrottle(func(string, ...any) { n++ }, time.Hour, nil)
	th.logf("k", "x")
	th.logf("k", "x")
	require.Equal(t, 1, n)
}
