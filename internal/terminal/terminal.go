// Package terminal resolves the terminal emulator a command should be
// run in and builds the invocation for it.
//
// It is the pure half of the run-in-terminal capability (the plugin
// source lives in internal/plugin builtin_runterm.go, the wiring in
// internal/app): every environment probe rides an injectable seam, so
// the whole decision matrix is unit-tested headlessly on any machine.
//
// The unit of currency is an argv: Command turns the command the user
// typed into the argv that launches it inside a terminal window. Only
// terminals whose "run this command" flag takes the command as
// SEPARATE arguments are supported -- a terminal that wants one
// re-quoted shell string (tilix -e) is left out rather than guessed
// at, because re-quoting is where launchers get command injection
// wrong.
package terminal

import "strings"

// Terminal is one resolved terminal emulator plus how to hand it a
// command. The zero value is not usable; build one with Detect.
type Terminal struct {
	// Name is the terminal's display name (the executable's base
	// name on unix, the application name on darwin) -- what a result
	// row tells the user it will open.
	Name string
	// Path is the resolved executable that gets exec'd.
	Path string
	// SupportsArgs reports whether the command may carry arguments.
	// False for the darwin "open -a Terminal <program>" path, which
	// can only start a program, not a command line: callers must not
	// offer to run an argument-carrying command there.
	SupportsArgs bool

	// prefix are the arguments between Path and the command (e.g.
	// "-e", "--", "-a Terminal"); nil when the terminal takes the
	// command as its trailing arguments (kitty, foot).
	prefix []string
}

// Command builds the full argv that runs argv inside the terminal.
// The command's arguments are passed through as separate arguments --
// never re-quoted into a shell string -- so a file name with spaces
// or quotes reaches the program intact. It returns nil for an empty
// command, or for one carrying arguments when SupportsArgs is false.
func (t Terminal) Command(argv []string) []string {
	if t.Path == "" || len(argv) == 0 || argv[0] == "" {
		return nil
	}
	if !t.SupportsArgs && len(argv) > 1 {
		return nil
	}
	out := make([]string, 0, 1+len(t.prefix)+len(argv))
	out = append(out, t.Path)
	out = append(out, t.prefix...)
	return append(out, argv...)
}

// Options carries the environment probes Detect needs. GOOS selects
// the platform's rules; Getenv and LookPath are the environment and
// PATH lookups (production: os.Getenv and exec.LookPath).
type Options struct {
	GOOS     string
	Getenv   func(string) string
	LookPath func(name string) (string, error)
}

// EnvTerminal is the conventional user override naming the preferred
// terminal emulator. A value that resolves on PATH wins over every
// probed candidate; its argument convention comes from the table when
// the name is known, and defaults to "-e" (what every X terminal
// since xterm accepts) when it is not.
const EnvTerminal = "TERMINAL"

// candidate is one known terminal: the executable to look for and the
// arguments that precede the command.
type candidate struct {
	exe    string
	prefix []string
}

// unixCandidates is the probe order on linux and the BSDs.
// x-terminal-emulator comes first because on Debian-family systems it
// IS the user's chosen terminal (the alternatives symlink); the rest
// run desktop-environment terminals before the minimal ones, so a
// GNOME or KDE session opens the terminal that matches it.
//
// The prefix per terminal is the flag that takes a command as
// separate trailing arguments:
//   - "--" for gnome-terminal (its -e re-parses one string and is
//     deprecated);
//   - "-x" for the xfce4/mate/terminator family (their -e is the
//     one-string form);
//   - "-e" for the X11 classics and most modern terminals;
//   - nothing at all for kitty and foot, which take the command as
//     their trailing arguments;
//   - "start --" for wezterm, whose command lives under a subcommand.
var unixCandidates = []candidate{
	{exe: "x-terminal-emulator", prefix: []string{"-e"}},
	{exe: "gnome-terminal", prefix: []string{"--"}},
	{exe: "konsole", prefix: []string{"-e"}},
	{exe: "xfce4-terminal", prefix: []string{"-x"}},
	{exe: "mate-terminal", prefix: []string{"-x"}},
	{exe: "terminator", prefix: []string{"-x"}},
	{exe: "ghostty", prefix: []string{"-e"}},
	{exe: "kitty"},
	{exe: "wezterm", prefix: []string{"start", "--"}},
	{exe: "alacritty", prefix: []string{"-e"}},
	{exe: "foot"},
	{exe: "lxterminal", prefix: []string{"-e"}},
	{exe: "urxvt", prefix: []string{"-e"}},
	{exe: "rxvt", prefix: []string{"-e"}},
	{exe: "st", prefix: []string{"-e"}},
	{exe: "xterm", prefix: []string{"-e"}},
}

// prefixFor returns the known argument convention for a terminal
// executable's base name, or the "-e" default for an unknown one.
func prefixFor(exe string) []string {
	base := baseName(exe)
	for _, c := range unixCandidates {
		if c.exe == base {
			return c.prefix
		}
	}
	return []string{"-e"}
}

// baseName is filepath.Base for both separators, so a $TERMINAL given
// as an absolute path still matches the table (and a windows path
// works on any host).
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Detect resolves the terminal to run commands in, reporting false
// when the machine has none this package knows how to drive. Callers
// treat that as "the run-in-terminal capability does not exist here"
// -- there is no fallback that would run the command somewhere the
// user cannot see it.
func Detect(o Options) (Terminal, bool) {
	if o.LookPath == nil {
		return Terminal{}, false
	}
	switch o.GOOS {
	case "darwin":
		return detectDarwin(o)
	case "windows":
		return detectWindows(o)
	default:
		return detectUnix(o)
	}
}

func detectUnix(o Options) (Terminal, bool) {
	if o.Getenv != nil {
		if name := strings.TrimSpace(o.Getenv(EnvTerminal)); name != "" {
			if path, err := o.LookPath(name); err == nil {
				return Terminal{
					Name:         baseName(name),
					Path:         path,
					SupportsArgs: true,
					prefix:       prefixFor(name),
				}, true
			}
		}
	}
	for _, c := range unixCandidates {
		path, err := o.LookPath(c.exe)
		if err != nil {
			continue
		}
		return Terminal{Name: c.exe, Path: path, SupportsArgs: true, prefix: c.prefix}, true
	}
	return Terminal{}, false
}

// detectDarwin drives Terminal.app through "open -a": it starts a
// program in a new terminal window but cannot carry arguments, hence
// SupportsArgs false. (The alternative -- generating an AppleScript
// "do script" line -- means re-quoting the command into a shell
// string inside an osascript string, two nested quoting layers this
// package deliberately refuses to guess at.)
func detectDarwin(o Options) (Terminal, bool) {
	path, err := o.LookPath("open")
	if err != nil {
		return Terminal{}, false
	}
	return Terminal{
		Name:   "Terminal",
		Path:   path,
		prefix: []string{"-a", "Terminal"},
	}, true
}

// detectWindows prefers Windows Terminal (wt takes the command as
// trailing arguments) and falls back to a conhost window that stays
// open after the command exits ("cmd /c start" detaches the new
// console; the inner "cmd /k" keeps it readable).
func detectWindows(o Options) (Terminal, bool) {
	if path, err := o.LookPath("wt.exe"); err == nil {
		return Terminal{Name: "Windows Terminal", Path: path, SupportsArgs: true}, true
	}
	path, err := o.LookPath("cmd.exe")
	if err != nil {
		return Terminal{}, false
	}
	return Terminal{
		Name:         "cmd.exe",
		Path:         path,
		SupportsArgs: true,
		// The empty argument is start's window TITLE, which it
		// otherwise takes from the first quoted argument -- omitting
		// it makes start swallow the program path.
		prefix: []string{"/c", "start", "", "cmd.exe", "/k"},
	}, true
}
