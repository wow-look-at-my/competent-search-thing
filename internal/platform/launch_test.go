package platform

import (
	"errors"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenCommands(t *testing.T) {
	require.Equal(t, [][]string{{"xdg-open", "/tmp/a b.txt"}}, OpenCommands("linux", "/tmp/a b.txt"))
	require.Equal(t, [][]string{{"open", "/tmp/x"}}, OpenCommands("darwin", "/tmp/x"))
	require.Equal(t, [][]string{{"rundll32", "url.dll,FileProtocolHandler", `C:\Users\x file.txt`}},
		OpenCommands("windows", `C:\Users\x file.txt`))
	require.Nil(t, OpenCommands("plan9", "/tmp/x"))
}

func TestRevealCommands(t *testing.T) {
	linux := RevealCommands("linux", "/tmp/dir/file.txt")
	require.Len(t, linux, 2)
	require.Equal(t, []string{
		"dbus-send", "--session",
		"--dest=org.freedesktop.FileManager1",
		"/org/freedesktop/FileManager1",
		"org.freedesktop.FileManager1.ShowItems",
		"array:string:file:///tmp/dir/file.txt",
		"string:",
	}, linux[0])
	require.Equal(t, []string{"xdg-open", "/tmp/dir"}, linux[1], "fallback opens the parent directory")

	require.Equal(t, [][]string{{"open", "-R", "/tmp/x"}}, RevealCommands("darwin", "/tmp/x"))
	require.Equal(t, [][]string{{"explorer", `/select,C:\x\y.txt`}}, RevealCommands("windows", `C:\x\y.txt`))
	require.Nil(t, RevealCommands("plan9", "/tmp/x"))
}

func TestFileURIEscaping(t *testing.T) {
	// Spaces are percent-encoded by net/url; commas additionally,
	// because dbus-send splits array:string: arguments on commas.
	cmds := RevealCommands("linux", "/tmp/weird name,with comma")
	require.Equal(t, "array:string:file:///tmp/weird%20name%2Cwith%20comma", cmds[0][5])
}

// fakeRunner records every argv and fails those in failFirst.
type fakeRunner struct {
	calls     [][]string
	failFirst int // fail this many leading calls
}

func (f *fakeRunner) run(argv []string) error {
	f.calls = append(f.calls, argv)
	if len(f.calls) <= f.failFirst {
		return errors.New("exec: not found")
	}
	return nil
}

func TestLauncherOpen(t *testing.T) {
	fr := &fakeRunner{}
	l := &Launcher{GOOS: "linux", Run: fr.run}
	require.NoError(t, l.Open("/tmp/x"))
	require.Equal(t, [][]string{{"xdg-open", "/tmp/x"}}, fr.calls)

	require.Error(t, l.Open(""), "empty path is rejected")
	require.Error(t, l.Open("   "), "blank path is rejected")
	require.Len(t, fr.calls, 1, "nothing ran for invalid paths")
}

func TestLauncherRevealFallsBack(t *testing.T) {
	fr := &fakeRunner{failFirst: 1}
	l := &Launcher{GOOS: "linux", Run: fr.run}
	require.NoError(t, l.Reveal("/tmp/dir/f.txt"))
	require.Len(t, fr.calls, 2, "dbus-send failed, xdg-open fallback ran")
	require.Equal(t, "dbus-send", fr.calls[0][0])
	require.Equal(t, []string{"xdg-open", "/tmp/dir"}, fr.calls[1])
}

func TestLauncherAllCandidatesFail(t *testing.T) {
	fr := &fakeRunner{failFirst: 99}
	l := &Launcher{GOOS: "linux", Run: fr.run}
	err := l.Reveal("/tmp/x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "dbus-send")
	require.Contains(t, err.Error(), "xdg-open")
	require.Error(t, l.Reveal(""), "empty path is rejected")
}

func TestLauncherUnsupportedGOOS(t *testing.T) {
	l := &Launcher{GOOS: "plan9", Run: func([]string) error { return nil }}
	err := l.Open("/tmp/x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "plan9")
	require.Error(t, l.Reveal("/tmp/x"))
}

func TestNewLauncherStartsRealProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/true equivalent")
	}
	l := NewLauncher()
	require.Equal(t, runtime.GOOS, l.GOOS)
	require.NoError(t, l.Run([]string{"true"}), "startDetached starts a real command")
	require.Error(t, l.Run([]string{"definitely-not-a-binary-xyz"}))
}
