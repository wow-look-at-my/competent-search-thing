//go:build darwin

package icons

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform/native"
)

// maxRealBundles bounds the /Applications sweeps on a pathological
// machine; CI runners carry a few dozen bundles, well under the cap,
// so every one of theirs is asserted.
const maxRealBundles = 300

// realAppBundles lists up to maxRealBundles .app entries in
// /Applications that actually exist (broken symlinks skipped --
// macOS shows no icon for those either), skipping the whole test when
// the directory is unreadable.
func realAppBundles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("/Applications")
	if err != nil {
		t.Skipf("/Applications unreadable: %v", err)
	}
	var bundles []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".app") || len(bundles) >= maxRealBundles {
			continue
		}
		path := "/Applications/" + e.Name()
		if _, err := os.Stat(path); err != nil {
			continue
		}
		bundles = append(bundles, path)
	}
	return bundles
}

// TestRealApplicationsPurePathResolves runs the PURE plist/icns
// extraction (no native seam) against the runner's real /Applications:
// at least one installed app must resolve, and the icns-vs-miss tally
// is logged so the primary path's coverage stays measured, not
// guessed. The misses named here are exactly the set the NativeAppIcon
// seam exists to serve.
func TestRealApplicationsPurePathResolves(t *testing.T) {
	bundles := realAppBundles(t)
	svc := NewService(Options{Logf: t.Logf})
	resolved := 0
	var misses []string
	for _, bundle := range bundles {
		key := "app:" + bundle
		got := svc.Resolve([]string{key}, 64)
		if uri, ok := got[key]; ok && strings.HasPrefix(uri, "data:image/png;base64,") {
			resolved++
		} else {
			misses = append(misses, filepath.Base(bundle))
		}
	}
	t.Logf("icons: real /Applications, pure path: %d bundles, %d resolved, %d missed (native-seam territory): %v",
		len(bundles), resolved, len(misses), misses)
	if len(bundles) == 0 {
		t.Log("icons: empty /Applications on this runner; nothing to assert")
		return
	}
	require.Greater(t, resolved, 0,
		"a populated /Applications where no bundle resolves means the icns extraction path regressed")
}

// TestRealApplicationsAllResolveWithNativeFallback is the product
// acceptance gate behind the field report ("little snitch doesn't get
// a fallback, it gets the real icon"): with the production
// NativeAppIcon seam wired -- exactly what internal/app's buildIcons
// wires -- EVERY .app bundle in /Applications must resolve to a real
// image. NSWorkspace renders an icon for every bundle macOS itself can
// (Assets.car included), so ANY miss here is a real bug, never an
// acceptable degrade.
func TestRealApplicationsAllResolveWithNativeFallback(t *testing.T) {
	bundles := realAppBundles(t)
	if len(bundles) == 0 {
		t.Log("icons: empty /Applications on this runner; nothing to assert")
		return
	}
	svc := NewService(Options{Logf: t.Logf, NativeAppIcon: native.AppIconPNG})
	var misses []string
	for _, bundle := range bundles {
		key := "app:" + bundle
		uri, ok := svc.Resolve([]string{key}, 64)[key]
		if !ok || !strings.HasPrefix(uri, "data:image/png;base64,") {
			misses = append(misses, filepath.Base(bundle))
		}
	}
	t.Logf("icons: real /Applications, native fallback wired: %d bundles, %d missed: %v",
		len(bundles), len(misses), misses)
	require.Empty(t, misses,
		"macOS shows an icon for every one of these bundles (Launchpad does); the service must too")
}

// TestRealAssetsCarOnlyAppResolves pins the Little Snitch class
// specifically: an installed app whose Info.plist carries
// CFBundleIconName WITHOUT CFBundleIconFile keeps its icon in an
// Assets.car catalog the pure path cannot read -- the native seam
// must resolve it to the same icon Launchpad shows. Skipped when the
// runner has no such app (the sweep above still covers whatever is
// installed).
func TestRealAssetsCarOnlyAppResolves(t *testing.T) {
	target := ""
	for _, bundle := range realAppBundles(t) {
		data, err := readCapped(filepath.Join(bundle, "Contents", "Info.plist"), maxPlistBytes)
		if err != nil {
			continue
		}
		iconFile, iconName := plistIconRefs(data)
		if iconFile == "" && iconName != "" {
			target = bundle
			break
		}
	}
	if target == "" {
		t.Skip("no Assets.car-only app (CFBundleIconName without CFBundleIconFile) on this runner")
	}
	svc := NewService(Options{Logf: t.Logf, NativeAppIcon: native.AppIconPNG})
	key := "app:" + target
	uri := svc.Resolve([]string{key}, 64)[key]
	require.True(t, strings.HasPrefix(uri, "data:image/png;base64,"),
		"Assets.car-only app %s must resolve through the native seam, got %q", target, uri)
}
