package launch

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyTarget(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		isDir bool
		want  Target
	}{
		{
			name: "http url",
			raw:  "https://example.com/a?b=c",
			want: Target{Raw: "https://example.com/a?b=c", URI: "https://example.com/a?b=c", IsURL: true, Scheme: "https"},
		},
		{
			name: "scheme case folded",
			raw:  "HTTP://Example.com/x",
			want: Target{Raw: "HTTP://Example.com/x", URI: "HTTP://Example.com/x", IsURL: true, Scheme: "http"},
		},
		{
			name: "plain file",
			raw:  "/home/u/notes.txt",
			want: Target{Raw: "/home/u/notes.txt", URI: "file:///home/u/notes.txt"},
		},
		{
			name:  "directory",
			raw:   "/home/u/projects",
			isDir: true,
			want:  Target{Raw: "/home/u/projects", URI: "file:///home/u/projects", IsDir: true},
		},
		{
			name: "path with spaces is uri-escaped",
			raw:  "/tmp/a b.txt",
			want: Target{Raw: "/tmp/a b.txt", URI: "file:///tmp/a%20b.txt"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ClassifyTarget(tt.raw, tt.isDir))
		})
	}
}

func TestClassifyTargetWindowsPathIsNotURL(t *testing.T) {
	got := ClassifyTarget(`C:\Users\u\file.txt`, false)
	require.False(t, got.IsURL, "a drive-letter path must not parse as a URL")
	require.Equal(t, `C:\Users\u\file.txt`, got.Raw)
	require.Empty(t, got.Scheme)
}

func TestShouldMint(t *testing.T) {
	tests := []struct {
		name     string
		h        Handler
		resolved bool
		want     bool
	}{
		{name: "unresolved always mints", want: true},
		{name: "unresolved ignores handler fields", h: Handler{Terminal: true}, want: true},
		{name: "startup-notify handler", h: Handler{StartupNotify: true}, resolved: true, want: true},
		{name: "dbus-activatable handler", h: Handler{DBusActivatable: true}, resolved: true, want: true},
		{name: "both", h: Handler{StartupNotify: true, DBusActivatable: true}, resolved: true, want: true},
		{name: "plain handler skips the mint (no dangling busy cursor)", h: Handler{DesktopID: "x.desktop", Exec: "x"}, resolved: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ShouldMint(tt.h, tt.resolved))
		})
	}
}

func TestCredentialEnv(t *testing.T) {
	require.Nil(t, CredentialEnv(Credential{}), "no credential, no env")
	require.Nil(t, CredentialEnv(Credential{Kind: KindNone}))
	require.Equal(t,
		[]string{"DESKTOP_STARTUP_ID=abc_TIME1", "XDG_ACTIVATION_TOKEN=abc_TIME1"},
		CredentialEnv(Credential{ID: "abc_TIME1", Kind: KindX11SN}),
		"both variables carry the same id")
}

func TestLogLine(t *testing.T) {
	h := Handler{DesktopID: "code.desktop"}
	cred := Credential{ID: "0123456789abcdef", Kind: KindX11SN}
	require.Equal(t,
		"launch: open /tmp/x handler=code.desktop credential=x11-sn:01234567 transport=exec watcher=on",
		LogLine("open", "/tmp/x", h, true, cred, TransportExec, true))
	require.Equal(t,
		"launch: open https://x.org handler=- credential=none transport=xdg-open watcher=off",
		LogLine("open", "https://x.org", Handler{}, false, Credential{Kind: KindNone}, TransportXdgOpen, false))
	require.Equal(t,
		"launch: reveal /tmp/y handler=- credential=wayland-gdk:short transport=showitems watcher=off",
		LogLine("reveal", "/tmp/y", Handler{DesktopID: "org.gnome.Nautilus.desktop"}, false,
			Credential{ID: "short", Kind: KindWaylandGDK}, TransportShowItems, false),
		"unresolved handlers log '-' even when the struct has leftovers; short ids stay whole")
}

func TestValidDesktopID(t *testing.T) {
	require.NoError(t, ValidDesktopID("code.desktop"))
	require.NoError(t, ValidDesktopID("org.gnome.Nautilus.desktop"))
	for name, id := range map[string]string{
		"empty":            "",
		"path separator":   "apps/code.desktop",
		"backslash":        `apps\code.desktop`,
		"no suffix":        "code",
		"bare suffix":      ".desktop",
		"parent traversal": "../code.desktop",
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, ValidDesktopID(id))
		})
	}
}
