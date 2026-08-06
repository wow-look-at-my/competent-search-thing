# internal/ffext

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/ffext` -- the Firefox companion-extension bridge, pure
and headless-tested (real temp sockets, scripted fakes, pipe-driven
stdio): the pure half of switch-to-tab (webextension/ is the
extension, internal/app ffext.go the wiring, internal/cli
firefox-host the relay entry). Topology: extension <->
native-messaging frames <-> host process <-> JSON lines on a SECOND
unix socket <-> the app's bridge Server; the host only reframes
bytes, both hops carry ONE message shape (requests
{id,type:listTabs|activate,tabId,windowId}; replies {id,ok,tabs?|
error}; unsolicited pushes {type:tabsChanged,tabs} -- unknown
fields ignored, the ipc tolerance contract; tab rows carry
favIconUrl -> Tab.FavIconURL, the browser-reported favicon
location riding that tolerance contract with NO protocol bump --
empty from older extensions -- consumed by the app layer's
NoteFavicon hint feed, with a sync_test pin keeping logic.mjs's
tabRow emitting it). Constants HostName
"competent_search_thing" (Firefox's ^\w+(\.\w+)*$ rule),
ExtensionID (the pinned gecko id), ProtocolVersion, Msg* -- all
LOCKSTEP with webextension/logic.mjs via sync_test.go (the theme
sync_test precedent; it also pins manifest.json's permissions
exactly [nativeMessaging, tabs], MV2 persistent background page,
and that the wrapper names the firefox-host subcommand).
frame.go: ReadFrame/WriteFrame -- 4-byte NATIVE-endian length
prefix (binary.NativeEndian per MDN) + JSON body, single-Write
frames, caps MaxOutFrame 1 MB (Firefox kills the port beyond it)
/ MaxInFrame 8 MiB, torn stream = ErrUnexpectedEOF, clean end =
io.EOF. SocketPath: $COMPETENT_SEARCH_FFEXT_SOCKET override, else
$XDG_RUNTIME_DIR/competent-search-thing-ffext.sock, else per-uid
under os.TempDir() (the ipc.SocketPath mirror). token.go:
Token/ParseToken "c<conn>:<tab>:<window>" -- digits ONLY (strconv
alone would take a leading sign), conn >= 1, 64-byte cap; the
activate_tab wire token. manifest.go: ManifestPath per OS (linux+
unix-likes ~/.mozilla/native-messaging-hosts/, darwin ~/Library/
Application Support/Mozilla/NativeMessagingHosts/, windows =
configDir + HKCU registry via registry_windows.go, stub elsewhere),
WrapperPath/WrapperContent (configDir/firefox-host.{sh,bat};
sh single-quote escaping, exec "<stable exe>" firefox-host "$@"),
ManifestContent (name/description/path/type stdio/
allowed_extensions=[ExtensionID]), InstallHost = read-compare-
atomic-write both pieces (config.Save temp+rename shape, wrapper
0700) with self-heal: unchanged = zero writes, changed wrapper
reports PreviousExe for the app's loud repair log (the gsettings
precedent). server.go: Listen = the ipc stale-socket recovery +
chmod 0600 + ErrAlreadyRunning, but conns are PERSISTENT (8 MiB
line cap): per-conn pending map correlates replies by id --
listTabs replies store their dump ON THE CONN GOROUTINE (pendingReq
.isList) so snapshot updates are strictly arrival-ordered vs
tabsChanged pushes (a requester-side store could clobber a newer
push); Tabs() = the merged per-conn snapshot (Tab tagged with the
owning conn id, negative wire ids skipped, float lastAccessed
tolerated) + newest update time (the app's freshness gate);
KickRefresh = single-flight async listTabs fan-out (also fired
once per fresh conn); Activate routes to the owning conn
(ErrNotConnected when gone) under activateTimeout 1200ms /
listTimeout 1000ms / writeTimeout 2s; dead conns drop their tabs
and close their pending channels; Close idempotent+nil-safe.
host.go: RunHost, the relay loop -- stdin frames -> socket lines
(json.Compact when a frame carries newlines, drop-with-log while
the app is down, one log per episode), socket lines -> stdout
frames, reconnect with capped exponential backoff (1s..30s,
seam-shrinkable), stdin EOF = clean nil return (Firefox closed the
port), shutdownConn refuses a dial that lands after teardown.
Deliberately NO schema in schemas/ (internal two-party protocol,
the ipc stance).
