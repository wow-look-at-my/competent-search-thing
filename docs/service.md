# internal/service

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/service` -- the per-user login-service layer (README
"Runs as a login service" holds the user-facing story), pure logic
over an injectable Runner (the internal/gsettings pattern:
production Run execs launchctl/systemctl shell-free, 10s/call,
stderr folded into errors; tests script exact argv -> output) with
Manager.GOOS dispatching to UNTAGGED per-OS backends -- darwin.go
(launchd LaunchAgent ~/Library/LaunchAgents/
com.wow-look-at-my.competent-search-thing.plist: bootstrap/enable/
print/bootout/kickstart on gui/<uid>, plist.go's
LaunchAgentPlist = RunAtLoad + KeepAlive {SuccessfulExit:false}
(CRASH-ONLY: the single-instance handoff exits 0 and must never
respawn-loop) + LimitLoadToSessionType Aqua + both std paths to
~/Library/Logs/<app>/<app>.log, xmlEscape/xmlUnescape) and linux.go
(systemd user unit $XDG_CONFIG_HOME|~/.config/systemd/user/
competent-search-thing.service: unit.go's SystemdUnit =
Restart=on-failure + RestartSec=2 + After/PartOf/WantedBy=
graphical-session.target, systemdExecPath %%-and-quote escaping;
install = write-if-changed + daemon-reload + enable + start only
under an ACTIVE graphical-session.target) -- so both CI jobs test
every OS shape and the one darwin-only real_darwin_test.go lints
generated plists with the real plutil. NewManager fills production
values (platform.StableExecutable(exe, args0) so a Cellar-versioned
path is never baked in -- the GNOME keybinding lesson --
OptOutFile = OptOutPath(config.Dir()) = <configDir>/service.optout,
SystemUnitDirs = /usr/lib+/lib systemd/user). TWO SURFACES:
Install/Uninstall/Status/Restart back the CLI (internal/cli
service.go: ONE self-registering `service` command file, subcommands
install|uninstall|status|restart over the newServiceManager seam;
install ClearOptOut()s first and REFUSES a foreign owner, uninstall
WriteOptOut()s after removing -- the marker is deliberately NOT a
config.json field), and `Ensure(ctx)` (ensure.go) is the app's
AUTOMATIC first-run registration (the 2026-07-21 "obviously it must
be fully automatic" reversal; internal/app service.go wires it):
the fixed decision matrix foreign-owner-yield (darwin
brewServiceOwner = homebrew.mxcl plist stat + loaded-job probe;
linux = deb unit in SystemUnitDirs -- a same-named user unit would
SHADOW it -- or brew's homebrew.<app>.service user unit, yielded
with a stop-once hint because taking over a brew-services unit
uninvited is rude) -> opt-out marker -> current (byte-equal file =
EnsureCurrent, ZERO execs on linux, only the brew probe on darwin
-- the cheap every-boot path) -> stale repair (binary moved, e.g.
brew upgrade: rewrite + PreviousExe via unitExecStart/plistProgram
for the loud old->new line) -> fresh register (write + enable,
linux also daemon-reload). Ensure NEVER starts/restarts/bootstraps
ANYTHING -- the running app may BE the service instance, and a
darwin bootstrap of a RunAtLoad agent launches a copy immediately
(which would IPC-handoff and summon the bar uninvited); launchd
loads ~/Library/LaunchAgents by itself at login, so plist + enable
is the whole darwin job. Headless degrade: launchctl enable doubles
as the gui-domain probe and a failed enable/daemon-reload on a
FRESH write rolls the file back (EnsureUnavailable -- the state
stays "not registered" and converges on a later desktop boot),
while a repair keeps the better content + a note. EnsureResult
{Action Unsupported|Yielded|OptedOut|Current|Registered|Repaired|
Unavailable, ServicePath, Exe, Owner, OursToo, Hint, PreviousExe,
Note} carries everything the app's one log line formats.
Exhaustively unit-tested (scripted exact-argv runners, temp homes,
the full matrix per OS in ensure_test.go incl. the
never-starts-anything negatives).
