package gsettings

import "strings"

// The GVariant text-format helpers below cover exactly what the
// gsettings CLI emits and accepts for this package's keys: string
// values ('...' or "..." with backslash escapes) and arrays of strings
// ("['a', 'b']", "@as []"). They are parsers for CLI output, not a
// general GVariant implementation.

// parseVariantString parses one string literal at the start of s and
// returns its value plus the unconsumed remainder. ok is false when s
// does not start with a complete string literal. The common backslash
// escapes are decoded; anything else after a backslash is kept
// literally (accelerator strings and dconf paths never contain the
// exotic \u escapes).
func parseVariantString(s string) (val, rest string, ok bool) {
	if s == "" || (s[0] != '\'' && s[0] != '"') {
		return "", "", false
	}
	quote := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			if i+1 >= len(s) {
				return "", "", false
			}
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte(s[i])
			}
		case quote:
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(c)
		}
	}
	return "", "", false // unterminated literal
}

// parseVariantStringValue parses a whole gsettings get output as one
// string value (e.g. "'<Alt>space'\n").
func parseVariantStringValue(s string) (string, bool) {
	val, rest, ok := parseVariantString(strings.TrimSpace(s))
	if !ok || strings.TrimSpace(rest) != "" {
		return "", false
	}
	return val, true
}

// parseStringArray parses a GVariant text array-of-strings value:
// "['a', 'b']", "[]" and the annotated empty form "@as []". ok is
// false for any other shape -- non-array values and arrays holding
// non-strings are the caller's cue to ignore the value.
func parseStringArray(s string) ([]string, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "@") {
		// A type annotation like "@as" precedes empty containers.
		sp := strings.IndexAny(s, " \t")
		if sp < 0 {
			return nil, false
		}
		s = strings.TrimSpace(s[sp+1:])
	}
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return nil, false
	}
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return []string{}, true
	}
	var out []string
	for {
		val, rest, ok := parseVariantString(inner)
		if !ok {
			return nil, false
		}
		out = append(out, val)
		rest = strings.TrimSpace(rest)
		if rest == "" {
			return out, true
		}
		if !strings.HasPrefix(rest, ",") {
			return nil, false
		}
		inner = strings.TrimSpace(rest[1:])
	}
}

// quoteVariantString serializes s as a single-quoted GVariant text
// string ("a" -> "'a'"), escaping backslash and the quote character.
func quoteVariantString(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' || s[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('\'')
	return b.String()
}

// quoteVariantArray serializes items as a GVariant text array of
// single-quoted strings: ['a', 'b'].
func quoteVariantArray(items []string) string {
	quoted := make([]string, len(items))
	for i, it := range items {
		quoted[i] = quoteVariantString(it)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
