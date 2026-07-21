// Package ffext is the Firefox companion-extension bridge: the pure
// half of "activate the exact tab you picked" (the WebExtension lives
// in the repo's webextension/ directory, the app wiring in
// internal/app's ffext.go, the relay subcommand in internal/cli).
//
// # Topology
//
// The shipped WebExtension owns a native-messaging port to a host
// process ("<binary> firefox-host", spawned by Firefox via a generated
// wrapper script). The host is a dumb relay between two framings:
//
//	Firefox extension  <-- native-messaging frames -->  host process
//	host process       <-- JSON lines (unix socket) -->  app bridge Server
//
// The app-side Server initiates every request (listTabs, activate);
// the extension answers with correlated replies and may push
// unsolicited tabsChanged updates. Both hops carry the SAME JSON
// message shapes -- the host only reframes bytes, it never parses
// message semantics.
//
// # Wire shapes (protocol version 1)
//
//	-> {"id":N,"type":"listTabs"}
//	<- {"id":N,"ok":true,"tabs":[{"id":..,"windowId":..,"title":..,
//	     "url":..,"pinned":..,"lastAccessed":..,"active":..,
//	     "favIconUrl":..}, ...]}
//	-> {"id":N,"type":"activate","tabId":T,"windowId":W}
//	<- {"id":N,"ok":true} | {"id":N,"ok":false,"error":"..."}
//	<- {"type":"tabsChanged","tabs":[...]}          (unsolicited push)
//
// Unknown fields are ignored on both sides (the internal/ipc tolerance
// contract). The webextension/logic.mjs constants must stay in
// lockstep with this package's; internal/ffext/sync_test.go enforces
// it.
package ffext

import (
	"fmt"
	"os"
	"path/filepath"
)

// HostName is the native-messaging host name: the manifest's "name"
// field and the string the extension passes to runtime.connectNative.
// It must match Firefox's host-name rule ^\w+(\.\w+)*$ and, on
// windows, doubles as the registry key name.
const HostName = "competent_search_thing"

// ExtensionID is the pinned WebExtension id
// (browser_specific_settings.gecko.id in webextension/manifest.json)
// the native manifest's allowed_extensions names.
const ExtensionID = "tab-activation@competent-search-thing.pazer.build"

// ProtocolVersion is the bridge protocol version shared with
// webextension/logic.mjs (PROTOCOL_VERSION there; the sync test pins
// the two equal).
const ProtocolVersion = 1

// EnvSocket names the environment variable that overrides the computed
// bridge socket path (tests and unusual setups) -- the ipc.EnvSocket
// twin for the SECOND socket.
const EnvSocket = "COMPETENT_SEARCH_FFEXT_SOCKET"

// The wire message types (shared with webextension/logic.mjs; the
// sync test greps for the literals).
const (
	MsgListTabs    = "listTabs"
	MsgActivate    = "activate"
	MsgTabsChanged = "tabsChanged"
)

// SocketPath returns the unix socket path the bridge listens on and
// the host relay dials: the EnvSocket override when set, else
// $XDG_RUNTIME_DIR/competent-search-thing-ffext.sock, else a per-user
// name under os.TempDir(). getenv is injectable for tests; production
// passes os.Getenv (the ipc.SocketPath mirror).
func SocketPath(getenv func(string) string) string {
	if p := getenv(EnvSocket); p != "" {
		return p
	}
	if dir := getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "competent-search-thing-ffext.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("competent-search-thing-ffext-%d.sock", os.Getuid()))
}

// Tab is one live Firefox tab in the bridge's merged snapshot. Conn
// tags the host connection that reported it (multi-profile Firefox =
// multiple hosts), so an activation can be routed back to the owning
// connection.
type Tab struct {
	Conn         int64
	ID           int64
	WindowID     int64
	Title        string
	URL          string
	Pinned       bool
	LastAccessed int64 // milliseconds since the Unix epoch, 0 unknown
	Active       bool
	// FavIconURL is the tab's favicon as Firefox reports it (an http(s)
	// URL or a data: URI), "" when the browser has none. The app layer
	// feeds it to the icon resolver's favicon hint side-channel; an
	// older extension simply never sends the field (the tolerance
	// contract), so it stays "".
	FavIconURL string
}

// request is one bridge->extension message (relayed verbatim by the
// host): a correlated listTabs or activate.
type request struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	TabID    int64  `json:"tabId,omitempty"`
	WindowID int64  `json:"windowId,omitempty"`
}

// inbound is one extension->bridge message: a correlated reply (ID
// set) or a tabsChanged push (Type set, no ID). Unknown fields are
// ignored (the tolerance contract).
type inbound struct {
	ID    int64     `json:"id"`
	Type  string    `json:"type"`
	OK    bool      `json:"ok"`
	Error string    `json:"error"`
	Tabs  []wireTab `json:"tabs"`
}

// wireTab is one tab row as the extension reports it (the tabs.Tab
// projection logic.mjs's tabRow builds). lastAccessed arrives as a
// JSON number that may carry a fractional part, hence float64.
type wireTab struct {
	ID           int64   `json:"id"`
	WindowID     int64   `json:"windowId"`
	Title        string  `json:"title"`
	URL          string  `json:"url"`
	Pinned       bool    `json:"pinned"`
	LastAccessed float64 `json:"lastAccessed"`
	Active       bool    `json:"active"`
	FavIconURL   string  `json:"favIconUrl"`
}
