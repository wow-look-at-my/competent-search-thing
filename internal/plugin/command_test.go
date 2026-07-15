package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// requireSh skips the test when /bin/sh is unavailable (the command
// transport fixtures are POSIX shell scripts).
func requireSh(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available on this system")
	}
}

// writeScript drops an executable /bin/sh script into dir.
func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755))
}

// commandManifest builds a ready-to-use command manifest rooted at dir.
func commandManifest(dir string, argv ...string) *Manifest {
	return &Manifest{
		ID:      "cmd",
		Name:    "Cmd",
		Type:    TypeCommand,
		Command: &CommandSpec{Argv: argv},
		Dir:     dir,
	}
}

func testRequest() Request {
	return Request{
		V:        ProtocolVersion,
		Query:    "!cmd 2+2",
		Stripped: "2+2",
		Gen:      7,
		Targeted: true,
		Bang:     "cmd",
		Settings: json.RawMessage("{}"),
	}
}

func TestCommandTransportRoundTrip(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	// The script tees stdin into a file IN ITS WORKING DIRECTORY (so
	// finding req.json in dir also proves cwd == manifest dir) and
	// answers on stdout. argv[0] is relative with a separator, proving
	// resolution against the manifest dir.
	writeScript(t, dir, "run.sh", `cat > req.json
printf '%s' '{"v":1,"results":[{"title":"four","score":100}]}'
`)
	tr := &commandTransport{m: commandManifest(dir, "./run.sh")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := tr.roundTrip(ctx, testRequest())
	require.NoError(t, err)
	require.Equal(t, 1, resp.V)
	require.Len(t, resp.Results, 1)
	require.Equal(t, "four", resp.Results[0].Title)

	// The exact request JSON the plugin received.
	data, err := os.ReadFile(filepath.Join(dir, "req.json"))
	require.NoError(t, err)
	var got Request
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, testRequest(), got)
	require.Nil(t, got.Context, "undeclared context stays absent")
}

func TestCommandTransportPathLookup(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	// argv[0] "sh" has no separator: PATH lookup. The script argument
	// is relative and resolves against cwd = manifest dir.
	writeScript(t, dir, "run.sh", `printf '%s' '{"results":[{"title":"ok"}]}'`)
	tr := &commandTransport{m: commandManifest(dir, "sh", "run.sh")}
	resp, err := tr.roundTrip(context.Background(), testRequest())
	require.NoError(t, err)
	require.Len(t, resp.Results, 1)
}

func TestCommandTransportAbsoluteProgram(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	writeScript(t, dir, "run.sh", `printf '%s' '{"results":[]}'`)
	tr := &commandTransport{m: commandManifest(dir, filepath.Join(dir, "run.sh"))}
	resp, err := tr.roundTrip(context.Background(), testRequest())
	require.NoError(t, err)
	require.Empty(t, resp.Results)
}

func TestCommandTransportTimeoutKills(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	writeScript(t, dir, "run.sh", `sleep 5`)
	tr := &commandTransport{m: commandManifest(dir, "./run.sh")}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := tr.roundTrip(ctx, testRequest())
	elapsed := time.Since(start)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, elapsed, 2*time.Second, "kill must be prompt, not wait for the child")
}

func TestCommandTransportExitError(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	writeScript(t, dir, "run.sh", `echo "boom: bad settings" >&2
exit 1
`)
	tr := &commandTransport{m: commandManifest(dir, "./run.sh")}
	_, err := tr.roundTrip(context.Background(), testRequest())
	require.Error(t, err)
	require.Contains(t, err.Error(), "exit status 1")
	require.Contains(t, err.Error(), "boom: bad settings", "stderr snippet included")
}

func TestCommandTransportInvalidJSON(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	writeScript(t, dir, "run.sh", `echo "warning: guessing" >&2
printf 'not json at all'
`)
	tr := &commandTransport{m: commandManifest(dir, "./run.sh")}
	_, err := tr.roundTrip(context.Background(), testRequest())
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid response JSON")
	require.Contains(t, err.Error(), "warning: guessing")
}

func TestCommandTransportOversizedOutput(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	writeScript(t, dir, "run.sh", `head -c 1200000 /dev/zero`)
	tr := &commandTransport{m: commandManifest(dir, "./run.sh")}
	_, err := tr.roundTrip(context.Background(), testRequest())
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds")
}

func TestCommandTransportVersionRejected(t *testing.T) {
	requireSh(t)
	dir := t.TempDir()
	writeScript(t, dir, "run.sh", `printf '%s' '{"v":2,"results":[]}'`)
	tr := &commandTransport{m: commandManifest(dir, "./run.sh")}
	_, err := tr.roundTrip(context.Background(), testRequest())
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported response version 2")
}

func TestResolveProgram(t *testing.T) {
	abs := string(filepath.Separator) + filepath.Join("abs", "prog")
	tests := []struct {
		name, prog, want string
	}{
		{name: "absolute untouched", prog: abs, want: abs},
		{name: "separator resolves to dir", prog: "sub/prog", want: filepath.Join("/base", "sub", "prog")},
		{name: "dot slash resolves to dir", prog: "./prog", want: filepath.Join("/base", "prog")},
		{name: "bare name via PATH", prog: "prog", want: "prog"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, resolveProgram(tt.prog, "/base"))
		})
	}
}

func TestTruncWriter(t *testing.T) {
	w := &truncWriter{max: 5}
	n, err := w.Write([]byte("abc"))
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.False(t, w.truncated())
	n, err = w.Write([]byte("defgh"))
	require.NoError(t, err)
	require.Equal(t, 5, n, "reported length is the full write")
	require.True(t, w.truncated())
	require.Equal(t, "abcde", w.buf.String(), "storage capped at max")
	require.Equal(t, int64(8), w.total)
}

func TestStderrSnippet(t *testing.T) {
	require.Equal(t, "", stderrSnippet(nil))
	require.Equal(t, "", stderrSnippet([]byte("  \n\t ")))
	require.Equal(t, "; stderr: a  b", stderrSnippet([]byte("a\x00\nb\n")))
	long := strings.Repeat("x", 1000)
	got := stderrSnippet([]byte(long))
	require.Len(t, got, len("; stderr: ")+stderrSnippetRunes)
}
