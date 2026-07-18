package icons

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logRecorder captures Logf lines for assertions.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *logRecorder) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

func TestResolveKeyProtocol(t *testing.T) {
	f := newFixture(t)
	writeMimeFile(t, f.data, "globs2", "50:application/pdf:*.pdf\n")
	f.writeTheme(t, "TestTheme", fixed16Index)
	pdf, folder, app := []byte("pdficon"), []byte("foldericon"), []byte("appicon")
	f.writeIcon(t, "TestTheme/16x16/apps/application-pdf.png", pdf)
	f.writeIcon(t, "TestTheme/16x16/apps/folder.png", folder)
	f.writeIcon(t, "TestTheme/16x16/apps/gimp.png", app)
	svc := NewService(f.options(t))

	got := svc.Resolve([]string{
		"dir", "file:report.pdf", "app:gimp",
		"bogus", "nope:x", "file:", "app:", "", "dir", // junk + a duplicate
	}, 16)
	require.Equal(t, map[string]string{
		"dir":             pngURI(folder),
		"file:report.pdf": pngURI(pdf),
		"app:gimp":        pngURI(app),
	}, got, "the three key forms resolve; unknown/malformed keys are absent; duplicates collapse")
}

func TestResolveEmptyKeys(t *testing.T) {
	f := newFixture(t)
	svc := NewService(f.options(t))
	got := svc.Resolve(nil, 16)
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestResolveDirFallsToInodeDirectory(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme", fixed16Index)
	inode := []byte("inode")
	f.writeIcon(t, "TestTheme/16x16/apps/inode-directory.png", inode)
	svc := NewService(f.options(t))
	require.Equal(t, pngURI(inode), svc.Resolve([]string{"dir"}, 16)["dir"],
		"no folder icon: inode-directory is the second candidate")
}

func TestResolveFileIconChain(t *testing.T) {
	f := newFixture(t)
	writeMimeFile(t, f.data, "globs2", "50:application/pdf:*.pdf\n50:text/x-go:*.go\n")
	writeMimeFile(t, f.data, "generic-icons", "application/pdf:x-office-document\n")
	f.writeTheme(t, "TestTheme", fixed16Index)
	office, textgen := []byte("office"), []byte("textgen")
	f.writeIcon(t, "TestTheme/16x16/apps/x-office-document.png", office)
	f.writeIcon(t, "TestTheme/16x16/apps/text-x-generic.png", textgen)
	svc := NewService(f.options(t))

	got := svc.Resolve([]string{"file:report.pdf", "file:main.go", "file:mystery.zzz"}, 16)
	require.Equal(t, pngURI(office), got["file:report.pdf"],
		"no application-pdf icon: the generic-icons entry is next in the chain")
	require.Equal(t, pngURI(textgen), got["file:main.go"],
		"no text-x-go icon: the media-class generic is the last link")
	require.NotContains(t, got, "file:mystery.zzz", "unknown mimetype = honest miss")
}

func TestResolveSizeClamping(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme",
		"[Icon Theme]\n[scalable/apps]\nSize=64\nMinSize=8\nMaxSize=256\nType=Scalable\n")
	svc := NewService(f.options(t))

	require.Empty(t, svc.Resolve([]string{"app:cx"}, -3), "nothing on disk yet")
	require.Empty(t, svc.Resolve([]string{"app:cy"}, 100000))
	f.writeIcon(t, "TestTheme/scalable/apps/cx.png", []byte("cx"))
	f.writeIcon(t, "TestTheme/scalable/apps/cy.png", []byte("cy"))
	require.Empty(t, svc.Resolve([]string{"app:cx"}, 8),
		"size -3 was clamped to 8: the earlier miss is negative-cached under the same key")
	require.Empty(t, svc.Resolve([]string{"app:cy"}, 256),
		"size 100000 was clamped to 256: same negative-cache key")
	require.Equal(t, pngURI([]byte("cx")), svc.Resolve([]string{"app:cx"}, 9)["app:cx"],
		"a fresh size resolves now that the file exists")
}

func TestResolveDataURIEncoding(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme", fixed16Index)
	raw := []byte{0x89, 'P', 'N', 'G', 0x00, 0x01, 0xFF}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
	f.writeIcon(t, "TestTheme/16x16/apps/binicon.png", raw)
	f.writeIcon(t, "TestTheme/16x16/apps/vec.svg", svg)
	svc := NewService(f.options(t))

	got := svc.Resolve([]string{"app:binicon", "app:vec"}, 16)
	require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(raw), got["app:binicon"])
	require.Equal(t, "data:image/svg+xml;base64,"+base64.StdEncoding.EncodeToString(svg), got["app:vec"])
}

func TestResolveEmptyIconFileMisses(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme", fixed16Index)
	f.writeIcon(t, "TestTheme/16x16/apps/hollow.png", nil)
	svc := NewService(f.options(t))
	require.Empty(t, svc.Resolve([]string{"app:hollow"}, 16), "zero-byte icon files are not served")
}

func TestResolveLRUCacheAndEviction(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme", fixed16Index)
	a := []byte("a-icon")
	f.writeIcon(t, "TestTheme/16x16/apps/icon-a.png", a)
	f.writeIcon(t, "TestTheme/16x16/apps/icon-b.png", []byte("b"))
	f.writeIcon(t, "TestTheme/16x16/apps/icon-c.png", []byte("c"))
	opt := f.options(t)
	opt.CacheEntries = 2
	svc := NewService(opt)

	require.Equal(t, pngURI(a), svc.Resolve([]string{"app:icon-a"}, 16)["app:icon-a"])
	require.NoError(t, os.Remove(filepath.Join(f.iconBase, "TestTheme/16x16/apps/icon-a.png")))
	require.Equal(t, pngURI(a), svc.Resolve([]string{"app:icon-a"}, 16)["app:icon-a"],
		"served from the cache after the file vanished")
	svc.Resolve([]string{"app:icon-b", "app:icon-c"}, 16) // two fresh entries evict icon-a at cap 2
	require.Empty(t, svc.Resolve([]string{"app:icon-a"}, 16),
		"evicted: the re-lookup hits the disk again and the file is gone")
}

func TestResolveNegativeCaching(t *testing.T) {
	f := newFixture(t)
	f.writeTheme(t, "TestTheme", fixed16Index)
	svc := NewService(f.options(t))

	require.Empty(t, svc.Resolve([]string{"app:late"}, 16))
	f.writeIcon(t, "TestTheme/16x16/apps/late.png", []byte("late"))
	require.Empty(t, svc.Resolve([]string{"app:late"}, 16),
		"the earlier miss short-circuits repeat lookups at the same size")
	require.Equal(t, pngURI([]byte("late")), svc.Resolve([]string{"app:late"}, 24)["app:late"],
		"a different size is a different cache key (24 serves via closest-match)")
}

func TestThemeDetectionPrecedence(t *testing.T) {
	writeSettingsINI := func(t *testing.T, cfgRoot, theme string) {
		writeTestFile(t, filepath.Join(cfgRoot, "gtk-3.0", "settings.ini"),
			[]byte("[Other]\ngtk-icon-theme-name=Wrong\n[Settings]\n# comment\ngtk-icon-theme-name = "+theme+"\n"))
	}
	cases := []struct {
		name      string
		gsettings func(context.Context) (string, error)
		env       func(t *testing.T, f *fixture) map[string]string
		wantChain string
	}{
		{
			name:      "gsettings wins over settings.ini",
			gsettings: fixedGsettings("'GsTheme'\n"),
			env: func(t *testing.T, f *fixture) map[string]string {
				cfg := filepath.Join(f.root, "cfg")
				writeSettingsINI(t, cfg, "IniTheme")
				return map[string]string{"XDG_CONFIG_HOME": cfg}
			},
			wantChain: "icons: theme chain [GsTheme Adwaita hicolor]",
		},
		{
			name:      "gsettings error falls to settings.ini via XDG_CONFIG_HOME",
			gsettings: errGsettings,
			env: func(t *testing.T, f *fixture) map[string]string {
				cfg := filepath.Join(f.root, "cfg")
				writeSettingsINI(t, cfg, "IniTheme")
				return map[string]string{"XDG_CONFIG_HOME": cfg}
			},
			wantChain: "icons: theme chain [IniTheme Adwaita hicolor]",
		},
		{
			name:      "blank gsettings output falls to settings.ini",
			gsettings: fixedGsettings("''\n"),
			env: func(t *testing.T, f *fixture) map[string]string {
				cfg := filepath.Join(f.root, "cfg")
				writeSettingsINI(t, cfg, "\"QuotedTheme\"")
				return map[string]string{"XDG_CONFIG_HOME": cfg}
			},
			wantChain: "icons: theme chain [QuotedTheme Adwaita hicolor]",
		},
		{
			name:      "settings.ini under HOME/.config",
			gsettings: errGsettings,
			env: func(t *testing.T, f *fixture) map[string]string {
				home := filepath.Join(f.root, "home")
				writeSettingsINI(t, filepath.Join(home, ".config"), "HomeTheme")
				return map[string]string{"HOME": home}
			},
			wantChain: "icons: theme chain [HomeTheme Adwaita hicolor]",
		},
		{
			name:      "both fail leaves the fallback pair",
			gsettings: errGsettings,
			env: func(t *testing.T, f *fixture) map[string]string {
				return nil
			},
			wantChain: "icons: theme chain [Adwaita hicolor]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			env := tc.env(t, f)
			var rec logRecorder
			opt := f.options(t)
			opt.RunGsettings = tc.gsettings
			opt.Getenv = func(k string) string { return env[k] }
			opt.Logf = rec.logf
			svc := NewService(opt)
			svc.Resolve(nil, 16) // first Resolve pays the detection
			require.Contains(t, rec.joined(), tc.wantChain)
		})
	}
}

func TestGsettingsRunsOnceWithDeadline(t *testing.T) {
	f := newFixture(t)
	var count atomic.Int32
	var sawDeadline atomic.Bool
	opt := f.options(t)
	opt.RunGsettings = func(ctx context.Context) (string, error) {
		count.Add(1)
		_, has := ctx.Deadline()
		sawDeadline.Store(has)
		return "'TestTheme'", nil
	}
	svc := NewService(opt)
	svc.Resolve(nil, 16)
	svc.Resolve([]string{"dir"}, 32)
	svc.Resolve([]string{"app:x"}, 16)
	require.EqualValues(t, 1, count.Load(), "theme detection happens once per service")
	require.True(t, sawDeadline.Load(), "detection passes a bounded context")
}

func TestResolveConcurrent(t *testing.T) {
	f := newFixture(t)
	writeMimeFile(t, f.data, "globs2", "50:application/pdf:*.pdf\n")
	f.writeTheme(t, "TestTheme", fixed16Index)
	pdf, folder := []byte("pdf"), []byte("folder")
	f.writeIcon(t, "TestTheme/16x16/apps/application-pdf.png", pdf)
	f.writeIcon(t, "TestTheme/16x16/apps/folder.png", folder)
	svc := NewService(f.options(t))

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				got := svc.Resolve([]string{"dir", "file:a.pdf", "app:missing", "bogus"}, 16)
				assert.Equal(t, pngURI(folder), got["dir"])
				assert.Equal(t, pngURI(pdf), got["file:a.pdf"])
				assert.NotContains(t, got, "app:missing")
				assert.NotContains(t, got, "bogus")
			}
		}()
	}
	wg.Wait()
}

// TestNewServiceDefaults exercises every default seam (real env, real
// gsettings exec bounded to 3s, real data dirs): whatever the host
// looks like, unknown keys stay absent and nothing crashes.
func TestNewServiceDefaults(t *testing.T) {
	svc := NewService(Options{})
	got := svc.Resolve([]string{"nonsense-key", "file:x.zzznotreal"}, 16)
	require.NotNil(t, got)
	require.NotContains(t, got, "nonsense-key")
	require.NotContains(t, got, "file:x.zzznotreal")
}

func TestLRU(t *testing.T) {
	l := newLRU(2)
	l.put("a", "1")
	l.put("b", "2")
	v, ok := l.get("a")
	require.True(t, ok)
	require.Equal(t, "1", v)
	l.put("c", "3") // evicts b: a was refreshed by the get above
	_, ok = l.get("b")
	require.False(t, ok)
	l.put("a", "9") // update-in-place
	v, ok = l.get("a")
	require.True(t, ok)
	require.Equal(t, "9", v)
	_, ok = l.get("c")
	require.True(t, ok)
	_, ok = l.get("zzz")
	require.False(t, ok)
}
