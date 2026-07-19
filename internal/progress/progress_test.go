package progress

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fixedBase is the fake clock's start; fixedStamp is its stamp form,
// pinning the log-package-compatible prefix format.
var fixedBase = time.Date(2009, 11, 10, 23, 0, 0, 0, time.UTC)

const fixedStamp = "2009/11/10 23:00:00 "

// fakeClock is a hand-advanced time source.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// fakeRAM counts reads and serves a settable value.
type fakeRAM struct {
	calls int
	v     uint64
}

func (r *fakeRAM) read() uint64 { r.calls++; return r.v }

// newTTYPrinter wires a TTY-mode Printer onto buf with fake seams (nil
// logf: the TTY path never appends).
func newTTYPrinter(buf *bytes.Buffer, clk *fakeClock, ram *fakeRAM) *Printer {
	p := New(buf, true, nil)
	p.now = clk.now
	p.readRAM = ram.read
	return p
}

// recordingLogf returns a logf sink appending formatted lines to the
// returned slice.
func recordingLogf(lines *[]string) func(string, ...any) {
	return func(format string, args ...any) {
		*lines = append(*lines, fmt.Sprintf(format, args...))
	}
}

func TestStamp(t *testing.T) {
	require.Equal(t, fixedStamp, stamp(fixedBase))
}

func TestTTYAccessor(t *testing.T) {
	var buf bytes.Buffer
	require.True(t, New(&buf, true, nil).TTY())
	require.False(t, New(&buf, false, nil).TTY())
}

func TestTTYFirstRender(t *testing.T) {
	tests := []struct {
		name    string
		entries int64
		ram     uint64
		want    string
	}{
		{"small count", 100, 384_200_000, "index: indexing... 100 entries, 384.2MB ram"},
		{"spec line", 12449259, 384_200_000, "index: indexing... 12449259 entries, 384.2MB ram"},
		{"gb figure", 5, 12_600_000_000, "index: indexing... 5 entries, 12.6GB ram"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			p := newTTYPrinter(&buf, &fakeClock{t: fixedBase}, &fakeRAM{v: tt.ram})
			p.Indexing(tt.entries)
			// First render: "\r" + stamped line, no padding yet.
			require.Equal(t, "\r"+fixedStamp+tt.want, buf.String())
		})
	}
}

func TestTTYPaddingAndWidthGrowth(t *testing.T) {
	var buf bytes.Buffer
	p := newTTYPrinter(&buf, &fakeClock{t: fixedBase}, &fakeRAM{v: 384_200_000})

	long := fixedStamp + "index: indexing... 12345 entries, 384.2MB ram"
	short := fixedStamp + "index: indexing... 9 entries, 384.2MB ram"
	longer := fixedStamp + "index: indexing... 123456789 entries, 384.2MB ram"

	p.Indexing(12345)
	require.Equal(t, "\r"+long, buf.String())

	// A shorter line pads out to the widest line rendered so far.
	buf.Reset()
	p.Indexing(9)
	require.Equal(t, "\r"+short+strings.Repeat(" ", len(long)-len(short)), buf.String())

	// A longer line grows the width and needs no padding of its own.
	buf.Reset()
	p.Indexing(123456789)
	require.Equal(t, "\r"+longer, buf.String())

	// The next shrink pads out to the NEW width.
	buf.Reset()
	p.Indexing(9)
	require.Equal(t, "\r"+short+strings.Repeat(" ", len(longer)-len(short)), buf.String())
}

func TestMemResampleCadence(t *testing.T) {
	var buf bytes.Buffer
	clk := &fakeClock{t: fixedBase}
	ram := &fakeRAM{v: 384_200_000}
	p := newTTYPrinter(&buf, clk, ram)

	p.Indexing(1)
	p.Indexing(2)
	require.Equal(t, 1, ram.calls, "same-instant ticks share one sample")

	// Inside the window the CACHED figure renders even though the
	// underlying value moved.
	ram.v = 1_500_000_000
	clk.advance(memEvery - time.Millisecond)
	buf.Reset()
	p.Indexing(3)
	require.Equal(t, 1, ram.calls, "still inside the resample window")
	require.Contains(t, buf.String(), "384.2MB ram")

	// At the window boundary the figure is resampled.
	clk.advance(time.Millisecond)
	buf.Reset()
	p.Indexing(4)
	require.Equal(t, 2, ram.calls, "window elapsed, fresh sample")
	require.Contains(t, buf.String(), "1.5GB ram")
}

func TestWriteInterception(t *testing.T) {
	var buf bytes.Buffer
	p := newTTYPrinter(&buf, &fakeClock{t: fixedBase}, &fakeRAM{v: 384_200_000})
	p.Indexing(100)
	line := fixedStamp + "index: indexing... 100 entries, 384.2MB ram"
	erase := "\r" + strings.Repeat(" ", len(line)) + "\r"

	// A newline-terminated log line: erase, payload, redraw -- and n
	// counts the payload only, never the erase/redraw bytes.
	buf.Reset()
	payload := "2026/07/18 12:00:01 hotkey: alt+space summons the searchbar\n"
	n, err := p.Write([]byte(payload))
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.Equal(t, erase+payload+"\r"+line, buf.String())

	// A fragment without a trailing newline: erase, payload, NO
	// redraw.
	buf.Reset()
	frag := "partial fragment"
	n, err = p.Write([]byte(frag))
	require.NoError(t, err)
	require.Equal(t, len(frag), n)
	require.Equal(t, erase+frag, buf.String())
}

func TestWritePassthrough(t *testing.T) {
	// TTY but nothing rendered yet: bytes pass through untouched.
	var buf bytes.Buffer
	p := newTTYPrinter(&buf, &fakeClock{t: fixedBase}, &fakeRAM{v: 1})
	n, err := p.Write([]byte("plain\n"))
	require.NoError(t, err)
	require.Equal(t, len("plain\n"), n)
	require.Equal(t, "plain\n", buf.String())

	// Non-TTY printers never intercept, even mid-build (Indexing off a
	// TTY writes nothing to w, so w holds exactly the payload).
	var out bytes.Buffer
	q := New(&out, false, nil)
	q.now = (&fakeClock{t: fixedBase}).now
	q.readRAM = (&fakeRAM{v: 1}).read
	q.Indexing(1)
	n, err = q.Write([]byte("log line\n"))
	require.NoError(t, err)
	require.Equal(t, len("log line\n"), n)
	require.Equal(t, "log line\n", out.String())
}

func TestDoneClearsAndResets(t *testing.T) {
	var buf bytes.Buffer
	clk := &fakeClock{t: fixedBase}
	ram := &fakeRAM{v: 384_200_000}
	p := newTTYPrinter(&buf, clk, ram)

	p.Indexing(123456789)
	wide := fixedStamp + "index: indexing... 123456789 entries, 384.2MB ram"
	buf.Reset()
	p.Done()
	require.Equal(t, "\r"+strings.Repeat(" ", len(wide))+"\r", buf.String())

	// Inactive again: Write passes through.
	buf.Reset()
	_, err := p.Write([]byte("after\n"))
	require.NoError(t, err)
	require.Equal(t, "after\n", buf.String())

	// A following build renders fresh: the width reset means the much
	// shorter line carries no stale padding, and the RAM figure is
	// resampled despite the unmoved clock.
	require.Equal(t, 1, ram.calls)
	buf.Reset()
	p.Indexing(7)
	require.Equal(t, "\r"+fixedStamp+"index: indexing... 7 entries, 384.2MB ram", buf.String())
	require.Equal(t, 2, ram.calls, "Done cleared the mem sample")
}

func TestDoneIdleIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	p := newTTYPrinter(&buf, &fakeClock{t: fixedBase}, &fakeRAM{v: 1})
	p.Done()
	require.Zero(t, buf.Len(), "idle Done writes nothing")

	// Non-TTY Done never writes either.
	var out bytes.Buffer
	q := New(&out, false, nil)
	q.Done()
	require.Zero(t, out.Len())
}

func TestNonTTYThrottle(t *testing.T) {
	var out bytes.Buffer
	var lines []string
	clk := &fakeClock{t: fixedBase}
	p := New(&out, false, recordingLogf(&lines))
	p.now = clk.now
	p.readRAM = (&fakeRAM{v: 384_200_000}).read

	p.Indexing(1) // first tick always logs
	p.Indexing(2) // same instant: suppressed
	clk.advance(appendEvery - time.Millisecond)
	p.Indexing(3) // still inside the window: suppressed
	clk.advance(time.Millisecond)
	p.Indexing(4) // window elapsed: logs

	require.Equal(t, []string{
		"index: indexing... 1 entries, 384.2MB ram",
		"index: indexing... 4 entries, 384.2MB ram",
	}, lines)
	require.Zero(t, out.Len(), "non-TTY Indexing never writes to w")
}

func TestDoneResetsNonTTYThrottle(t *testing.T) {
	var lines []string
	clk := &fakeClock{t: fixedBase}
	p := New(&bytes.Buffer{}, false, recordingLogf(&lines))
	p.now = clk.now
	p.readRAM = (&fakeRAM{v: 384_200_000}).read

	p.Indexing(1)
	p.Indexing(2) // suppressed
	p.Done()
	p.Indexing(3) // post-Done: logs immediately at the same instant
	require.Len(t, lines, 2)
	require.Equal(t, "index: indexing... 3 entries, 384.2MB ram", lines[1])
}

func TestNonTTYNilLogfDoesNotPanic(t *testing.T) {
	var out bytes.Buffer
	p := New(&out, false, nil) // default now/readRAM seams on purpose
	p.Indexing(1)
	p.Done()
	require.Zero(t, out.Len())
}

func TestIsTerminalRegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notatty")
	require.NoError(t, err)
	defer f.Close()
	require.False(t, IsTerminal(f))
}

func TestRAMString(t *testing.T) {
	s := RAMString()
	require.NotEmpty(t, s)
	require.True(t, strings.HasSuffix(s, "MB") || strings.HasSuffix(s, "GB"),
		"RAMString() = %q, want an MB or GB suffix", s)
}

// TestGoroutineSafety exists for the race detector: ticks, intercepted
// log writes and Done from concurrent goroutines must serialize under
// the mutex.
func TestGoroutineSafety(t *testing.T) {
	var buf bytes.Buffer
	p := newTTYPrinter(&buf, &fakeClock{t: fixedBase}, &fakeRAM{v: 384_200_000})
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				p.Indexing(int64(g*1000 + i))
				_, _ = p.Write([]byte("hotkey: log line\n"))
			}
		}(g)
	}
	wg.Wait()
	p.Done()
	require.False(t, p.active, "Done leaves the printer inactive")
}
