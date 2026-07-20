package ffext

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenRoundTrip(t *testing.T) {
	for _, tc := range []struct{ conn, tab, win int64 }{
		{1, 0, 0},
		{1, 42, 3},
		{99, 1<<40 + 7, 12},
	} {
		tok := Token(tc.conn, tc.tab, tc.win)
		conn, tab, win, err := ParseToken(tok)
		require.NoError(t, err, tok)
		require.Equal(t, tc.conn, conn)
		require.Equal(t, tc.tab, tab)
		require.Equal(t, tc.win, win)
	}
	require.Equal(t, "c1:42:3", Token(1, 42, 3), "the documented token shape")
}

func TestParseTokenRejects(t *testing.T) {
	for _, bad := range []string{
		"",                             // empty
		"1:2:3",                        // missing the c prefix
		"c1:2",                         // too few fields
		"c1:2:3:4",                     // too many fields
		"c1:2:x",                       // non-numeric field
		"c-1:2:3",                      // negative conn
		"c1:-2:3",                      // negative tab
		"c1:2:-3",                      // negative window
		"c0:2:3",                       // conn ids start at 1
		"c1:2.5:3",                     // non-integer
		"c1: 2:3",                      // embedded space
		"c" + strings.Repeat("1", 100), // over the length cap
		"c1:2:99999999999999999999",    // int64 overflow
		"c+1:2:3",                      // explicit sign
		"c0x1:2:3",                     // hex
	} {
		_, _, _, err := ParseToken(bad)
		require.Error(t, err, "token %q must be rejected", bad)
	}
}
