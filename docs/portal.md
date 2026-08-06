# internal/portal

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/portal` -- XDG Desktop Portal GlobalShortcuts client over
godbus/dbus/v5 (direct dep), the Wayland-native global-hotkey path.
PURE D-Bus client, deliberately NO app wiring yet. `Dial` (private
session-bus conn; the caller owns Close -- dropping the conn ends
the portal session), `Available` (fast probe:
org.freedesktop.portal.Desktop has an owner + GlobalShortcuts
`version` property >= 1; distinct wrapped ErrNoPortal vs
ErrNoGlobalShortcuts), `TriggerString` (platform.Hotkey ->
shortcuts-spec syntax: CTRL/SHIFT/ALT/LOGO modifiers + xkbcommon
keysym names, alt+space -> "ALT+space", enter -> "Return";
unmappable = error), `Register(ctx, conn, Options{ShortcutID,
Description, PreferredTrigger, OnActivated})` -> `*Session`.
Register follows the portal Request convention: subscribe on the
PREDICTED /request/SENDER/TOKEN path (crypto/rand tokens) BEFORE
each call, falling back to the returned handle; then CreateSession
(session_handle typed "s" -- documented erratum) -> ListShortcuts ->
BindShortcuts ONLY when the id is not already bound (a session may
attempt binding exactly ONCE; the portal remembers approvals across
sessions) -> Activated dispatch filtered to this session handle +
shortcut id (Deactivated ignored). Response code 1 = ErrDenied,
2 = portal error; create/list wait 25s, bind 5min (interactive
approval dialog), all ctx-abortable. Signal channels are BUFFERED
(godbus silently drops on a full channel). Session exposes
BoundDescription + Handle() for logging; Close() = best-effort
org.freedesktop.portal.Session.Close + match removal, idempotent,
never closes the conn. Tested headlessly against an in-package fake
portal service on a throwaway `dbus-daemon --session` per test
(spawned and killed strictly by PID; t.Skip when the binary is
absent -- present in CI's ubuntu-24.04). Consumed by internal/app's
hotkey.go (startPortalShortcut: Dial -> Available -> TriggerString
-> Register with ShortcutID "toggle", which must stay stable across
runs -- the portal keys remembered approvals on it).
