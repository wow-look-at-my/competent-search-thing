package app

import (
	"github.com/wow-look-at-my/competent-search-thing/internal/fileicons"
)

// GetFileIcons returns the decoded per-file-type icon mapping table
// (internal/fileicons: the embedded data.bin artifact decoded through
// the first-party binpazer reader, cached after the first call). The
// frontend fetches it once at wire-up (fileicons.ts initFileIcons)
// and compiles the match tables client-side; until the answer lands
// it renders the uncolored pack defaults, which the decode-failure
// fallback also degrades to -- so this method never errors.
func (a *App) GetFileIcons() fileicons.Table {
	return *fileicons.Load()
}
