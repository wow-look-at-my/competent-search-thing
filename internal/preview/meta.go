package preview

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// imageExts are the extensions the thumbnail provider accepts
// (lowercased, no dot).
var imageExts = map[string]bool{
	"png":  true,
	"jpg":  true,
	"jpeg": true,
	"gif":  true,
	"webp": true,
	"bmp":  true,
}

// isImageExt reports whether path's extension names a supported image
// format.
func isImageExt(path string) bool {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	return imageExts[ext]
}

// humanSize renders a byte count for humans: "512 B", "1.4 MB",
// "12 KB". Values below 10 units keep one decimal; the ".0" is
// dropped.
func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	unit := ""
	for _, u := range units {
		v /= 1024
		unit = u
		if v < 1024 {
			break
		}
	}
	s := fmt.Sprintf("%.1f", v)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + unit
}

// kindGuess names what path looks like from its FileInfo and
// extension alone (no file IO): "directory", "symlink", "image",
// "text", or "file" when nothing is known. The dispatcher refines
// "file" to "binary" after sniffing the head.
func kindGuess(path string, fi os.FileInfo) string {
	switch {
	case fi.IsDir():
		return "directory"
	case fi.Mode()&os.ModeSymlink != 0:
		return "symlink"
	case isImageExt(path):
		return "image"
	case LangHint(path) != "":
		return "text"
	default:
		return "file"
	}
}

// metaTimeFormat renders modification times ("2006-01-02 15:04").
const metaTimeFormat = "2006-01-02 15:04"

// MetaFor builds the metadata card rows for path: Size, Modified,
// Mode, Kind (guessed from the FileInfo and extension), and Path,
// followed by any extra rows the caller appends (symlink targets, the
// binary note). Pure: no file IO beyond the FileInfo handed in.
func MetaFor(path string, fi os.FileInfo, extra ...MetaRow) []MetaRow {
	rows := []MetaRow{
		{Label: "Size", Value: humanSize(fi.Size())},
		{Label: "Modified", Value: fi.ModTime().Local().Format(metaTimeFormat)},
		{Label: "Mode", Value: fi.Mode().String()},
		{Label: "Kind", Value: kindGuess(path, fi)},
		{Label: "Path", Value: path},
	}
	return append(rows, extra...)
}
