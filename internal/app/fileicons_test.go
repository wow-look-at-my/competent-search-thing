package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// GetFileIcons serves the embedded artifact's decoded table (cached
// by internal/fileicons.Load) -- no seams involved, so a bare test
// app answers the real data.
func TestGetFileIconsServesTheDecodedTable(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{})
	tab := a.GetFileIcons()
	assert.Len(t, tab.FileRules, 2363)
	assert.Len(t, tab.DirRules, 51)
	assert.Equal(t, "oct", tab.DefFile.Font)
	assert.Equal(t, 0xf011, tab.DefFile.CP)
	assert.Equal(t, 0xf016, tab.DefDir.CP)
}
