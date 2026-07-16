// Package ipc implements the single-instance layer: a tiny line-based
// protocol over a unix domain socket that lets any other process --
// the CLI subcommands in internal/cli, or whatever keybinding
// mechanism a desktop environment offers -- summon the running
// searchbar. This is the Wayland-friendly counterpart to the X11
// global hotkey: compositors that refuse key grabs can still bind a
// key to "competent-search-thing toggle".
//
// Protocol: one request per connection. The client writes a single
// command line ("toggle", "show", "hide", "version" or "ping",
// newline-terminated) and reads exactly one response line back: "ok"
// for an executed command, the bare version string for "version", or
// "err <reason>" -- "err not ready" while the app is still booting (or
// for an unwired handler), "err unknown command" otherwise.
package ipc

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnvSocket names the environment variable that overrides the
// computed socket path (tests and unusual setups).
const EnvSocket = "COMPETENT_SEARCH_SOCKET"

// The wire commands a client may send.
const (
	CmdToggle  = "toggle"
	CmdShow    = "show"
	CmdHide    = "hide"
	CmdVersion = "version"
	CmdPing    = "ping"
)

// Canned response lines (the "version" response is the version string
// itself and has no constant).
const (
	// ReplyOK acknowledges an executed toggle/show/hide/ping.
	ReplyOK = "ok"
	// ReplyNotReady answers toggle/show/hide while no handler is
	// wired yet (the app is still booting).
	ReplyNotReady = "err not ready"
	// replyUnknown answers anything that is not a known command.
	replyUnknown = "err unknown command"
)

// SocketPath returns the unix socket path the app listens on and
// clients talk to: the EnvSocket override when set, else
// $XDG_RUNTIME_DIR/competent-search-thing.sock, else a per-user name
// under os.TempDir(). getenv is injectable for tests; production
// passes os.Getenv.
func SocketPath(getenv func(string) string) string {
	if p := getenv(EnvSocket); p != "" {
		return p
	}
	if dir := getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "competent-search-thing.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("competent-search-thing-%d.sock", os.Getuid()))
}
