package gsettings

import (
	"context"
	"errors"
	"fmt"
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
}

// EnsureBinding makes sure a GNOME custom keybinding exists that runs
// command (see ToggleCommand) and reports the accelerator in effect.
//
// An entry the app created earlier is respected: its binding is never
// rewritten -- a user edit in GNOME Settings survives restarts -- and
// only a stale command (the binary moved) is refreshed. A fresh entry
// gets the first conflict-free candidate of [configured hotkey,
// Ctrl+Alt+Space, Super+Space]; "taken" is every accelerator found in
// the standard GNOME keybinding schemas plus every other
// custom-keybinding entry, because mutter silently refuses grabs for
// combinations that are already bound. When every candidate is taken,
// ErrAllTaken is returned and nothing is written. A second run with an
// unchanged world performs zero writes.
func EnsureBinding(ctx context.Context, run Runner, hk platform.Hotkey, command string) (Applied, error) {
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
		return ensureExisting(ctx, run, applied, command)
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

	if _, err := run(ctx, "set", mediaKeysSchema, customListKey, quoteVariantArray(append(paths, OurPath))); err != nil {
		return applied, fmt.Errorf("gsettings: appending to %s: %w", customListKey, err)
	}
	applied.Changed = true
	for _, kv := range [][2]string{
		{"name", BindingName},
		{"command", command},
		{"binding", chosen},
	} {
		if _, err := run(ctx, "set", customKeybindingSchema+":"+OurPath, kv[0], quoteVariantString(kv[1])); err != nil {
			return applied, fmt.Errorf("gsettings: setting %s: %w", kv[0], err)
		}
	}
	applied.Binding = chosen
	applied.FellBack = !sameAccel(chosen, requested)
	return applied, nil
}

// ensureExisting handles an entry the app created earlier: the binding
// -- whatever the user made it -- is left alone, and only a stale
// command is rewritten.
func ensureExisting(ctx context.Context, run Runner, applied Applied, command string) (Applied, error) {
	applied.Existing = true
	bindingOut, err := run(ctx, "get", customKeybindingSchema+":"+OurPath, "binding")
	if err != nil {
		return applied, fmt.Errorf("gsettings: reading the existing binding: %w", err)
	}
	if v, ok := parseVariantStringValue(bindingOut); ok {
		applied.Binding = v
	}
	commandOut, err := run(ctx, "get", customKeybindingSchema+":"+OurPath, "command")
	if err != nil {
		return applied, fmt.Errorf("gsettings: reading the existing command: %w", err)
	}
	current, _ := parseVariantStringValue(commandOut)
	if current != command {
		if _, err := run(ctx, "set", customKeybindingSchema+":"+OurPath, "command", quoteVariantString(command)); err != nil {
			return applied, fmt.Errorf("gsettings: refreshing the keybinding command: %w", err)
		}
		applied.Changed = true
	}
	return applied, nil
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
