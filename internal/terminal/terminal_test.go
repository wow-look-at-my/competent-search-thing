package terminal

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lookPath builds a LookPath seam resolving exactly the named
// executables to /usr/bin/<name>, in the "found" set's spelling.
func lookPath(found ...string) func(string) (string, error) {
	set := map[string]bool{}
	for _, f := range found {
		set[f] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

func env(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func TestDetectUnixPrefersDebianAlternative(t *testing.T) {
	term, ok := Detect(Options{GOOS: "linux", LookPath: lookPath("x-terminal-emulator", "xterm")})
	require.True(t, ok)
	assert.Equal(t, "x-terminal-emulator", term.Name)
	assert.Equal(t, "/usr/bin/x-terminal-emulator", term.Path)
	assert.True(t, term.SupportsArgs)
	assert.Equal(t, []string{"/usr/bin/x-terminal-emulator", "-e", "htop"}, term.Command([]string{"htop"}))
}

func TestDetectUnixCandidateOrderAndPrefixes(t *testing.T) {
	tests := []struct {
		name    string
		found   []string
		wantExe string
		wantCmd []string
	}{
		{
			name:    "gnome-terminal uses the -- separator",
			found:   []string{"gnome-terminal", "xterm"},
			wantExe: "gnome-terminal",
			wantCmd: []string{"/usr/bin/gnome-terminal", "--", "htop", "-d", "5"},
		},
		{
			name:    "konsole takes -e",
			found:   []string{"konsole", "xterm"},
			wantExe: "konsole",
			wantCmd: []string{"/usr/bin/konsole", "-e", "htop", "-d", "5"},
		},
		{
			name:    "xfce4-terminal takes -x",
			found:   []string{"xfce4-terminal"},
			wantExe: "xfce4-terminal",
			wantCmd: []string{"/usr/bin/xfce4-terminal", "-x", "htop", "-d", "5"},
		},
		{
			name:    "kitty takes the command as trailing arguments",
			found:   []string{"kitty"},
			wantExe: "kitty",
			wantCmd: []string{"/usr/bin/kitty", "htop", "-d", "5"},
		},
		{
			name:    "wezterm runs it under its start subcommand",
			found:   []string{"wezterm"},
			wantExe: "wezterm",
			wantCmd: []string{"/usr/bin/wezterm", "start", "--", "htop", "-d", "5"},
		},
		{
			name:    "desktop terminals outrank the X11 classics",
			found:   []string{"xterm", "st", "konsole"},
			wantExe: "konsole",
			wantCmd: []string{"/usr/bin/konsole", "-e", "htop", "-d", "5"},
		},
		{
			name:    "xterm is the last resort",
			found:   []string{"xterm"},
			wantExe: "xterm",
			wantCmd: []string{"/usr/bin/xterm", "-e", "htop", "-d", "5"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			term, ok := Detect(Options{GOOS: "linux", LookPath: lookPath(tt.found...)})
			require.True(t, ok)
			assert.Equal(t, tt.wantExe, term.Name)
			assert.Equal(t, tt.wantCmd, term.Command([]string{"htop", "-d", "5"}))
		})
	}
}

func TestDetectUnixHonorsTerminalEnv(t *testing.T) {
	// A known name gets its own convention, not the default -e.
	term, ok := Detect(Options{
		GOOS:     "linux",
		Getenv:   env(map[string]string{EnvTerminal: "gnome-terminal"}),
		LookPath: lookPath("gnome-terminal", "xterm"),
	})
	require.True(t, ok)
	assert.Equal(t, "gnome-terminal", term.Name)
	assert.Equal(t, []string{"/usr/bin/gnome-terminal", "--", "htop"}, term.Command([]string{"htop"}))

	// An unknown terminal falls back to the universal -e, and an
	// absolute spelling still matches the table by base name.
	term, ok = Detect(Options{
		GOOS:     "linux",
		Getenv:   env(map[string]string{EnvTerminal: "myterm"}),
		LookPath: lookPath("myterm", "xterm"),
	})
	require.True(t, ok)
	assert.Equal(t, "myterm", term.Name)
	assert.Equal(t, []string{"/usr/bin/myterm", "-e", "htop"}, term.Command([]string{"htop"}))
}

func TestDetectUnixIgnoresUnresolvableTerminalEnv(t *testing.T) {
	term, ok := Detect(Options{
		GOOS:     "linux",
		Getenv:   env(map[string]string{EnvTerminal: "nope"}),
		LookPath: lookPath("xterm"),
	})
	require.True(t, ok)
	assert.Equal(t, "xterm", term.Name)
}

func TestDetectUnixNoTerminal(t *testing.T) {
	_, ok := Detect(Options{GOOS: "linux", LookPath: lookPath()})
	assert.False(t, ok)
}

func TestDetectRequiresLookPath(t *testing.T) {
	_, ok := Detect(Options{GOOS: "linux"})
	assert.False(t, ok)
}

func TestDetectDarwin(t *testing.T) {
	term, ok := Detect(Options{GOOS: "darwin", LookPath: lookPath("open")})
	require.True(t, ok)
	assert.Equal(t, "Terminal", term.Name)
	assert.False(t, term.SupportsArgs)
	assert.Equal(t, []string{"/usr/bin/open", "-a", "Terminal", "/usr/bin/htop"}, term.Command([]string{"/usr/bin/htop"}))
	// A command with arguments has no representation here.
	assert.Nil(t, term.Command([]string{"/usr/bin/htop", "-d", "5"}))

	_, ok = Detect(Options{GOOS: "darwin", LookPath: lookPath()})
	assert.False(t, ok)
}

func TestDetectWindows(t *testing.T) {
	term, ok := Detect(Options{GOOS: "windows", LookPath: lookPath("wt.exe", "cmd.exe")})
	require.True(t, ok)
	assert.Equal(t, "Windows Terminal", term.Name)
	assert.Equal(t, []string{"/usr/bin/wt.exe", "htop"}, term.Command([]string{"htop"}))

	term, ok = Detect(Options{GOOS: "windows", LookPath: lookPath("cmd.exe")})
	require.True(t, ok)
	assert.Equal(t, "cmd.exe", term.Name)
	assert.Equal(t, []string{"/usr/bin/cmd.exe", "/c", "start", "", "cmd.exe", "/k", "htop"}, term.Command([]string{"htop"}))

	_, ok = Detect(Options{GOOS: "windows", LookPath: lookPath()})
	assert.False(t, ok)
}

func TestCommandRejectsEmptyInput(t *testing.T) {
	term, ok := Detect(Options{GOOS: "linux", LookPath: lookPath("xterm")})
	require.True(t, ok)
	assert.Nil(t, term.Command(nil))
	assert.Nil(t, term.Command([]string{""}))
	assert.Nil(t, Terminal{}.Command([]string{"htop"}))
}

// The returned argv must not alias the terminal's own prefix slice: a
// caller mutating one command's argv can never corrupt the next.
func TestCommandDoesNotAliasPrefix(t *testing.T) {
	term, ok := Detect(Options{GOOS: "linux", LookPath: lookPath("gnome-terminal")})
	require.True(t, ok)
	first := term.Command([]string{"htop"})
	first[1] = "clobbered"
	assert.Equal(t, []string{"/usr/bin/gnome-terminal", "--", "vim"}, term.Command([]string{"vim"}))
}
