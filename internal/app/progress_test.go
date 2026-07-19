package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/index"
	"github.com/wow-look-at-my/competent-search-thing/internal/progress"
)

// TestBuildIndexRendersProgressAndStartupSummary drives the real
// buildIndex path over a temp fixture tree (the
// TestBuildIndexCancelledDiscardsPartialAndLogs harness: a direct,
// synchronous buildIndex call, so the captured log buffer is only ever
// written from this goroutine) with a recording non-TTY printer, and
// pins the whole progress contract: ticks reach the printer with the
// RAM figure, the completion log is verbatim, and the one
// startup-complete summary fires only after the watch layer is up.
// Not parallel: it rewires the process-global log output.
func TestBuildIndexRendersProgressAndStartupSummary(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "world.md"), []byte("x"), 0o644))
	m := index.NewManager([]string{dir}, nil, 0)
	a, _ := newTestApp(t, m, Options{})

	// The walk's progress callbacks arrive on walker goroutines; the
	// recorder is mutex-guarded like every emit sink.
	var mu sync.Mutex
	var ticks []string
	a.newProgress = func() *progress.Printer {
		return progress.New(io.Discard, false, func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			ticks = append(ticks, fmt.Sprintf(format, args...))
		})
	}

	a.buildIndex(context.Background())
	// Join the watch layer's goroutines before reading the buffer, so
	// nothing can write to it concurrently.
	a.Shutdown(context.Background())

	mu.Lock()
	got := append([]string(nil), ticks...)
	mu.Unlock()
	tickRe := regexp.MustCompile(`^index: indexing\.\.\. \d+ entries, .+ ram$`)
	found := false
	for _, l := range got {
		if tickRe.MatchString(l) {
			found = true
			break
		}
	}
	require.True(t, found,
		"at least one indexing tick reaches the printer's logf sink, RAM figure included: %q", got)

	out := buf.String()
	require.Contains(t, out, "index: 2 entries in", "the completion log is kept verbatim")
	watchAt := strings.Index(out, "watch: live index updates started")
	require.GreaterOrEqual(t, watchAt, 0, "the watch layer start is logged")
	sumRe := regexp.MustCompile(`(?m)^.*index: startup complete: \d+ entries in .+, .+ ram$`)
	loc := sumRe.FindStringIndex(out)
	require.NotNil(t, loc, "the startup summary is logged: %q", out)
	require.Greater(t, loc[0], watchAt, "the summary fires only after watch establishment")
}

// TestInstallProgressLogRoutesThroughTTYPrinter exercises the TTY
// branch in isolation: after install, standard log lines land in the
// printer's stream. Not parallel: it rewires the process-global log
// output.
func TestInstallProgressLogRoutesThroughTTYPrinter(t *testing.T) {
	var buf bytes.Buffer
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	installProgressLog(progress.New(&buf, true, nil))
	log.Printf("interleaved line")
	require.Contains(t, buf.String(), "interleaved line",
		"TTY mode: the standard logger writes through the printer")
}

// TestInstallProgressLogLeavesNonTTYAlone pins the other half of the
// contract: non-TTY printers (and nil) never touch the logger, so
// tests capturing log output are never clobbered. Not parallel.
func TestInstallProgressLogLeavesNonTTYAlone(t *testing.T) {
	var direct, printer bytes.Buffer
	log.SetOutput(&direct)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	installProgressLog(progress.New(&printer, false, nil))
	installProgressLog(nil) // nil-safe
	log.Printf("stays on the previous sink")
	require.Contains(t, direct.String(), "stays on the previous sink")
	require.Empty(t, printer.String(), "a non-TTY printer never intercepts the logger")
}

// TestShutdownRestoresLoggerFromTTYPrinter pins the teardown half:
// Startup installs the TTY printer as the log output, Shutdown clears
// it and hands the logger back to stderr. Not parallel.
func TestShutdownRestoresLoggerFromTTYPrinter(t *testing.T) {
	var tty bytes.Buffer
	a, _ := newTestApp(t, nil, Options{}) // nil manager: no build goroutine
	p := progress.New(&tty, true, nil)
	a.newProgress = func() *progress.Printer { return p }
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	a.Startup(context.Background())
	require.Same(t, p, log.Writer(), "Startup pointed the standard logger at the TTY printer")
	log.Printf("through the printer")
	require.Contains(t, tty.String(), "through the printer")

	a.Shutdown(context.Background())
	require.Same(t, os.Stderr, log.Writer(), "Shutdown restored the standard logger to stderr")
}
