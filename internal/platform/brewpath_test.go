package platform

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseBrewCellar(t *testing.T) {
	cases := []struct {
		name string
		path string
		want BrewCellar
		ok   bool
	}{
		{
			name: "linuxbrew default prefix",
			path: "/home/linuxbrew/.linuxbrew/Cellar/competent-search-thing/100/bin/competent-search-thing",
			want: BrewCellar{
				Prefix:  "/home/linuxbrew/.linuxbrew",
				Formula: "competent-search-thing",
				Version: "100",
				Rest:    "bin/competent-search-thing",
			},
			ok: true,
		},
		{
			name: "per-user linuxbrew prefix",
			path: "/home/alice/.linuxbrew/Cellar/cst/1.2.3/bin/cst",
			want: BrewCellar{Prefix: "/home/alice/.linuxbrew", Formula: "cst", Version: "1.2.3", Rest: "bin/cst"},
			ok:   true,
		},
		{
			name: "apple silicon prefix",
			path: "/opt/homebrew/Cellar/cst/1.2.3_1/libexec/cst",
			want: BrewCellar{Prefix: "/opt/homebrew", Formula: "cst", Version: "1.2.3_1", Rest: "libexec/cst"},
			ok:   true,
		},
		{
			name: "usr-local prefix",
			path: "/usr/local/Cellar/cst/2/bin/cst",
			want: BrewCellar{Prefix: "/usr/local", Formula: "cst", Version: "2", Rest: "bin/cst"},
			ok:   true,
		},
		{
			name: "arbitrary prefix",
			path: "/somewhere/odd/Cellar/cst/9/x",
			want: BrewCellar{Prefix: "/somewhere/odd", Formula: "cst", Version: "9", Rest: "x"},
			ok:   true,
		},
		{
			name: "cellar directly under the root",
			path: "/Cellar/cst/1/bin/cst",
			want: BrewCellar{Prefix: "/", Formula: "cst", Version: "1", Rest: "bin/cst"},
			ok:   true,
		},
		{
			name: "double cellar takes the last occurrence",
			path: "/outer/Cellar/wrap/9/libexec/Cellar/inner/2/bin/cst",
			want: BrewCellar{Prefix: "/outer/Cellar/wrap/9/libexec", Formula: "inner", Version: "2", Rest: "bin/cst"},
			ok:   true,
		},
		{
			name: "uncleaned path is cleaned first",
			path: "/p//Cellar/cst/./1.0/bin/../bin/cst",
			want: BrewCellar{Prefix: "/p", Formula: "cst", Version: "1.0", Rest: "bin/cst"},
			ok:   true,
		},
		{name: "relative path", path: "p/Cellar/cst/1/bin/cst", ok: false},
		{name: "no cellar component", path: "/usr/local/bin/cst", ok: false},
		{name: "cellar as the final component", path: "/p/bin/Cellar", ok: false},
		{name: "cellar substring of a component", path: "/p/NotCellar/cst/1/bin/cst", ok: false},
		{name: "missing version and rest", path: "/p/Cellar/cst", ok: false},
		{name: "missing rest", path: "/p/Cellar/cst/1.0.0", ok: false},
		{name: "trailing slash leaves no rest", path: "/p/Cellar/cst/1.0.0/", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseBrewCellar(tc.path)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got, "not-ok parses return the zero value")
		})
	}
}

func TestBrewStableCandidates(t *testing.T) {
	got := brewStableCandidates("/opt/homebrew/Cellar/cst/1.0.0/bin/cst")
	require.Equal(t, []string{
		"/opt/homebrew/bin/cst",
		"/opt/homebrew/opt/cst/bin/cst",
	}, got, "the linked spelling first, the opt fallback second")

	require.Nil(t, brewStableCandidates("/usr/local/bin/cst"), "non-Cellar paths derive nothing")
	require.Nil(t, brewStableCandidates("relative/Cellar/cst/1/bin/cst"), "relative paths derive nothing")
}
