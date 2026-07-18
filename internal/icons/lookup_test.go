package icons

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

/* --- shared fixture helpers (used by service_test.go too) ---------- */

// fixture is one throwaway icon world: a data dir (mime db + themed
// icons under data/icons), a ~/.icons stand-in, and a pixmaps dir.
type fixture struct {
	root, data, iconBase, home, pixmaps string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		root:    root,
		data:    filepath.Join(root, "data"),
		home:    filepath.Join(root, "home-icons"),
		pixmaps: filepath.Join(root, "pixmaps"),
	}
	f.iconBase = filepath.Join(f.data, "icons")
	return f
}

// options is a hermetic Options set over the fixture dirs: env empty,
// gsettings answers 'TestTheme', logs to the test log. Tests tweak
// the returned value before NewService.
func (f *fixture) options(t *testing.T) Options {
	t.Helper()
	return Options{
		Getenv:       func(string) string { return "" },
		RunGsettings: fixedGsettings("'TestTheme'\n"),
		Logf:         t.Logf,
		DataDirs:     []string{f.data},
		HomeIcons:    f.home,
		PixmapDirs:   []string{f.pixmaps},
	}
}

// writeTheme drops an index.theme for a theme under data/icons.
func (f *fixture) writeTheme(t *testing.T, name, index string) {
	t.Helper()
	writeTestFile(t, filepath.Join(f.iconBase, name, "index.theme"), []byte(index))
}

// writeIcon drops an icon file at data/icons/<rel>.
func (f *fixture) writeIcon(t *testing.T, rel string, data []byte) {
	t.Helper()
	writeTestFile(t, filepath.Join(f.iconBase, rel), data)
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

func fixedGsettings(out string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return out, nil }
}

func errGsettings(context.Context) (string, error) {
	return "", errors.New("no gsettings here")
}

func pngURI(data []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

func svgURI(data []byte) string {
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(data)
}

// fixed16Index is the simplest useful theme: one Fixed 16px dir.
const fixed16Index = "[Icon Theme]\n[16x16/apps]\nSize=16\nType=Fixed\n"

/* --- lookup behavior ----------------------------------------------- */

func TestLookupExactSizeBeatsClosest(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme",
		"[Icon Theme]\n[16x16/apps]\nSize=16\nType=Fixed\n\n[32x32/apps]\nSize=32\nType=Fixed\n")
	png16, png32 := []byte("png-16"), []byte("png-32")
	f.writeIcon(t, "TestTheme/16x16/apps/foo.png", png16)
	f.writeIcon(t, "TestTheme/32x32/apps/foo.png", png32)
	svc := NewService(f.options(t))

	require.Equal(t, pngURI(png32), svc.Resolve([]string{"app:foo"}, 32)["app:foo"])
	require.Equal(t, pngURI(png16), svc.Resolve([]string{"app:foo"}, 16)["app:foo"])
	require.Equal(t, pngURI(png16), svc.Resolve([]string{"app:foo"}, 20)["app:foo"],
		"no exact dir for 20: the closest size (16, distance 4) beats 32 (distance 12)")
}

func TestLookupInheritsChain(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "Child",
		"[Icon Theme]\nInherits=Parent\n[16x16/apps]\nSize=16\nType=Fixed\n")
	f.writeTheme(t, "Parent", "[Icon Theme]\n[48x48/apps]\nSize=48\nType=Fixed\n")
	parentOnly, childSmall := []byte("parent-only"), []byte("child-16")
	f.writeIcon(t, "Parent/48x48/apps/onlyparent.png", parentOnly)
	f.writeIcon(t, "Child/16x16/apps/both.png", childSmall)
	f.writeIcon(t, "Parent/48x48/apps/both.png", []byte("parent-48"))
	opt := f.options(t)
	opt.RunGsettings = fixedGsettings("'Child'")
	svc := NewService(opt)

	got := svc.Resolve([]string{"app:onlyparent", "app:both"}, 48)
	require.Equal(t, pngURI(parentOnly), got["app:onlyparent"],
		"misses walk down the Inherits chain")
	require.Equal(t, pngURI(childSmall), got["app:both"],
		"an earlier theme wins even at a worse size (theme precedence beats size)")
}

func TestLookupInheritsCycleAndJunk(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "A", "[Icon Theme]\nInherits=../evil,B\n[16x16/apps]\nSize=16\nType=Fixed\n")
	f.writeTheme(t, "B", "[Icon Theme]\nInherits=A\n[16x16/apps]\nSize=16\nType=Fixed\n")
	inB := []byte("in-b")
	f.writeIcon(t, "B/16x16/apps/cyc.png", inB)
	opt := f.options(t)
	opt.RunGsettings = fixedGsettings("'A'")
	svc := NewService(opt)
	require.Equal(t, pngURI(inB), svc.Resolve([]string{"app:cyc"}, 16)["app:cyc"],
		"inheritance cycles terminate and traversal-shaped theme names probe nothing")
}

func TestLookupInheritsDepthCap(t *testing.T) {
	f := newFixture(t)
	// D01 -> D02 -> ... -> D12; the cap keeps D01..D09 (depth 0..8).
	for i := 1; i <= 12; i++ {
		idx := "[Icon Theme]\n"
		if i < 12 {
			idx += "Inherits=D" + twoDigits(i+1) + "\n"
		}
		f.writeTheme(t, "D"+twoDigits(i), idx+"[16x16/apps]\nSize=16\nType=Fixed\n")
	}
	deep := []byte("too-deep")
	f.writeIcon(t, "D12/16x16/apps/deep.png", deep)
	within := []byte("within")
	f.writeIcon(t, "D09/16x16/apps/within.png", within)
	opt := f.options(t)
	opt.RunGsettings = fixedGsettings("'D01'")
	var rec logRecorder
	opt.Logf = rec.logf
	svc := NewService(opt)

	got := svc.Resolve([]string{"app:deep", "app:within"}, 16)
	require.Equal(t, pngURI(within), got["app:within"], "depth 8 is still inside the cap")
	require.NotContains(t, got, "app:deep", "themes beyond the depth cap are not consulted")
	require.Contains(t, rec.joined(),
		"icons: theme chain [D01 D02 D03 D04 D05 D06 D07 D08 D09 Adwaita hicolor]")
}

func twoDigits(n int) string {
	return string([]byte{byte('0' + n/10), byte('0' + n%10)})
}

func TestLookupFallbackThemes(t *testing.T) {
	f := newFixture(t) // detected TestTheme does not exist on disk
	f.writeTheme(t, "Adwaita", fixed16Index)
	f.writeTheme(t, "hicolor", fixed16Index)
	adw, hic := []byte("adw"), []byte("hic")
	f.writeIcon(t, "Adwaita/16x16/apps/inboth.png", adw)
	f.writeIcon(t, "hicolor/16x16/apps/inboth.png", hic)
	f.writeIcon(t, "hicolor/16x16/apps/hionly.png", hic)
	svc := NewService(f.options(t))

	got := svc.Resolve([]string{"app:inboth", "app:hionly"}, 16)
	require.Equal(t, pngURI(adw), got["app:inboth"], "Adwaita sits in front of hicolor")
	require.Equal(t, pngURI(hic), got["app:hionly"], "hicolor is the themed last resort")
}

func TestLookupUnthemedAndPixmapFallbacks(t *testing.T) {
	f := newFixture(t)
	loose, pix, homey := []byte("loose"), []byte("pix"), []byte("home")
	writeTestFile(t, filepath.Join(f.iconBase, "loose.png"), loose)
	writeTestFile(t, filepath.Join(f.pixmaps, "pixonly.png"), pix)
	writeTestFile(t, filepath.Join(f.home, "homey.png"), homey)
	writeTestFile(t, filepath.Join(f.iconBase, "shadow.png"), loose)
	writeTestFile(t, filepath.Join(f.pixmaps, "shadow.png"), pix)
	svc := NewService(f.options(t))

	got := svc.Resolve([]string{"app:loose", "app:pixonly", "app:homey", "app:shadow"}, 16)
	require.Equal(t, pngURI(loose), got["app:loose"], "loose files directly in an icon base")
	require.Equal(t, pngURI(pix), got["app:pixonly"], "pixmap dirs are the very last resort")
	require.Equal(t, pngURI(homey), got["app:homey"], "the ~/.icons stand-in is an icon base")
	require.Equal(t, pngURI(loose), got["app:shadow"], "icon-base loose files beat pixmap files")
}

func TestLookupExtensionPreference(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme", fixed16Index)
	png, svg := []byte("png-bytes"), []byte("<svg/>")
	f.writeIcon(t, "TestTheme/16x16/apps/dual.png", png)
	f.writeIcon(t, "TestTheme/16x16/apps/dual.svg", svg)
	f.writeIcon(t, "TestTheme/16x16/apps/vector.svg", svg)
	svc := NewService(f.options(t))

	got := svc.Resolve([]string{"app:dual", "app:vector"}, 16)
	require.Equal(t, pngURI(png), got["app:dual"], "png beats svg in the same dir")
	require.Equal(t, svgURI(svg), got["app:vector"], "svg-only icons serve as image/svg+xml")
}

func TestLookupTraversalRejection(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme", fixed16Index)
	f.writeIcon(t, "TestTheme/16x16/apps/real.png", []byte("real"))
	svc := NewService(f.options(t))

	got := svc.Resolve([]string{
		"app:../TestTheme/16x16/apps/real",
		"app:a/b",
		`app:a\b`,
		"app:..",
		"app:with..dots",
	}, 16)
	require.Empty(t, got, "path separators and dotdot in themed names are rejected outright")
}

func TestLookupAbsolutePathRefs(t *testing.T) {
	f := newFixture(t)
	png, svg := []byte("abs-png"), []byte("<svg>abs</svg>")
	absPNG := filepath.Join(f.root, "art", "icon.png")
	absUpper := filepath.Join(f.root, "art", "shout.PNG")
	absSVG := filepath.Join(f.root, "art", "icon.svg")
	absXPM := filepath.Join(f.root, "art", "icon.xpm")
	absJPG := filepath.Join(f.root, "art", "photo.jpg")
	absBig := filepath.Join(f.root, "art", "big.png")
	absMissing := filepath.Join(f.root, "art", "missing.png")
	writeTestFile(t, absPNG, png)
	writeTestFile(t, absUpper, png)
	writeTestFile(t, absSVG, svg)
	writeTestFile(t, absXPM, []byte("xpm"))
	writeTestFile(t, absJPG, []byte("jpg"))
	writeTestFile(t, absBig, bytes.Repeat([]byte("x"), 64))
	opt := f.options(t)
	opt.MaxFileBytes = 32
	svc := NewService(opt)

	got := svc.Resolve([]string{
		"app:" + absPNG, "app:" + absUpper, "app:" + absSVG, "app:" + absXPM,
		"app:" + absJPG, "app:" + absBig, "app:" + absMissing,
	}, 16)
	require.Equal(t, pngURI(png), got["app:"+absPNG], "absolute .png paths serve directly")
	require.Equal(t, pngURI(png), got["app:"+absUpper], "extension check is case-insensitive")
	require.Equal(t, svgURI(svg), got["app:"+absSVG], "absolute .svg paths serve directly")
	require.NotContains(t, got, "app:"+absXPM, "xpm is deliberately unsupported")
	require.NotContains(t, got, "app:"+absJPG, "only png and svg are served")
	require.NotContains(t, got, "app:"+absBig, "files over MaxFileBytes are skipped")
	require.NotContains(t, got, "app:"+absMissing)
}

func TestLookupIconExtStripping(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme", fixed16Index)
	bar, calc := []byte("bar"), []byte("calc")
	f.writeIcon(t, "TestTheme/16x16/apps/bar.png", bar)
	f.writeIcon(t, "TestTheme/16x16/apps/org.gnome.Calc.png", calc)
	svc := NewService(f.options(t))

	got := svc.Resolve([]string{
		"app:bar.png", "app:bar.svg", "app:bar.xpm", "app:bar.PNG", "app:org.gnome.Calc",
	}, 16)
	require.Equal(t, pngURI(bar), got["app:bar.png"], "Icon=foo.png strips to the themed name")
	require.Equal(t, pngURI(bar), got["app:bar.svg"], "the stripped name resolves whatever the claimed extension")
	require.Equal(t, pngURI(bar), got["app:bar.xpm"], "xpm strips too -- the NAME is themed, only files are format-gated")
	require.Equal(t, pngURI(bar), got["app:bar.PNG"], "extension stripping is case-insensitive")
	require.Equal(t, pngURI(calc), got["app:org.gnome.Calc"], "reverse-dns names keep their dotted tail")
}

func TestLookupScaledDirs(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme", "[Icon Theme]\n[16x16@2/apps]\nSize=16\nScale=2\nType=Fixed\n")
	hi := []byte("hidpi")
	f.writeIcon(t, "TestTheme/16x16@2/apps/hi.png", hi)
	svc := NewService(f.options(t))
	require.Equal(t, pngURI(hi), svc.Resolve([]string{"app:hi"}, 32)["app:hi"],
		"a 16x16@2 dir serves 32 physical px")
}

func TestLookupThemeSpansBases(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme", fixed16Index) // index in data/icons only
	span := []byte("span")
	writeTestFile(t, filepath.Join(f.home, "TestTheme", "16x16", "apps", "span.png"), span)
	svc := NewService(f.options(t))
	require.Equal(t, pngURI(span), svc.Resolve([]string{"app:span"}, 16)["app:span"],
		"icon files are searched in every base even when index.theme lives in just one")
}

func TestLookupIndexFromFirstBaseWins(t *testing.T) {
	f := newFixture(t)
	writeTestFile(t, filepath.Join(f.home, "Dup", "index.theme"),
		[]byte("[Icon Theme]\n[sub-a]\nSize=16\nType=Fixed\n"))
	writeTestFile(t, filepath.Join(f.iconBase, "Dup", "index.theme"),
		[]byte("[Icon Theme]\n[sub-b]\nSize=16\nType=Fixed\n"))
	a := []byte("content-a")
	writeTestFile(t, filepath.Join(f.iconBase, "Dup", "sub-a", "x.png"), a)
	writeTestFile(t, filepath.Join(f.iconBase, "Dup", "sub-b", "x.png"), []byte("content-b"))
	opt := f.options(t)
	opt.RunGsettings = fixedGsettings("'Dup'")
	svc := NewService(opt)
	require.Equal(t, pngURI(a), svc.Resolve([]string{"app:x"}, 16)["app:x"],
		"the first base's index.theme defines the subdir list")
}

func TestLookupOversizedThemedIconSkipped(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme",
		"[Icon Theme]\n[16x16/apps]\nSize=16\nType=Fixed\n\n[32x32/apps]\nSize=32\nType=Fixed\n")
	small := []byte("small")
	f.writeIcon(t, "TestTheme/16x16/apps/heavy.png", bytes.Repeat([]byte("x"), 64))
	f.writeIcon(t, "TestTheme/32x32/apps/heavy.png", small)
	opt := f.options(t)
	opt.MaxFileBytes = 32
	svc := NewService(opt)
	require.Equal(t, pngURI(small), svc.Resolve([]string{"app:heavy"}, 16)["app:heavy"],
		"an oversized candidate is invisible and the search continues")
}
