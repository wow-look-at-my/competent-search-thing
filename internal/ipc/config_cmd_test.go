package ipc

// The config command's wire tests live in their own file (the shared
// helpers are in ipc_test.go): the command rides handlerFor exactly
// like toggle/show/hide, so only its own JSON shapes need pinning.

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfigCommandJSONRoundTrip(t *testing.T) {
	s, path := listen(t, "1.0")
	got := make(chan string, 8)
	s.SetHandlers(Handlers{Config: func() { got <- "config" }})

	rep, err := Send(path, CmdConfig, time.Second)
	require.NoError(t, err)
	require.True(t, rep.OK)
	require.Equal(t, CmdConfig, rep.Accepted, "the JSON ack names the accepted command")
	awaitSignal(t, got, "config")

	require.JSONEq(t, `{"ok":true,"accepted":"config"}`,
		rawExchange(t, path, `{"cmd":"config"}`), "the raw JSON reply shape")
}

func TestConfigCommandNotReady(t *testing.T) {
	_, path := listen(t, "1.0")
	// Before SetHandlers -- and equally with Handlers whose Config
	// member is nil -- the command answers not-ready.
	rep, err := Send(path, CmdConfig, time.Second)
	require.NoError(t, err)
	require.False(t, rep.OK)
	require.True(t, rep.NotReady())
}

func TestConfigCommandNilMemberIsNotReady(t *testing.T) {
	s, path := listen(t, "1.0")
	s.SetHandlers(Handlers{Toggle: func() {}}) // wired, but no Config
	rep, err := Send(path, CmdConfig, time.Second)
	require.NoError(t, err)
	require.True(t, rep.NotReady())
}

func TestUnknownCommandReplyHelper(t *testing.T) {
	// A daemon predating the config command answers unknown-command;
	// Reply.UnknownCommand is how callers branch on that without wire
	// strings. Simulated with a fake JSON daemon so this keeps passing
	// after the real server learns the command.
	path := testSocket(t)
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 256)
			_, _ = conn.Read(buf)
			_, _ = conn.Write([]byte(`{"ok":false,"error":"unknown command"}` + "\n"))
			_ = conn.Close()
		}
	}()

	rep, err := Send(path, CmdConfig, time.Second)
	require.NoError(t, err)
	require.True(t, rep.UnknownCommand())
	require.False(t, rep.NotReady())

	// The current server, with the handler wired, is NOT unknown.
	s, real := listen(t, "1.0")
	s.SetHandlers(Handlers{Config: func() {}})
	rep, err = Send(real, CmdConfig, time.Second)
	require.NoError(t, err)
	require.False(t, rep.UnknownCommand())
}
