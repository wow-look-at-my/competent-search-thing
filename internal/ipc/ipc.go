// Package ipc implements the single-instance layer: a tiny JSON-line
// protocol over a unix domain socket that lets any other process --
// the CLI subcommands in internal/cli, or whatever keybinding
// mechanism a desktop environment offers -- summon the running
// searchbar. This is the Wayland-friendly counterpart to the X11
// global hotkey: compositors that refuse key grabs can still bind a
// key to "competent-search-thing toggle".
//
// # Protocol
//
// One request per connection, one newline-terminated line each way,
// both lines JSON objects. The request is {"cmd":"toggle"} -- cmds
// toggle, show, hide, config, quit, version, ping -- and the response
// is one JSON object:
//
//	{"ok":true}                             ping
//	{"ok":true,"version":"1.2.3","build":"abcdef123456"}  version (build = the
//	                                        vcs-revision stamp; omitted on
//	                                        unstamped dev builds)
//	{"ok":true,"accepted":"toggle"}         toggle/show/hide/config/quit accepted
//	{"ok":false,"error":"not ready"}        no handler wired yet (booting)
//	{"ok":false,"error":"unknown command"}  unrecognized cmd value
//	{"ok":false,"error":"invalid request"}  the line does not parse as JSON
//
// A request line that does not parse as a JSON request -- including
// the bare command words of the deleted pre-JSON line protocol --
// earns the invalid-request error, and no handler runs.
//
// Unknown JSON fields are IGNORED in requests (json.Unmarshal into a
// struct does that naturally) -- that is the tolerance contract that
// lets the protocol grow fields without breaking older daemons.
// Response readers must apply the same tolerance.
//
// The acknowledgement is written BEFORE the toggle/show/hide handler
// runs, so an app whose main thread is briefly stalled (startup
// indexing) acknowledges instantly instead of timing the client out;
// an OK acknowledgement means accepted, not completed.
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
	CmdQuit    = "quit"
	CmdVersion = "version"
	CmdPing    = "ping"
)

// Response.Error texts.
const (
	errNotReady       = "not ready"
	errUnknownCommand = "unknown command"
	errInvalidRequest = "invalid request"
)

// Request is the JSON wire request: one object per line. Unknown
// fields are ignored by the server -- the forward-compatibility
// contract.
type Request struct {
	Cmd string `json:"cmd"`
}

// Response is the JSON wire response: one object per line, in exactly
// one of the shapes listed in the package comment. Readers must
// ignore unknown fields, the same tolerance the server applies to
// requests -- that tolerance is what made adding the build field safe
// in both directions.
type Response struct {
	OK       bool   `json:"ok"`
	Accepted string `json:"accepted,omitempty"` // the acked command; ack = accepted, not completed
	Version  string `json:"version,omitempty"`  // the version answer
	// Build rides the version answer: the binary's vcs-revision stamp
	// (see OwnBuild). The app version constant never changes across
	// releases, so this is the version-skew discriminator behind the
	// new-instance-wins takeover. Empty (omitted) on unstamped dev
	// builds and on daemons predating the field.
	Build string `json:"build,omitempty"`
	Error string `json:"error,omitempty"` // set when OK is false
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
