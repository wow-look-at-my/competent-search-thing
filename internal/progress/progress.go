// Package progress renders the initial index build's progress line
// without spamming startup logs.
//
// On a TTY the line is redrawn IN PLACE: plain "\r" plus space padding
// out to the widest line rendered so far -- no ANSI escapes, so dumb
// terminals and Windows conhost stay readable. The rendered line
// carries a self-formatted timestamp identical to the log package
// default ("2006/01/02 15:04:05 "), so it reads like an ordinary log
// line that happens to update in place.
//
// Interception contract: in TTY mode the app points the standard
// logger at the Printer (log.SetOutput(printer)), which implements
// io.Writer. Every log write then erases the progress line, writes the
// log line, and -- when the write ends in '\n', which the log package
// guarantees -- redraws the progress line after it, so ordinary
// logging never tears the display. The Printer itself writes straight
// to the raw TTY (w), never through the log package, so there is no
// recursion.
//
// Off a TTY (CI, pipes, log files) nothing renders in place: Indexing
// appends plain lines through the logf sink (usually log.Printf),
// throttled to one line per appendEvery so long builds stay readable.
package progress

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// appendEvery is the non-TTY cadence: at most one appended log line
// per window, so CI logs and files do not scroll away under a
// multi-minute build.
const appendEvery = 5 * time.Second

// memEvery bounds how often the RAM figure is resampled; progress
// ticks arrive far more often than the number changes meaningfully.
const memEvery = time.Second

// Printer renders the "index: indexing..." progress line. All methods
// are goroutine-safe: progress callbacks, intercepted log writes and
// Done arrive on arbitrary goroutines.
type Printer struct {
	mu      sync.Mutex
	w       io.Writer                        // raw TTY target, usually os.Stderr
	tty     bool                             // render in place vs append through logf
	logf    func(format string, args ...any) // non-TTY sink, usually log.Printf; nil = drop
	now     func() time.Time                 // clock seam
	readRAM func() uint64                    // memory seam, default RAM

	line       string    // currently rendered full line incl. stamp
	active     bool      // a progress line is on screen
	width      int       // widest line rendered, for erase padding
	lastAppend time.Time // last non-TTY appended line
	mem        uint64    // last sampled RAM figure
	lastMem    time.Time // when mem was sampled
}

// New builds a Printer writing in-place lines to w (the raw TTY
// target, usually os.Stderr) when tty is true, and appending through
// logf (usually log.Printf) otherwise. A nil logf is tolerated --
// non-TTY lines are simply dropped -- so tests and TTY-only use can
// pass nil.
func New(w io.Writer, tty bool, logf func(string, ...any)) *Printer {
	return &Printer{w: w, tty: tty, logf: logf, now: time.Now, readRAM: RAM}
}

// TTY reports whether the printer renders in place.
func (p *Printer) TTY() bool { return p.tty }

// Indexing renders one progress tick: the entry count plus the process
// RAM figure, resampled at most every memEvery. On a TTY the line
// replaces itself in place; otherwise it is appended through logf at
// most once per appendEvery.
func (p *Printer) Indexing(entries int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.now()
	if p.lastMem.IsZero() || t.Sub(p.lastMem) >= memEvery {
		p.mem = p.readRAM()
		p.lastMem = t
	}
	text := fmt.Sprintf("index: indexing... %d entries, %s ram", entries, FormatBytes(p.mem))
	if p.tty {
		p.render(stamp(t) + text)
		return
	}
	if !p.lastAppend.IsZero() && t.Sub(p.lastAppend) < appendEvery {
		return
	}
	p.lastAppend = t
	if p.logf != nil {
		p.logf("%s", text)
	}
}

// Done clears the in-place line (when one is rendered) and resets all
// render and throttle state, so a later rescan build starts fresh.
// Safe to call when nothing was rendered: no bytes are written.
func (p *Printer) Done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tty && p.active {
		p.erase()
	}
	p.line = ""
	p.active = false
	p.width = 0
	p.lastAppend = time.Time{}
	p.mem = 0
	p.lastMem = time.Time{}
}

// Write implements io.Writer so the app can log.SetOutput(p) in TTY
// mode. While a progress line is on screen, a log write erases it,
// writes b, and -- when b ends in '\n', which the log package
// guarantees -- redraws the progress line after it; a fragment without
// a trailing newline skips the redraw rather than mangle the row. The
// returned n/err cover writing b only, never the erase/redraw bytes.
// Inactive or non-TTY printers pass b through untouched.
func (p *Printer) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.tty || !p.active {
		return p.w.Write(b)
	}
	p.erase()
	n, err := p.w.Write(b)
	if len(b) > 0 && b[len(b)-1] == '\n' {
		p.render(p.line)
	}
	return n, err
}

// render draws full as the current in-place line, space-padded out to
// the widest line rendered so far so a shrinking line leaves no
// residue (caller holds mu; the write is best-effort).
func (p *Printer) render(full string) {
	pad := p.width - len(full)
	if pad < 0 {
		pad = 0
	}
	io.WriteString(p.w, "\r"+full+strings.Repeat(" ", pad))
	if len(full) > p.width {
		p.width = len(full)
	}
	p.line = full
	p.active = true
}

// erase blanks the rendered line and parks the cursor at column zero
// (caller holds mu).
func (p *Printer) erase() {
	io.WriteString(p.w, "\r"+strings.Repeat(" ", p.width)+"\r")
}

// stamp formats t exactly like the log package's default prefix, so
// the in-place line reads as a normal log line.
func stamp(t time.Time) string {
	return t.Format("2006/01/02 15:04:05 ")
}

// IsTerminal reports whether f is a terminal -- the tty argument New
// wants for real streams.
func IsTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// RAMString formats the current process memory figure for one-off log
// lines.
func RAMString() string { return FormatBytes(RAM()) }
