package plugin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWordStart merges the cases the firefox and openwindows builtins
// each pinned while they still carried private copies of the helper.
func TestWordStart(t *testing.T) {
	tests := []struct {
		s, needle string
		want      bool
	}{
		{"main.go - code", "main", true}, // start of string
		{"app - main.go", "main", true},  // after a space
		{"foo-main", "main", true},       // after punctuation
		{"[main] scratch", "main", true}, // after a bracket
		{"domain", "main", false},        // mid-word only
		{"domain main", "main", true},    // later word-start occurrence wins
		{"xx2main", "main", false},       // digits end a word too
		{"pull requests", "pull", true},
		{"pull requests", "requests", true},
		{"pull requests", "equests", false},
		{"hacker-news daily", "news", true},
		{"hacker-news daily", "acker", false},
		{"go", "go", true},
		{"a b", "", false}, // empty needle never matches
		{"", "x", false},
		{"", "main", false},
		{"main", "nomatch", false},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, wordStart(tt.s, tt.needle), "wordStart(%q, %q)", tt.s, tt.needle)
	}
}
