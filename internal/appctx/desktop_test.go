package appctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeDesktop(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestScanDesktopDirsParsesValidEntries(t *testing.T) {
	dir := t.TempDir()
	// firefox: themed icon name; editor: absolute icon path; plain: no
	// Icon key at all (stays empty). Icon values are kept verbatim.
	writeDesktop(t, dir, "firefox.desktop",
		"[Desktop Entry]\nType=Application\nName=Firefox\nExec=firefox %u\nIcon=firefox\n")
	writeDesktop(t, dir, "editor.desktop",
		"# a comment\n\n[Desktop Entry]\nType=Application\nName=Editor\nExec=/usr/bin/editor --new \"%f\"\nIcon=/usr/share/pixmaps/editor.png\nTerminal=false\nNoDisplay=false\n")
	writeDesktop(t, dir, "plain.desktop",
		"[Desktop Entry]\nType=Application\nName=Plain\nExec=plain\n")

	got := ScanDesktopDirs([]string{dir})
	require.Equal(t, []InstalledApp{
		{Name: "Editor", Exec: `/usr/bin/editor --new "%f"`, ID: "editor.desktop", Icon: "/usr/share/pixmaps/editor.png"},
		{Name: "Firefox", Exec: "firefox %u", ID: "firefox.desktop", Icon: "firefox"},
		{Name: "Plain", Exec: "plain", ID: "plain.desktop"},
	}, got, "sorted by Name, Exec and Icon kept raw, ID = file name")
}

func TestScanDesktopDirsSkipsUndesirableEntries(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"no-display", "[Desktop Entry]\nType=Application\nName=A\nExec=a\nNoDisplay=true\n"},
		{"no-display-case-insensitive", "[Desktop Entry]\nType=Application\nName=A\nExec=a\nNoDisplay=TRUE\n"},
		{"hidden", "[Desktop Entry]\nType=Application\nName=A\nExec=a\nHidden=true\n"},
		{"terminal", "[Desktop Entry]\nType=Application\nName=A\nExec=a\nTerminal=true\n"},
		{"wrong-type", "[Desktop Entry]\nType=Link\nName=A\nExec=a\nURL=https://example.com\n"},
		{"missing-type", "[Desktop Entry]\nName=A\nExec=a\n"},
		{"missing-name", "[Desktop Entry]\nType=Application\nExec=a\n"},
		{"empty-name", "[Desktop Entry]\nType=Application\nName=\nExec=a\n"},
		{"missing-exec", "[Desktop Entry]\nType=Application\nName=A\n"},
		{"empty-exec", "[Desktop Entry]\nType=Application\nName=A\nExec=\n"},
		{"keys-in-other-section", "[Desktop Action new]\nType=Application\nName=A\nExec=a\n"},
		{"keys-before-any-section", "Type=Application\nName=A\nExec=a\n"},
		{"localized-name-only", "[Desktop Entry]\nType=Application\nName[en]=A\nExec=a\n"},
		{"empty-file", ""},
		{"binary-junk", "\x00\x01\x02 not ini\n[half section\n===\nkey no value\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeDesktop(t, dir, "x.desktop", tc.content)
			require.Empty(t, ScanDesktopDirs([]string{dir}))
		})
	}
}

func TestScanDesktopDirsParsingQuirks(t *testing.T) {
	dir := t.TempDir()
	// Whitespace around "=" is trimmed; localized Name[xx] does not
	// override the plain Name; duplicate keys are last-wins; keys in
	// sections after [Desktop Entry] are ignored; a second [Desktop
	// Entry] section keeps counting.
	writeDesktop(t, dir, "spaced.desktop",
		"[Desktop Entry]\nType = Application\nName = Spaced App \nExec = run --it\nName[fr]=Autre\n")
	writeDesktop(t, dir, "dup.desktop",
		"[Desktop Entry]\nType=Application\nName=First\nName=Second\nExec=a\n")
	writeDesktop(t, dir, "trailer.desktop",
		"[Desktop Entry]\nType=Application\nName=Trailer\nExec=a\n[Desktop Action x]\nHidden=true\nName=Nope\n")
	writeDesktop(t, dir, "split.desktop",
		"[Desktop Entry]\nType=Application\nName=Split\n[Other]\nExec=wrong\n[Desktop Entry]\nExec=right\n")

	got := ScanDesktopDirs([]string{dir})
	require.Equal(t, []InstalledApp{
		{Name: "Second", Exec: "a", ID: "dup.desktop"},
		{Name: "Spaced App", Exec: "run --it", ID: "spaced.desktop"},
		{Name: "Split", Exec: "right", ID: "split.desktop"},
		{Name: "Trailer", Exec: "a", ID: "trailer.desktop"},
	}, got)
}

func TestScanDesktopDirsPrecedence(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	writeDesktop(t, dir1, "dup.desktop",
		"[Desktop Entry]\nType=Application\nName=First\nExec=first\n")
	writeDesktop(t, dir2, "dup.desktop",
		"[Desktop Entry]\nType=Application\nName=Second\nExec=second\n")
	// A Hidden entry in the earlier dir shadows the later valid one by
	// presence: that is how users disable a system-wide app locally.
	writeDesktop(t, dir1, "disabled.desktop",
		"[Desktop Entry]\nType=Application\nName=Ghost\nExec=ghost\nHidden=true\n")
	writeDesktop(t, dir2, "disabled.desktop",
		"[Desktop Entry]\nType=Application\nName=Ghost\nExec=ghost\n")
	writeDesktop(t, dir2, "only2.desktop",
		"[Desktop Entry]\nType=Application\nName=Only\nExec=only\n")

	got := ScanDesktopDirs([]string{dir1, dir2})
	require.Equal(t, []InstalledApp{
		{Name: "First", Exec: "first", ID: "dup.desktop"},
		{Name: "Only", Exec: "only", ID: "only2.desktop"},
	}, got)
}

func TestScanDesktopDirsFileFiltering(t *testing.T) {
	dir := t.TempDir()
	writeDesktop(t, dir, "notes.txt",
		"[Desktop Entry]\nType=Application\nName=Txt\nExec=a\n")
	writeDesktop(t, dir, "upper.DESKTOP",
		"[Desktop Entry]\nType=Application\nName=Upper\nExec=a\n")
	// A subdirectory whose name ends in .desktop is not a file.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub.desktop"), 0o755))
	// A dangling symlink is listed but unreadable: skipped silently.
	require.NoError(t, os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "dangling.desktop")))
	// One giant line (beyond bufio.Scanner's token limit) is junk.
	writeDesktop(t, dir, "huge.desktop",
		"[Desktop Entry]\nType=Application\nName=Huge\nExec="+strings.Repeat("a", 80*1024)+"\n")
	writeDesktop(t, dir, "ok.desktop",
		"[Desktop Entry]\nType=Application\nName=OK\nExec=ok\n")

	got := ScanDesktopDirs([]string{dir, filepath.Join(dir, "does-not-exist")})
	require.Equal(t, []InstalledApp{{Name: "OK", Exec: "ok", ID: "ok.desktop"}}, got)
}

func TestDesktopDirs(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{
			name: "all set",
			env:  map[string]string{"XDG_DATA_HOME": "/dh", "XDG_DATA_DIRS": "/a:/b"},
			want: []string{"/dh/applications", "/a/applications", "/b/applications"},
		},
		{
			name: "defaults from HOME",
			env:  map[string]string{"HOME": "/home/u"},
			want: []string{
				"/home/u/.local/share/applications",
				"/usr/local/share/applications",
				"/usr/share/applications",
			},
		},
		{
			name: "no home at all",
			env:  map[string]string{},
			want: []string{"/usr/local/share/applications", "/usr/share/applications"},
		},
		{
			name: "empty data-dir segments skipped",
			env:  map[string]string{"XDG_DATA_HOME": "/dh", "XDG_DATA_DIRS": "/a::/b:"},
			want: []string{"/dh/applications", "/a/applications", "/b/applications"},
		},
		{
			name: "duplicates keep the first position",
			env:  map[string]string{"XDG_DATA_HOME": "/usr/share", "XDG_DATA_DIRS": "/usr/share:/x"},
			want: []string{"/usr/share/applications", "/x/applications"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, DesktopDirs(env(tc.env)))
		})
	}
}
