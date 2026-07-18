package preview

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHumanSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{10 * 1024, "10 KB"},
		{1024 * 1024, "1 MB"},
		{1467 * 1024, "1.4 MB"},
		{5 << 30, "5 GB"},
		{3 << 40, "3 TB"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, humanSize(tc.n), "humanSize(%d)", tc.n)
	}
}

func TestIsImageExt(t *testing.T) {
	for _, p := range []string{"/a/x.png", "x.JPG", "x.jpeg", "x.gif", "x.webp", "x.BMP"} {
		require.True(t, isImageExt(p), "isImageExt(%q)", p)
	}
	for _, p := range []string{"/a/x.txt", "x.svg", "x.tiff", "png", "x"} {
		require.False(t, isImageExt(p), "isImageExt(%q)", p)
	}
}

func metaValue(t *testing.T, rows []MetaRow, label string) string {
	t.Helper()
	for _, r := range rows {
		if r.Label == label {
			return r.Value
		}
	}
	t.Fatalf("no %q row in %+v", label, rows)
	return ""
}

func TestMetaForRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	require.NoError(t, os.WriteFile(path, make([]byte, 2048), 0o644))
	fi, err := os.Lstat(path)
	require.NoError(t, err)

	rows := MetaFor(path, fi)
	require.Equal(t, "Size", rows[0].Label)
	require.Equal(t, "2 KB", rows[0].Value)
	require.Equal(t, "Modified", rows[1].Label)
	require.Regexp(t, `^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$`, rows[1].Value)
	require.Equal(t, "Mode", rows[2].Label)
	require.Contains(t, rows[2].Value, "rw-")
	require.Equal(t, "text", metaValue(t, rows, "Kind"))
	require.Equal(t, path, metaValue(t, rows, "Path"))
}

func TestMetaForKindsAndExtras(t *testing.T) {
	dir := t.TempDir()
	fi, err := os.Lstat(dir)
	require.NoError(t, err)
	require.Equal(t, "directory", metaValue(t, MetaFor(dir, fi), "Kind"))

	img := filepath.Join(dir, "shot.png")
	require.NoError(t, os.WriteFile(img, []byte("x"), 0o644))
	fi, err = os.Lstat(img)
	require.NoError(t, err)
	require.Equal(t, "image", metaValue(t, MetaFor(img, fi), "Kind"))

	blob := filepath.Join(dir, "blob.dat")
	require.NoError(t, os.WriteFile(blob, []byte("x"), 0o644))
	fi, err = os.Lstat(blob)
	require.NoError(t, err)
	require.Equal(t, "file", metaValue(t, MetaFor(blob, fi), "Kind"))

	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(blob, link))
	fi, err = os.Lstat(link)
	require.NoError(t, err)
	rows := MetaFor(link, fi, MetaRow{Label: "Target", Value: blob})
	require.Equal(t, "symlink", metaValue(t, rows, "Kind"))
	require.Equal(t, blob, metaValue(t, rows, "Target"), "extra rows are appended")
	require.Equal(t, "Target", rows[len(rows)-1].Label)
}
