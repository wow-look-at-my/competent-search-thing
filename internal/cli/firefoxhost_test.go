package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/ffext"
)

// testFfextSocketEnv points the relay at a private bridge socket path
// (the testSocketEnv twin for the SECOND socket).
func testFfextSocketEnv(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "clif")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "f.sock")
	t.Setenv(ffext.EnvSocket, path)
	return path
}

// runFirefoxHost executes the subcommand with injected stdio on its
// own goroutine and returns the exit-code channel.
func runFirefoxHost(t *testing.T, e *env, args []string) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		root := newRoot(e)
		root.SetArgs(args)
		var out, errOut bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errOut)
		err := root.Execute()
		// The relay must never write to cobra's out: that stream is
		// the native-messaging frame channel in production.
		if out.Len() != 0 {
			t.Errorf("firefox-host wrote %q to cobra out", out.String())
		}
		done <- err
	}()
	return done
}

func TestFirefoxHostExitsOnStdinEOF(t *testing.T) {
	testSocketEnv(t)     // isolation: never the real IPC socket
	testFfextSocketEnv(t) // no bridge listening: the relay must still run
	gui := &guiRecorder{}
	e := &env{version: testVersion, runGUI: gui.run,
		hostIn: strings.NewReader(""), hostOut: io.Discard}
	done := runFirefoxHost(t, e, []string{"firefox-host"})
	select {
	case err := <-done:
		require.NoError(t, err, "stdin EOF is the clean shutdown")
	case <-time.After(3 * time.Second):
		t.Fatal("firefox-host did not exit on stdin EOF")
	}
	require.Zero(t, gui.count(), "the relay must never boot the GUI")
}

func TestFirefoxHostIgnoresFirefoxArgs(t *testing.T) {
	testSocketEnv(t)
	testFfextSocketEnv(t)
	e := &env{version: testVersion, runGUI: (&guiRecorder{}).run,
		hostIn: strings.NewReader(""), hostOut: io.Discard}
	// Firefox passes [manifest path, extension id]; both are ignored.
	done := runFirefoxHost(t, e, []string{"firefox-host", "/tmp/manifest.json", ffext.ExtensionID})
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("firefox-host did not exit")
	}
}

func TestFirefoxHostRelaysToBridge(t *testing.T) {
	testSocketEnv(t)
	sock := testFfextSocketEnv(t)
	srv, err := ffext.Listen(sock, ffext.ServerOptions{Logf: t.Logf})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	e := &env{version: testVersion, runGUI: (&guiRecorder{}).run, hostIn: inR, hostOut: outW}
	done := runFirefoxHost(t, e, []string{"firefox-host"})

	// The bridge asks the fresh connection for its tabs; the request
	// frame arrives on the relay's stdout.
	frame := readHostFrame(t, outR)
	require.Equal(t, ffext.MsgListTabs, frame["type"])

	// The extension side answers through stdin; the bridge snapshot
	// fills -- the whole relay loop end-to-end through the subcommand.
	reply := map[string]any{
		"id": frame["id"], "ok": true,
		"tabs": []map[string]any{{
			"id": 5, "windowId": 2, "title": "CLI relay", "url": "https://cli.example/",
			"pinned": false, "lastAccessed": 99, "active": true,
		}},
	}
	raw, err := json.Marshal(reply)
	require.NoError(t, err)
	require.NoError(t, ffext.WriteFrame(inW, raw, ffext.MaxOutFrame))

	deadline := time.Now().Add(3 * time.Second)
	for {
		tabs, _ := srv.Tabs()
		if len(tabs) == 1 {
			require.Equal(t, "CLI relay", tabs[0].Title)
			break
		}
		require.False(t, time.Now().After(deadline), "the reply never reached the bridge")
		time.Sleep(5 * time.Millisecond)
	}

	require.NoError(t, inW.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("firefox-host did not exit after stdin EOF")
	}
}

// readHostFrame reads one native-messaging frame off the relay's
// stdout with a deadline.
func readHostFrame(t *testing.T, r io.Reader) map[string]any {
	t.Helper()
	type res struct {
		body []byte
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		body, err := ffext.ReadFrame(r, ffext.MaxInFrame)
		ch <- res{body, err}
	}()
	select {
	case got := <-ch:
		require.NoError(t, got.err)
		var m map[string]any
		require.NoError(t, json.Unmarshal(got.body, &m))
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("no frame from the relay")
		return nil
	}
}
