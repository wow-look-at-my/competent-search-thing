package platform

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestThermalStateString(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "nominal"},
		{1, "fair"},
		{2, "serious"},
		{3, "critical"},
		{4, "unknown(4)"},
		{-1, "unknown(-1)"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, ThermalStateString(c.in))
	}
}

func TestUncapStatusString(t *testing.T) {
	cases := []struct {
		in   UncapStatus
		want string
	}{
		{UncapApplied, "applied"},
		{UncapNoWindow, "no window yet"},
		{UncapNoWebView, "no webview found"},
		{UncapSPIMissing, "WKPreferences feature SPI unavailable"},
		{UncapFeatureNotFound, "feature key not found"},
		{UncapUnavailable, "unavailable on this platform"},
		{UncapStatus(42), "unknown status 42"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, c.in.String())
	}
}
