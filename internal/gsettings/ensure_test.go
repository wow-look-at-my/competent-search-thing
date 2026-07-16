package gsettings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

const entrySchemaPath = customKeybindingSchema + ":" + OurPath

// gnomeDefaultWM is a trimmed GNOME 46 wm.keybindings listing: both
// Alt+Space (activate-window-menu) and Super+Space (switch-input-
// source) are taken out of the box.
const gnomeDefaultWM = `org.gnome.desktop.wm.keybindings activate-window-menu ['<Alt>space']
org.gnome.desktop.wm.keybindings switch-input-source ['<Super>space', 'XF86Keyboard']
org.gnome.desktop.wm.keybindings switch-input-source-backward ['<Shift><Super>space', '<Shift>XF86Keyboard']
org.gnome.desktop.wm.keybindings minimize ['<Super>h']
org.gnome.desktop.wm.keybindings maximize @as []
`

// gnomeDefaultMediaKeys mixes array and non-array values like the real
// listing does; only the arrays may contribute accelerators.
const gnomeDefaultMediaKeys = `org.gnome.settings-daemon.plugins.media-keys volume-step 6
org.gnome.settings-daemon.plugins.media-keys custom-keybindings @as []
org.gnome.settings-daemon.plugins.media-keys screensaver ['<Super>l']
org.gnome.settings-daemon.plugins.media-keys mic-mute ['']
`

// scriptedRunner answers gsettings invocations from a canned per-argv
// table and records every call in order. An invocation nothing was
// scripted for fails the test: the exact argv surface is part of the
// contract under test.
type scriptedRunner struct {
	t     *testing.T
	mu    sync.Mutex
	calls [][]string
	out   map[string]string
	errs  map[string]error
}

func newScriptedRunner(t *testing.T) *scriptedRunner {
	return &scriptedRunner{t: t, out: map[string]string{}, errs: map[string]error{}}
}

func argvKey(args []string) string { return strings.Join(args, "\x00") }

func (s *scriptedRunner) on(out string, args ...string) { s.out[argvKey(args)] = out }

func (s *scriptedRunner) fail(err error, args ...string) { s.errs[argvKey(args)] = err }

func (s *scriptedRunner) run(_ context.Context, args ...string) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, args)
	s.mu.Unlock()
	k := argvKey(args)
	if err, ok := s.errs[k]; ok {
		return "", err
	}
	if out, ok := s.out[k]; ok {
		return out, nil
	}
	s.t.Fatalf("unexpected gsettings call: %q", args)
	return "", nil
}

func (s *scriptedRunner) setCalls() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sets [][]string
	for _, c := range s.calls {
		if len(c) > 0 && c[0] == "set" {
			sets = append(sets, c)
		}
	}
	return sets
}

// scriptFreshWorld scripts an empty custom-keybindings list plus the
// GNOME-default schema listings (extra listings override per schema).
func scriptFreshWorld(s *scriptedRunner, overrides map[string]string) {
	s.on("@as []", "get", mediaKeysSchema, customListKey)
	listings := map[string]string{
		"org.gnome.desktop.wm.keybindings":         gnomeDefaultWM,
		"org.gnome.mutter.keybindings":             "",
		"org.gnome.mutter.wayland.keybindings":     "",
		"org.gnome.shell.keybindings":              "",
		"org.gnome.settings-daemon.plugins.media-keys": gnomeDefaultMediaKeys,
	}
	for schema, listing := range overrides {
		listings[schema] = listing
	}
	for schema, listing := range listings {
		s.on(listing, "list-recursively", schema)
	}
}

func TestEnsureBindingFallsBackInGnomeDefaultWorld(t *testing.T) {
	// The GNOME 46 stock desktop: configured alt+space is taken by
	// activate-window-menu AND the super+space fallback is taken by
	// switch-input-source -- ctrl+alt+space must win.
	s := newScriptedRunner(t)
	scriptFreshWorld(s, nil)
	s.on("", "set", mediaKeysSchema, customListKey, "['"+OurPath+"']")
	s.on("", "set", entrySchemaPath, "name", "'Competent Search (summon)'")
	s.on("", "set", entrySchemaPath, "command", "'/usr/bin/cst toggle'")
	s.on("", "set", entrySchemaPath, "binding", "'<Control><Alt>space'")

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "/usr/bin/cst toggle")
	require.NoError(t, err)
	require.Equal(t, Applied{
		Binding:   "<Control><Alt>space",
		Requested: "<Alt>space",
		FellBack:  true,
		Changed:   true,
	}, applied)

	// The exact argv sequence: one list read, five schema scans, then
	// the four writes, nothing else.
	require.Equal(t, [][]string{
		{"get", mediaKeysSchema, customListKey},
		{"list-recursively", "org.gnome.desktop.wm.keybindings"},
		{"list-recursively", "org.gnome.mutter.keybindings"},
		{"list-recursively", "org.gnome.mutter.wayland.keybindings"},
		{"list-recursively", "org.gnome.shell.keybindings"},
		{"list-recursively", "org.gnome.settings-daemon.plugins.media-keys"},
		{"set", mediaKeysSchema, customListKey, "['" + OurPath + "']"},
		{"set", entrySchemaPath, "name", "'Competent Search (summon)'"},
		{"set", entrySchemaPath, "command", "'/usr/bin/cst toggle'"},
		{"set", entrySchemaPath, "binding", "'<Control><Alt>space'"},
	}, s.calls)
}

func TestEnsureBindingUsesConfiguredWhenFree(t *testing.T) {
	s := newScriptedRunner(t)
	scriptFreshWorld(s, nil)
	s.on("", "set", mediaKeysSchema, customListKey, "['"+OurPath+"']")
	s.on("", "set", entrySchemaPath, "name", "'Competent Search (summon)'")
	s.on("", "set", entrySchemaPath, "command", "'/usr/bin/cst toggle'")
	s.on("", "set", entrySchemaPath, "binding", "'<Control><Shift>k'")

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "ctrl+shift+k"), "/usr/bin/cst toggle")
	require.NoError(t, err)
	require.False(t, applied.FellBack)
	require.True(t, applied.Changed)
	require.Equal(t, "<Control><Shift>k", applied.Binding)
	require.Equal(t, "<Control><Shift>k", applied.Requested)
}

func TestEnsureBindingConfiguredSuperSpaceDedupsCandidates(t *testing.T) {
	// Configured super+space is itself the second fallback: the
	// candidate list must not retry it, and ctrl+alt+space wins.
	s := newScriptedRunner(t)
	scriptFreshWorld(s, nil)
	s.on("", "set", mediaKeysSchema, customListKey, "['"+OurPath+"']")
	s.on("", "set", entrySchemaPath, "name", "'Competent Search (summon)'")
	s.on("", "set", entrySchemaPath, "command", "'/usr/bin/cst toggle'")
	s.on("", "set", entrySchemaPath, "binding", "'<Control><Alt>space'")

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "super+space"), "/usr/bin/cst toggle")
	require.NoError(t, err)
	require.True(t, applied.FellBack)
	require.Equal(t, "<Control><Alt>space", applied.Binding)
	require.Equal(t, []string{"<Super>space", "<Control><Alt>space"}, candidates("<Super>space"))
}

func TestEnsureBindingAllTaken(t *testing.T) {
	s := newScriptedRunner(t)
	scriptFreshWorld(s, map[string]string{
		"org.gnome.shell.keybindings": `org.gnome.shell.keybindings toggle-overview ['<Primary><Alt>space']` + "\n",
	})

	_, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "/usr/bin/cst toggle")
	require.ErrorIs(t, err, ErrAllTaken)
	require.Contains(t, err.Error(), "<Alt>space", "the error names the candidates for the manual instructions")
	require.Empty(t, s.setCalls(), "nothing is written when every candidate is taken")
}

func TestEnsureBindingRespectsExistingUserEditedBinding(t *testing.T) {
	s := newScriptedRunner(t)
	s.on("['/org/x/custom0/', '"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.on("'<Shift><Super>t'", "get", entrySchemaPath, "binding")
	s.on("'/usr/bin/cst toggle'", "get", entrySchemaPath, "command")

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "/usr/bin/cst toggle")
	require.NoError(t, err)
	require.Equal(t, Applied{
		Binding:   "<Shift><Super>t",
		Requested: "<Alt>space",
		Existing:  true,
	}, applied)
	require.Empty(t, s.setCalls(), "a second run performs zero writes")
	// No conflict scan happens either: the exact calls are the three
	// reads.
	require.Equal(t, [][]string{
		{"get", mediaKeysSchema, customListKey},
		{"get", entrySchemaPath, "binding"},
		{"get", entrySchemaPath, "command"},
	}, s.calls)
}

func TestEnsureBindingRefreshesStaleCommandKeepsBinding(t *testing.T) {
	s := newScriptedRunner(t)
	s.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.on("'<Control><Alt>space'", "get", entrySchemaPath, "binding")
	s.on("'/old/place/cst toggle'", "get", entrySchemaPath, "command")
	s.on("", "set", entrySchemaPath, "command", "'/new/place/cst toggle'")

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "/new/place/cst toggle")
	require.NoError(t, err)
	require.True(t, applied.Existing)
	require.True(t, applied.Changed)
	require.Equal(t, "<Control><Alt>space", applied.Binding)
	require.Equal(t, [][]string{
		{"set", entrySchemaPath, "command", "'/new/place/cst toggle'"},
	}, s.setCalls(), "only the command is rewritten; the binding is never touched")
}

func TestEnsureBindingExistingReadErrorsAreFatal(t *testing.T) {
	s := newScriptedRunner(t)
	s.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.fail(errors.New("dconf on fire"), "get", entrySchemaPath, "binding")
	_, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "cst toggle")
	require.ErrorContains(t, err, "dconf on fire")

	s2 := newScriptedRunner(t)
	s2.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s2.on("'<Alt>space'", "get", entrySchemaPath, "binding")
	s2.fail(errors.New("nope"), "get", entrySchemaPath, "command")
	_, err = EnsureBinding(context.Background(), s2.run, mustParse(t, "alt+space"), "cst toggle")
	require.ErrorContains(t, err, "nope")
}

func TestEnsureBindingOtherEntriesBlockCandidates(t *testing.T) {
	// Another app's custom entry holds ctrl+alt+space; the wm takes
	// alt+space; super+space is free in this world -- it must win.
	s := newScriptedRunner(t)
	s.on("['/org/x/custom0/']", "get", mediaKeysSchema, customListKey)
	s.on(`org.gnome.desktop.wm.keybindings activate-window-menu ['<Alt>space']`+"\n",
		"list-recursively", "org.gnome.desktop.wm.keybindings")
	for _, schema := range takenSchemas[1:] {
		s.on("", "list-recursively", schema)
	}
	s.on("'<Primary><Alt>space'", "get", customKeybindingSchema+":/org/x/custom0/", "binding")
	s.on("", "set", mediaKeysSchema, customListKey, "['/org/x/custom0/', '"+OurPath+"']")
	s.on("", "set", entrySchemaPath, "name", "'Competent Search (summon)'")
	s.on("", "set", entrySchemaPath, "command", "'cst toggle'")
	s.on("", "set", entrySchemaPath, "binding", "'<Super>space'")

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "cst toggle")
	require.NoError(t, err)
	require.True(t, applied.FellBack)
	require.Equal(t, "<Super>space", applied.Binding)
}

func TestEnsureBindingCapsOtherEntryReads(t *testing.T) {
	paths := make([]string, 70)
	s := newScriptedRunner(t)
	for i := range paths {
		paths[i] = fmt.Sprintf("/org/x/custom%d/", i)
		s.on("''", "get", customKeybindingSchema+":"+paths[i], "binding")
	}
	s.on(quoteVariantArray(paths), "get", mediaKeysSchema, customListKey)
	for _, schema := range takenSchemas {
		s.on("", "list-recursively", schema)
	}
	s.on("", "set", mediaKeysSchema, customListKey, quoteVariantArray(append(paths, OurPath)))
	s.on("", "set", entrySchemaPath, "name", "'Competent Search (summon)'")
	s.on("", "set", entrySchemaPath, "command", "'cst toggle'")
	s.on("", "set", entrySchemaPath, "binding", "'<Alt>space'")

	_, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "cst toggle")
	require.NoError(t, err)

	reads := 0
	for _, c := range s.calls {
		if c[0] == "get" && len(c) == 3 && c[2] == "binding" {
			reads++
		}
	}
	require.Equal(t, maxOtherEntries, reads, "foreign entry reads are capped")
}

func TestEnsureBindingToleratesSchemaScanErrors(t *testing.T) {
	// A missing schema must not disable the backend: the scan just
	// knows fewer conflicts.
	s := newScriptedRunner(t)
	s.on("@as []", "get", mediaKeysSchema, customListKey)
	for _, schema := range takenSchemas {
		s.fail(errors.New("No such schema"), "list-recursively", schema)
	}
	s.on("", "set", mediaKeysSchema, customListKey, "['"+OurPath+"']")
	s.on("", "set", entrySchemaPath, "name", "'Competent Search (summon)'")
	s.on("", "set", entrySchemaPath, "command", "'cst toggle'")
	s.on("", "set", entrySchemaPath, "binding", "'<Alt>space'")

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "cst toggle")
	require.NoError(t, err)
	require.False(t, applied.FellBack)
	require.Equal(t, "<Alt>space", applied.Binding)
}

func TestEnsureBindingToleratesUnreadableOtherEntry(t *testing.T) {
	s := newScriptedRunner(t)
	s.on("['/org/x/broken/', '/org/x/garbage/']", "get", mediaKeysSchema, customListKey)
	for _, schema := range takenSchemas {
		s.on("", "list-recursively", schema)
	}
	s.fail(errors.New("gone"), "get", customKeybindingSchema+":/org/x/broken/", "binding")
	s.on("6", "get", customKeybindingSchema+":/org/x/garbage/", "binding")
	s.on("", "set", mediaKeysSchema, customListKey, "['/org/x/broken/', '/org/x/garbage/', '"+OurPath+"']")
	s.on("", "set", entrySchemaPath, "name", "'Competent Search (summon)'")
	s.on("", "set", entrySchemaPath, "command", "'cst toggle'")
	s.on("", "set", entrySchemaPath, "binding", "'<Alt>space'")

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "cst toggle")
	require.NoError(t, err)
	require.Equal(t, "<Alt>space", applied.Binding)
}

func TestEnsureBindingListReadFailureIsFatal(t *testing.T) {
	s := newScriptedRunner(t)
	s.fail(errors.New("no dconf"), "get", mediaKeysSchema, customListKey)
	_, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "cst toggle")
	require.ErrorContains(t, err, "no dconf")
}

func TestEnsureBindingGarbageListIsFatal(t *testing.T) {
	s := newScriptedRunner(t)
	s.on("6", "get", mediaKeysSchema, customListKey)
	_, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "cst toggle")
	require.ErrorContains(t, err, "unparseable")
}

func TestEnsureBindingWriteFailuresPropagate(t *testing.T) {
	s := newScriptedRunner(t)
	scriptFreshWorld(s, nil)
	s.fail(errors.New("read-only"), "set", mediaKeysSchema, customListKey, "['"+OurPath+"']")
	_, err := EnsureBinding(context.Background(), s.run, mustParse(t, "ctrl+shift+k"), "cst toggle")
	require.ErrorContains(t, err, "read-only")

	s2 := newScriptedRunner(t)
	scriptFreshWorld(s2, nil)
	s2.on("", "set", mediaKeysSchema, customListKey, "['"+OurPath+"']")
	s2.fail(errors.New("boom"), "set", entrySchemaPath, "name", "'Competent Search (summon)'")
	_, err = EnsureBinding(context.Background(), s2.run, mustParse(t, "ctrl+shift+k"), "cst toggle")
	require.ErrorContains(t, err, "boom")
}

func TestEnsureBindingUnconvertibleHotkeyMakesNoCalls(t *testing.T) {
	s := newScriptedRunner(t)
	_, err := EnsureBinding(context.Background(), s.run, platform.Hotkey{Key: "bogus"}, "cst toggle")
	require.Error(t, err)
	require.Empty(t, s.calls, "conversion fails before any gsettings call")
}

// dconfSim is a tiny stateful dconf: get/set act on a value map, so a
// second EnsureBinding run sees what the first one wrote.
type dconfSim struct {
	values   map[string]string // "schema[:path]\x00key" -> raw GVariant text
	listings map[string]string
	sets     int
}

func (d *dconfSim) run(_ context.Context, args ...string) (string, error) {
	switch args[0] {
	case "list-recursively":
		return d.listings[args[1]], nil
	case "get":
		v, ok := d.values[args[1]+"\x00"+args[2]]
		if !ok {
			return "''", nil // unset relocatable keys default to ''
		}
		return v, nil
	case "set":
		d.values[args[1]+"\x00"+args[2]] = args[3]
		d.sets++
		return "", nil
	}
	return "", fmt.Errorf("unexpected call %q", args)
}

func TestEnsureBindingSecondRunIsIdempotent(t *testing.T) {
	sim := &dconfSim{
		values: map[string]string{
			mediaKeysSchema + "\x00" + customListKey: "@as []",
		},
		listings: map[string]string{
			"org.gnome.desktop.wm.keybindings": gnomeDefaultWM,
		},
	}
	hk := mustParse(t, "alt+space")

	first, err := EnsureBinding(context.Background(), sim.run, hk, "/usr/bin/cst toggle")
	require.NoError(t, err)
	require.Equal(t, "<Control><Alt>space", first.Binding)
	require.True(t, first.Changed)
	require.Equal(t, 4, sim.sets, "list append + name + command + binding")

	second, err := EnsureBinding(context.Background(), sim.run, hk, "/usr/bin/cst toggle")
	require.NoError(t, err)
	require.Equal(t, 4, sim.sets, "second run performs zero set calls")
	require.True(t, second.Existing)
	require.False(t, second.Changed)
	require.Equal(t, "<Control><Alt>space", second.Binding)

	// The binary moved: exactly one more write, the command.
	third, err := EnsureBinding(context.Background(), sim.run, hk, "/new/cst toggle")
	require.NoError(t, err)
	require.Equal(t, 5, sim.sets)
	require.True(t, third.Existing)
	require.True(t, third.Changed)
	require.Equal(t, "<Control><Alt>space", third.Binding)
	require.Equal(t, "'/new/cst toggle'", sim.values[entrySchemaPath+"\x00command"])
}
