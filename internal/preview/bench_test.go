package preview

import (
	"bytes"
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkTextPreview reads a 256 KiB text fixture at the default
// cap -- the worst-case text preview.
func BenchmarkTextPreview(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "big.go")
	line := []byte("func generatedFixtureLine() int { return 42 } // filler\n")
	content := bytes.Repeat(line, 256*1024/len(line)+1)[:256*1024]
	require.NoError(b, os.WriteFile(path, content, 0o644))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		content, _, _, err := ReadCapped(path, 256)
		require.Nil(b, err)

		require.Equal(b, 256*1024, len(content))

	}
}

// BenchmarkThumbnail decodes and downscales a ~2000x1500 JPEG -- the
// typical photo-preview cost.
func BenchmarkThumbnail(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	writeImage(b, path, testImage(2000, 1500))
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ip, err := Thumbnail(ctx, path, 800)
		require.Nil(b, err)

		require.Equal(b, 800, ip.W)

	}
}

// BenchmarkDirListing lists a 500-entry directory at the default cap.
func BenchmarkDirListing(b *testing.B) {
	dir := b.TempDir()
	for i := 0; i < 500; i++ {
		require.NoError(b, os.WriteFile(filepath.Join(dir, fmt.Sprintf("entry-%03d.txt", i)), nil, 0o644))

	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dp, err := ListCapped(dir, 200)
		require.Nil(b, err)

		require.Equal(b, 500, dp.Total)

	}
}

// BenchmarkMetaCard builds the metadata card rows from an existing
// FileInfo -- the fast first payload.
func BenchmarkMetaCard(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "notes.md")
	require.NoError(b, os.WriteFile(path, make([]byte, 4096), 0o644))

	fi, err := os.Lstat(path)
	require.Nil(b, err)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := MetaFor(path, fi)
		require.Equal(b, 5, len(rows))

	}
}
