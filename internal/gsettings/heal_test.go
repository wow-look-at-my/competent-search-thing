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

// brewEntryLayout builds the Homebrew shape the migration tests need
// under root: the real binary in a versioned Cellar dir plus the
// stable <root>/bin symlink shim. Returns (cellar binary, shim).
func brewEntryLayout(t *testing.T, root, version string) (real, shim string) {
	t.Helper()
	real = writeExecFile(t, filepath.Join(root, "Cellar", "cst", version, "bin", "cst"))
	shim = filepath.Join(root, "bin", "cst")
	require.NoError(t, os.MkdirAll(filepath.Dir(shim), 0o755))
	require.NoError(t, os.Symlink(real, shim))
	return real, shim
}

func TestEnsureBindingMigratesLiveCellarSpellingToStable(t *testing.T) {
	// The stored command still launches the running binary, but its
	// spelling is the version-pinned Cellar path -- the one the next
	// brew upgrade deletes while gsd keeps running it. The self-heal
	// migrates it to the stable spelling while BOTH paths still
	// resolve: Repaired, exactly one set call (the command key; the
	// binding is the user's), and the read-back verifies the new
	// command on disk.
	real, shim := brewEntryLayout(t, t.TempDir(), "1.1.0")
	stored := ToggleCommand(real)
	command := ToggleCommand(shim)
	require.NotEqual(t, stored, command)

	s := newScriptedRunner(t)
	scriptExistingEntry(s, "<Control><Alt>space", stored)
	s.on("", "set", entrySchemaPath, "command", quoteVariantString(command))
	scriptVerify(s, "['"+OurPath+"']", "'<Control><Alt>space'", quoteVariantString(command))

	applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), command)
	require.NoError(t, err)
	require.True(t, applied.Existing)
	require.True(t, applied.Repaired)
	require.True(t, applied.Changed)
	require.Equal(t, stored, applied.PreviousCommand)
	require.True(t, applied.Verified)
	require.Equal(t, command, applied.DiskCommand)
	require.Equal(t, [][]string{
		{"set", entrySchemaPath, "command", quoteVariantString(command)},
	}, s.setCalls(), "exactly the command key is written; the binding argv never appears")
}

func TestEnsureBindingMigratesAcrossBrewPrefixShapes(t *testing.T) {
	// The Cellar classification reads the prefix from the path itself,
	// so the migration works for any install prefix.
	for _, prefix := range []string{
		filepath.Join("opt", "homebrew"),
		filepath.Join("home", "alice", ".linuxbrew"),
		filepath.Join("weird", "custom-prefix"),
	} {
		t.Run(prefix, func(t *testing.T) {
			real, shim := brewEntryLayout(t, filepath.Join(t.TempDir(), prefix), "2.0.0")
			stored := ToggleCommand(real)
			command := ToggleCommand(shim)

			s := newScriptedRunner(t)
			scriptExistingEntry(s, "<Super>space", stored)
			s.on("", "set", entrySchemaPath, "command", quoteVariantString(command))
			scriptVerify(s, "['"+OurPath+"']", "'<Super>space'", quoteVariantString(command))

			applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), command)
			require.NoError(t, err)
			require.True(t, applied.Repaired)
			require.Equal(t, stored, applied.PreviousCommand)
			require.Len(t, s.setCalls(), 1)
		})
	}
}

func TestEnsureBindingKeepsUpgradeStableSpellings(t *testing.T) {
	// Working spellings that are not version-pinned are never churned,
	// whatever the new command looks like: the stable linked path
	// (even against an opt-spelled new command), a custom symlink the
	// user wrote, and a versioned spelling when the new command is
	// versioned too (no stable spelling was derivable; churning
	// versioned->versioned buys nothing). Zero writes each, and the
	// read-back verifies the stored command actually on disk.
	root := t.TempDir()
	real, shim := brewEntryLayout(t, root, "1.1.0")
	optDir := filepath.Join(root, "opt", "cst")
	require.NoError(t, os.MkdirAll(filepath.Dir(optDir), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(root, "Cellar", "cst", "1.1.0"), optDir))
	custom := filepath.Join(root, "home", "mylauncher")
	require.NoError(t, os.MkdirAll(filepath.Dir(custom), 0o755))
	require.NoError(t, os.Symlink(real, custom))
	// A second versioned spelling of the same binary (hardlink), for
	// the versioned->versioned case.
	real2 := filepath.Join(root, "Cellar", "cst", "1.2.0", "bin", "cst")
	require.NoError(t, os.MkdirAll(filepath.Dir(real2), 0o755))
	require.NoError(t, os.Link(real, real2))

	cases := map[string]struct{ stored, command string }{
		"stable stored, identical new":    {ToggleCommand(shim), ToggleCommand(shim)},
		"stable stored, opt new":          {ToggleCommand(shim), ToggleCommand(filepath.Join(optDir, "bin", "cst"))},
		"custom symlink stored":           {ToggleCommand(custom), ToggleCommand(shim)},
		"versioned stored, versioned new": {ToggleCommand(real), ToggleCommand(real2)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newScriptedRunner(t)
			scriptExistingEntry(s, "<Control><Alt>space", tc.stored)
			scriptVerify(s, "['"+OurPath+"']", "'<Control><Alt>space'", quoteVariantString(tc.stored))

			applied, err := EnsureBinding(context.Background(), s.run, mustParse(t, "alt+space"), tc.command)
			require.NoError(t, err)
			require.True(t, applied.Existing)
			require.False(t, applied.Repaired)
			require.False(t, applied.Changed)
			require.True(t, applied.Verified)
			require.Equal(t, tc.stored, applied.DiskCommand)
			require.Empty(t, s.setCalls(), "a spelling that survives upgrades is never churned")
		})
	}
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
