package gsettings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The startup self-heal of an existing entry's command: liveness is
// decided against the real filesystem, so these tests build real
// files and symlinks in temp dirs.

// writeExecFile creates an executable file at path (parents included)
// and returns path -- the heal tests decide liveness against the real
// filesystem.
func writeExecFile(t *testing.T, path string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
	return path
}

// scriptExistingEntry scripts the three decision reads for an entry
// already in the list, with the given stored binding and command.
func scriptExistingEntry(s *scriptedRunner, binding, command string) {
	s.on("['"+OurPath+"']", "get", mediaKeysSchema, customListKey)
	s.on(quoteVariantString(binding), "get", entrySchemaPath, "binding")
	s.on(quoteVariantString(command), "get", entrySchemaPath, "command")
}

func TestEnsureBindingHealsDeadCommandPath(t *testing.T) {
	// The Homebrew-upgrade field bug: the entry stores the versioned
	// Cellar path of the previous release, which the upgrade deleted.
	// The command key self-heals to the new command; the binding -- the
	// user's accelerator -- is never written.
	root := t.TempDir()
	newExe := writeExecFile(t, filepath.Join(root, "bin", "cst"))
	newCommand := ToggleCommand(newExe)
	deadCommand := filepath.Join(root, "Cellar", "cst", "1.0.0", "bin", "cst") + " toggle"

	s := newScriptedRunner(t)
	scriptExistingEntry(s, "<Control><Alt>space", deadCommand)
	s.on("", "set", entrySchemaPath, "command", quoteVariantString(newCommand))
	scriptVerify(s, "['"+OurPath+"']", "'<Control><Alt>space'", quoteVariantString(newCommand))

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), newCommand)
	require.NoError(t, err)
	require.True(t, applied.Existing)
	require.True(t, applied.Repaired)
	require.True(t, applied.Changed)
	require.Equal(t, deadCommand, applied.PreviousCommand)
	require.True(t, applied.Verified, "the heal path still runs the three-read verification")
	require.Equal(t, newCommand, applied.DiskCommand)
	require.Equal(t, [][]string{
		{"set", entrySchemaPath, "command", quoteVariantString(newCommand)},
	}, s.setCalls(), "only the command key is written; the binding argv never appears")
}

func TestEnsureBindingHealsCommandPointingAtDifferentBinary(t *testing.T) {
	// The stored path still exists but is not the running binary (an
	// old version kept on disk, or a foreign program the user pointed
	// the entry at): realpath mismatch, heal.
	root := t.TempDir()
	oldExe := writeExecFile(t, filepath.Join(root, "Cellar", "cst", "1.0.0", "bin", "cst"))
	newExe := writeExecFile(t, filepath.Join(root, "Cellar", "cst", "1.1.0", "bin", "cst"))
	oldCommand := oldExe + " toggle"
	newCommand := ToggleCommand(newExe)

	s := newScriptedRunner(t)
	scriptExistingEntry(s, "<Super>space", oldCommand)
	s.on("", "set", entrySchemaPath, "command", quoteVariantString(newCommand))
	scriptVerify(s, "['"+OurPath+"']", "'<Super>space'", quoteVariantString(newCommand))

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), newCommand)
	require.NoError(t, err)
	require.True(t, applied.Repaired)
	require.Equal(t, oldCommand, applied.PreviousCommand)
	require.True(t, applied.Verified)
	require.Len(t, s.setCalls(), 1)
}

func TestEnsureBindingKeepsWorkingCommandSpelling(t *testing.T) {
	// The stored command differs textually from the new one but still
	// launches the very binary the new command names (here: the entry
	// holds the version-pinned path, the new command the stable shim,
	// both resolving to the same file): zero writes, and the read-back
	// verifies the command actually on disk, so the caller still gets
	// an honest Verified for a working entry.
	root := t.TempDir()
	real := writeExecFile(t, filepath.Join(root, "Cellar", "cst", "1.1.0", "bin", "cst"))
	shim := filepath.Join(root, "bin", "cst")
	require.NoError(t, os.MkdirAll(filepath.Dir(shim), 0o755))
	require.NoError(t, os.Symlink(real, shim))
	stored := real + " toggle"
	command := ToggleCommand(shim)
	require.NotEqual(t, stored, command)

	s := newScriptedRunner(t)
	scriptExistingEntry(s, "<Control><Alt>space", stored)
	scriptVerify(s, "['"+OurPath+"']", "'<Control><Alt>space'", quoteVariantString(stored))

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), command)
	require.NoError(t, err)
	require.True(t, applied.Existing)
	require.False(t, applied.Repaired)
	require.False(t, applied.Changed)
	require.True(t, applied.Verified)
	require.Equal(t, stored, applied.DiskCommand)
	require.Empty(t, s.setCalls(), "a still-working spelling is never churned")
}

func TestEnsureBindingHealsUnlaunchableCommands(t *testing.T) {
	// Commands that cannot launch anything under gsd -- empty,
	// unparseable, relative -- always heal.
	newExe := writeExecFile(t, filepath.Join(t.TempDir(), "cst"))
	newCommand := ToggleCommand(newExe)
	cases := map[string]string{
		"empty":       "",
		"unparseable": `"broken toggle`,
		"relative":    "cst toggle",
	}
	for name, stored := range cases {
		t.Run(name, func(t *testing.T) {
			s := newScriptedRunner(t)
			scriptExistingEntry(s, "<Control><Alt>space", stored)
			s.on("", "set", entrySchemaPath, "command", quoteVariantString(newCommand))
			scriptVerify(s, "['"+OurPath+"']", "'<Control><Alt>space'", quoteVariantString(newCommand))

			applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), newCommand)
			require.NoError(t, err)
			require.True(t, applied.Repaired)
			require.Equal(t, stored, applied.PreviousCommand)
			require.Len(t, s.setCalls(), 1)
		})
	}
}

func TestEnsureBindingRepairWriteFailurePropagates(t *testing.T) {
	newExe := writeExecFile(t, filepath.Join(t.TempDir(), "cst"))
	newCommand := ToggleCommand(newExe)

	s := newScriptedRunner(t)
	scriptExistingEntry(s, "<Control><Alt>space", "/gone/cst toggle")
	s.fail(errors.New("dconf denied"), "set", entrySchemaPath, "command", quoteVariantString(newCommand))

	_, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), newCommand)
	require.ErrorContains(t, err, "dconf denied")
}

func TestEnsureBindingHealThenIdempotent(t *testing.T) {
	// After the one-time heal, subsequent runs with an unchanged world
	// perform zero writes.
	root := t.TempDir()
	newExe := writeExecFile(t, filepath.Join(root, "bin", "cst"))
	command := ToggleCommand(newExe)
	dead := filepath.Join(root, "removed", "cst") + " toggle"
	sim := &dconfSim{
		values: map[string]string{
			mediaKeysSchema + "\x00" + customListKey: "['" + OurPath + "']",
			entrySchemaPath + "\x00binding":          "'<Control><Alt>space'",
			entrySchemaPath + "\x00command":          quoteVariantString(dead),
		},
		listings: map[string]string{},
	}

	first, err := EnsureBinding(context.Background(), sim.run, mustParse(t, "alt+space"), command)
	require.NoError(t, err)
	require.True(t, first.Repaired)
	require.Equal(t, dead, first.PreviousCommand)
	require.Equal(t, 1, sim.sets, "the heal writes exactly the command key")
	require.True(t, first.Verified)
	require.Equal(t, command, first.DiskCommand)

	second, err := EnsureBinding(context.Background(), sim.run, mustParse(t, "alt+space"), command)
	require.NoError(t, err)
	require.Equal(t, 1, sim.sets, "the run after a heal is zero-write")
	require.False(t, second.Repaired)
	require.False(t, second.Changed)
	require.True(t, second.Verified)
}
