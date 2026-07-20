package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnknownKeysCleanDocument(t *testing.T) {
	data, err := Encode(Default())
	require.NoError(t, err)
	require.Empty(t, UnknownKeys(data), "a Save-produced document has no unknown keys")
}

func TestUnknownKeysReportsDottedPaths(t *testing.T) {
	raw := []byte(`{
		"$schema": "https://example.test/config.schema.json",
		"maxResults": 10,
		"frobnicate": true,
		"watcher": {"maxWatches": 5, "frobnicate": 1},
		"search": {"frecency": {"halfLifeDays": 7, "bogus": 2}},
		"plugins": {"entries": {"calc": {"enabled": false, "extra": 1}}},
		"rewrites": [{"name": "n", "pattern": "p", "replacement": "r", "wat": 1}]
	}`)
	got := UnknownKeys(raw)
	require.Equal(t, []string{
		"frobnicate",
		"plugins.entries.calc.extra",
		"rewrites[0].wat",
		"search.frecency.bogus",
		"watcher.frobnicate",
	}, got, "sorted dotted paths, maps by key, arrays by index")
	require.NotContains(t, got, "$schema",
		"the $schema editor hint is a known reserved Config field, never an unknown key")
}

func TestUnknownKeysSkipsOpaqueSettings(t *testing.T) {
	// A plugin entry's settings object is opaque json.RawMessage: it
	// round-trips verbatim, so its keys are never "unknown".
	raw := []byte(`{"plugins": {"entries": {"calc": {"settings": {"anything": {"goes": 1}}}}}}`)
	require.Empty(t, UnknownKeys(raw))
}

func TestUnknownKeysToleratesShapeMismatches(t *testing.T) {
	// Wrong-shaped values are the strict decoder's problem; the key
	// walk just skips them instead of guessing.
	require.Empty(t, UnknownKeys([]byte(`{"watcher": 5, "rewrites": "nope"}`)))
	require.Nil(t, UnknownKeys([]byte(`[1, 2]`)), "a non-object document has no keys to vet")
	require.Nil(t, UnknownKeys([]byte(`not json`)))
}

func TestCurrentRootsVersionMatchesDefault(t *testing.T) {
	require.Equal(t, Default().RootsVersion, CurrentRootsVersion())
	require.Positive(t, CurrentRootsVersion())
}

func TestSaveIsAtomicTempRename(t *testing.T) {
	dir := setConfigDir(t)
	p := filepath.Join(dir, "config.json")

	// First save materializes the file with Encode's exact bytes.
	require.NoError(t, Save(Default()))
	want, err := Encode(Default())
	require.NoError(t, err)
	got, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, want, got, "Save writes exactly Encode's bytes")

	// A second save over the existing file works and leaves no temp
	// residue behind (the temp file was renamed onto config.json).
	c := Default()
	c.MaxResults = 123
	require.NoError(t, Save(c))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.HasPrefix(e.Name(), ".config-"),
			"no leftover temp file: %s", e.Name())
	}
	var onDisk Config
	data, err := os.ReadFile(p)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &onDisk))
	require.Equal(t, 123, onDisk.MaxResults)

	info, err := os.Stat(p)
	require.NoError(t, err)
	if fsSupportsChmod() {
		require.Equal(t, os.FileMode(0o644), info.Mode().Perm(),
			"the historical 0644 perms survive the atomic rewrite")
	}
}

func TestSaveFailureKeepsExistingFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("directory write permissions do not bind root")
	}
	dir := setConfigDir(t)
	p := filepath.Join(dir, "config.json")
	require.NoError(t, Save(Default()))
	before, err := os.ReadFile(p)
	require.NoError(t, err)

	// A read-only directory refuses the temp-file create, so Save
	// fails BEFORE touching config.json -- the previous content
	// survives intact (the atomicity payoff).
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	c := Default()
	c.MaxResults = 999
	require.Error(t, Save(c))

	require.NoError(t, os.Chmod(dir, 0o755))
	after, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, before, after, "a failed save never corrupts the existing file")
}

// fsSupportsChmod reports whether the test filesystem honors chmod
// (windows perms map differently; the linux/darwin CI jobs both do).
func fsSupportsChmod() bool {
	return os.PathSeparator == '/'
}
