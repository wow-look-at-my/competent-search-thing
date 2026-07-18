package icons

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeMimeFile drops content at <dir>/mime/<name>.
func writeMimeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	mimeDir := filepath.Join(dir, "mime")
	require.NoError(t, os.MkdirAll(mimeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mimeDir, name), []byte(content), 0o644))
}

func TestMimeForName(t *testing.T) {
	dir := t.TempDir()
	writeMimeFile(t, dir, "globs2", `# This file was automatically generated.
80:text/html:*.html
50:application/pdf:*.pdf
50:application/x-compressed-tar:*.tar.gz
50:application/gzip:*.gz
50:text/x-makefile:makefile
50:text/x-genie:*.gs:cs
50:application/x-perf-data:perf.data:cs
60:text/x-csrc:*.c
50:text/x-chdr:*.[h]
20:low/weight:x-?.bin
70:high/weight:x-?.*
10:aa/low:*.foo
80:aa/high:*.FOO
30:aa/tie-first:*.tie
30:aa/tie-second:*.TIE
junk line without colons
notanumber:some/mime:*.zz
-5:neg/weight:*.neg
50:nomime-noslash:*.q
50::*.emptymime
50:empty/glob:
`)
	db := loadMimeDB([]string{dir})

	cases := []struct {
		name, file, want string
	}{
		{"simple suffix", "index.html", "text/html"},
		{"suffix folds by default", "INDEX.HTML", "text/html"},
		{"double extension beats single", "backup.tar.gz", "application/x-compressed-tar"},
		{"single extension still works", "backup.gz", "application/gzip"},
		{"literal name", "makefile", "text/x-makefile"},
		{"literal folds by default", "Makefile", "text/x-makefile"},
		{"cs literal exact", "perf.data", "application/x-perf-data"},
		{"cs literal wrong case misses", "PERF.DATA", ""},
		{"cs suffix exact", "hello.gs", "text/x-genie"},
		{"cs suffix wrong case misses", "hello.GS", ""},
		{"suffix beats complex", "main.c", "text/x-csrc"},
		{"complex bracket glob", "main.h", "text/x-chdr"},
		{"complex weight order", "x-1.bin", "high/weight"},
		{"same folded suffix keeps higher weight", "a.foo", "aa/high"},
		{"same folded suffix tie keeps first", "a.tie", "aa/tie-first"},
		{"unknown extension", "a.zz", ""},
		{"mime without slash dropped", "a.q", ""},
		{"empty mime dropped", "a.emptymime", ""},
		{"negative weight dropped", "a.neg", ""},
		{"no match at all", "README", ""},
		{"empty name", "", ""},
		{"whole name is the suffix", ".pdf", "application/pdf"},
		{"fold changes byte length safely", "xK.gz", "application/gzip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, db.MimeForName(tc.file))
		})
	}
}

func TestMimeDBMultipleDataDirsFirstWins(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	writeMimeFile(t, dir1, "globs2", "50:one/first:*.dup\n")
	writeMimeFile(t, dir2, "globs2", "80:two/second:*.dup\n50:two/only:*.only2\n")
	writeMimeFile(t, dir1, "generic-icons", "a/b:icon-one\n")
	writeMimeFile(t, dir2, "generic-icons", "a/b:icon-two\nc/d:icon-cd\n")

	db := loadMimeDB([]string{dir1, dir2})
	require.Equal(t, "one/first", db.MimeForName("x.dup"),
		"the first data dir's exact glob wins even against a heavier later one")
	require.Equal(t, "two/only", db.MimeForName("x.only2"),
		"later dirs still contribute globs the first dir lacks")
	require.Equal(t, []string{"a-b", "icon-one", "application-x-generic"}, db.IconNames("a/b"),
		"the first data dir's generic-icons entry wins")
	require.Equal(t, []string{"c-d", "icon-cd", "application-x-generic"}, db.IconNames("c/d"))
}

func TestMimeDBMissingFiles(t *testing.T) {
	db := loadMimeDB([]string{t.TempDir(), filepath.Join(t.TempDir(), "nope")})
	require.Equal(t, "", db.MimeForName("a.pdf"))
	require.Equal(t, []string{"application-pdf", "application-x-generic"}, db.IconNames("application/pdf"),
		"the icon-name chain works without any generic-icons data")
}

func TestGenericIconsParsing(t *testing.T) {
	dir := t.TempDir()
	writeMimeFile(t, dir, "generic-icons", `# comment
application/pdf:x-office-document

application/zip:package-x-generic
malformed-no-colon
:empty-mime
no-icon:
`)
	db := loadMimeDB([]string{dir})
	require.Equal(t, []string{"application-pdf", "x-office-document", "application-x-generic"},
		db.IconNames("application/pdf"))
	require.Equal(t, []string{"application-zip", "package-x-generic", "application-x-generic"},
		db.IconNames("application/zip"))
}

func TestIconNamesChain(t *testing.T) {
	dir := t.TempDir()
	writeMimeFile(t, dir, "generic-icons",
		"application/pdf:x-office-document\napplication/foo:application-x-generic\n")
	db := loadMimeDB([]string{dir})

	cases := []struct {
		name, mime string
		want       []string
	}{
		{"generic entry in the middle", "application/pdf",
			[]string{"application-pdf", "x-office-document", "application-x-generic"}},
		{"text media class", "text/x-go", []string{"text-x-go", "text-x-generic"}},
		{"image media class", "image/png", []string{"image-png", "image-x-generic"}},
		{"audio media class", "audio/mpeg", []string{"audio-mpeg", "audio-x-generic"}},
		{"video media class", "video/mp4", []string{"video-mp4", "video-x-generic"}},
		{"font media class", "font/woff", []string{"font-woff", "font-x-generic"}},
		{"other media class falls to application", "model/iges",
			[]string{"model-iges", "application-x-generic"}},
		{"generic entry equal to the class generic dedupes", "application/foo",
			[]string{"application-foo", "application-x-generic"}},
		{"empty mime", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, db.IconNames(tc.mime))
		})
	}
}

// TestMimeDBRealContainerData sanity-checks the parser against the
// real shared-mime-info files when the host has them (this container
// and CI both do); skipped silently elsewhere so the suite stays
// hermetic.
func TestMimeDBRealContainerData(t *testing.T) {
	if _, err := os.Stat("/usr/share/mime/globs2"); err != nil {
		t.Skip("no /usr/share/mime/globs2 on this host")
	}
	db := loadMimeDB([]string{"/usr/share"})
	require.Equal(t, "application/pdf", db.MimeForName("report.pdf"))
	require.Equal(t, "text/html", db.MimeForName("page.html"))
	require.Equal(t, "application/x-compressed-tar", db.MimeForName("backup.tar.gz"))
}
