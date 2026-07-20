package gsettings

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// dconfSim is a tiny stateful dconf: get/set act on a value map, so a
// second EnsureBinding run sees what the first one wrote (and the
// read-back verification sees the writes).
type dconfSim struct {
	values      map[string]string // "schema[:path]\x00key" -> raw GVariant text
	listings    map[string]string
	sets        int
	onListWrite func() // called just before a custom-keybindings list set
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
		if args[1] == mediaKeysSchema && args[2] == customListKey && d.onListWrite != nil {
			d.onListWrite()
		}
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
	require.True(t, first.Verified, "the read-back sees the simulated dconf state")
	require.True(t, first.InList)
	require.Equal(t, "<Control><Alt>space", first.DiskBinding)
	require.Equal(t, "/usr/bin/cst toggle", first.DiskCommand)
	require.Equal(t, 4, sim.sets, "name + command + binding + list append")

	second, err := EnsureBinding(context.Background(), sim.run, hk, "/usr/bin/cst toggle")
	require.NoError(t, err)
	require.Equal(t, 4, sim.sets, "second run performs zero set calls")
	require.True(t, second.Existing)
	require.False(t, second.Changed)
	require.True(t, second.Verified)
	require.Equal(t, "<Control><Alt>space", second.Binding)

	// The binary moved (the stored /usr/bin/cst does not exist):
	// exactly one more write, the command.
	third, err := EnsureBinding(context.Background(), sim.run, hk, "/new/cst toggle")
	require.NoError(t, err)
	require.Equal(t, 5, sim.sets)
	require.True(t, third.Existing)
	require.True(t, third.Changed)
	require.True(t, third.Repaired)
	require.Equal(t, "/usr/bin/cst toggle", third.PreviousCommand)
	require.True(t, third.Verified)
	require.Equal(t, "/new/cst toggle", third.DiskCommand)
	require.Equal(t, "<Control><Alt>space", third.Binding)
	require.Equal(t, "'/new/cst toggle'", sim.values[entrySchemaPath+"\x00command"])
}

func TestEnsureBindingSimWritesEntryBeforeList(t *testing.T) {
	// The order regression guard on the stateful sim: at the moment
	// the custom-keybindings list first contains OurPath, the entry
	// must already be complete -- gsd reads it exactly then, and drops
	// (or half-processes) an incomplete entry. See
	// gsd-media-keys-manager.c media_key_new_for_path.
	sim := &dconfSim{
		values: map[string]string{
			mediaKeysSchema + "\x00" + customListKey: "@as []",
		},
		listings: map[string]string{},
	}
	sim.onListWrite = func() {
		require.NotEqual(t, "", sim.values[entrySchemaPath+"\x00binding"])

		require.NotEqual(t, "", sim.values[entrySchemaPath+"\x00command"])

	}

	_, err := EnsureBinding(context.Background(), sim.run, mustParse(t, "alt+space"), "/usr/bin/cst toggle")
	require.NoError(t, err)
}

func TestEnsureBindingVerifyCatchesMissingListMembership(t *testing.T) {
	// Every write reports success but the read-back list does not
	// contain the entry (a lying backend, another process racing us):
	// the caller must get Verified=false and an explanation, never a
	// success claim.
	s := newScriptedRunner(t)
	scriptFreshWorld(s, nil)
	s.on("", "set", entrySchemaPath, "name", "'Competent Search (summon)'")
	s.on("", "set", entrySchemaPath, "command", "'cst toggle'")
	s.on("", "set", entrySchemaPath, "binding", "'<Control><Alt>space'")
	s.on("", "set", mediaKeysSchema, customListKey, "['"+OurPath+"']")
	scriptVerify(s, "@as []", "'<Control><Alt>space'", "'cst toggle'")

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "cst toggle")
	require.NoError(t, err, "verification failures degrade, they do not error")
	require.False(t, applied.Verified)
	require.False(t, applied.InList)
	require.Contains(t, applied.VerifyNote, "not in the custom-keybindings list")
}

func TestEnsureBindingVerifyCatchesCommandMismatch(t *testing.T) {
	s := newScriptedRunner(t)
	scriptFreshWorld(s, nil)
	s.on("", "set", entrySchemaPath, "name", "'Competent Search (summon)'")
	s.on("", "set", entrySchemaPath, "command", "'cst toggle'")
	s.on("", "set", entrySchemaPath, "binding", "'<Control><Alt>space'")
	s.on("", "set", mediaKeysSchema, customListKey, "['"+OurPath+"']")
	scriptVerify(s, "['"+OurPath+"']", "'<Control><Alt>space'", "'other-app doit'")

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "cst toggle")
	require.NoError(t, err)
	require.False(t, applied.Verified)
	require.True(t, applied.InList)
	require.Equal(t, "other-app doit", applied.DiskCommand)
	require.Contains(t, applied.VerifyNote, `command on disk is "other-app doit"`)
}

func TestEnsureBindingExistingEmptyBindingIsUnverified(t *testing.T) {
	// A sticky entry whose binding the user cleared: gsd grabs nothing
	// for an empty binding, so the summary must not claim a summon key.
	s := newScriptedRunner(t)
	s.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.on("''", "get", entrySchemaPath, "binding")
	s.on("'cst toggle'", "get", entrySchemaPath, "command")
	scriptVerify(s, "['"+OurPath+"']", "''", "'cst toggle'")

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), "cst toggle")
	require.NoError(t, err)
	require.True(t, applied.Existing)
	require.False(t, applied.Verified)
	require.Contains(t, applied.VerifyNote, "binding on disk is empty")
	require.Empty(t, s.setCalls(), "an existing entry is still never rewritten")
}

func TestEnsureBindingVerifyReadFailuresAreNonFatal(t *testing.T) {
	// The writes landed; only the read-back breaks. EnsureBinding must
	// not fail -- it reports the unverified state with the reason.
	sim := &dconfSim{
		values: map[string]string{
			mediaKeysSchema + "\x00" + customListKey: "@as []",
		},
		listings: map[string]string{},
	}
	failing := func(ctx context.Context, args ...string) (string, error) {
		if args[0] == "get" && args[1] == mediaKeysSchema && sim.sets == 4 {
			return "", errors.New("dconf went away")
		}
		return sim.run(ctx, args...)
	}

	applied, err := EnsureBinding(context.Background(), failing, mustParse(t, "alt+space"), "cst toggle")
	require.NoError(t, err)
	require.True(t, applied.Changed, "the writes happened")
	require.False(t, applied.Verified)
	require.Contains(t, applied.VerifyNote, "dconf went away")
}
