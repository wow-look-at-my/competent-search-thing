package preview

import (
	"os"
	"sort"
	"strings"
)

// ListCapped reads the directory at path and returns a listing capped
// at maxEntries: directories first, then files, each group sorted
// case-insensitively by name. Per-entry sizes come from entry.Info()
// (errors degrade to size 0). It never recurses.
func ListCapped(path string, maxEntries int) (*DirPreview, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		ni := strings.ToLower(entries[i].Name())
		nj := strings.ToLower(entries[j].Name())
		if ni != nj {
			return ni < nj
		}
		return entries[i].Name() < entries[j].Name()
	})
	total := len(entries)
	truncated := false
	if maxEntries > 0 && total > maxEntries {
		entries = entries[:maxEntries]
		truncated = true
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		var size int64
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		out = append(out, DirEntry{Name: e.Name(), IsDir: e.IsDir(), Size: size})
	}
	return &DirPreview{Entries: out, Total: total, Truncated: truncated}, nil
}
