package launch

import "strings"

// targetFieldCodes are the Exec field codes that expand to the launch
// target (%f/%F file path, %u/%U URI); their presence decides whether
// the target is appended when the line carries no code at all.
const targetFieldCodes = "fFuU"

// droppedFieldCodes are the Exec field codes that expand to nothing
// here: %i (icon), %c (translated name) and %k (desktop file path)
// need data the launch path does not carry, and %d %D %n %N %v %m are
// deprecated by the spec.
const droppedFieldCodes = "ickdDnNvm"

// ExpandExec splits a freedesktop .desktop Exec line into argv with t
// substituted for its field codes: %f/%F expand to the raw target
// (the path for path targets; DIVERGENCE: the URL for URL targets --
// the spec wants a downloaded local copy there, which a launcher
// cannot provide), %u/%U to t.URI (file:// for paths), %% to a literal
// percent, %i/%c/%k and the deprecated codes to nothing, and an
// unknown code keeps its percent literally. Tokenization matches
// internal/plugin's parseDesktopExec: space/tab separate, double
// quotes group (backslash escapes the next character inside them).
// When the line carries no target field code at all, the target is
// appended as the last argument (DIVERGENCE from GLib, which would
// silently launch without the file; matches xdg-open's fallback). An
// empty t.Raw (a bare application launch) makes every target code
// expand to nothing, so a lone %f argument disappears entirely --
// byte-identical argv to parseDesktopExec. An Exec line whose program
// token does not survive a target-less expansion (empty, field codes
// only, an explicit "" program) is unlaunchable and returns nil: the
// target must never end up as argv[0].
func ExpandExec(exec string, t Target) []string {
	if base, _ := expandExecArgs(exec, Target{}); len(base) == 0 || base[0] == "" {
		return nil
	}
	argv, sawTargetCode := expandExecArgs(exec, t)
	if !sawTargetCode && t.Raw != "" {
		argv = append(argv, t.Raw)
	}
	return argv
}

// expandExecArgs is ExpandExec's tokenizer pass: the argv with t
// substituted (no no-code target append) plus whether a target field
// code was seen.
func expandExecArgs(exec string, t Target) ([]string, bool) {
	var argv []string
	var cur strings.Builder
	started := false // tracks explicit empty "" and empty-expansion args
	inQuote := false
	sawTargetCode := false
	flush := func() {
		if started || cur.Len() > 0 {
			argv = append(argv, cur.String())
		}
		cur.Reset()
		started = false
	}
	writeTarget := func(val string) {
		sawTargetCode = true
		if val != "" {
			cur.WriteString(val)
			started = true
		}
	}
	for i := 0; i < len(exec); i++ {
		c := exec[i]
		switch {
		case inQuote:
			switch c {
			case '\\':
				if i+1 < len(exec) {
					i++
					cur.WriteByte(exec[i])
				}
			case '"':
				inQuote = false
			default:
				cur.WriteByte(c)
			}
		case c == '"':
			inQuote = true
			started = true
		case c == ' ' || c == '\t':
			flush()
		case c == '%' && i+1 < len(exec):
			next := exec[i+1]
			switch {
			case next == '%':
				cur.WriteByte('%')
				started = true
				i++
			case next == 'f' || next == 'F':
				writeTarget(t.Raw)
				i++
			case next == 'u' || next == 'U':
				if t.Raw == "" {
					writeTarget("")
				} else {
					writeTarget(t.URI)
				}
				i++
			case strings.ContainsRune(droppedFieldCodes, rune(next)):
				i++ // expands to nothing
			default:
				cur.WriteByte('%') // unknown code: keep the percent literally
				started = true
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	flush()
	return argv, sawTargetCode
}
