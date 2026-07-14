package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Transport limits shared by both transports.
const (
	// maxResponseBytes caps a plugin's response body (command stdout
	// or HTTP body). Anything larger is an error, never truncated-and-
	// parsed.
	maxResponseBytes = 1 << 20 // 1 MiB
	// maxStderrBytes is how much command stderr is retained for error
	// messages.
	maxStderrBytes = 8 << 10 // 8 KiB
	// commandWaitDelay bounds how long a killed command may linger
	// (e.g. a child holding the output pipes open) before Wait gives
	// up on it.
	commandWaitDelay = 250 * time.Millisecond
	// stderrSnippetRunes caps the stderr excerpt quoted in errors.
	stderrSnippetRunes = 400
)

// transport delivers one Request to an external plugin and decodes its
// Response. Implementations must honor ctx for cancellation/timeout
// and must be safe for concurrent use.
type transport interface {
	roundTrip(ctx context.Context, req Request) (*Response, error)
}

// commandTransport runs one subprocess per query: the request JSON is
// written to stdin (then stdin is closed), the response JSON is read
// from stdout. No shell is involved -- the manifest's argv is executed
// directly, with the manifest directory as the working directory.
type commandTransport struct {
	m *Manifest
}

func (t *commandTransport) roundTrip(ctx context.Context, req Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}
	argv := t.m.Command.Argv
	prog := resolveProgram(argv[0], t.m.Dir)

	stdout := &truncWriter{max: maxResponseBytes}
	stderr := &truncWriter{max: maxStderrBytes}
	cmd := exec.CommandContext(ctx, prog, argv[1:]...)
	cmd.Dir = t.m.Dir
	cmd.Stdin = bytes.NewReader(body) // os/exec closes the pipe at EOF
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = commandWaitDelay // hard deadline after the kill

	runErr := cmd.Run()
	snippet := stderrSnippet(stderr.buf.Bytes())
	if ctx.Err() != nil {
		return nil, fmt.Errorf("command killed: %w%s", ctx.Err(), snippet)
	}
	if runErr != nil {
		return nil, fmt.Errorf("command failed: %w%s", runErr, snippet)
	}
	if stdout.truncated() {
		return nil, fmt.Errorf("command output exceeds %d bytes (got at least %d)%s",
			maxResponseBytes, stdout.total, snippet)
	}
	return decodeResponse(stdout.buf.Bytes(), snippet)
}

// resolveProgram implements the manifest rule for command.argv[0]: an
// absolute path runs as-is, a program containing a path separator
// resolves relative to the manifest directory, and a bare name goes
// through the usual PATH lookup.
func resolveProgram(prog, dir string) string {
	if filepath.IsAbs(prog) {
		return prog
	}
	if strings.ContainsRune(prog, '/') || strings.ContainsRune(prog, filepath.Separator) {
		return filepath.Join(dir, prog)
	}
	return prog
}

// decodeResponse parses a response body and enforces the protocol
// version. snippet (possibly empty) is appended to errors so command
// failures carry their stderr.
func decodeResponse(data []byte, snippet string) (*Response, error) {
	var resp Response
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("invalid response JSON: %w%s", err, snippet)
	}
	if resp.V != 0 && resp.V != ProtocolVersion {
		return nil, fmt.Errorf("unsupported response version %d (want %d)%s",
			resp.V, ProtocolVersion, snippet)
	}
	return &resp, nil
}

// stderrSnippet renders captured stderr as an error suffix: control
// characters become spaces (log-injection defense), the text is
// trimmed and capped. Empty stderr yields the empty string.
func stderrSnippet(b []byte) string {
	s := strings.TrimSpace(stripControl(string(b)))
	if s == "" {
		return ""
	}
	return "; stderr: " + truncateRunes(s, stderrSnippetRunes)
}

// truncWriter stores up to max bytes and counts (while discarding) the
// rest, so a runaway process cannot balloon memory yet still drains
// and exits instead of blocking on a full pipe.
type truncWriter struct {
	max   int
	buf   bytes.Buffer
	total int64
}

func (w *truncWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.total += int64(n)
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		w.buf.Write(p)
	}
	return n, nil
}

func (w *truncWriter) truncated() bool { return w.total > int64(w.max) }
