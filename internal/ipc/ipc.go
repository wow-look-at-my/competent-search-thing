// Package ipc implements the single-instance layer: a tiny line-based
// protocol over a unix domain socket that lets any other process --
// the CLI subcommands in internal/cli, or whatever keybinding
// mechanism a desktop environment offers -- summon the running
// searchbar. This is the Wayland-friendly counterpart to the X11
// global hotkey: compositors that refuse key grabs can still bind a
// key to "competent-search-thing toggle".
//
// # Protocol
//
// One request per connection, one newline-terminated line each way.
// Two wire shapes coexist; the server picks per request by sniffing
// the request line's first non-space byte: '{' selects the JSON (v2)
// shape, anything else takes the legacy (v1) path byte-for-byte.
//
// JSON (v2), what Send speaks: the request is one JSON object,
// {"cmd":"toggle"} -- cmds toggle, show, hide, config, version, ping
// -- and the response is one JSON object:
//
//	{"ok":true}                             ping
//	{"ok":true,"version":"1.2.3"}           version
//	{"ok":true,"accepted":"toggle"}         toggle/show/hide accepted
//	{"ok":false,"error":"not ready"}        no handler wired yet (booting)
//	{"ok":false,"error":"unknown command"}  unrecognized cmd value
//	{"ok":false,"error":"invalid request"}  '{' line that is not valid JSON
//
// Unknown JSON fields are IGNORED in requests (json.Unmarshal into a
// struct does that naturally) -- that is the tolerance contract that
// lets the protocol grow fields without breaking older daemons.
// Response readers must apply the same tolerance.
//
// Legacy (v1): the request is the bare command word and the response
// is "ok", the bare version string (for "version"), or "err <reason>"
// ("err not ready", "err unknown command").
//
// # Version skew
//
// The CLI and the daemon can differ across an upgrade; both
// directions degrade gracefully:
//
//   - old client vs NEW daemon: the bare command word takes the
//     legacy path and the replies are byte-identical to a pre-JSON
//     daemon's.
//   - NEW client vs old daemon: the old daemon answers the JSON
//     request line with its legacy "err unknown command" string;
//     Send detects exactly that reply, redials (one request per
//     connection) and retries ONCE with the legacy bare-word
//     request, mapping the legacy reply into the same Reply shape
//     (Reply.Legacy reports the fallback).
//
// In both shapes the acknowledgement is written BEFORE the
// toggle/show/hide handler runs, so an app whose main thread is
// briefly stalled (startup indexing) acknowledges instantly instead
// of timing the client out; an "ok" acknowledgement means accepted,
// not completed.
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
	CmdConfig  = "config"
	CmdVersion = "version"
	CmdPing    = "ping"
)

// Legacy (v1) response lines (the legacy "version" response is the
// version string itself and has no constant).
const (
	// ReplyOK acknowledges an accepted toggle/show/hide (executed
	// right after the reply is written) or an executed ping.
	ReplyOK = "ok"
	// ReplyNotReady answers toggle/show/hide while no handler is
	// wired yet (the app is still booting).
	ReplyNotReady = "err not ready"
	// replyUnknown answers anything that is not a known command. Its
	// exact bytes double as the client's old-daemon detector: only a
	// pre-JSON daemon answers a JSON request with this line.
	replyUnknown = "err unknown command"
)

// JSON (v2) Response.Error texts.
const (
	errNotReady       = "not ready"
	errUnknownCommand = "unknown command"
	errInvalidRequest = "invalid request"
)

// Request is the JSON (v2) wire request: one object per line. Unknown
// fields are ignored by the server -- the forward-compatibility
// contract.
type Request struct {
	Cmd string `json:"cmd"`
}

// Response is the JSON (v2) wire response: one object per line, in
// exactly one of the shapes listed in the package comment. Readers
// must ignore unknown fields, the same tolerance the server applies
// to requests.
type Response struct {
	OK       bool   `json:"ok"`
	Accepted string `json:"accepted,omitempty"` // the acked command; ack = accepted, not completed
	Version  string `json:"version,omitempty"`  // the version answer
	Error    string `json:"error,omitempty"`    // set when OK is false
}

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
