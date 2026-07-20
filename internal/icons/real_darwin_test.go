//go:build darwin

package icons

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRealApplicationsResolve runs the production bundle-icon path
// against the mac runner's real /Applications: at least one installed
// app must resolve to a PNG data URI, and the icns-vs-miss tally is
// logged so the estimated Assets.car-only fraction (5-15%) stays
// measured, not guessed. A runner with no .app bundles at all is
// tolerated with the logged count; a populated /Applications where
// NOTHING resolves means the extraction path regressed and fails.
func TestRealApplicationsResolve(t *testing.T) {
	entries, err := os.ReadDir("/Applications")
	if err != nil {
		t.Skipf("/Applications unreadable: %v", err)
	}
	svc := NewService(Options{Logf: t.Logf})
	bundles, resolved := 0, 0
	var misses []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".app") {
			continue
		}
		bundles++
		key := "app:/Applications/" + e.Name()
		got := svc.Resolve([]string{key}, 64)
		if uri, ok := got[key]; ok && strings.HasPrefix(uri, "data:image/png;base64,") {
			resolved++
		} else {
			misses = append(misses, e.Name())
		}
	}
	t.Logf("icons: real /Applications: %d bundles, %d resolved, %d missed (glyph fallback): %v",
		bundles, resolved, len(misses), misses)
	if bundles == 0 {
		t.Log("icons: empty /Applications on this runner; nothing to assert")
		return
	}
	require.Greater(t, resolved, 0,
		"a populated /Applications where no bundle resolves means the icns extraction path regressed")
}
