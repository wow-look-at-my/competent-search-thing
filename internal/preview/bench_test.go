package preview

import (
	"bytes"
	"context"
	"fmt"
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
	if err := os.WriteFile(path, content, 0o644); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		content, _, _, err := ReadCapped(path, 256)
		if err != nil {
			b.Fatal(err)
		}
		if len(content) != 256*1024 {
			b.Fatalf("read %d bytes", len(content))
		}
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
		if err != nil {
			b.Fatal(err)
		}
		if ip.W != 800 {
			b.Fatalf("width %d", ip.W)
		}
	}
}

// BenchmarkDirListing lists a 500-entry directory at the default cap.
func BenchmarkDirListing(b *testing.B) {
	dir := b.TempDir()
	for i := 0; i < 500; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("entry-%03d.txt", i)), nil, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dp, err := ListCapped(dir, 200)
		if err != nil {
			b.Fatal(err)
		}
		if dp.Total != 500 {
			b.Fatalf("total %d", dp.Total)
		}
	}
}

// BenchmarkMetaCard builds the metadata card rows from an existing
// FileInfo -- the fast first payload.
func BenchmarkMetaCard(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, make([]byte, 4096), 0o644); err != nil {
		b.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows := MetaFor(path, fi)
		if len(rows) != 5 {
			b.Fatalf("rows %d", len(rows))
		}
	}
}
