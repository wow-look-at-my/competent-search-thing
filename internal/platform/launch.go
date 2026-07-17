package platform

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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
// interface (highlights the file itself); --print-reply makes
// dbus-send wait for the method reply, so a missing file manager
// surfaces as a fast non-zero exit and the launcher falls back to
// opening the parent directory with xdg-open. An unsupported goos
// returns nil.
func RevealCommands(goos, path string) [][]string {
	switch goos {
	case "linux":
		return [][]string{
			{
				"dbus-send", "--session", "--print-reply",
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

// DefaultGrace is how long Open/Reveal watch a started handler for a
// fast failure before assuming it is up. xdg-open, gio, and dbus-send
// exit well inside it in the failure modes that matter (no handler
// registered, no session bus, no file manager); a handler still
// running when it expires is treated as success and merely
// reaper-logged if it fails later.
const DefaultGrace = 1500 * time.Millisecond

// maxStderrCapture bounds how much of a child's stderr is kept for
// error messages: launcher diagnostics are short, and the cap keeps a
// chatty grandchild from bloating memory.
const maxStderrCapture = 8 << 10

// Launcher runs open/reveal commands and logs every spawn and every
// failure -- a failed open must never be invisible. The zero value is
// unusable; get one from NewLauncher.
type Launcher struct {
	// GOOS selects the command tables (runtime.GOOS in production).
	GOOS string
	// Run starts argv fire-and-forget (the plugin run_command path,
	// where the child is a long-lived application): it returns once
	// the process has started, and a background reaper logs a
	// non-zero exit. Seam for tests.
	Run func(argv []string) error
	// Start begins argv and returns a wait func that blocks until the
	// process exits, folding captured stderr into a non-zero exit
	// error. Seam for tests; production is startObserved.
	Start func(argv []string) (wait func() error, err error)
	// Grace bounds how long Open/Reveal wait for a fast failure
	// (DefaultGrace when zero).
	Grace time.Duration
	// Logf receives the spawn and failure log lines (log.Printf
	// shaped; nil drops them).
	Logf func(format string, v ...interface{})
}

// NewLauncher returns a Launcher for the current operating system that
// really starts processes and logs through the standard logger.
func NewLauncher() *Launcher {
	l := &Launcher{GOOS: runtime.GOOS, Grace: DefaultGrace, Logf: log.Printf}
	l.Run = l.runDetached
	l.Start = l.startObserved
	return l
}

func (l *Launcher) logf(format string, v ...interface{}) {
	if l.Logf != nil {
		l.Logf(format, v...)
	}
}

// startObserved starts argv with its stderr captured to an unlinked
// temp file. A file, not a pipe, on purpose: a launched grandchild
// (the actual application) inherits the descriptor, and with a pipe
// that would either block the reaper for the application's whole
// lifetime or -- if the read end were closed early -- SIGPIPE the
// application when it logs. The returned wait blocks until the
// process exits and returns its exit error with the captured stderr
// folded in.
func (l *Launcher) startObserved(argv []string) (func() error, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	var capture *os.File
	if f, err := os.CreateTemp("", "competent-search-stderr-*"); err == nil {
		_ = os.Remove(f.Name()) // unlink now; the fd keeps it readable
		capture = f
		cmd.Stderr = f
	} // capture setup failing only costs the stderr evidence
	if err := cmd.Start(); err != nil {
		if capture != nil {
			_ = capture.Close()
		}
		return nil, err
	}
	return func() error {
		err := cmd.Wait()
		msg := readCapped(capture, maxStderrCapture)
		if capture != nil {
			_ = capture.Close()
		}
		if err != nil && msg != "" {
			return fmt.Errorf("%w; stderr: %s", err, msg)
		}
		return err
	}, nil
}

// readCapped returns up to limit bytes of f from the start, trimmed;
// best-effort ("" on any problem).
func readCapped(f *os.File, limit int64) string {
	if f == nil {
		return ""
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// runDetached starts argv without blocking and reaps it in the
// background, logging a non-zero exit (with captured stderr) so a
// failed launch is never invisible. It is the production Run value.
func (l *Launcher) runDetached(argv []string) error {
	l.logf("run: exec %q", argv)
	wait, err := l.startObserved(argv)
	if err != nil {
		l.logf("run: %s: failed to start: %v", argv[0], err)
		return err
	}
	go func() {
		if err := wait(); err != nil {
			l.logf("run: %s: %v", argv[0], err)
		}
	}()
	return nil
}

// Open launches path with the OS default handler. It blocks at most
// Grace: long enough to catch a handler that fails immediately, never
// long enough to wait out one that is busy starting an application.
func (l *Launcher) Open(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("open: empty path")
	}
	return l.launch("open", OpenCommands(l.GOOS, path))
}

// Reveal shows path selected in the OS file manager, with the same
// bounded-wait semantics as Open.
func (l *Launcher) Reveal(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("reveal: empty path")
	}
	return l.launch("reveal", RevealCommands(l.GOOS, path))
}

// launch tries each candidate argv until one succeeds. Every spawn is
// logged with its exact argv. A candidate fails when it cannot start
// OR when it exits non-zero within the grace window -- fast failures
// (xdg-open with no registered handler, dbus-send with no file
// manager) surface as returned errors and fall through to the next
// candidate. A child still running when the window closes counts as
// success: launches stay non-blocking by design, and the background
// reaper still logs a late failure.
func (l *Launcher) launch(verb string, candidates [][]string) error {
	if len(candidates) == 0 {
		return fmt.Errorf("%s: unsupported platform %q", verb, l.GOOS)
	}
	var errs []error
	for _, argv := range candidates {
		l.logf("%s: exec %q", verb, argv)
		wait, err := l.Start(argv)
		if err != nil {
			err = fmt.Errorf("%s: failed to start: %w", argv[0], err)
			l.logf("%s: %v", verb, err)
			errs = append(errs, err)
			continue
		}
		if err := l.awaitGrace(verb, argv[0], wait); err != nil {
			errs = append(errs, err)
			continue
		}
		return nil
	}
	return fmt.Errorf("%s: %w", verb, errors.Join(errs...))
}

// awaitGrace waits for the child to exit, up to the grace window. A
// non-zero exit inside the window is logged and returned; a child
// still running when it expires is presumed up, and the wait is
// handed to a background reaper that logs an eventual failure.
func (l *Launcher) awaitGrace(verb, name string, wait func() error) error {
	done := make(chan error, 1)
	go func() { done <- wait() }()
	grace := l.Grace
	if grace <= 0 {
		grace = DefaultGrace
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			err = fmt.Errorf("%s: %w", name, err)
			l.logf("%s: %v", verb, err)
			return err
		}
		return nil
	case <-timer.C:
		go func() {
			if err := <-done; err != nil {
				l.logf("%s: %s (after the %v grace window): %v", verb, name, grace, err)
			}
		}()
		return nil
	}
}
