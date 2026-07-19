package gsettings

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// The media-keys schema pair the custom keybinding lives in, and the
// app's fixed entry under it.
const (
	mediaKeysSchema        = "org.gnome.settings-daemon.plugins.media-keys"
	customKeybindingSchema = mediaKeysSchema + ".custom-keybinding"
	customListKey          = "custom-keybindings"

	// OurPath is the dconf directory of the app's custom keybinding
	// entry (the trailing slash is required -- it is a dconf dir path).
	OurPath = "/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/competent-search-thing/"

	// BindingName is the entry name shown in GNOME Settings > Keyboard.
	BindingName = "Competent Search (summon)"

	// maxOtherEntries caps how many foreign custom-keybinding entries
	// the conflict scan reads.
	maxOtherEntries = 64
)

// takenSchemas are the non-relocatable schemas whose accelerator
// arrays the conflict scan reads (GNOME wm/mutter/shell/media-keys
// defaults all live here; on GNOME 46, Alt+Space is
// wm activate-window-menu and Super+Space is wm switch-input-source).
var takenSchemas = []string{
	"org.gnome.desktop.wm.keybindings",
	"org.gnome.mutter.keybindings",
	"org.gnome.mutter.wayland.keybindings",
	"org.gnome.shell.keybindings",
	mediaKeysSchema,
}

// fallbackAccels are tried, in order, when the configured hotkey is
// already taken. Ctrl+Alt+Space is free on a stock GNOME 46 desktop;
// Super+Space is the last resort (taken by switch-input-source by
// default, but users commonly rebind that away).
var fallbackAccels = []string{"<Control><Alt>space", "<Super>space"}

// ErrAllTaken reports that the configured hotkey and every fallback
// candidate is already bound by GNOME or another custom keybinding, so
// no binding was written (a conflicting custom grab would fail
// silently). The caller should tell the user to bind a key manually.
var ErrAllTaken = errors.New("gsettings: every candidate key combination is already taken")

// Applied reports what EnsureBinding did and which accelerator now
// summons the app.
type Applied struct {
	// Binding is the GNOME accelerator in effect for the app's entry.
	Binding string
	// Requested is the accelerator converted from the configured
	// hotkey.
	Requested string
	// FellBack is true when a fresh entry got a fallback accelerator
	// because the requested one was already taken.
	FellBack bool
	// Changed is true when at least one gsettings set call was made.
	Changed bool
	// Existing is true when the app's entry already existed and its
	// (possibly user-edited) binding was deliberately left untouched.
	Existing bool
	// Repaired is true when an existing entry's stored command was
	// rewritten (the command key, never the binding): it no longer
	// launched the running binary -- its executable was gone, not
	// absolute, unparseable, or resolved to a different file (a
	// versioned install directory left behind by an upgrade) -- or it
	// still did but through a version-pinned Homebrew Cellar spelling
	// that the next upgrade would kill (see commandNeedsRepair).
	Repaired bool
	// PreviousCommand is the stored command a repair replaced ("" when
	// Repaired is false).
	PreviousCommand string
	// Rebound is true when BindingOptions.ForceBinding rewrote an
	// existing entry's accelerator to the (conflict-checked) requested
	// one -- the config-editor path, where the user changed the hotkey
	// value on purpose. Never true on the default sticky path.
	Rebound bool
	// PreviousBinding is the accelerator a forced rebind replaced (""
	// when Rebound is false).
	PreviousBinding string
	// RebindSkipped explains, in one human-readable clause, why a
	// requested forced rebind kept the existing accelerator instead
	// (every candidate taken); "" when no rebind was requested or it
	// went through. The existing binding still works -- this is a
	// notice, not a failure.
	RebindSkipped string

	// The read-back verification: after the writes (or the sticky
	// no-op), the parent list and the entry's binding/command are
	// re-read in fresh gsettings invocations, so the fields below
	// describe what is actually on disk -- not what was attempted. A
	// gsettings set that returns success proves nothing about whether
	// gnome-settings-daemon will see (let alone grab) the binding, so
	// callers must only claim success when Verified is true.

	// InList is true when OurPath was read back as a member of the
	// custom-keybindings list.
	InList bool
	// DiskBinding is the entry's binding as re-read from disk.
	DiskBinding string
	// DiskCommand is the entry's command as re-read from disk.
	DiskCommand string
	// Verified is true when the read-back confirmed everything a
	// working keybinding needs: list membership, a non-empty binding
	// matching Binding, and the exact command that was requested.
	Verified bool
	// VerifyNote explains, in one human-readable sentence, what the
	// read-back found missing or mismatched ("" when Verified).
	VerifyNote string
}

// EnsureBinding makes sure a GNOME custom keybinding exists that runs
// command (see ToggleCommand) and reports the accelerator in effect.
//
// An entry the app created earlier is respected: its binding is never
// rewritten -- a user edit in GNOME Settings survives restarts -- and
// the stored command self-heals (see commandNeedsRepair): dead or
// foreign commands are rewritten to the new command, and so is a
// still-working spelling that is pinned to a Homebrew Cellar version
// while the new command's is stable (the migration that keeps the
// binding alive across upgrades); any other still-working spelling --
// even one that differs textually from command -- is left alone. A
// fresh entry gets the first conflict-free candidate of [configured
// hotkey, Ctrl+Alt+Space, Super+Space]; "taken" is every accelerator
// found in the standard GNOME keybinding schemas plus every other
// custom-keybinding entry, because mutter silently refuses grabs for
// combinations that are already bound. When every candidate is taken,
// ErrAllTaken is returned and nothing is written. A second run with an
// unchanged world performs zero writes.
//
// Write order is load-bearing: the entry's name/command/binding are
// written FIRST and the path is appended to the custom-keybindings
// list LAST. gnome-settings-daemon reacts to the list change by
// reading the entry immediately (gsd-media-keys-manager.c,
// media_key_new_for_path) and DROPS an entry whose command and binding
// are both still empty ("Key binding (%s) is incomplete"); a command
// written after that drop is silently lost (update_custom_binding_
// command only touches keys that exist). Appending the path last means
// the one notification gsd is guaranteed to see -- the list change on
// the schema object it has watched since startup -- finds a complete
// entry, with no reliance on per-entry signal timing.
//
// Both the fresh and the existing path end with a read-back
// verification (see the Applied fields): every conclusion offered to
// the caller was re-read from disk, never inferred from write success.
func EnsureBinding(ctx context.Context, run Runner, hk platform.Hotkey, command string) (Applied, error) {
	return EnsureBindingWith(ctx, run, hk, command, BindingOptions{})
}

// BindingOptions tunes EnsureBindingWith beyond the sticky default.
type BindingOptions struct {
	// ForceBinding rewrites an existing entry's accelerator to the
	// requested hotkey (conflict-checked, with the usual fallbacks) --
	// wired ONLY from the config live-apply path, where the user just
	// changed the configured hotkey and expects the key to follow. The
	// default (false) keeps the historical sticky behavior: an existing
	// entry's binding -- possibly edited in GNOME Settings -- is never
	// touched.
	ForceBinding bool
}

// EnsureBindingWith is EnsureBinding with options; see BindingOptions.
func EnsureBindingWith(ctx context.Context, run Runner, hk platform.Hotkey, command string, o BindingOptions) (Applied, error) {
	requested, err := ConvertHotkey(hk)
	if err != nil {
		return Applied{}, err
	}
	applied := Applied{Requested: requested}

	listOut, err := run(ctx, "get", mediaKeysSchema, customListKey)
	if err != nil {
		return applied, fmt.Errorf("gsettings: reading %s: %w", customListKey, err)
	}
	paths, ok := parseStringArray(listOut)
	if !ok {
		return applied, fmt.Errorf("gsettings: unparseable %s value %q", customListKey, strings.TrimSpace(listOut))
	}

	if slices.Contains(paths, OurPath) {
		applied, expected, err := ensureExisting(ctx, run, applied, command)
		if err != nil {
			return applied, err
		}
		if o.ForceBinding && !sameAccel(applied.Binding, requested) {
			applied, err = rebindExisting(ctx, run, applied, paths, requested)
			if err != nil {
				return applied, err
			}
		}
		return verifyOnDisk(ctx, run, applied, expected), nil
	}

	taken := collectTaken(ctx, run, paths)
	chosen := ""
	for _, candidate := range candidates(requested) {
		if !taken[normalizeAccel(candidate)] {
			chosen = candidate
			break
		}
	}
	if chosen == "" {
		return applied, fmt.Errorf("%w (tried %s)", ErrAllTaken, strings.Join(candidates(requested), ", "))
	}

	for _, kv := range [][2]string{
		{"name", BindingName},
		{"command", command},
		{"binding", chosen},
	} {
		if _, err := run(ctx, "set", customKeybindingSchema+":"+OurPath, kv[0], quoteVariantString(kv[1])); err != nil {
			return applied, fmt.Errorf("gsettings: setting %s: %w", kv[0], err)
		}
		applied.Changed = true
	}
	if _, err := run(ctx, "set", mediaKeysSchema, customListKey, quoteVariantArray(append(paths, OurPath))); err != nil {
		return applied, fmt.Errorf("gsettings: appending to %s: %w", customListKey, err)
	}
	applied.Binding = chosen
	applied.FellBack = !sameAccel(chosen, requested)
	return verifyOnDisk(ctx, run, applied, command), nil
}

// verifyOnDisk re-reads the parent list plus the entry's binding and
// command in fresh gsettings invocations and fills Applied's read-back
// fields. command is the command expected on disk: what was written on
// the fresh and repair paths, or the untouched stored command when an
// existing entry was healthy. Read failures never fail EnsureBinding
// -- the writes already happened -- they just leave Verified false
// with a note, so the caller warns instead of claiming success.
func verifyOnDisk(ctx context.Context, run Runner, applied Applied, command string) Applied {
	note := func(format string, args ...any) Applied {
		applied.VerifyNote = fmt.Sprintf(format, args...)
		return applied
	}

	listOut, err := run(ctx, "get", mediaKeysSchema, customListKey)
	if err != nil {
		return note("re-reading %s failed: %v", customListKey, err)
	}
	paths, ok := parseStringArray(listOut)
	if !ok {
		return note("re-read %s is unparseable: %q", customListKey, strings.TrimSpace(listOut))
	}
	applied.InList = slices.Contains(paths, OurPath)

	bindingOut, err := run(ctx, "get", customKeybindingSchema+":"+OurPath, "binding")
	if err != nil {
		return note("re-reading the binding failed: %v", err)
	}
	applied.DiskBinding, _ = parseVariantStringValue(bindingOut)

	commandOut, err := run(ctx, "get", customKeybindingSchema+":"+OurPath, "command")
	if err != nil {
		return note("re-reading the command failed: %v", err)
	}
	applied.DiskCommand, _ = parseVariantStringValue(commandOut)

	switch {
	case !applied.InList:
		return note("the entry is not in the %s list", customListKey)
	case applied.DiskBinding == "":
		return note("the binding on disk is empty")
	case applied.DiskBinding != applied.Binding:
		return note("the binding on disk is %q, expected %q", applied.DiskBinding, applied.Binding)
	case applied.DiskCommand != command:
		return note("the command on disk is %q, expected %q", applied.DiskCommand, command)
	}
	applied.Verified = true
	return applied
}

// rebindExisting is the ForceBinding half of the existing-entry path:
// the configured hotkey changed, so the stored accelerator is
// rewritten to the first conflict-free candidate (the fresh path's
// exact ladder: requested, then the fallbacks). When every candidate
// is taken the EXISTING (working) binding is kept and the skip is
// reported via RebindSkipped -- never an error, because a working key
// remains bound.
func rebindExisting(ctx context.Context, run Runner, applied Applied, paths []string, requested string) (Applied, error) {
	taken := collectTaken(ctx, run, paths)
	chosen := ""
	for _, candidate := range candidates(requested) {
		if !taken[normalizeAccel(candidate)] {
			chosen = candidate
			break
		}
	}
	if chosen == "" {
		applied.RebindSkipped = fmt.Sprintf("every candidate accelerator is already taken (tried %s); keeping %s",
			strings.Join(candidates(requested), ", "), applied.Binding)
		return applied, nil
	}
	if sameAccel(chosen, applied.Binding) {
		// The ladder landed on the accelerator already installed (the
		// requested one is taken and the entry already holds the
		// fallback): nothing to write.
		return applied, nil
	}
	if _, err := run(ctx, "set", customKeybindingSchema+":"+OurPath, "binding", quoteVariantString(chosen)); err != nil {
		return applied, fmt.Errorf("gsettings: rebinding to %s: %w", chosen, err)
	}
	applied.Changed = true
	applied.Rebound = true
	applied.PreviousBinding = applied.Binding
	applied.Binding = chosen
	applied.FellBack = !sameAccel(chosen, requested)
	return applied, nil
}

// ensureExisting handles an entry the app created earlier: the binding
// -- whatever the user made it -- is left alone, and the stored
// command is rewritten only when it no longer launches the running
// binary (commandNeedsRepair). It returns the command the read-back
// verification should expect on disk: the stored one when it was left
// alone, command after a repair.
func ensureExisting(ctx context.Context, run Runner, applied Applied, command string) (_ Applied, expected string, err error) {
	applied.Existing = true
	bindingOut, err := run(ctx, "get", customKeybindingSchema+":"+OurPath, "binding")
	if err != nil {
		return applied, "", fmt.Errorf("gsettings: reading the existing binding: %w", err)
	}
	if v, ok := parseVariantStringValue(bindingOut); ok {
		applied.Binding = v
	}
	commandOut, err := run(ctx, "get", customKeybindingSchema+":"+OurPath, "command")
	if err != nil {
		return applied, "", fmt.Errorf("gsettings: reading the existing command: %w", err)
	}
	current, _ := parseVariantStringValue(commandOut)
	if current == command || !commandNeedsRepair(current, command) {
		return applied, current, nil
	}
	if _, err := run(ctx, "set", customKeybindingSchema+":"+OurPath, "command", quoteVariantString(command)); err != nil {
		return applied, "", fmt.Errorf("gsettings: repairing the keybinding command: %w", err)
	}
	applied.Changed = true
	applied.Repaired = true
	applied.PreviousCommand = current
	return applied, command, nil
}

// commandNeedsRepair decides whether an existing entry's stored
// command (current, textually different from the new command) must be
// rewritten. The accelerator is the user's; the command is app-owned
// -- but a spelling that still launches the running binary is left
// alone UNLESS it is a version-pinned Homebrew Cellar spelling, so a
// working entry is never churned pointlessly. Repair triggers when
// current cannot launch that binary any more -- it is empty or
// unparseable, its executable path is not absolute (gsd runs commands
// with its own cwd and PATH), the path no longer exists, or it exists
// but is a different file than the one the new command names (the
// versioned install directory left behind by an upgrade, or a foreign
// program) -- and additionally when it still launches the running
// binary but through a Cellar-versioned path while the new command's
// is stable (platform.ParseBrewCellar): such a spelling works today
// and dies at the next upgrade, gsd still running the deleted path,
// so it migrates to the upgrade-stable spelling while both still
// resolve. Stable spellings, custom symlinks the user wrote, and a
// versioned spelling when the new command is versioned too (no
// stable spelling was derivable; churning buys nothing) are all kept
// verbatim. The file comparison follows symlinks (os.Stat +
// os.SameFile), so a stable shim and the resolved path it points at
// count as the same binary.
func commandNeedsRepair(current, command string) bool {
	oldExe, ok := commandExecutable(current)
	if !ok || oldExe == "" || !filepath.IsAbs(oldExe) {
		return true
	}
	oldInfo, err := os.Stat(oldExe)
	if err != nil {
		return true // the stored executable is gone
	}
	newExe, ok := commandExecutable(command)
	if !ok || newExe == "" {
		return false // nothing sound to compare against; keep the live command
	}
	newInfo, err := os.Stat(newExe)
	if err != nil {
		// The replacement cannot be verified to exist: never overwrite
		// a command whose executable is alive with a doubtful one.
		return false
	}
	if !os.SameFile(oldInfo, newInfo) {
		return true
	}
	// Same binary on both sides, but the stored spelling is pinned to
	// a Cellar version while the new one is not: migrate to the
	// upgrade-stable spelling now, while the versioned path still
	// resolves.
	_, oldVersioned := platform.ParseBrewCellar(oldExe)
	_, newVersioned := platform.ParseBrewCellar(newExe)
	return oldVersioned && !newVersioned
}

// candidates returns the accelerators to try, first the configured
// one, then the fallbacks, with normalization-level duplicates
// dropped.
func candidates(requested string) []string {
	out := []string{requested}
	for _, fb := range fallbackAccels {
		if !sameAccel(fb, requested) {
			out = append(out, fb)
		}
	}
	return out
}

// collectTaken gathers every accelerator already claimed: the
// array-of-strings values across the standard keybinding schemas plus
// the binding of every other custom-keybindings entry (capped). The
// scan is best-effort protection against silent grab failures, so an
// unreadable schema or entry is skipped rather than fatal -- it must
// never disable the whole backend.
func collectTaken(ctx context.Context, run Runner, paths []string) map[string]bool {
	taken := make(map[string]bool)
	add := func(accel string) {
		if n := normalizeAccel(accel); n != "" {
			taken[n] = true
		}
	}
	for _, schema := range takenSchemas {
		out, err := run(ctx, "list-recursively", schema)
		if err != nil {
			continue
		}
		for _, accel := range accelsFromListing(out) {
			add(accel)
		}
	}
	read := 0
	for _, p := range paths {
		if p == OurPath {
			continue
		}
		if read == maxOtherEntries {
			break
		}
		read++
		out, err := run(ctx, "get", customKeybindingSchema+":"+p, "binding")
		if err != nil {
			continue
		}
		if v, ok := parseVariantStringValue(out); ok {
			add(v)
		}
	}
	return taken
}

// accelsFromListing extracts the elements of every array-of-strings
// value from `gsettings list-recursively <schema>` output. Lines are
// "schema key value"; values that are not string arrays (ints, bools,
// plain strings, ...) are ignored. Elements that are not accelerators
// (dconf paths, XF86 keysyms) get filtered later by normalizeAccel
// never matching a candidate.
func accelsFromListing(out string) []string {
	var accels []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest := line
		for i := 0; i < 2; i++ {
			sp := strings.IndexByte(rest, ' ')
			if sp < 0 {
				rest = ""
				break
			}
			rest = rest[sp+1:]
		}
		if rest == "" {
			continue
		}
		if arr, ok := parseStringArray(rest); ok {
			accels = append(accels, arr...)
		}
	}
	return accels
}
