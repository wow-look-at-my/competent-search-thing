package gsettings

// EnsureBindingWith(BindingOptions{ForceBinding}) tests: the config
// live-apply path's forced accelerator rebind over an EXISTING entry.
// The scripting helpers live in ensure_test.go.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// scriptTakenScan scripts the five-schema conflict scan with the stock
// GNOME 46 listings (Alt+Space and Super+Space taken), plus overrides.
func scriptTakenScan(s *scriptedRunner, overrides map[string]string) {
	listings := map[string]string{
		"org.gnome.desktop.wm.keybindings":             gnomeDefaultWM,
		"org.gnome.mutter.keybindings":                 "",
		"org.gnome.mutter.wayland.keybindings":         "",
		"org.gnome.shell.keybindings":                  "",
		"org.gnome.settings-daemon.plugins.media-keys": gnomeDefaultMediaKeys,
	}
	for schema, listing := range overrides {
		listings[schema] = listing
	}
	for schema, listing := range listings {
		s.on(listing, "list-recursively", schema)
	}
}

func TestForceRebindRewritesChangedAccelerator(t *testing.T) {
	s := newScriptedRunner(t)
	s.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.on("'<Control><Alt>space'", "get", entrySchemaPath, "binding")
	s.on("'/usr/bin/cst toggle'", "get", entrySchemaPath, "command")
	scriptTakenScan(s, nil)
	s.on("", "set", entrySchemaPath, "binding", "'<Control><Alt>t'")
	scriptVerify(s, "['"+OurPath+"']", "'<Control><Alt>t'", "'/usr/bin/cst toggle'")

	applied, err := EnsureBindingWith(context.Background(), s.run,
		mustParse(t, "ctrl+alt+t"), "/usr/bin/cst toggle", BindingOptions{ForceBinding: true})
	require.NoError(t, err)
	require.True(t, applied.Existing)
	require.True(t, applied.Rebound)
	require.True(t, applied.Changed)
	require.Equal(t, "<Control><Alt>space", applied.PreviousBinding)
	require.Equal(t, "<Control><Alt>t", applied.Binding)
	require.False(t, applied.FellBack)
	require.True(t, applied.Verified, "the read-back confirms the new accelerator on disk")
	require.Empty(t, applied.RebindSkipped)
	require.Equal(t, [][]string{
		{"set", entrySchemaPath, "binding", "'<Control><Alt>t'"},
	}, s.setCalls(), "exactly the binding key is rewritten")
}

func TestForceRebindSameAcceleratorWritesNothing(t *testing.T) {
	// The stored binding already matches the (normalization-level)
	// requested one: no conflict scan, no writes -- the exact sticky
	// call surface.
	s := newScriptedRunner(t)
	s.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.on("'<Primary><Alt>space'", "get", entrySchemaPath, "binding")
	s.on("'/usr/bin/cst toggle'", "get", entrySchemaPath, "command")
	scriptVerify(s, "['"+OurPath+"']", "'<Primary><Alt>space'", "'/usr/bin/cst toggle'")

	applied, err := EnsureBindingWith(context.Background(), s.run,
		mustParse(t, "ctrl+alt+space"), "/usr/bin/cst toggle", BindingOptions{ForceBinding: true})
	require.NoError(t, err)
	require.False(t, applied.Rebound)
	require.True(t, applied.Verified)
	require.Empty(t, s.setCalls(), "an unchanged accelerator performs zero writes")
	require.Equal(t, [][]string{
		{"get", mediaKeysSchema, customListKey},
		{"get", entrySchemaPath, "binding"},
		{"get", entrySchemaPath, "command"},
		{"get", mediaKeysSchema, customListKey},
		{"get", entrySchemaPath, "binding"},
		{"get", entrySchemaPath, "command"},
	}, s.calls, "no conflict scan either")
}

func TestForceRebindFallsBackWhenRequestedTaken(t *testing.T) {
	// Alt+Space is taken by GNOME's window menu; the forced rebind
	// lands on the first free fallback, honestly flagged FellBack.
	s := newScriptedRunner(t)
	s.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.on("'<Shift><Super>t'", "get", entrySchemaPath, "binding")
	s.on("'/usr/bin/cst toggle'", "get", entrySchemaPath, "command")
	scriptTakenScan(s, nil)
	s.on("", "set", entrySchemaPath, "binding", "'<Control><Alt>space'")
	scriptVerify(s, "['"+OurPath+"']", "'<Control><Alt>space'", "'/usr/bin/cst toggle'")

	applied, err := EnsureBindingWith(context.Background(), s.run,
		mustParse(t, "alt+space"), "/usr/bin/cst toggle", BindingOptions{ForceBinding: true})
	require.NoError(t, err)
	require.True(t, applied.Rebound)
	require.True(t, applied.FellBack)
	require.Equal(t, "<Shift><Super>t", applied.PreviousBinding)
	require.Equal(t, "<Control><Alt>space", applied.Binding)
	require.True(t, applied.Verified)
}

func TestForceRebindAllTakenKeepsWorkingBinding(t *testing.T) {
	// Every candidate is claimed: the existing (working) binding is
	// kept, the skip is reported as a notice -- never an error, never
	// a write.
	s := newScriptedRunner(t)
	s.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.on("'<Shift><Super>t'", "get", entrySchemaPath, "binding")
	s.on("'/usr/bin/cst toggle'", "get", entrySchemaPath, "command")
	scriptTakenScan(s, map[string]string{
		"org.gnome.shell.keybindings": `org.gnome.shell.keybindings toggle-overview ['<Primary><Alt>space']` + "\n",
	})
	scriptVerify(s, "['"+OurPath+"']", "'<Shift><Super>t'", "'/usr/bin/cst toggle'")

	applied, err := EnsureBindingWith(context.Background(), s.run,
		mustParse(t, "alt+space"), "/usr/bin/cst toggle", BindingOptions{ForceBinding: true})
	require.NoError(t, err, "a kept working binding is not a failure")
	require.False(t, applied.Rebound)
	require.NotEmpty(t, applied.RebindSkipped)
	require.Contains(t, applied.RebindSkipped, "<Shift><Super>t", "the notice names the kept binding")
	require.Equal(t, "<Shift><Super>t", applied.Binding)
	require.True(t, applied.Verified, "the read-back verifies the kept binding")
	require.Empty(t, s.setCalls())
}

func TestForceRebindLandingOnInstalledFallbackWritesNothing(t *testing.T) {
	// The requested accelerator is taken and the entry ALREADY holds
	// the fallback the ladder lands on: nothing to write.
	s := newScriptedRunner(t)
	s.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.on("'<Control><Alt>space'", "get", entrySchemaPath, "binding")
	s.on("'/usr/bin/cst toggle'", "get", entrySchemaPath, "command")
	scriptTakenScan(s, nil)
	scriptVerify(s, "['"+OurPath+"']", "'<Control><Alt>space'", "'/usr/bin/cst toggle'")

	applied, err := EnsureBindingWith(context.Background(), s.run,
		mustParse(t, "alt+space"), "/usr/bin/cst toggle", BindingOptions{ForceBinding: true})
	require.NoError(t, err)
	require.False(t, applied.Rebound)
	require.Empty(t, applied.RebindSkipped)
	require.Equal(t, "<Control><Alt>space", applied.Binding)
	require.Empty(t, s.setCalls())
}

func TestForceRebindWriteFailureIsFatal(t *testing.T) {
	s := newScriptedRunner(t)
	s.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.on("'<Control><Alt>space'", "get", entrySchemaPath, "binding")
	s.on("'/usr/bin/cst toggle'", "get", entrySchemaPath, "command")
	scriptTakenScan(s, nil)
	boom := errors.New("dconf sad")
	s.fail(boom, "set", entrySchemaPath, "binding", "'<Control><Alt>t'")

	_, err := EnsureBindingWith(context.Background(), s.run,
		mustParse(t, "ctrl+alt+t"), "/usr/bin/cst toggle", BindingOptions{ForceBinding: true})
	require.ErrorIs(t, err, boom)
	require.Contains(t, err.Error(), "rebinding")
}

func TestEnsureBindingDefaultStaysSticky(t *testing.T) {
	// The zero-options wrapper never rebinds -- the historical sticky
	// contract, byte-identical call surface (pinned again here next to
	// the force tests so the pair reads as one contract).
	s := newScriptedRunner(t)
	s.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.on("'<Shift><Super>t'", "get", entrySchemaPath, "binding")
	s.on("'/usr/bin/cst toggle'", "get", entrySchemaPath, "command")
	scriptVerify(s, "['"+OurPath+"']", "'<Shift><Super>t'", "'/usr/bin/cst toggle'")

	applied, err := EnsureBinding(context.Background(), s.run,
		mustParse(t, "ctrl+alt+t"), "/usr/bin/cst toggle")
	require.NoError(t, err)
	require.False(t, applied.Rebound)
	require.Equal(t, "<Shift><Super>t", applied.Binding, "the user's accelerator survives")
	require.Empty(t, s.setCalls())
}
