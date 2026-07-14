package platform

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// OpenCommands returns the candidate command lines (tried in order)
// that open path with the operating system's default handler on goos.
// Windows uses rundll32's FileProtocolHandler, which takes the path as
// a plain argument and so has none of the quoting pitfalls of
// "cmd /C start" or bare explorer.exe. An unsupported goos returns nil.
func OpenCommands(goos, path string) [][]string {
	switch goos {
	case "linux":
		return [][]string{{"xdg-open", path}}
	case "darwin":
		return [][]string{{"open", path}}
	case "windows":
		return [][]string{{"rundll32", "url.dll,FileProtocolHandler", path}}
	}
	return nil
}

// RevealCommands returns the candidate command lines (tried in order)
// that show path selected in the operating system's file manager. On
// Linux the first choice is the freedesktop FileManager1 D-Bus
// interface (highlights the file itself); the fallback opens the
// parent directory with xdg-open. An unsupported goos returns nil.
func RevealCommands(goos, path string) [][]string {
	switch goos {
	case "linux":
		return [][]string{
			{
				"dbus-send", "--session",
				"--dest=org.freedesktop.FileManager1",
				"/org/freedesktop/FileManager1",
				"org.freedesktop.FileManager1.ShowItems",
				"array:string:" + fileURI(path),
				"string:",
			},
			{"xdg-open", filepath.Dir(path)},
		}
	case "darwin":
		return [][]string{{"open", "-R", path}}
	case "windows":
		// The comma form is a single argument; explorer parses it.
		return [][]string{{"explorer", "/select," + path}}
	}
	return nil
}

// fileURI renders path as a file:// URI for dbus-send. Commas are
// percent-encoded on top of the standard escaping because dbus-send
// splits array:string: arguments on commas.
func fileURI(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	return strings.ReplaceAll(u.String(), ",", "%2C")
}

// Launcher runs open/reveal commands. The zero value is unusable; get
// one from NewLauncher. Run is a seam for tests: it receives the argv
// and starts it without waiting (fire and forget).
type Launcher struct {
	// GOOS selects the command tables (runtime.GOOS in production).
	GOOS string
	// Run starts argv without waiting for it to exit.
	Run func(argv []string) error
}

// NewLauncher returns a Launcher for the current operating system that
// really starts processes.
func NewLauncher() *Launcher {
	return &Launcher{GOOS: runtime.GOOS, Run: startDetached}
}

// startDetached starts argv without blocking and reaps it in the
// background so no zombie processes accumulate.
func startDetached(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Open launches path with the OS default handler, without blocking.
func (l *Launcher) Open(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("open: empty path")
	}
	return l.launch("open", OpenCommands(l.GOOS, path))
}

// Reveal shows path selected in the OS file manager, without blocking.
// Candidates are tried in order; a later candidate only runs when an
// earlier one failed to START (e.g. missing binary) -- a command that
// starts and then fails is not detected, by design (non-blocking).
func (l *Launcher) Reveal(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("reveal: empty path")
	}
	return l.launch("reveal", RevealCommands(l.GOOS, path))
}

// launch tries each candidate argv until one starts.
func (l *Launcher) launch(verb string, candidates [][]string) error {
	if len(candidates) == 0 {
		return fmt.Errorf("%s: unsupported platform %q", verb, l.GOOS)
	}
	var errs []error
	for _, argv := range candidates {
		err := l.Run(argv)
		if err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", argv[0], err))
	}
	return fmt.Errorf("%s: %w", verb, errors.Join(errs...))
}
