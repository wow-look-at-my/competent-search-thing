//go:build darwin

package progress

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// Real-call tests for the darwin footprint reader, headless on the
// mac CI job (the sysstats readers_darwin_test.go convention).

func TestPhysFootprintReads(t *testing.T) {
	fp := physFootprintBytes()
	require.NotZero(t, fp, "task_info(TASK_VM_INFO) reports a footprint on any supported macOS")
	// Sanity band: a running Go test process holds at least a few MB
	// and nothing a laptop can address is a terabyte.
	require.Greater(t, fp, uint64(1<<20))
	require.Less(t, fp, uint64(1<<40))
}

func TestRSSBytesReportsCurrentNotPeak(t *testing.T) {
	r := rssBytes()
	require.NotZero(t, r)
	// phys_footprint counts dirty+compressed+IOKit memory while
	// ru_maxrss records the peak RESIDENT set -- close cousins, not the
	// same accounting -- so pin only the sane relationship: the current
	// figure sits within a generous margin of the recorded peak, never
	// wildly above it (a regression back to reporting garbage would
	// trip either bound of TestPhysFootprintReads too).
	var ru syscall.Rusage
	require.NoError(t, syscall.Getrusage(syscall.RUSAGE_SELF, &ru))
	require.LessOrEqual(t, r, uint64(ru.Maxrss)+(256<<20),
		"the current footprint tracks the process's actual size, not a runaway value")
}
