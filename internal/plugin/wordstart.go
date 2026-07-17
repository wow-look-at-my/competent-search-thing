package plugin

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// wordStart reports whether needle occurs in s starting a word: at
// index 0 or right after a rune that is neither a letter nor a digit
// (so "main" starts a word in "app - main.go" and "foo-main" but not
// in "domain"). Both strings are expected pre-lowercased; an empty
// needle never matches. Shared by the firefox, tabs and openwindows
// builtins.
func wordStart(s, needle string) bool {
	if needle == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(s[from:], needle)
		if i < 0 {
			return false
		}
		at := from + i
		if at == 0 {
			return true
		}
		r, _ := utf8.DecodeLastRuneInString(s[:at])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return true
		}
		from = at + 1
	}
}
