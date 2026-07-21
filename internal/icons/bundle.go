package icons

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// macOS .app bundle icon resolution: an "app:" ref that is an absolute
// path ending in ".app" (the darwin appctx source's natural ref -- see
// internal/platform/native appsource_darwin.go) resolves through
// Contents/Info.plist -> CFBundleIconFile (".icns" appended when the
// value has no extension) -> Contents/Resources/<file> -> the best
// modern PNG entry of the icns container, passed through as a data URI
// with no image decoding. The ref SHAPE alone selects this branch, so
// the whole path is headless-testable on any OS against fixture
// bundles. When that pure extraction misses -- CFBundleIconName
// without CFBundleIconFile (the icon lives in an Assets.car catalog
// this package deliberately does not parse), legacy-only icns
// payloads, or any other structural miss -- the injectable
// Options.NativeAppIcon seam is asked BEFORE the miss is
// negative-cached: on darwin the production seam is the OS's own
// rendering (NSWorkspace iconForFile, exactly what Launchpad/Finder
// show, asset catalogs included), so an installed app only ever falls
// to the glyph when macOS itself has no icon for it. A nil seam (off
// darwin, and in tests that want the pure path alone) keeps the old
// miss-into-glyph behavior byte-identical.

// Bundle-path size caps. The Info.plist cap is generous (real ones
// top out around 100KB); the icns container cap bounds the one-time
// read while per-entry selection separately skips entries larger than
// maxIcnsEntryBytes, so a resolved data URI stays row-icon sized.
const (
	maxPlistBytes     = 4 << 20
	maxIcnsBytes      = 32 << 20
	maxIcnsEntryBytes = 512 << 10
)

// bundleIcon serves one .app bundle path through the same two-level
// cache as every other icon source (bundle paths contain separators,
// so the key family cannot collide with themed names). The pure
// plist/icns extraction stays primary (fast, no OS round-trip); the
// NativeAppIcon seam backs it up on a miss, and ONLY a miss of both
// is negative-cached -- the ordering that keeps a pure-path miss from
// ever pinning the glyph over an icon the OS can render. A native hit
// lands in the positive cache like any other hit. Callers hold s.mu.
func (s *Service) bundleIcon(bundle string, size int) (string, bool) {
	ck := bundle + "|" + strconv.Itoa(size)
	if uri, ok := s.cache.get(ck); ok {
		return uri, true
	}
	if _, neg := s.negative.get(ck); neg {
		return "", false
	}
	uri := s.readBundleIcon(bundle, size)
	if uri == "" {
		uri = s.nativeBundleIcon(bundle, size)
	}
	if uri == "" {
		s.negative.put(ck, "")
		return "", false
	}
	s.cache.put(ck, uri)
	return uri, true
}

// nativeBundleIcon asks the NativeAppIcon seam for the OS's own
// rendering of the bundle's icon; "" without a seam, on a nil/empty
// answer, or on bytes that are not a PNG within the file-size cap
// (defense in depth -- the production seam is our own shim, but junk
// must miss into the glyph, never ship to the frontend).
func (s *Service) nativeBundleIcon(bundle string, size int) string {
	if s.nativeAppIcon == nil {
		return ""
	}
	png := s.nativeAppIcon(bundle, size)
	if len(png) == 0 || int64(len(png)) > s.maxFileBytes || !bytes.HasPrefix(png, pngMagic) {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// readBundleIcon performs the uncached plist -> icns -> PNG walk; ""
// on any miss (no plist, no CFBundleIconFile, a non-icns ref, a
// traversal-shaped ref, no acceptable PNG entry).
func (s *Service) readBundleIcon(bundle string, size int) string {
	plistData, err := readCapped(filepath.Join(bundle, "Contents", "Info.plist"), maxPlistBytes)
	if err != nil {
		return ""
	}
	iconFile, _ := plistIconRefs(plistData)
	if iconFile == "" {
		// No CFBundleIconFile: either no icon at all or an
		// Assets.car-only app (CFBundleIconName) -- the caller falls
		// to the NativeAppIcon seam.
		return ""
	}
	if filepath.Ext(iconFile) == "" {
		iconFile += ".icns"
	}
	// The value is a file name inside Resources, never a path: reject
	// separators and ".." (safeIconName), and anything not .icns.
	if !safeIconName(iconFile) || filepath.Ext(iconFile) != ".icns" {
		return ""
	}
	icnsData, err := readCapped(filepath.Join(bundle, "Contents", "Resources", iconFile), maxIcnsBytes)
	if err != nil {
		return ""
	}
	png, ok := icnsBestPNG(icnsData, size, maxIcnsEntryBytes)
	if !ok {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// readCapped reads a regular file of at most max bytes; the stat-first
// check keeps an oversized file from ever being pulled into memory.
func readCapped(path string, max int64) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() <= 0 || fi.Size() > max {
		return nil, errors.New("not a readable regular file within the size cap")
	}
	return os.ReadFile(path)
}
