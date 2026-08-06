# internal/tray

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/tray` -- the tray icon: org.kde.StatusNotifierItem +
com.canonical.dbusmenu implemented DIRECTLY over godbus (no cgo, no
GTK/libappindicator -- nothing fights Wails for a main loop), pure
and headless-tested like internal/portal. `New(Options{ID, Title,
Tooltip getter, Menu []MenuItem{Label,Separator,OnClick},
OnActivate, Logf})` + `Start(ctx)`: Dial opens a PRIVATE session-bus
conn via dbus.SessionBusPrivateNoAutoStartup (NEVER autolaunches a
dbus-daemon; no bus = one quiet log line, Start returns nil,
degraded); export of /StatusNotifierItem (methods
Activate/SecondaryActivate/XAyatanaSecondaryActivate -> OnActivate,
ContextMenu/Scroll no-ops) + /MenuBar + org.freedesktop.DBus
.Properties on both (the GNOME extension reads EVERYTHING via
GetAll and needs Id+Menu before it shows anything) + Introspectable
on /, /StatusNotifierItem and /MenuBar (the extension's brute-force
item scan walks the introspection tree from "/"); RequestName
org.kde.StatusNotifierItem-<pid>-1 (KDE convention, best-effort);
registration calls StatusNotifierWatcher.RegisterStatusNotifierItem
with the OBJECT PATH "/StatusNotifierItem" -- the v42 extension
(Ubuntu 22.04) resolves a leading "/" against the sender directly,
while a bus-name argument takes an async name resolution that can
fail; a NameOwnerChanged watch (buffered chan, portal precedent)
re-registers whenever org.kde.StatusNotifierWatcher gains an owner
(GNOME Shell restart, extension reload, host appearing after a
degraded start -- "no StatusNotifierItem host" is one log line, not
an error). SNI props: Category ApplicationStatus, Status Active,
ItemIsMenu false, Menu /MenuBar, IconPixmap ONLY (no IconName: the
extension prefers a set name, mangles it with a "-panel" suffix and
warns per failed theme lookup -- the pixmap renders
deterministically); the icon is a magnifier DRAWN IN CODE (icon.go,
analytic coverage rasterizer, stdlib math only, no assets) at
22/24/48 px in ARGB32 network byte order (bytes A,R,G,B, straight
alpha -- v42 argbToRgba parses exactly that; rgba.go's exported
`MagnifierRGBA(size)` is the one PREMULTIPLIED-RGBA variant of the
same rasterizer, consumed by the darwin Dock icon in
internal/platform/native panel_darwin.go); ToolTip
(sa(iiay)ss) carries Title + the summon-shortcut text, re-read from
the Tooltip getter at every (re-)registration and announced via
NewToolTip on change (GNOME's extension ignores tooltips; KDE
shows them). dbusmenu: static tree, root 0 children ids 1..n,
revision pinned 1, Version 3 (libdbusmenu's value), GetLayout
honoring recursionDepth + propertyNames filter (the extension calls
GetLayout(0,-1,["type","children-display"]) then
GetGroupProperties(ids,[]) for the rest), GetProperty, Event
("clicked" -> OnClick; opened/closed/hovered ignored; unknown id =
dbus error), EventGroup (unknown ids reported back), AboutToShow
false, AboutToShowGroup. Close() cancels the watch goroutine,
closes the conn (which unregisters the item), idempotent + nil-safe
+ bounded. Tested against a fake org.kde.StatusNotifierWatcher on a
throwaway dbus-daemon (spawn/kill by captured PID, t.Skip without
the binary): registration argument, GetAll host-side reads, full
dbusmenu surface, watcher-restart + late-host re-registration,
tooltip refresh, close/cancel goroutine hygiene. Test gotcha:
dbus.Store REUSES a non-nil dest slice's backing array and MERGES
into existing maps -- always decode into fresh variables.
