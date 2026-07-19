package app

import (
	"io"
	"log"
	"os"

	"github.com/wow-look-at-my/competent-search-thing/internal/progress"
)

// startProgress builds the index-build progress printer once. Startup
// runs it (through progressOnce) before kicking the initial build;
// buildIndex runs the same Once, so tests driving it directly get the
// printer too. The newProgress seam yields the printer; a nil seam or
// a nil printer degrades to an inert non-TTY printer writing nowhere,
// so the build path never nil-checks. On a TTY the standard logger is
// pointed at the printer (installProgressLog) so ordinary log lines
// interleave cleanly with the in-place progress line; Shutdown
// restores stderr.
func (a *App) startProgress() {
	var p *progress.Printer
	if a.newProgress != nil {
		p = a.newProgress()
	}
	if p == nil {
		p = progress.New(io.Discard, false, nil)
	}
	a.mu.Lock()
	a.progress = p
	a.mu.Unlock()
	installProgressLog(p)
}

// buildProgress is the production value behind the newProgress seam:
// the raw stderr stream, TTY detection on it, and log.Printf as the
// non-TTY sink (throttled "indexing..." lines for CI logs and files).
func (a *App) buildProgress() *progress.Printer {
	return progress.New(os.Stderr, progress.IsTerminal(os.Stderr), log.Printf)
}

// installProgressLog points the standard logger at a TTY printer:
// every ordinary log line then routes through the printer, which
// erases the in-place progress line, writes the log bytes to the real
// stream, and redraws -- the interception contract documented in
// internal/progress. Non-TTY printers (and nil) leave the logger
// untouched: they never render in place, so there is nothing to
// interleave with.
func installProgressLog(p *progress.Printer) {
	if p == nil || !p.TTY() {
		return
	}
	log.SetOutput(p)
}
