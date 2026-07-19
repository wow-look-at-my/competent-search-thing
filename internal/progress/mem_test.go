package progress

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name string
		b    uint64
		want string
	}{
		{"zero", 0, "0.0MB"},
		{"tiny", 1234, "0.0MB"},
		{"one mb", 1_000_000, "1.0MB"},
		{"spec mb line", 384_200_000, "384.2MB"},
		{"tenth rounds", 123_456_789, "123.5MB"},
		{"just below the boundary", 999_940_000, "999.9MB"},
		// float64(999_950_000)/1e6 lands a hair above 999.95, so %.1f
		// rounds up -- the point pinned here is the UNIT: below 1e9
		// stays MB.
		{"boundary stays mb", 999_950_000, "1000.0MB"},
		{"exactly 1e9 flips to gb", 1_000_000_000, "1.0GB"},
		{"spec gb line", 12_600_000_000, "12.6GB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, FormatBytes(tt.b), "FormatBytes(%d)", tt.b)
		})
	}
}

func TestRAM(t *testing.T) {
	require.NotZero(t, RAM(), "the runtime always maps memory, so even the Sys fallback is positive")
}

func TestRSSBytes(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		require.NotZero(t, rssBytes(), "a live process has a resident set")
	default:
		require.Zero(t, rssBytes(), "the fallback stub reports 0")
	}
}
