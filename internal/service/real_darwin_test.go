//go:build darwin

package service

// The darwin-only real-tool gate (the readers_darwin_test.go
// precedent): plutil ships with every macOS, so linting the generated
// plist against Apple's own parser is cheap and honest. This runs
// un-gated on the mac CI job and HARD-fails on an invalid plist.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneratedPlistPassesPlutilLint(t *testing.T) {
	cases := []struct {
		name, exe, log string
	}{
		{"plain", "/usr/local/bin/competent-search-thing", "/Users/u/Library/Logs/competent-search-thing/competent-search-thing.log"},
		{"specials", `/opt/a & b/<weird> "app"`, `/logs/it's & <odd>.log`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "agent.plist")
			require.NoError(t, os.WriteFile(p, []byte(LaunchAgentPlist(tc.exe, tc.log)), 0o644))
			out, err := exec.Command("plutil", "-lint", p).CombinedOutput()
			require.NoError(t, err, "plutil -lint rejected the generated plist: %s", out)
		})
	}
}
