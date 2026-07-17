package firefox

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeBase creates a profiles.ini base directory with the given ini
// content and returns it.
func writeBase(t *testing.T, ini string) string {
	t.Helper()
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "profiles.ini"), []byte(ini), 0o644))
	return base
}

// mkProfile creates base/rel with a places.sqlite stamped mtime and
// returns the profile dir.
func mkProfile(t *testing.T, base, rel string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(base, rel)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, placesFile)
	require.NoError(t, os.WriteFile(p, []byte("not a real db"), 0o644))
	require.NoError(t, os.Chtimes(p, mtime, mtime))
	return dir
}

func TestFindProfileInstallDefaultWins(t *testing.T) {
	base := writeBase(t, `
[Install4F96D1932A9F858E]
Default=abc.default-release
Locked=1

[Profile1]
Name=older
IsRelative=1
Path=abc.default
Default=1

[Profile0]
Name=default-release
IsRelative=1
Path=abc.default-release

[General]
StartWithLastProfile=1
Version=2
`)
	mkProfile(t, base, "abc.default", time.Now())
	want := mkProfile(t, base, "abc.default-release", time.Now())

	p, ok := FindProfile([]string{base})
	require.True(t, ok)
	require.Equal(t, want, p.Dir, "the Install section's Default wins over Default=1")
	require.Equal(t, base, p.Base)
}

func TestFindProfileDefaultFlagFallback(t *testing.T) {
	base := writeBase(t, `
[Profile0]
Name=plain
IsRelative=1
Path=plain.one

[Profile1]
Name=chosen
IsRelative=1
Path=chosen.two
Default=1
`)
	mkProfile(t, base, "plain.one", time.Now())
	want := mkProfile(t, base, "chosen.two", time.Now())

	p, ok := FindProfile([]string{base})
	require.True(t, ok)
	require.Equal(t, want, p.Dir)
}

func TestFindProfileSingleProfileFallback(t *testing.T) {
	base := writeBase(t, `
; a comment line
# another comment
[General]
Version=2

[Profile0]
Name=only
IsRelative=1
Path=only.prof
`)
	want := mkProfile(t, base, "only.prof", time.Now())
	p, ok := FindProfile([]string{base})
	require.True(t, ok)
	require.Equal(t, want, p.Dir)
}

func TestFindProfileMultipleWithoutDefaultIsAmbiguous(t *testing.T) {
	base := writeBase(t, `
[Profile0]
Path=a.one
IsRelative=1

[Profile1]
Path=b.two
IsRelative=1
`)
	mkProfile(t, base, "a.one", time.Now())
	mkProfile(t, base, "b.two", time.Now())
	_, ok := FindProfile([]string{base})
	require.False(t, ok, "several profiles and no default: no guess")
}

func TestFindProfileAbsolutePath(t *testing.T) {
	abs := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(abs, placesFile), []byte("x"), 0o644))
	base := writeBase(t, `
[Profile0]
Name=abs
IsRelative=0
Path=`+abs+`
Default=1
`)
	p, ok := FindProfile([]string{base})
	require.True(t, ok)
	require.Equal(t, abs, p.Dir, "IsRelative=0 keeps the path as-is")
}

func TestFindProfileMissingIsRelativeInferred(t *testing.T) {
	base := writeBase(t, `
[Profile0]
Path=guessed.rel
Default=1
`)
	want := mkProfile(t, base, "guessed.rel", time.Now())
	p, ok := FindProfile([]string{base})
	require.True(t, ok)
	require.Equal(t, want, p.Dir, "a non-absolute path without IsRelative resolves against the base")
}

func TestFindProfileInstallDefaultWithoutMatchingSection(t *testing.T) {
	base := writeBase(t, `
[InstallFFFF]
Default=loner.prof
`)
	want := mkProfile(t, base, "loner.prof", time.Now())
	p, ok := FindProfile([]string{base})
	require.True(t, ok)
	require.Equal(t, want, p.Dir, "an unmatched Install default resolves relative to the base")
}

func TestFindProfileStaleEntrySkipped(t *testing.T) {
	base := writeBase(t, `
[Profile0]
Path=vanished.prof
IsRelative=1
Default=1
`)
	_, ok := FindProfile([]string{base})
	require.False(t, ok, "a profiles.ini pointing at a missing directory yields nothing")
}

func TestFindProfileNewestPlacesWins(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour)
	fresh := time.Now().Add(-time.Hour)
	ini := `
[Profile0]
Path=p.default
IsRelative=1
Default=1
`
	classic := writeBase(t, ini)
	mkProfile(t, classic, "p.default", old)
	snap := writeBase(t, ini)
	snapDir := mkProfile(t, snap, "p.default", fresh)

	p, ok := FindProfile([]string{classic, snap})
	require.True(t, ok)
	require.Equal(t, snapDir, p.Dir, "the base with the newer places.sqlite is the one in use")
	require.Equal(t, snap, p.Base)

	// Ties (identical mtimes) keep the earlier base.
	tieA := writeBase(t, ini)
	tieDir := mkProfile(t, tieA, "p.default", old)
	tieB := writeBase(t, ini)
	mkProfile(t, tieB, "p.default", old)
	p, ok = FindProfile([]string{tieA, tieB})
	require.True(t, ok)
	require.Equal(t, tieDir, p.Dir)
}

func TestFindProfileWithoutPlacesStillFound(t *testing.T) {
	base := writeBase(t, `
[Profile0]
Path=fresh.prof
IsRelative=1
Default=1
`)
	dir := filepath.Join(base, "fresh.prof")
	require.NoError(t, os.MkdirAll(dir, 0o755)) // no places.sqlite yet
	p, ok := FindProfile([]string{base})
	require.True(t, ok, "a never-browsed profile is still a profile")
	require.Equal(t, dir, p.Dir)

	// ...but it loses against a base that HAS history.
	browsing := writeBase(t, `
[Profile0]
Path=used.prof
IsRelative=1
Default=1
`)
	used := mkProfile(t, browsing, "used.prof", time.Now().Add(-time.Hour))
	p, ok = FindProfile([]string{base, browsing})
	require.True(t, ok)
	require.Equal(t, used, p.Dir)
}

func TestFindProfileNothingAnywhere(t *testing.T) {
	_, ok := FindProfile(nil)
	require.False(t, ok)
	_, ok = FindProfile([]string{t.TempDir()}) // no profiles.ini
	require.False(t, ok)
	_, ok = FindProfile([]string{filepath.Join(t.TempDir(), "missing")})
	require.False(t, ok)
	_, ok = FindProfile([]string{writeBase(t, "[General]\nVersion=2\n")})
	require.False(t, ok, "an ini without profile sections yields nothing")
}

func TestBaseDirs(t *testing.T) {
	noEnv := func(string) string { return "" }

	linux := BaseDirs("linux", "/home/u", noEnv)
	require.Equal(t, []string{
		"/home/u/.mozilla/firefox",
		"/home/u/snap/firefox/common/.mozilla/firefox",
		"/home/u/.var/app/org.mozilla.firefox/.mozilla/firefox",
	}, linux, "classic, then snap, then flatpak")
	require.Nil(t, BaseDirs("linux", "", noEnv))

	win := BaseDirs("windows", "", func(k string) string {
		if k == "APPDATA" {
			return `C:\Users\u\AppData\Roaming`
		}
		return ""
	})
	require.Equal(t, []string{filepath.Join(`C:\Users\u\AppData\Roaming`, "Mozilla", "Firefox")}, win)
	require.Nil(t, BaseDirs("windows", `C:\Users\u`, noEnv), "no APPDATA: nothing to probe")

	mac := BaseDirs("darwin", "/Users/u", noEnv)
	require.Equal(t, []string{filepath.Join("/Users/u", "Library", "Application Support", "Firefox")}, mac)
	require.Nil(t, BaseDirs("darwin", "", noEnv))
}

func TestDefaultBaseDirsUsesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dirs := DefaultBaseDirs()
	require.NotEmpty(t, dirs)
	require.Equal(t, filepath.Join(home, ".mozilla", "firefox"), dirs[0])
}

func TestParseINIDialect(t *testing.T) {
	sections := parseINI("orphan=ignored\n[A]\nk=v\nk=dup-ignored\n = no-key\nnoequals\n[ B ]\nx = spaced \n")
	require.Len(t, sections, 2)
	require.Equal(t, "A", sections[0].name)
	require.Equal(t, map[string]string{"k": "v"}, sections[0].keys, "first key wins; malformed lines skipped")
	require.Equal(t, "B", sections[1].name)
	require.Equal(t, map[string]string{"x": "spaced"}, sections[1].keys)
}
