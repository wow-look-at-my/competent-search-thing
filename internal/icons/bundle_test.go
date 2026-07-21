package icons

import (
	"encoding/base64"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeBundle assembles a fixture .app bundle: Contents/Info.plist
// (raw bytes) and optionally Contents/Resources/<iconFile> (raw
// bytes). Returns the bundle path. The bundle branch is selected by
// ref shape alone, so these fixtures exercise the darwin path on any
// OS.
func writeBundle(t *testing.T, dir, name string, plist []byte, iconFile string, icns []byte) string {
	t.Helper()
	bundle := filepath.Join(dir, name)
	writeTestFile(t, filepath.Join(bundle, "Contents", "Info.plist"), plist)
	if iconFile != "" {
		writeTestFile(t, filepath.Join(bundle, "Contents", "Resources", iconFile), icns)
	}
	return bundle
}

// xmlIconPlist builds a minimal XML Info.plist with a
// CFBundleIconFile value.
func xmlIconPlist(iconFile string) []byte {
	return []byte(`<plist version="1.0"><dict>` +
		`<key>CFBundleIconFile</key><string>` + iconFile + `</string>` +
		`</dict></plist>`)
}

func TestBundleIconResolvesXMLPlist(t *testing.T) {
	f := newFixture(t)
	entry := tinyPNG(t, 2, color.White)
	bundle := writeBundle(t, f.root, "Editor.app", xmlIconPlist("electron"),
		"electron.icns", buildIcns(t, icnsEntry{"ic07", entry}))
	svc := NewService(f.options(t))

	key := "app:" + bundle
	got := svc.Resolve([]string{key}, 64)
	require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(entry), got[key],
		"the extension-less CFBundleIconFile gets .icns appended and the PNG entry passes through")
}

func TestBundleIconResolvesBinaryPlist(t *testing.T) {
	f := newFixture(t)
	entry := tinyPNG(t, 2, color.Black)
	bundle := writeBundle(t, f.root, "Xcodeish.app", buildIconPlist(t, "AppIcon.icns", ""),
		"AppIcon.icns", buildIcns(t, icnsEntry{"ic08", entry}))
	svc := NewService(f.options(t))

	key := "app:" + bundle
	got := svc.Resolve([]string{key}, 64)
	require.Contains(t, got, key, "bplist00 Info.plist resolves too")
	require.True(t, strings.HasPrefix(got[key], "data:image/png;base64,"))
}

func TestBundleIconMisses(t *testing.T) {
	f := newFixture(t)
	entry := tinyPNG(t, 2, color.White)
	goodIcns := buildIcns(t, icnsEntry{"ic07", entry})

	cases := map[string]string{
		"no plist": writeBundle(t, f.root, "Bare.app", nil, "", nil),
		"assets-car only": writeBundle(t, f.root, "Catalyst.app",
			buildIconPlist(t, "", "AppIconAsset"), "", nil),
		"icon file missing on disk": writeBundle(t, f.root, "Ghost.app",
			xmlIconPlist("gone"), "", nil),
		"legacy-only icns": writeBundle(t, f.root, "Legacy.app",
			xmlIconPlist("old"), "old.icns",
			buildIcns(t, icnsEntry{"is32", []byte{1, 2, 3}})),
		"traversal-shaped icon ref": writeBundle(t, f.root, "Sneaky.app",
			xmlIconPlist("../../../etc/passwd"), "", nil),
		"non-icns extension": writeBundle(t, f.root, "Odd.app",
			xmlIconPlist("thing.tiff"), "thing.tiff", goodIcns),
		"corrupt icns": writeBundle(t, f.root, "Broken.app",
			xmlIconPlist("broken"), "broken.icns", []byte("not an icns")),
	}
	// The no-plist case needs the Contents dir without the file.
	require.NoError(t, os.Remove(filepath.Join(f.root, "Bare.app", "Contents", "Info.plist")))

	svc := NewService(f.options(t))
	for name, bundle := range cases {
		got := svc.Resolve([]string{"app:" + bundle}, 64)
		require.Empty(t, got, "case %q must miss into the glyph fallback", name)
	}
}

func TestBundleIconMissesAreNegativeCached(t *testing.T) {
	f := newFixture(t)
	bundle := writeBundle(t, f.root, "Late.app", xmlIconPlist("icon"), "", nil)
	svc := NewService(f.options(t))
	key := "app:" + bundle

	require.Empty(t, svc.Resolve([]string{key}, 64), "first probe misses")
	// The icon file appearing later does not resurrect the entry: the
	// miss was negative-cached (same policy as every other source).
	writeTestFile(t, filepath.Join(bundle, "Contents", "Resources", "icon.icns"),
		buildIcns(t, icnsEntry{"ic07", tinyPNG(t, 2, color.White)}))
	require.Empty(t, svc.Resolve([]string{key}, 64))
}

func TestBundleIconOversizePlistRejected(t *testing.T) {
	f := newFixture(t)
	huge := make([]byte, maxPlistBytes+1)
	copy(huge, xmlIconPlist("icon"))
	bundle := writeBundle(t, f.root, "Huge.app", huge, "icon.icns",
		buildIcns(t, icnsEntry{"ic07", tinyPNG(t, 2, color.White)}))
	svc := NewService(f.options(t))
	require.Empty(t, svc.Resolve([]string{"app:" + bundle}, 64))
}

func TestBundleIconCaseInsensitiveAppExt(t *testing.T) {
	f := newFixture(t)
	entry := tinyPNG(t, 2, color.White)
	bundle := writeBundle(t, f.root, "Loud.APP", xmlIconPlist("i"),
		"i.icns", buildIcns(t, icnsEntry{"ic07", entry}))
	svc := NewService(f.options(t))
	key := "app:" + bundle
	require.Contains(t, svc.Resolve([]string{key}, 64), key)
}

func TestBundleIconPositiveCacheHit(t *testing.T) {
	f := newFixture(t)
	entry := tinyPNG(t, 2, color.White)
	bundle := writeBundle(t, f.root, "Warm.app", xmlIconPlist("i"),
		"i.icns", buildIcns(t, icnsEntry{"ic07", entry}))
	svc := NewService(f.options(t))
	key := "app:" + bundle
	first := svc.Resolve([]string{key}, 64)[key]
	require.NotEmpty(t, first)
	// Remove the backing files: the second resolve must serve the
	// cached URI without touching the disk.
	require.NoError(t, os.RemoveAll(bundle))
	require.Equal(t, first, svc.Resolve([]string{key}, 64)[key])
}

/* --- the NativeAppIcon seam (the OS-rendering fallback) ------------- */

func TestBundleIconNativeFallbackServesAssetsCarOnly(t *testing.T) {
	f := newFixture(t)
	// CFBundleIconName without CFBundleIconFile: the pure plist/icns
	// path misses (the Assets.car-only shape). Without the seam this
	// exact fixture negative-caches into the glyph (TestBundleIconMisses
	// "assets-car only"); with it, the seam runs BEFORE the
	// negative-caching decision, so the key resolves to the OS's icon
	// instead of pinning the miss.
	bundle := writeBundle(t, f.root, "Catalyst.app", buildIconPlist(t, "", "AppIconAsset"), "", nil)
	nativePNG := tinyPNG(t, 4, color.White)
	calls := 0
	gotPath, gotSize := "", 0
	opt := f.options(t)
	opt.NativeAppIcon = func(path string, sizePx int) []byte {
		calls++
		gotPath, gotSize = path, sizePx
		return nativePNG
	}
	svc := NewService(opt)
	key := "app:" + bundle

	require.Equal(t, pngURI(nativePNG), svc.Resolve([]string{key}, 64)[key],
		"the seam's PNG serves as a data URI when the pure path misses")
	require.Equal(t, bundle, gotPath, "the seam sees the bundle path")
	require.Equal(t, 64, gotSize, "the seam sees the clamped pixel size")

	// A native hit lands in the positive cache like any other hit:
	// the repeat resolve serves from cache without re-asking the seam.
	require.Equal(t, pngURI(nativePNG), svc.Resolve([]string{key}, 64)[key])
	require.Equal(t, 1, calls)
}

func TestBundleIconNativeFallbackNilStaysNegativeCached(t *testing.T) {
	f := newFixture(t)
	bundle := writeBundle(t, f.root, "NoIcon.app", buildIconPlist(t, "", "AppIconAsset"), "", nil)
	var answer []byte
	opt := f.options(t)
	opt.NativeAppIcon = func(string, int) []byte { return answer }
	svc := NewService(opt)
	key := "app:" + bundle

	require.Empty(t, svc.Resolve([]string{key}, 64),
		"pure miss + seam answering nil = the honest glyph fallback")
	// The seam WAS consulted and had nothing, so the miss is
	// negative-cached like every other source's; a later answer does
	// not resurrect the entry within this process.
	answer = tinyPNG(t, 2, color.White)
	require.Empty(t, svc.Resolve([]string{key}, 64))
}

func TestBundleIconNativeFallbackNotConsultedOnPureHit(t *testing.T) {
	f := newFixture(t)
	entry := tinyPNG(t, 2, color.White)
	bundle := writeBundle(t, f.root, "Plain.app", xmlIconPlist("i"),
		"i.icns", buildIcns(t, icnsEntry{"ic07", entry}))
	calls := 0
	opt := f.options(t)
	opt.NativeAppIcon = func(string, int) []byte {
		calls++
		return tinyPNG(t, 4, color.Black)
	}
	svc := NewService(opt)
	key := "app:" + bundle

	require.Equal(t, pngURI(entry), svc.Resolve([]string{key}, 64)[key],
		"the pure plist/icns extraction stays primary")
	require.Zero(t, calls, "the seam is a fallback, never a replacement")
}

func TestBundleIconNativeFallbackRejectsInvalidBytes(t *testing.T) {
	// Defense in depth: the seam contract is a PNG within the byte
	// cap; anything else misses into the glyph rather than shipping
	// junk to the frontend.
	oversize := make([]byte, defaultMaxFileBytes+1)
	copy(oversize, pngMagic)
	cases := map[string][]byte{
		"not a png": []byte("GIF89a definitely not a png"),
		"oversized": oversize,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			bundle := writeBundle(t, f.root, "Odd.app", buildIconPlist(t, "", "X"), "", nil)
			opt := f.options(t)
			opt.NativeAppIcon = func(string, int) []byte { return payload }
			svc := NewService(opt)
			require.Empty(t, svc.Resolve([]string{"app:" + bundle}, 64))
		})
	}
}
