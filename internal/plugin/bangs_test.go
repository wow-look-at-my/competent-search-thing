package plugin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewBangSetDefaults(t *testing.T) {
	s := NewBangSet(nil, nil)
	require.Empty(t, s.Errors())
	for _, q := range []string{"!x", "/x", "@x"} {
		bq, ok := s.Parse(q)
		require.True(t, ok, "query %q", q)
		require.Equal(t, "x", bq.Name)
	}
	_, ok := s.Parse("$x")
	require.False(t, ok, "non-default sigil rejected")
}

func TestNewBangSetCustomSigils(t *testing.T) {
	s := NewBangSet([]string{"$", "?"}, nil)
	require.Empty(t, s.Errors())
	_, ok := s.Parse("$x")
	require.True(t, ok)
	_, ok = s.Parse("?x")
	require.True(t, ok)
	_, ok = s.Parse("!x")
	require.False(t, ok, "defaults replaced by the custom set")
}

func TestNewBangSetInvalidSigils(t *testing.T) {
	tests := []struct {
		name  string
		sigil string
	}{
		{name: "two runes", sigil: "ab"},
		{name: "letter", sigil: "a"},
		{name: "digit", sigil: "1"},
		{name: "space", sigil: " "},
		{name: "empty", sigil: ""},
		{name: "unicode letter", sigil: "\u00e9"},
		{name: "unicode space", sigil: "\u00a0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewBangSet([]string{tt.sigil}, nil)
			require.Len(t, s.Errors(), 1)
			require.Contains(t, s.Errors()[0].Error(), "sigil")
			// All invalid: the defaults kick in.
			_, ok := s.Parse("!x")
			require.True(t, ok)
		})
	}
}

func TestNewBangSetMixedSigils(t *testing.T) {
	s := NewBangSet([]string{"$", "ab"}, nil)
	require.Len(t, s.Errors(), 1)
	_, ok := s.Parse("$x")
	require.True(t, ok, "valid custom sigil active")
	_, ok = s.Parse("!x")
	require.False(t, ok, "one valid sigil suppresses the defaults")
}

func TestNewBangSetMultibyteSigil(t *testing.T) {
	// A single non-letter multi-byte rune is a valid sigil.
	s := NewBangSet([]string{"\u00a7"}, nil) // section sign
	require.Empty(t, s.Errors())
	bq, ok := s.Parse("\u00a7calc x")
	require.True(t, ok)
	require.Equal(t, "\u00a7", bq.Sigil)
	require.Equal(t, "calc", bq.Name)
	require.Equal(t, "x", bq.Rest)
}

func TestBangSetRegister(t *testing.T) {
	s := NewBangSet(nil, nil)
	require.NoError(t, s.Register("calc", "p1"))
	err := s.Register("calc", "p2")
	require.Error(t, err)
	require.Contains(t, err.Error(), `"calc"`)
	require.Contains(t, err.Error(), `"p1"`)

	// First registration wins.
	p, bang, ok := s.Resolve("calc")
	require.True(t, ok)
	require.Equal(t, "p1", p)
	require.Equal(t, "calc", bang)

	// Register lowercases.
	require.NoError(t, s.Register("PS", "p3"))
	p, bang, ok = s.Resolve("ps")
	require.True(t, ok)
	require.Equal(t, "p3", p)
	require.Equal(t, "ps", bang)
}

func TestBangSetParse(t *testing.T) {
	s := NewBangSet(nil, nil)
	tests := []struct {
		name   string
		query  string
		want   BangQuery
		wantOK bool
	}{
		{
			name:   "name and rest",
			query:  "!calc 2+2",
			want:   BangQuery{Sigil: "!", Name: "calc", Rest: "2+2", HasSpace: true},
			wantOK: true,
		},
		{
			name:   "name only",
			query:  "!calc",
			want:   BangQuery{Sigil: "!", Name: "calc"},
			wantOK: true,
		},
		{
			name:   "bare sigil",
			query:  "!",
			want:   BangQuery{Sigil: "!"},
			wantOK: true,
		},
		{
			name:   "sigil then space",
			query:  "! rest here",
			want:   BangQuery{Sigil: "!", HasSpace: true, Rest: "rest here"},
			wantOK: true,
		},
		{
			name:   "uppercase name lowercased",
			query:  "!CALC x",
			want:   BangQuery{Sigil: "!", Name: "calc", Rest: "x", HasSpace: true},
			wantOK: true,
		},
		{
			name:   "digits dash underscore in name",
			query:  "@a1_b-2",
			want:   BangQuery{Sigil: "@", Name: "a1_b-2"},
			wantOK: true,
		},
		{
			name:   "slash sigil",
			query:  "/ps foo",
			want:   BangQuery{Sigil: "/", Name: "ps", Rest: "foo", HasSpace: true},
			wantOK: true,
		},
		{
			name:   "rest keeps extra spaces raw",
			query:  "!calc  2 + 2 ",
			want:   BangQuery{Sigil: "!", Name: "calc", Rest: " 2 + 2 ", HasSpace: true},
			wantOK: true,
		},
		{
			name:   "empty space rest",
			query:  "!calc ",
			want:   BangQuery{Sigil: "!", Name: "calc", Rest: "", HasSpace: true},
			wantOK: true,
		},
		{name: "plain query", query: "hello"},
		{name: "empty query", query: ""},
		{name: "leading space", query: " !calc"},
		{name: "invalid char after name", query: "!ca!c"},
		{name: "colon after name", query: "!foo:bar"},
		{name: "non-ascii after name", query: "!h\u00e9llo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := s.Parse(tt.query)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestBangSetResolve(t *testing.T) {
	s := NewBangSet(nil, map[string]string{
		"Add":     "calc", // alias keys and targets lowercased
		"ghost":   "does-not-exist",
		"weather": "wttr",
	})
	require.NoError(t, s.Register("calc", "calcprov"))
	require.NoError(t, s.Register("config", "appprov"))
	require.NoError(t, s.Register("c", "cprov"))

	tests := []struct {
		name     string
		typed    string
		wantProv string
		wantBang string
		wantOK   bool
	}{
		{name: "exact", typed: "calc", wantProv: "calcprov", wantBang: "calc", wantOK: true},
		{name: "exact beats prefix", typed: "c", wantProv: "cprov", wantBang: "c", wantOK: true},
		{name: "alias resolves to canonical bang", typed: "add", wantProv: "calcprov", wantBang: "calc", wantOK: true},
		{name: "alias lookup is case-insensitive", typed: "ADD", wantProv: "calcprov", wantBang: "calc", wantOK: true},
		{name: "alias to unregistered bang ignored", typed: "ghost"},
		{name: "unique prefix", typed: "cal", wantProv: "calcprov", wantBang: "calc", wantOK: true},
		{name: "unique prefix of another bang", typed: "co", wantProv: "appprov", wantBang: "config", wantOK: true},
		{name: "unknown", typed: "zzz"},
		{name: "empty", typed: ""},
		{name: "uppercase exact", typed: "CALC", wantProv: "calcprov", wantBang: "calc", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prov, bang, ok := s.Resolve(tt.typed)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.wantProv, prov)
			require.Equal(t, tt.wantBang, bang)
		})
	}
}

func TestBangSetResolveAmbiguousPrefix(t *testing.T) {
	s := NewBangSet(nil, nil)
	require.NoError(t, s.Register("calc", "p1"))
	require.NoError(t, s.Register("calendar", "p2"))
	_, _, ok := s.Resolve("cal")
	require.False(t, ok, "two bangs share the prefix")

	// Adding a third that does not share it leaves the ambiguity.
	require.NoError(t, s.Register("ps", "p3"))
	_, _, ok = s.Resolve("cal")
	require.False(t, ok)

	// Exact still works despite being a prefix of another bang.
	prov, bang, ok := s.Resolve("calc")
	require.True(t, ok)
	require.Equal(t, "p1", prov)
	require.Equal(t, "calc", bang)
}

func TestBangSetResolvePrefixExcludesAliases(t *testing.T) {
	// Aliases do not participate in prefix resolution.
	s := NewBangSet(nil, map[string]string{"code": "calc"})
	require.NoError(t, s.Register("calc", "p1"))
	_, _, ok := s.Resolve("co")
	require.False(t, ok, `"co" is a prefix of alias "code" only, not of any bang`)
}

func TestBangSetCandidates(t *testing.T) {
	s := NewBangSet(nil, nil)
	require.NoError(t, s.Register("rescan", "app"))
	require.NoError(t, s.Register("reload", "app"))
	require.NoError(t, s.Register("calc", "calcprov"))

	all := s.Candidates("")
	require.Equal(t, []BangInfo{
		{Bang: "calc", ProviderID: "calcprov"},
		{Bang: "reload", ProviderID: "app"},
		{Bang: "rescan", ProviderID: "app"},
	}, all, "empty partial returns all, sorted")

	re := s.Candidates("re")
	require.Equal(t, []BangInfo{
		{Bang: "reload", ProviderID: "app"},
		{Bang: "rescan", ProviderID: "app"},
	}, re)

	require.Equal(t, []BangInfo{{Bang: "calc", ProviderID: "calcprov"}}, s.Candidates("CAL"),
		"partial lowercased before matching")

	require.Empty(t, s.Candidates("zzz"))
}
