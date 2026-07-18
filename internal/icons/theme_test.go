package icons

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseThemeIndex(t *testing.T) {
	idx := parseThemeIndex([]byte(`
[Icon Theme]
Name=Test
Inherits= Parent , Other ,,
Comment=for parser tests

[16x16/apps]
Size=16
Type=fixed

[24x24/apps]
Size=24
Threshold=3

[scalable/apps]
Size=128
MinSize=8
MaxSize=512
Type=Scalable

[16x16@2/apps]
Size=16
Scale=2
Type=FIXED

[no-size/apps]
Type=Fixed

[junk-size/apps]
Size=abc

[junk-optionals/apps]
Size=48
Scale=zero
Threshold=-1
MinSize=x
MaxSize=

; semicolon comment
# hash comment
garbage line with no equals sign
`))
	require.Equal(t, []string{"Parent", "Other"}, idx.inherits,
		"Inherits trims entries and drops blanks")
	require.Equal(t, []themeDir{
		{path: "16x16/apps", size: 16, scale: 1, minSize: 16, maxSize: 16, threshold: 2, typ: typeFixed},
		{path: "24x24/apps", size: 24, scale: 1, minSize: 24, maxSize: 24, threshold: 3, typ: typeThreshold},
		{path: "scalable/apps", size: 128, scale: 1, minSize: 8, maxSize: 512, threshold: 2, typ: typeScalable},
		{path: "16x16@2/apps", size: 16, scale: 2, minSize: 16, maxSize: 16, threshold: 2, typ: typeFixed},
		{path: "junk-optionals/apps", size: 48, scale: 1, minSize: 48, maxSize: 48, threshold: 2, typ: typeThreshold},
	}, idx.dirs, "dirs keep file order; sections without a valid Size are dropped; junk optionals keep defaults")
}

func TestParseThemeIndexEdges(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		idx := parseThemeIndex(nil)
		require.Empty(t, idx.inherits)
		require.Empty(t, idx.dirs)
	})
	t.Run("keys before any section are ignored", func(t *testing.T) {
		idx := parseThemeIndex([]byte("Size=16\nInherits=X\n[16x16/a]\nSize=16\n"))
		require.Empty(t, idx.inherits)
		require.Len(t, idx.dirs, 1)
	})
	t.Run("no icon theme section", func(t *testing.T) {
		idx := parseThemeIndex([]byte("[32x32/apps]\nSize=32\n"))
		require.Empty(t, idx.inherits)
		require.Equal(t, []themeDir{
			{path: "32x32/apps", size: 32, scale: 1, minSize: 32, maxSize: 32, threshold: 2, typ: typeThreshold},
		}, idx.dirs)
	})
	t.Run("giant line does not truncate the parse", func(t *testing.T) {
		// hicolor's Directories= line is ~50KB; anything after it
		// must still parse.
		giant := "[Icon Theme]\nDirectories=" + strings.Repeat("16x16/apps,", 20000) +
			"\nInherits=After\n[16x16/apps]\nSize=16\n"
		idx := parseThemeIndex([]byte(giant))
		require.Equal(t, []string{"After"}, idx.inherits)
		require.Len(t, idx.dirs, 1)
	})
}

func TestThemeDirMatches(t *testing.T) {
	cases := []struct {
		name string
		dir  themeDir
		want map[int]bool
	}{
		{
			name: "fixed",
			dir:  themeDir{size: 16, scale: 1, minSize: 16, maxSize: 16, threshold: 2, typ: typeFixed},
			want: map[int]bool{16: true, 15: false, 17: false},
		},
		{
			name: "fixed at scale 2",
			dir:  themeDir{size: 16, scale: 2, minSize: 16, maxSize: 16, threshold: 2, typ: typeFixed},
			want: map[int]bool{32: true, 16: false, 31: false},
		},
		{
			name: "threshold",
			dir:  themeDir{size: 24, scale: 1, minSize: 24, maxSize: 24, threshold: 2, typ: typeThreshold},
			want: map[int]bool{22: true, 24: true, 26: true, 21: false, 27: false},
		},
		{
			name: "threshold at scale 2",
			dir:  themeDir{size: 24, scale: 2, minSize: 24, maxSize: 24, threshold: 2, typ: typeThreshold},
			want: map[int]bool{44: true, 48: true, 52: true, 43: false, 53: false},
		},
		{
			name: "scalable",
			dir:  themeDir{size: 128, scale: 1, minSize: 8, maxSize: 512, threshold: 2, typ: typeScalable},
			want: map[int]bool{8: true, 128: true, 512: true, 7: false, 513: false},
		},
		{
			name: "scalable at scale 2",
			dir:  themeDir{size: 128, scale: 2, minSize: 8, maxSize: 256, threshold: 2, typ: typeScalable},
			want: map[int]bool{16: true, 512: true, 15: false, 513: false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for size, want := range tc.want {
				require.Equal(t, want, tc.dir.matches(size), "size %d", size)
			}
		})
	}
}

func TestThemeDirDistance(t *testing.T) {
	fixed := themeDir{size: 16, scale: 1, minSize: 16, maxSize: 16, threshold: 2, typ: typeFixed}
	require.Equal(t, 0, fixed.distance(16))
	require.Equal(t, 4, fixed.distance(20))
	require.Equal(t, 4, fixed.distance(12))

	fixed2x := themeDir{size: 16, scale: 2, minSize: 16, maxSize: 16, threshold: 2, typ: typeFixed}
	require.Equal(t, 0, fixed2x.distance(32))
	require.Equal(t, 2, fixed2x.distance(30))

	thresh := themeDir{size: 24, scale: 1, minSize: 24, maxSize: 24, threshold: 2, typ: typeThreshold}
	require.Equal(t, 0, thresh.distance(24))
	require.Equal(t, 0, thresh.distance(26), "inside the threshold band")
	require.Equal(t, 3, thresh.distance(21), "below the band: MinSize*Scale - want")
	require.Equal(t, 6, thresh.distance(30), "above the band: want - MaxSize*Scale")

	scal := themeDir{size: 128, scale: 1, minSize: 8, maxSize: 512, threshold: 2, typ: typeScalable}
	require.Equal(t, 0, scal.distance(100))
	require.Equal(t, 4, scal.distance(4))
	require.Equal(t, 88, scal.distance(600))
}
