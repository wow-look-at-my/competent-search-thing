// Package gsettings installs the app's summon hotkey as a GNOME
// custom keybinding by driving the gsettings CLI -- the fallback
// global-hotkey backend for GNOME Wayland sessions whose portal has no
// GlobalShortcuts interface (GNOME < 48, e.g. Ubuntu 24.04 / GNOME
// 46). GNOME's settings daemon grabs custom media-keys bindings
// through the compositor, so the mechanism works natively on Wayland.
//
// The pitfall this package exists to handle: mutter REFUSES a grab for
// a combination that is already bound (a wm keybinding, another custom
// entry, ...) and gnome-settings-daemon only logs a warning -- the
// custom binding then silently never fires. GNOME 46 defaults make the
// obvious combinations exactly that trap: Alt+Space is
// activate-window-menu and Super+Space is switch-input-source.
// EnsureBinding therefore scans the standard keybinding schemas plus
// the other custom entries first and only writes a combination nothing
// else claims.
//
// The package is pure logic over an injectable Runner; unit tests
// never touch a real dconf database.
package gsettings

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// runTimeout bounds each gsettings invocation the default Runner
// makes; the CLI answers in milliseconds when dconf is healthy.
const runTimeout = 3 * time.Second

// Runner executes one gsettings invocation (argv without the leading
// "gsettings") and returns its stdout. Production uses Run; tests
// inject a scripted fake.
type Runner func(ctx context.Context, args ...string) (stdout string, err error)

// Run is the production Runner: it execs the gsettings binary directly
// (no shell) with a per-call timeout, returning stdout and folding any
// stderr output into the error.
func Run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gsettings", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("gsettings %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("gsettings %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// ToggleCommand builds the command string the GNOME keybinding runs:
// "<exe> toggle". gnome-settings-daemon parses the command with GLib's
// shell rules, so an executable path containing spaces or quote
// characters is double-quoted with the two characters that stay
// special inside GLib double quotes -- backslash and double quote --
// escaped.
func ToggleCommand(exe string) string {
	if strings.ContainsAny(exe, " \t\"'\\") {
		exe = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(exe) + `"`
	}
	return exe + " toggle"
}

// commandExecutable extracts the executable token -- the first word --
// from a keybinding command, undoing the quoting ToggleCommand applies
// and gnome-settings-daemon parses with (GLib shell rules): inside
// double quotes a backslash escapes only backslash and double quote
// (before anything else both characters are kept, like g_shell_
// unquote), single quotes are verbatim runs, adjacent quoted and bare
// segments concatenate, and an unquoted space or tab ends the token.
// ok is false for an empty command or an unterminated quote.
func commandExecutable(command string) (exe string, ok bool) {
	s := strings.TrimLeft(command, " \t")
	if s == "" {
		return "", false
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		switch s[i] {
		case ' ', '\t':
			return b.String(), true
		case '"':
			i++
			closed := false
			for i < len(s) {
				if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '\\' || s[i+1] == '"') {
					b.WriteByte(s[i+1])
					i += 2
					continue
				}
				if s[i] == '"' {
					i++
					closed = true
					break
				}
				b.WriteByte(s[i])
				i++
			}
			if !closed {
				return "", false
			}
		case '\'':
			i++
			end := strings.IndexByte(s[i:], '\'')
			if end < 0 {
				return "", false
			}
			b.WriteString(s[i : i+end])
			i += end + 1
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String(), true
}
