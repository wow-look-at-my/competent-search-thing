package platform

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Two-monitor layout used across the tests: a 1080p primary at the
// origin and a 1440p secondary LEFT of it (negative origin).
var (
	primary   = Display{Rect: Rect{X: 0, Y: 0, W: 1920, H: 1080}, Work: Rect{X: 0, Y: 40, W: 1920, H: 1040}, Primary: true}
	leftOf    = Display{Rect: Rect{X: -2560, Y: -200, W: 2560, H: 1440}, Work: Rect{X: -2560, Y: -200, W: 2560, H: 1440}}
	twoScreen = []Display{primary, leftOf}
)

func TestRectContains(t *testing.T) {
	r := Rect{X: 10, Y: 20, W: 100, H: 50}
	require.True(t, r.Contains(10, 20), "top-left corner is inside")
	require.True(t, r.Contains(109, 69), "bottom-right pixel is inside")
	require.False(t, r.Contains(110, 20), "right edge is exclusive")
	require.False(t, r.Contains(10, 70), "bottom edge is exclusive")
	require.False(t, r.Contains(9, 20))
	require.True(t, Rect{X: -100, Y: -100, W: 50, H: 50}.Contains(-75, -75), "negative-origin rect")
}

func TestPickDisplay(t *testing.T) {
	tests := []struct {
		name   string
		ds     []Display
		cx, cy int
		want   Display
		ok     bool
	}{
		{"cursor on primary", twoScreen, 500, 500, primary, true},
		{"cursor on negative-origin monitor", twoScreen, -1000, 300, leftOf, true},
		{"cursor on left border of secondary", twoScreen, -2560, -200, leftOf, true},
		{"cursor between displays falls back to primary", twoScreen, -100, -1000, primary, true},
		{"no primary falls back to first", []Display{leftOf}, 9999, 9999, leftOf, true},
		{"empty list", nil, 0, 0, Display{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := PickDisplay(tc.ds, tc.cx, tc.cy)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBarPosition(t *testing.T) {
	tests := []struct {
		name       string
		d          Display
		winW, winH int
		wantX      int
		wantY      int
	}{
		{"centered on primary", primary, 680, 460, (1920 - 680) / 2, 1080/3 - 460/3},
		{"negative-origin display", leftOf, 680, 460, -2560 + (2560-680)/2, -200 + 1440/3 - 460/3},
		{"window wider than display clamps to origin", Display{Rect: Rect{X: 100, Y: 100, W: 400, H: 300}}, 680, 460, 100, 100},
		{"window taller than display pins to display top", Display{Rect: Rect{X: 0, Y: 0, W: 1920, H: 300}}, 680, 460, 620, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x, y := BarPosition(tc.d, tc.winW, tc.winH)
			require.Equal(t, tc.wantX, x, "x")
			require.Equal(t, tc.wantY, y, "y")
		})
	}
}

func TestBarPositionStaysInsideDisplay(t *testing.T) {
	for _, d := range twoScreen {
		x, y := BarPosition(d, 680, 460)
		require.True(t, d.Rect.Contains(x, y), "top-left inside %+v", d.Rect)
		require.True(t, d.Rect.Contains(x+679, y+459), "bottom-right inside %+v", d.Rect)
	}
}

func TestDisplayForWindow(t *testing.T) {
	tests := []struct {
		name   string
		ds     []Display
		wx, wy int
		want   Display
		ok     bool
	}{
		{"window on primary", twoScreen, 600, 300, primary, true},
		{"window on left monitor", twoScreen, -2000, 100, leftOf, true},
		{"straddling: center decides", twoScreen, -300, 200, primary, true},
		{"center off-screen, top-left decides", twoScreen, -2560, 1100, leftOf, true},
		{"nowhere falls back to primary", twoScreen, 50000, 50000, primary, true},
		{"empty list", nil, 0, 0, Display{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DisplayForWindow(tc.ds, tc.wx, tc.wy, 680, 460)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestWailsPosition(t *testing.T) {
	// Absolute target (100, 200) with the window currently on a monitor
	// whose geometry origin is (-2560, -200) and work origin (-2500, -150).
	cur := Display{
		Rect: Rect{X: -2560, Y: -200, W: 2560, H: 1440},
		Work: Rect{X: -2500, Y: -150, W: 2500, H: 1390},
	}
	x, y := WailsPosition("linux", cur, 100, 200)
	require.Equal(t, 2660, x, "linux offsets by the monitor geometry origin")
	require.Equal(t, 400, y)

	x, y = WailsPosition("windows", cur, 100, 200)
	require.Equal(t, 2600, x, "windows offsets by the work-area origin")
	require.Equal(t, 350, y)
}
