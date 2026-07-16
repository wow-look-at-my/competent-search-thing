package platform

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectSession(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantKind    SessionKind
		wantDesktop string
	}{
		{
			name:     "session type wayland wins over DISPLAY",
			env:      map[string]string{"XDG_SESSION_TYPE": "wayland", "DISPLAY": ":0"},
			wantKind: SessionWayland,
		},
		{
			name:     "session type x11 wins over WAYLAND_DISPLAY",
			env:      map[string]string{"XDG_SESSION_TYPE": "x11", "WAYLAND_DISPLAY": "wayland-0"},
			wantKind: SessionX11,
		},
		{
			name:     "wayland display fallback",
			env:      map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
			wantKind: SessionWayland,
		},
		{
			name:     "x display fallback",
			env:      map[string]string{"DISPLAY": ":0"},
			wantKind: SessionX11,
		},
		{
			name:     "unrecognized session type falls through to displays",
			env:      map[string]string{"XDG_SESSION_TYPE": "tty", "DISPLAY": ":1"},
			wantKind: SessionX11,
		},
		{
			name:     "wayland display beats x display when both set",
			env:      map[string]string{"WAYLAND_DISPLAY": "wayland-1", "DISPLAY": ":0"},
			wantKind: SessionWayland,
		},
		{
			name:     "nothing set",
			env:      map[string]string{},
			wantKind: SessionUnknown,
		},
		{
			name:        "desktop passes through raw",
			env:         map[string]string{"XDG_SESSION_TYPE": "wayland", "XDG_CURRENT_DESKTOP": "ubuntu:GNOME"},
			wantKind:    SessionWayland,
			wantDesktop: "ubuntu:GNOME",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string { return tt.env[k] }
			got := DetectSession(getenv)
			require.Equal(t, tt.wantKind, got.Kind)
			require.Equal(t, tt.wantDesktop, got.Desktop)
		})
	}
}

func TestSessionKindString(t *testing.T) {
	require.Equal(t, "x11", SessionX11.String())
	require.Equal(t, "wayland", SessionWayland.String())
	require.Equal(t, "unknown", SessionUnknown.String())
	require.Equal(t, "unknown", SessionKind(99).String())
}

func TestSessionIsGNOME(t *testing.T) {
	tests := []struct {
		desktop string
		want    bool
	}{
		{"GNOME", true},
		{"gnome", true},
		{"ubuntu:GNOME", true},
		{"GNOME-Classic:GNOME", true},
		{"gnome-flashback", false}, // segment must equal, not contain
		{"KDE", false},
		{"sway", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run("desktop="+tt.desktop, func(t *testing.T) {
			s := Session{Desktop: tt.desktop}
			require.Equal(t, tt.want, s.IsGNOME())
		})
	}
}
