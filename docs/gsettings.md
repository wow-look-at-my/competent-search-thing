# internal/gsettings

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/gsettings` -- the GNOME custom-keybinding fallback for
Wayland GNOME sessions whose portal lacks GlobalShortcuts (GNOME <
48, e.g. Ubuntu 24.04/GNOME 46): pure logic over an injectable
`Runner` seam (production `Run` execs the gsettings CLI, no shell,
3s/call timeout, stderr folded into errors; unit tests script argv
-> output). `ConvertHotkey` maps platform.Hotkey to GTK accelerator
syntax (<Control><Alt>space; keys per gdk_keyval_from_name: space,
lowercase letters/digits, F1, Return, Escape, Tab, Up/Down/...);
accelerator normalization treats <Primary>/<Ctrl>/<Ctl> as control,
ignores modifier order and case (conflict detection).
`EnsureBinding(ctx, run, hk, command)` (=
`EnsureBindingWith(..., BindingOptions{})`; the options variant's
ForceBinding -- wired ONLY from the app's config live-apply path --
rewrites an existing entry's accelerator to the requested hotkey
through the same conflict-checked candidate ladder, filling
Rebound/PreviousBinding on success, or RebindSkipped (a notice,
never an error -- the working binding is kept) when every candidate
is taken) -> `Applied{Binding,
Requested, FellBack, Changed, Existing, Rebound, PreviousBinding,
RebindSkipped, InList, DiskBinding,
DiskCommand, Verified, VerifyNote}`: reads the media-keys
custom-keybindings list; if the app's entry (fixed path ...
/custom-keybindings/competent-search-thing/) exists it is STICKY --
the binding is never rewritten without ForceBinding (user edits in
GNOME Settings
survive; Existing=true) and the stored command SELF-HEALS: it is
rewritten (command key only; Repaired=true + PreviousCommand for
the app's loud old->new repair log) when it can no longer launch
the running binary -- empty/unparseable (commandExecutable, the
GLib-shell inverse of ToggleCommand), a non-absolute executable, a
dead path, or a live path that is a different file (os.Stat +
os.SameFile vs the new command's exe) -- AND when it still launches
it but through a Cellar-versioned spelling while the new command's
is not (platform.ParseBrewCellar on both exes; the migration that
keeps the binding alive across brew upgrades -- the
brew-upgrade-broke-the-shortcut field fix), while any other
still-working spelling (stable, custom symlink, and
versioned->versioned when no stable spelling was derivable) is kept
verbatim (zero writes,
read-back verifies the on-disk command); a fresh entry gets the first free
candidate of [configured, <Control><Alt>space, <Super>space]
(normalization-deduped) checked against every accelerator in the
wm/mutter/mutter.wayland/shell/media-keys schemas
(`list-recursively`, arrays-of-strings only) plus every OTHER
custom entry's binding (capped 64) -- because mutter silently
refuses conflicting grabs and GNOME 46 defaults take BOTH Alt+Space
(activate-window-menu) and Super+Space (switch-input-source); all
candidates taken = sentinel ErrAllTaken, nothing written. Fresh
writes go entry keys (name/command/binding) FIRST, list append
LAST -- LOAD-BEARING ORDER: gsd (verified identical in
gsd-media-keys-manager.c 42.1 and 46.0) reads the entry the moment
the list changes, DROPS one whose command+binding are still empty
("Key binding ... is incomplete"), and a command written after
that drop is silently lost (update_custom_binding_command only
mutates existing keys), so list-last is what guarantees GNOME 42
sees a complete entry; never "simplify" back to list-first. Both
paths end with a read-back (3 fresh gets: list membership, binding,
command) filling the InList/Disk*/Verified/VerifyNote fields --
verification read failures degrade to Verified=false + note, never
an error. Writes are GVariant text (single-quoted,
parsed+serialized by tiny in-package helpers, incl. the "@as []"
empty form); the scan tolerates missing schemas/entries but
list/entry read and all write failures are fatal.
`ToggleCommand(exe)` builds the GLib-shell-safe "<exe> toggle"
command. `DaemonRunning(ctx)` (daemon.go) probes the session bus
(godbus) for org.gnome.SettingsDaemon.MediaKeys -- the gsd process
that turns the entry into a compositor grab; error = no bus =
caller skips the check. Exhaustively unit-tested against scripted
runners (exact argv sequences incl. write order and read-backs,
idempotent second run = zero sets, verification mismatch paths)
plus a LookPath-guarded smoke test of the real CLI and a
throwaway-dbus-daemon test of the probe.
